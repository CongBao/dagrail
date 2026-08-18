package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const readmeQuickStartGraph = `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"example"},"spec":{"roles":[{"id":"developer","capabilities":["node.run"]}],"nodes":[{"id":"implement","kind":"task","role":"developer","title":"Implement the change","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`

func TestREADMEQuickStartRunsFromAnEmptyProject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAGRAIL_HOME", filepath.Join(t.TempDir(), "runtime"))
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "cat > graph.json <<'JSON'\n"+readmeQuickStartGraph+"\nJSON") {
		t.Fatal("README quick-start graph diverged from the executable release test")
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(readmeQuickStartGraph), 0o600); err != nil {
		t.Fatal(err)
	}
	runReadmeCommand(t, "init", "--root", root, "--name", "example")
	runReadmeCommand(t, "graph", "validate", "--file", graphPath)
	runReadmeCommand(t, "graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "import-v1")
	runReadmeCommand(t, "frontier", "--root", root)
	runReadmeCommand(t, "role", "bind", "--root", root, "--role", "developer", "--harness", "codex", "--session", "quickstart", "--ttl", "15m", "--idempotency-key", "bind-developer")
	runReadmeCommand(t, "context", "--root", root, "--view", "worker", "--role", "developer", "--node", "implement")
	runReadmeCommand(t, "pre-wait", "--root", root)

	startRef := readmeActionRef(t, root, "node.start")
	runReadmeCommand(t, "action", "apply", "--root", root, "--ref", startRef, "--input", `{}`, "--idempotency-key", "start-implement-1")
	checkpointRef := readmeActionRef(t, root, "attempt.checkpoint")
	runReadmeCommand(t, "action", "apply", "--root", root, "--ref", checkpointRef, "--input", `{"summary":"durable checkpoint"}`, "--idempotency-key", "checkpoint-1")
}

func TestCLIContextSupportsMaximumMCPRoleIDAtMinimumBudget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAGRAIL_HOME", filepath.Join(t.TempDir(), "runtime"))
	roleID := strings.Repeat("r", 256)
	roles := []map[string]any{{"id": roleID, "capabilities": []string{"node.run"}}}
	outcomes := []map[string]any{{"id": "done", "class": "success"}}
	nodes := []map[string]any{{"id": "task", "kind": "task", "role": roleID, "title": "task", "outcomes": outcomes}}
	graph := map[string]any{
		"apiVersion": "dagrail.io/v1alpha1",
		"kind":       "Graph",
		"metadata":   map[string]any{"name": "long-role-cli"},
		"spec": map[string]any{
			"roles": roles,
			"nodes": nodes,
			"edges": []any{},
		},
	}
	rawGraph, _ := json.Marshal(graph)
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, rawGraph, 0o600); err != nil {
		t.Fatal(err)
	}
	runReadmeCommand(t, "init", "--root", root, "--name", "long-role-cli")
	runReadmeCommand(t, "graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "import")
	runReadmeCommand(t, "role", "bind", "--root", root, "--role", roleID, "--harness", "codex", "--session", "session", "--ttl", "15m", "--idempotency-key", "bind")
	contextRaw := runReadmeCommand(t, "context", "--root", root, "--view", "worker", "--role", roleID, "--node", "task", "--budget-bytes", "512")
	if len(contextRaw) > 513 || strings.Contains(string(contextRaw), roleID) {
		t.Fatalf("CLI context did not bound a long Role ID: %s", contextRaw)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(contextRaw, &envelope); err != nil {
		t.Fatal(err)
	}
	operationsRef, ok := envelope.Data["operationsRef"].(string)
	if !ok || len(operationsRef) != len("operations:")+64 {
		t.Fatalf("CLI context omitted the opaque operations ref: %s", contextRaw)
	}
	inspectRaw := runReadmeCommand(t, "inspect", "--root", root, operationsRef)
	if !strings.Contains(string(inspectRaw), `"roleId":"`+roleID+`"`) {
		t.Fatalf("opaque operations ref did not recover the full Role: %s", inspectRaw)
	}
}

func readmeActionRef(t *testing.T, root, kind string) string {
	t.Helper()
	raw := runReadmeCommand(t, "action", "list", "--root", root, "--role", "developer", "--node", "implement")
	var result struct {
		Actions []struct {
			Kind string `json:"kind"`
			Ref  string `json:"ref"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	for _, action := range result.Actions {
		if action.Kind == kind {
			return action.Ref
		}
	}
	t.Fatalf("README action %s was not available: %s", kind, raw)
	return ""
}

func runReadmeCommand(t *testing.T, args ...string) []byte {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := Run(args, bytes.NewReader(nil), &stdout, &stderr); err != nil {
		t.Fatalf("dagrail %v: %v\n%s", args, err, stderr.String())
	}
	return stdout.Bytes()
}
