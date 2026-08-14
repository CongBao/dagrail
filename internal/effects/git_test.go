package effects_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/cli"
)

func TestGitMergeEffectCreatesOneAuditableMergeAndReconcilesWithoutRepeating(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", t.TempDir())
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Test")
	git(t, root, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "README.md")
	git(t, root, "commit", "-m", "base")
	git(t, root, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(root, "feature.txt"), []byte("candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "feature.txt")
	git(t, root, "commit", "-m", "feature")
	candidate := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	git(t, root, "checkout", "main")

	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	if _, err := run("init", "--root", root, "--name", "git-effect"); err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := map[string]any{
		"apiVersion": "dagrail.io/v1alpha1", "kind": "Graph", "metadata": map[string]any{"name": "git-effect"},
		"spec": map[string]any{"roles": []any{map[string]any{"id": "integrator", "capabilities": []string{"effect.dispatch"}}}, "edges": []any{}, "nodes": []any{map[string]any{
			"id": "merge", "kind": "effect", "role": "integrator", "title": "merge candidate", "outcomes": []any{map[string]any{"id": "merged", "class": "success"}},
			"inputs": map[string]any{"adapter": "git.merge", "request": map[string]any{"repository": root, "targetBranch": "main", "candidate": candidate, "strategy": "merge-commit"}},
		}},
		},
	}
	graphRaw, _ := json.Marshal(graph)
	if err := os.WriteFile(graphPath, graphRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".dagrail/project.yaml", "graph.json")
	git(t, root, "commit", "-m", "configure DAGrail")
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "integrator", "--harness", "codex", "--session", "central", "--idempotency-key", "bind"); err != nil {
		t.Fatal(err)
	}
	start := actionRef(t, run, root, "integrator", "merge", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", start, "--idempotency-key", "start"); err != nil {
		t.Fatal(err)
	}
	prepare := actionRef(t, run, root, "integrator", "merge", "effect.prepare")
	resultRaw, err := run("action", "apply", "--root", root, "--ref", prepare, "--idempotency-key", "merge-action")
	if err != nil {
		t.Fatalf("merge effect: %v", err)
	}
	var result struct{ ActionID, Status string }
	if err := json.Unmarshal([]byte(resultRaw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "confirmed" {
		t.Fatalf("merge was not confirmed: %s", resultRaw)
	}
	mergeCommit := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	parents := strings.Fields(strings.TrimSpace(git(t, root, "show", "-s", "--format=%P", "HEAD")))
	if len(parents) != 2 || parents[1] != candidate {
		t.Fatalf("expected exact two-parent merge, got %v", parents)
	}
	message := git(t, root, "show", "-s", "--format=%B", "HEAD")
	if !strings.Contains(message, "DAGrail-Action: "+result.ActionID) {
		t.Fatalf("merge commit lacks action trailer: %s", message)
	}
	if _, err := run("reconcile", "--root", root, "--action", result.ActionID, "--idempotency-key", "reconcile-git"); err != nil {
		t.Fatal(err)
	}
	if after := strings.TrimSpace(git(t, root, "rev-parse", "HEAD")); after != mergeCommit {
		t.Fatalf("reconcile repeated merge: before %s after %s", mergeCommit, after)
	}
}

func actionRef(t *testing.T, run func(...string) (string, error), root, role, node, kind string) string {
	t.Helper()
	raw, err := run("action", "list", "--root", root, "--role", role, "--node", node)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Actions []struct{ Kind, Ref string } `json:"actions"`
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	for _, action := range value.Actions {
		if action.Kind == kind {
			return action.Ref
		}
	}
	t.Fatalf("missing %s in %s", kind, raw)
	return ""
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}
