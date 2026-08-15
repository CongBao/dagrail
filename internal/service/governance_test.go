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
