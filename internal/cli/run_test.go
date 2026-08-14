package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/cli"
)

func TestUserCanInitializeImportGraphAndReadFrontier(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{
  "apiVersion":"dagrail.io/v1alpha1",
  "kind":"Graph",
  "metadata":{"name":"example"},
  "spec":{
    "roles":[{"id":"developer","capabilities":["node.run"]}],
    "nodes":[
      {"id":"A","kind":"task","role":"developer","title":"first","outcomes":[{"id":"success","class":"success"}]},
      {"id":"B","kind":"task","role":"developer","title":"second","outcomes":[{"id":"success","class":"success"}]}
    ],
    "edges":[{"id":"A-to-B","from":"A","to":"B","when":{"outcome":"success"}}]
  }
}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &stdout, &stderr)
		if err != nil && stderr.Len() > 0 {
			t.Log(stderr.String())
		}
		return stdout.String(), err
	}

	if _, err := run("init", "--root", root, "--name", "example"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "import-example"); err != nil {
		t.Fatalf("import: %v", err)
	}
	out, err := run("frontier", "--root", root, "--format", "json")
	if err != nil {
		t.Fatalf("frontier: %v", err)
	}
	if !strings.Contains(out, `"ready":["A"]`) {
		t.Fatalf("expected only A ready, got %s", out)
	}
}

func TestWorkerCanBindStartCheckpointFinishAndUnlockDependentNode(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{
  "apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"lifecycle"},
  "spec":{"roles":[{"id":"developer","capabilities":["node.run"]}],
    "nodes":[
      {"id":"A","kind":"task","role":"developer","title":"first","outcomes":[{"id":"success","class":"success"}]},
      {"id":"B","kind":"task","role":"developer","title":"second","outcomes":[{"id":"success","class":"success"}]}
    ],"edges":[{"id":"A-to-B","from":"A","to":"B","when":{"outcome":"success"}}]}}
`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &stdout, &stderr)
		if err != nil && stderr.Len() > 0 {
			t.Log(stderr.String())
		}
		return stdout.String(), err
	}
	if _, err := run("init", "--root", root, "--name", "lifecycle"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "developer", "--harness", "codex", "--session", "session-A", "--idempotency-key", "bind-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "developer", "--harness", "claude-code", "--session", "session-B", "--idempotency-key", "bind-B"); err == nil {
		t.Fatal("second live binding for the same stable role must fail")
	}

	startRef := allowedActionRef(t, run, root, "developer", "A", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", startRef, "--input", `{}`, "--idempotency-key", "start-A"); err != nil {
		t.Fatalf("start: %v", err)
	}
	checkpointRef := allowedActionRef(t, run, root, "developer", "A", "attempt.checkpoint")
	if _, err := run("action", "apply", "--root", root, "--ref", checkpointRef, "--input", `{"summary":"candidate prepared","evidenceRefs":[{"digest":"sha256:abc","type":"test-report","size":12}]}`, "--idempotency-key", "checkpoint-A"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	finishRef := allowedActionRef(t, run, root, "developer", "A", "attempt.finish")
	if _, err := run("action", "apply", "--root", root, "--ref", finishRef, "--input", `{"outcome":"success"}`, "--idempotency-key", "finish-A"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	out, err := run("frontier", "--root", root, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"ready":["B"]`) {
		t.Fatalf("expected B ready after A succeeds, got %s", out)
	}
	context, err := run("context", "--root", root, "--view", "worker", "--role", "developer", "--node", "A", "--budget-bytes", "8192")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(context, `"summary":"candidate prepared"`) || !strings.Contains(context, `"outcome":"success"`) {
		t.Fatalf("successor context must contain checkpoint and terminal result, got %s", context)
	}
}

func allowedActionRef(t *testing.T, run func(...string) (string, error), root, role, node, kind string) string {
	t.Helper()
	out, err := run("action", "list", "--root", root, "--role", role, "--node", node)
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	var value struct {
		Actions []struct{ Kind, Ref string } `json:"actions"`
	}
	if err := json.Unmarshal([]byte(out), &value); err != nil {
		t.Fatalf("decode actions: %v", err)
	}
	for _, action := range value.Actions {
		if action.Kind == kind {
			return action.Ref
		}
	}
	t.Fatalf("action %s not found in %s", kind, out)
	return ""
}

func TestGraphChangeRequiresImpactTokenAndProtectsActiveNodes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"changes"},"spec":{"roles":[{"id":"developer","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"developer","title":"A","outcomes":[{"id":"success","class":"success"}]},{"id":"B","kind":"task","role":"developer","title":"B","outcomes":[{"id":"success","class":"success"}]}],"edges":[{"id":"A-B","from":"A","to":"B","when":{"outcome":"success"}}]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(root, "patch.json")
	patch := `{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"addNode","node":{"id":"C","kind":"gate","role":"developer","title":"C","outcomes":[{"id":"pass","class":"success"}]}},{"op":"addEdge","edge":{"id":"B-C","from":"B","to":"C","when":{"outcome":"success"}}}]}`
	if err := os.WriteFile(patchPath, []byte(patch), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	if _, err := run("init", "--root", root); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph"); err != nil {
		t.Fatal(err)
	}
	preview, err := run("graph", "preview-change", "--root", root, "--file", patchPath)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	var impact struct{ Token, ProposedRevision string }
	if err := json.Unmarshal([]byte(preview), &impact); err != nil {
		t.Fatal(err)
	}
	if impact.Token == "" || impact.ProposedRevision == "" {
		t.Fatalf("preview must return a bound impact token: %s", preview)
	}
	if _, err := run("graph", "apply-change", "--root", root, "--file", patchPath, "--token", impact.Token, "--idempotency-key", "patch-1"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	exported, err := run("graph", "export", "--root", root, "--format", "json")
	if err != nil || !strings.Contains(exported, `"id":"C"`) {
		t.Fatalf("exported graph lacks C: %v %s", err, exported)
	}
	if _, err := run("graph", "apply-change", "--root", root, "--file", patchPath, "--token", impact.Token, "--idempotency-key", "patch-stale"); err == nil {
		t.Fatal("consumed/stale impact token must fail")
	}

	if _, err := run("role", "bind", "--root", root, "--role", "developer", "--harness", "codex", "--session", "session-A", "--idempotency-key", "bind"); err != nil {
		t.Fatal(err)
	}
	startRef := allowedActionRef(t, run, root, "developer", "A", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", startRef, "--idempotency-key", "start"); err != nil {
		t.Fatal(err)
	}
	updatePath := filepath.Join(root, "update-active.json")
	update := `{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"updateNode","node":{"id":"A","kind":"task","role":"developer","title":"changed","outcomes":[{"id":"success","class":"success"}]}}]}`
	if err := os.WriteFile(updatePath, []byte(update), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "preview-change", "--root", root, "--file", updatePath); err == nil {
		t.Fatal("active node contract must be frozen")
	}
}

func TestContextBudgetInspectAndPreWaitAreMachineDecidable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"context"},"spec":{"roles":[{"id":"developer","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"developer","title":"A","objective":"` + strings.Repeat("bounded-", 3000) + `","outcomes":[{"id":"success","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	if _, err := run("init", "--root", root); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph"); err != nil {
		t.Fatal(err)
	}
	audit, err := run("pre-wait", "--root", root)
	if err != nil || !strings.Contains(audit, `"safeToWait":false`) || !strings.Contains(audit, `"readyNodes":["A"]`) {
		t.Fatalf("ready work must reject passive wait: %v %s", err, audit)
	}
	context, err := run("context", "--root", root, "--view", "worker", "--node", "A", "--budget-bytes", "1024")
	if err != nil {
		t.Fatal(err)
	}
	if len(context) > 1025 || !strings.Contains(context, `"truncated":true`) {
		t.Fatalf("context must honor budget: %d %s", len(context), context)
	}
	inspected, err := run("inspect", "--root", root, "node:A")
	if err != nil || !strings.Contains(inspected, `"objective":"bounded-`) {
		t.Fatalf("inspect must provide opt-in detail: %v %s", err, inspected)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "developer", "--harness", "codex", "--session", "s", "--idempotency-key", "bind"); err != nil {
		t.Fatal(err)
	}
	startRef := allowedActionRef(t, run, root, "developer", "A", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", startRef, "--idempotency-key", "start"); err != nil {
		t.Fatal(err)
	}
	audit, err = run("pre-wait", "--root", root)
	if err != nil || !strings.Contains(audit, `"safeToWait":true`) {
		t.Fatalf("running attempt permits bounded yield: %v %s", err, audit)
	}
	submitRef := allowedActionRef(t, run, root, "developer", "A", "attempt.submit")
	if _, err := run("action", "apply", "--root", root, "--ref", submitRef, "--idempotency-key", "submit"); err != nil {
		t.Fatal(err)
	}
	audit, err = run("pre-wait", "--root", root)
	if err != nil || !strings.Contains(audit, `"safeToWait":false`) || !strings.Contains(audit, `"submittedAttempts"`) {
		t.Fatalf("submitted work must advance before wait: %v %s", err, audit)
	}
}

func TestManualEffectRemainsUnknownUntilRecipientVisibleReceiptIsReconciled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect"},"spec":{"roles":[{"id":"orchestrator","capabilities":["effect.dispatch"]}],"nodes":[{"id":"deliver","kind":"effect","role":"orchestrator","title":"deliver handoff","inputs":{"adapter":"manual","request":{"instruction":"Deliver work package to reviewer"}},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	if _, err := run("init", "--root", root); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "orchestrator", "--harness", "codex", "--session", "central", "--idempotency-key", "bind"); err != nil {
		t.Fatal(err)
	}
	startRef := allowedActionRef(t, run, root, "orchestrator", "deliver", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", startRef, "--idempotency-key", "start"); err != nil {
		t.Fatal(err)
	}
	prepareRef := allowedActionRef(t, run, root, "orchestrator", "deliver", "effect.prepare")
	prepared, err := run("action", "apply", "--root", root, "--ref", prepareRef, "--idempotency-key", "deliver-1")
	if err != nil {
		t.Fatalf("prepare effect: %v", err)
	}
	var result struct{ ActionID, Status string }
	if err := json.Unmarshal([]byte(prepared), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "unknown" || result.ActionID == "" {
		t.Fatalf("manual dispatch cannot self-confirm delivery: %s", prepared)
	}
	audit, err := run("pre-wait", "--root", root)
	if err != nil || !strings.Contains(audit, `"pendingEffects"`) || !strings.Contains(audit, `"safeToWait":false`) {
		t.Fatalf("unknown effect must block dependent control action: %v %s", err, audit)
	}
	if _, err := run("reconcile", "--root", root, "--action", result.ActionID, "--receipt", `{"externalId":"receipt-1","recipientVisible":true}`, "--idempotency-key", "reconcile-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	inspected, err := run("inspect", "--root", root, "effect:"+result.ActionID)
	if err != nil || !strings.Contains(inspected, `"status":"confirmed"`) {
		t.Fatalf("effect should be confirmed from visible receipt: %v %s", err, inspected)
	}
	if ref := allowedActionRef(t, run, root, "orchestrator", "deliver", "attempt.finish"); ref == "" {
		t.Fatal("confirmed effect should allow explicit terminal outcome")
	}
}
