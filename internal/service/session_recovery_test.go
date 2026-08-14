package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReplacementSessionRecoversActiveAttemptFromDurableCheckpoint(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "session-recovery")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"recovery"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"worker","title":"A","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "thread-old", time.Hour, false, "bind-old"); err != nil {
		t.Fatal(err)
	}
	start := findActionRef(t, svc, "worker", "A", "node.start")
	if _, err := svc.ApplyAction(start, json.RawMessage(`{}`), "start"); err != nil {
		t.Fatal(err)
	}
	checkpoint := findActionRef(t, svc, "worker", "A", "attempt.checkpoint")
	if _, err := svc.ApplyAction(checkpoint, json.RawMessage(`{"summary":"candidate sha256:abc is ready for tests"}`), "checkpoint"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReleaseRole("worker", "thread-old", "release-old"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "thread-new", time.Hour, false, "bind-new"); err != nil {
		t.Fatal(err)
	}

	contextBytes, err := svc.Context("worker", "worker", "A", 8192)
	if err != nil {
		t.Fatal(err)
	}
	contextText := string(contextBytes)
	if !strings.Contains(contextText, `"sessionId":"thread-new"`) || !strings.Contains(contextText, `"summary":"candidate sha256:abc is ready for tests"`) {
		t.Fatalf("replacement context lacks lease or checkpoint: %s", contextText)
	}
	actions, err := svc.ListActions("worker", "A")
	if err != nil || len(actions.Actions) == 0 {
		t.Fatalf("replacement session cannot continue the attempt: %+v %v", actions, err)
	}
	state, err := svc.State()
	if err != nil || state.Leases["worker"].SessionID != "thread-new" || len(state.Checkpoints) != 1 {
		t.Fatalf("durable replacement state mismatch: %+v %v", state.Leases["worker"], err)
	}
}
