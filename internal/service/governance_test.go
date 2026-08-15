package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/sdk"
)

func TestDecisionRecordDrivesOneBranchAndAutomaticallySkipsTheOther(t *testing.T) {
	svc, root := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"decision"},"spec":{"roles":[{"id":"architect","capabilities":["node.decide"]},{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"choose","kind":"decision","role":"architect","title":"choose","decision":{"key":"route","source":"llm"},"outcomes":[{"id":"left","class":"success"},{"id":"right","class":"success"}]},{"id":"left-work","kind":"task","role":"worker","title":"left","outcomes":[{"id":"done","class":"success"}]},{"id":"right-work","kind":"task","role":"worker","title":"right","outcomes":[{"id":"done","class":"success"}]}],"edges":[{"id":"choose-left","from":"choose","to":"left-work","when":{"decision":{"key":"route","value":"left"}}},{"id":"choose-right","from":"choose","to":"right-work","when":{"decision":{"key":"route","value":"right"}}}]}}`)
	_ = root
	if _, err := svc.BindRole("architect", "codex", "architect-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "architect", "choose", "node.start"), json.RawMessage(`{}`), "start"); err != nil {
		t.Fatal(err)
	}
	result, err := svc.ApplyAction(findActionRef(t, svc, "architect", "choose", "decision.record"), json.RawMessage(`{"outcome":"right","evidenceRefs":[{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","type":"review-note","size":7}]}`), "decide")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.ObjectRef, "decision:") {
		t.Fatalf("completion did not return a decision ref: %+v", result)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Decisions) != 1 || state.Nodes["left-work"].Status != "skipped" || state.Nodes["right-work"].Status != "planned" {
		t.Fatalf("decision did not close the unused branch: decisions=%d left=%+v right=%+v", len(state.Decisions), state.Nodes["left-work"], state.Nodes["right-work"])
	}
	frontier, err := svc.Frontier()
	if err != nil || len(frontier.Ready) != 1 || frontier.Ready[0] != "right-work" {
		t.Fatalf("unexpected frontier after decision: %+v %v", frontier, err)
	}
}

func TestResourceMustBeClosedAndUnknownClosureMustBeReconciled(t *testing.T) {
	svc, _ := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"resource"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","resource.close","incident.manage"]}],"resourceCapacities":[{"kind":"browser","capacity":1}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"work","resources":[{"kind":"browser","quantity":1}],"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	if _, err := svc.BindRole("worker", "codex", "worker-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	started, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "node.start"), json.RawMessage(`{}`), "start")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "task.complete"), json.RawMessage(`{"outcome":"done"}`), "finish-too-early"); err == nil || !strings.Contains(err.Error(), "active resources") {
		t.Fatalf("attempt completed with an unclosed resource: %v", err)
	}
	unknown, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "resource.close"), json.RawMessage(`{"status":"unknown","receipt":{"observation":"browser process disappeared before acknowledgement"}}`), "close")
	if err != nil || unknown.Status != "unknown" || !strings.HasPrefix(unknown.ObjectRef, "resource:") {
		t.Fatalf("unknown closure was not persisted: %+v %v", unknown, err)
	}
	state, _ := svc.State()
	if state.Resources[strings.TrimPrefix(unknown.ObjectRef, "resource:")].Status != "active" || state.Incidents[unknown.ObjectRef].Status != "open" {
		t.Fatalf("unknown closure did not retain capacity and open an incident: %+v", state)
	}
	confirmed, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "resource.reconcile"), json.RawMessage(`{"status":"confirmed","receipt":{"observation":"process absent and port reusable"}}`), "reconcile")
	if err != nil || confirmed.Status != "confirmed" {
		t.Fatalf("closure reconcile failed: %+v %v", confirmed, err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "attempt.submit"), json.RawMessage(`{}`), "submit"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "task.complete"), json.RawMessage(`{"outcome":"done"}`), "finish"); err != nil {
		t.Fatal(err)
	}
	state, _ = svc.State()
	if state.Attempts[started.AttemptID].Status != "terminal" || state.Incidents[unknown.ObjectRef].Status != "resolved" {
		t.Fatalf("resource lifecycle did not close: %+v %+v", state.Attempts[started.AttemptID], state.Incidents[unknown.ObjectRef])
	}
}

type gatePolicy struct{ metadata sdk.Metadata }

func (p gatePolicy) Metadata() sdk.Metadata { return p.metadata }
func (gatePolicy) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["policyId","input"],"properties":{"policyId":{"type":"string"},"input":{"type":"object","required":["score"],"properties":{"score":{"type":"integer"}}},"evidence":{"type":"array"}}}`)
}
func (gatePolicy) Decide(_ context.Context, request sdk.PolicyRequest) (sdk.PolicyDecision, error) {
	var input struct {
		Score int `json:"score"`
	}
	_ = json.Unmarshal(request.Input, &input)
	if input.Score >= 7 {
		return sdk.PolicyDecision{Outcome: "pass", Facts: json.RawMessage(`{"risk":"low"}`)}, nil
	}
	return sdk.PolicyDecision{Outcome: "fail", Facts: json.RawMessage(`{"risk":"high"}`)}, nil
}

func TestGatePersistsProviderBoundDecisionInsteadOfOpaqueOutput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "gate")
	if err != nil {
		t.Fatal(err)
	}
	schema := gatePolicy{}.InputSchema()
	hash, err := sdk.InputSchemaHash(schema)
	if err != nil {
		t.Fatal(err)
	}
	provider := gatePolicy{metadata: sdk.Metadata{ID: "test.gate", Version: "1.0.0", SchemaHash: hash, Stability: sdk.StabilityStable}}
	if err := svc.Providers.RegisterPolicy(provider); err != nil {
		t.Fatal(err)
	}
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"gate"},"spec":{"providers":[{"id":"test.gate","version":"1.0.0","schemaHash":"` + hash + `"}],"roles":[{"id":"controller","capabilities":["node.gate"]}],"nodes":[{"id":"quality","kind":"gate","role":"controller","title":"quality","decision":{"key":"quality","source":"provider","providerId":"test.gate","policyId":"release"},"outcomes":[{"id":"pass","class":"success"},{"id":"fail","class":"failure"}]}],"edges":[]}}`
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "import", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("controller", "codex", "controller-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "controller", "quality", "node.start"), json.RawMessage(`{}`), "start"); err != nil {
		t.Fatal(err)
	}
	result, err := svc.ApplyAction(findActionRef(t, svc, "controller", "quality", "gate.evaluate"), json.RawMessage(`{"input":{"score":9}}`), "evaluate")
	if err != nil {
		t.Fatal(err)
	}
	state, _ := svc.State()
	record := state.Decisions[strings.TrimPrefix(result.ObjectRef, "decision:")]
	if record.Provider == nil || record.Provider.ID != "test.gate" || record.Provider.SchemaHash != hash || record.Facts.Policy["risk"] != "low" || state.Nodes["quality"].Outcome != "pass" {
		t.Fatalf("provider decision lost provenance or facts: %+v runtime=%+v", record, state.Nodes["quality"])
	}
}

func TestForgedSignedActionStillRequiresRoleCapability(t *testing.T) {
	svc, _ := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"capability-boundary"},"spec":{"roles":[{"id":"observer","capabilities":["observer"]}],"nodes":[{"id":"work","kind":"task","role":"observer","title":"work","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	lease, err := svc.BindRole("observer", "codex", "observer-session", time.Hour, false, "bind")
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := svc.load()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := svc.actionSecret()
	if err != nil {
		t.Fatal(err)
	}
	payload := actionRefPayload{
		ActionID:      "forged-action",
		ProjectID:     state.ProjectID,
		GraphRevision: state.GraphRevision,
		ProviderSet:   providerFingerprint(state.Graph),
		HeadHash:      state.HeadHash,
		RoleID:        "observer",
		SessionID:     lease.SessionID,
		NodeID:        "work",
		Kind:          "node.start",
		ExpiresAt:     svc.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
	}
	ref, err := signActionRef(payload, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(ref, json.RawMessage(`{}`), "forged"); err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("signed action bypassed role capability: %v", err)
	}
	state, _ = svc.State()
	if state.Nodes["work"].Status != "planned" || len(state.Attempts) != 0 {
		t.Fatalf("rejected action changed authority state: %+v", state.Nodes["work"])
	}
}

func TestIdempotencyKeyCannotMoveBetweenActionsOrRoles(t *testing.T) {
	svc, _ := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"idempotency"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]},{"id":"reviewer","capabilities":["node.review"]}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"work","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	if _, err := svc.BindRole("worker", "codex", "worker-session", time.Hour, false, "shared-bind"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "claude-code", "another-session", time.Hour, false, "shared-bind"); err == nil || !strings.Contains(err.Error(), "another command") {
		t.Fatalf("role binding reused a key with different request data: %v", err)
	}
	if _, err := svc.BindRole("reviewer", "codex", "reviewer-session", time.Hour, false, "shared-bind"); err == nil || !strings.Contains(err.Error(), "another command") {
		t.Fatalf("role binding reused another role's idempotency key: %v", err)
	}
	startRef := findActionRef(t, svc, "worker", "work", "node.start")
	if _, err := svc.ApplyAction(startRef, json.RawMessage(`{}`), "shared-action"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(startRef, json.RawMessage(`{"different":true}`), "shared-action"); err == nil || !strings.Contains(err.Error(), "another command") {
		t.Fatalf("idempotency key accepted different input for the same action: %v", err)
	}
	checkpoint := findActionRef(t, svc, "worker", "work", "attempt.checkpoint")
	if _, err := svc.ApplyAction(checkpoint, json.RawMessage(`{"summary":"durable"}`), "shared-action"); err == nil || !strings.Contains(err.Error(), "another command") {
		t.Fatalf("idempotency key moved to a different action: %v", err)
	}
	state, _ := svc.State()
	if len(state.Checkpoints) != 0 {
		t.Fatalf("rejected idempotency collision created checkpoint: %+v", state.Checkpoints)
	}
}

func TestGraphChangeRequiresCapabilityAndActiveLease(t *testing.T) {
	svc, root := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"graph-boundary"},"spec":{"roles":[{"id":"governor","capabilities":["graph.change"]},{"id":"observer","capabilities":["observer"]}],"nodes":[],"edges":[]}}`)
	patchPath := filepath.Join(root, "patch.json")
	patch := `{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"addRole","role":{"id":"worker","capabilities":["node.run"]}},{"op":"addNode","node":{"id":"work","kind":"task","role":"worker","title":"work","outcomes":[{"id":"done","class":"success"}]}}]}`
	if err := os.WriteFile(patchPath, []byte(patch), 0o600); err != nil {
		t.Fatal(err)
	}
	impact, err := svc.PreviewGraphChange(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyGraphChange(patchPath, impact.Token, "apply-unleased", "governor"); err == nil || !strings.Contains(err.Error(), "active lease") {
		t.Fatalf("unleased graph actor changed authority: %v", err)
	}
	if _, err := svc.BindRole("observer", "codex", "observer-session", time.Hour, false, "bind-observer"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyGraphChange(patchPath, impact.Token, "apply-observer", "observer"); err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("actor without graph.change changed authority: %v", err)
	}
	if _, err := svc.BindRole("governor", "codex", "governor-session", time.Hour, false, "bind-governor"); err != nil {
		t.Fatal(err)
	}
	impact, err = svc.PreviewGraphChange(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyGraphChange(patchPath, impact.Token, "apply-governor", "governor"); err != nil {
		t.Fatalf("leased graph governor could not apply preview: %v", err)
	}
	if err := os.WriteFile(patchPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"removeNode","nodeId":"work"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyGraphChange(patchPath, impact.Token, "apply-governor", "governor"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("changed patch replay was silently folded into the old result: %v", err)
	}
}

type cancellingGatePolicy struct {
	metadata sdk.Metadata
	entered  chan struct{}
}

func (p *cancellingGatePolicy) Metadata() sdk.Metadata { return p.metadata }
func (*cancellingGatePolicy) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["policyId","input"],"properties":{"policyId":{"type":"string"},"input":{"type":"object"},"evidence":{"type":"array"}}}`)
}
func (p *cancellingGatePolicy) Decide(ctx context.Context, _ sdk.PolicyRequest) (sdk.PolicyDecision, error) {
	close(p.entered)
	<-ctx.Done()
	return sdk.PolicyDecision{}, ctx.Err()
}

func TestCancelledPolicyDecisionDoesNotCommitAuthorityState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "cancelled-policy")
	if err != nil {
		t.Fatal(err)
	}
	provider := &cancellingGatePolicy{metadata: sdk.Metadata{ID: "test.cancel", Version: "1.0.0", Stability: sdk.StabilityStable}, entered: make(chan struct{})}
	provider.metadata.SchemaHash, err = sdk.InputSchemaHash(provider.InputSchema())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Providers.RegisterPolicy(provider); err != nil {
		t.Fatal(err)
	}
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"cancelled-policy"},"spec":{"providers":[{"id":"test.cancel","version":"1.0.0","schemaHash":"` + provider.metadata.SchemaHash + `"}],"roles":[{"id":"controller","capabilities":["node.gate"]}],"nodes":[{"id":"quality","kind":"gate","role":"controller","title":"quality","decision":{"key":"quality","source":"provider","providerId":"test.cancel","policyId":"release"},"outcomes":[{"id":"pass","class":"success"}]}],"edges":[]}}`
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "import", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("controller", "codex", "controller-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "controller", "quality", "node.start"), json.RawMessage(`{}`), "start"); err != nil {
		t.Fatal(err)
	}
	ref := findActionRef(t, svc, "controller", "quality", "gate.evaluate")
	before, _ := svc.State()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, applyErr := svc.ApplyActionContext(ctx, ref, json.RawMessage(`{"input":{}}`), "evaluate")
		result <- applyErr
	}()
	<-provider.entered
	cancel()
	if err := <-result; err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("cancelled provider invocation did not propagate cancellation: %v", err)
	}
	after, _ := svc.State()
	if after.HeadHash != before.HeadHash || len(after.Decisions) != 0 || after.Nodes["quality"].Status != "active" {
		t.Fatalf("cancelled provider invocation committed state: before=%s after=%s decisions=%d runtime=%+v", before.HeadHash, after.HeadHash, len(after.Decisions), after.Nodes["quality"])
	}
}

func governanceService(t *testing.T, graph string) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "governance")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "import", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	return svc, root
}
