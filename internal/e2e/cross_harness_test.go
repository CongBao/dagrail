package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/service"
)

func TestThreeHarnessBindingsCompleteIndependentAttempts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "three harnesses")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"three harnesses"},"spec":{"roles":[{"id":"codex-worker","capabilities":["node.run"]},{"id":"claude-worker","capabilities":["node.run"]},{"id":"copilot-worker","capabilities":["node.run"]}],"nodes":[{"id":"codex-node","kind":"task","role":"codex-worker","title":"Codex","outcomes":[{"id":"done","class":"success"}]},{"id":"claude-node","kind":"task","role":"claude-worker","title":"Claude","outcomes":[{"id":"done","class":"success"}]},{"id":"copilot-node","kind":"task","role":"copilot-worker","title":"Copilot","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "import", ""); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ harness, role, node string }{
		{"codex", "codex-worker", "codex-node"},
		{"claude-code", "claude-worker", "claude-node"},
		{"copilot-cli", "copilot-worker", "copilot-node"},
	}
	for _, item := range cases {
		session := item.harness + "-session"
		if _, err := svc.BindRole(item.role, item.harness, session, time.Hour, false, "bind/"+item.harness); err != nil {
			t.Fatal(err)
		}
		start := actionRef(t, svc, item.role, item.node, "node.start")
		if _, err := svc.ApplyAction(start, json.RawMessage(`{}`), "start/"+item.harness); err != nil {
			t.Fatal(err)
		}
		submit := actionRef(t, svc, item.role, item.node, "attempt.submit")
		if _, err := svc.ApplyAction(submit, json.RawMessage(`{}`), "submit/"+item.harness); err != nil {
			t.Fatal(err)
		}
		finish := actionRef(t, svc, item.role, item.node, "task.complete")
		if _, err := svc.ApplyAction(finish, json.RawMessage(`{"outcome":"done"}`), "finish/"+item.harness); err != nil {
			t.Fatal(err)
		}
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range cases {
		if runtime := state.Nodes[item.node]; runtime.Status != "terminal" || runtime.Outcome != "done" {
			t.Fatal(fmt.Sprintf("%s did not complete its attempt: %#v", item.harness, runtime))
		}
	}
}

func TestRoleLeaseRejectsSplitBrainAndSuccessorReadsCheckpoint(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "takeover")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return base }
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"takeover"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"work","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "import", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "session-1", time.Minute, false, "bind-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "claude-code", "session-2", time.Minute, false, "split-brain"); err == nil {
		t.Fatal("a second live session acquired the same stable role")
	}
	start := actionRef(t, svc, "worker", "work", "node.start")
	if _, err := svc.ApplyAction(start, json.RawMessage(`{}`), "start"); err != nil {
		t.Fatal(err)
	}
	checkpoint := actionRef(t, svc, "worker", "work", "attempt.checkpoint")
	if _, err := svc.ApplyAction(checkpoint, json.RawMessage(`{"summary":"candidate built; run focused review next"}`), "checkpoint"); err != nil {
		t.Fatal(err)
	}
	svc.Now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, err := svc.BindRole("worker", "claude-code", "session-2", time.Minute, true, "takeover"); err != nil {
		t.Fatal(err)
	}
	contextRaw, err := svc.Context("worker", "worker", "work", 8192)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(contextRaw, []byte("candidate built; run focused review next")) || bytes.Contains(contextRaw, []byte("session-1")) {
		t.Fatalf("successor context did not isolate the durable checkpoint: %s", contextRaw)
	}
}
