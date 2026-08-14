package providers

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CongBao/dagrail/sdk"
)

var policySchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["policyId","input"],"properties":{"policyId":{"type":"string"},"input":{"type":"object","required":["score"],"properties":{"score":{"type":"integer"}}},"evidence":{"type":"array"}}}`)

type testPolicy struct {
	metadata sdk.Metadata
	calls    atomic.Int32
	mode     string
}

func (p *testPolicy) Metadata() sdk.Metadata       { return p.metadata }
func (p *testPolicy) InputSchema() json.RawMessage { return policySchema }
func (p *testPolicy) Decide(ctx context.Context, request sdk.PolicyRequest) (sdk.PolicyDecision, error) {
	p.calls.Add(1)
	switch p.mode {
	case "panic":
		panic("broken provider")
	case "wait":
		<-ctx.Done()
		return sdk.PolicyDecision{}, ctx.Err()
	case "secret":
		return sdk.PolicyDecision{Outcome: "approve", Facts: json.RawMessage(`{"accessToken":"forbidden"}`)}, nil
	case "large":
		return sdk.PolicyDecision{Outcome: "approve", Facts: json.RawMessage(`{"value":"` + strings.Repeat("x", 1024) + `"}`)}, nil
	default:
		return sdk.PolicyDecision{Outcome: "approve", Facts: json.RawMessage(`{"risk":"low"}`)}, nil
	}
}

func TestRuntimeValidatesInputAndInvokesPolicy(t *testing.T) {
	hash, err := validateProviderSchema(policySchema)
	if err != nil {
		t.Fatal(err)
	}
	provider := &testPolicy{metadata: sdk.Metadata{ID: "test.policy", Version: "1.2.3", SchemaHash: hash, Stability: sdk.StabilityStable}}
	registry := New()
	if err := registry.RegisterPolicy(provider); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(registry)
	report := runtime.Check()
	if !report.Healthy || len(report.Checks) != 1 || report.Checks[0].SchemaHash != hash {
		t.Fatalf("unexpected conformance report: %+v", report)
	}
	result, err := runtime.Invoke(context.Background(), Invocation{Kind: KindPolicy, ProviderID: "test.policy", Input: json.RawMessage(`{"policyId":"release","input":{"score":7}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Output), `"outcome":"approve"`) || provider.calls.Load() != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}

	_, err = runtime.Invoke(context.Background(), Invocation{Kind: KindPolicy, ProviderID: "test.policy", Input: json.RawMessage(`{"policyId":"release","input":{"score":"seven"}}`)})
	if err == nil || !strings.Contains(err.Error(), "does not match schema") {
		t.Fatalf("expected schema failure, got %v", err)
	}
	if provider.calls.Load() != 1 {
		t.Fatal("provider must not be entered for invalid input")
	}
}

func TestRuntimeIsolatesPanicTimeoutAndUnsafeOutput(t *testing.T) {
	for _, test := range []struct{ name, mode, want string }{
		{"panic", "panic", "provider panicked"},
		{"timeout", "wait", "timed out or was cancelled"},
		{"secret", "secret", "may contain a secret"},
		{"large", "large", "byte limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &testPolicy{metadata: sdk.Metadata{ID: "test." + test.name, Version: "1.0.0", SchemaHash: "development", Stability: sdk.StabilityExperimental}, mode: test.mode}
			registry := New()
			if err := registry.RegisterPolicy(provider); err != nil {
				t.Fatal(err)
			}
			runtime := NewRuntime(registry)
			runtime.CallTimeout = 10 * time.Millisecond
			if test.mode == "large" {
				runtime.MaxOutputBytes = 128
			}
			_, err := runtime.Invoke(context.Background(), Invocation{Kind: KindPolicy, ProviderID: provider.metadata.ID, Input: json.RawMessage(`{"policyId":"x","input":{"score":1}}`)})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}

type badImporter struct{}

func (badImporter) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "test.importer", Version: "1.0.0", SchemaHash: "development", Stability: sdk.StabilityExperimental}
}
func (badImporter) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (badImporter) Import(context.Context, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"not":"a graph"}`), nil
}

func TestRuntimeRejectsInvalidImporterOutputAndReportsMissingSchemas(t *testing.T) {
	registry := New()
	if err := registry.RegisterImporter(badImporter{}); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(registry)
	_, err := runtime.Invoke(context.Background(), Invocation{Kind: KindImporter, ProviderID: "test.importer", Input: json.RawMessage(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "output graph") {
		t.Fatalf("expected invalid graph, got %v", err)
	}

	missing := &schemaLessPredicate{}
	if err := registry.RegisterPredicate(missing); err != nil {
		t.Fatal(err)
	}
	report := runtime.Check()
	if report.Healthy {
		t.Fatalf("schema-less callable provider must fail: %+v", report)
	}
}

type schemaLessPredicate struct{}

func (*schemaLessPredicate) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "test.schema-less", Version: "1.0.0", SchemaHash: "development"}
}
func (*schemaLessPredicate) Evaluate(context.Context, sdk.PredicateRequest) (bool, error) {
	return true, nil
}

type fixtureNodeKind struct{ metadata sdk.Metadata }

func (p fixtureNodeKind) Metadata() sdk.Metadata { return p.metadata }
func (fixtureNodeKind) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["target"],"properties":{"target":{"type":"string"}}}`)
}
func (fixtureNodeKind) Outcomes() []sdk.OutcomeDefinition {
	return []sdk.OutcomeDefinition{{ID: "done", Class: "success"}, {ID: "return", Class: "retryable"}}
}

func TestNodeKindContractValidatesInputAndClosedOutcomes(t *testing.T) {
	schema := fixtureNodeKind{}.InputSchema()
	hash, err := validateProviderSchema(schema)
	if err != nil {
		t.Fatal(err)
	}
	provider := fixtureNodeKind{metadata: sdk.Metadata{ID: "fixture.deploy", Version: "1.0.0", SchemaHash: hash, Stability: sdk.StabilityStable}}
	registry := New()
	if err := registry.RegisterNodeKind(provider); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(registry)
	outcomes := []sdk.OutcomeDefinition{{ID: "done", Class: "success"}, {ID: "return", Class: "retryable"}}
	if err := runtime.ValidateNodeKind("fixture.deploy", json.RawMessage(`{"target":"staging"}`), outcomes); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ValidateNodeKind("fixture.deploy", json.RawMessage(`{"target":7}`), outcomes); err == nil {
		t.Fatal("invalid node input must fail")
	}
	if err := runtime.ValidateNodeKind("fixture.deploy", json.RawMessage(`{"target":"staging"}`), outcomes[:1]); err == nil {
		t.Fatal("open outcome contract must fail")
	}
}
