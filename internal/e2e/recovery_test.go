package e2e_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/cli"
	"github.com/CongBao/dagrail/internal/service"
)

func TestJournalIsAuthoritativeIdempotentAndTamperEvident(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"recovery"},"spec":{"roles":[{"id":"dev","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"dev","title":"A","outcomes":[{"id":"ok","class":"success"}]}],"edges":[]}}`
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
	first, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "same")
	if err != nil {
		t.Fatal(err)
	}
	second, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "same")
	if err != nil {
		t.Fatal(err)
	}
	var firstResult, secondResult map[string]any
	_ = json.Unmarshal([]byte(first), &firstResult)
	_ = json.Unmarshal([]byte(second), &secondResult)
	if firstResult["sequence"] != secondResult["sequence"] {
		t.Fatalf("idempotent import changed result: %s %s", first, second)
	}
	verified, err := run("journal", "verify", "--root", root)
	if err != nil || !strings.Contains(verified, `"segments":1`) {
		t.Fatalf("verify: %v %s", err, verified)
	}
	svc, err := service.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.Project.DataDir, "journal", ".interrupted.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(filepath.Join(svc.Project.DataDir, "projection.sqlite"+suffix))
	}
	frontier, err := run("frontier", "--root", root, "--format", "json")
	if err != nil || !strings.Contains(frontier, `"ready":["A"]`) {
		t.Fatalf("projection did not rebuild from journal: %v %s", err, frontier)
	}
	entries, err := os.ReadDir(filepath.Join(svc.Project.DataDir, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	var segmentPath string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			segmentPath = filepath.Join(svc.Project.DataDir, "journal", entry.Name())
		}
	}
	data, err := os.ReadFile(segmentPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "graph.imported", "graph.tampered", 1))
	if err := os.WriteFile(segmentPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run("journal", "verify", "--root", root); err == nil {
		t.Fatal("tampered journal was accepted")
	}
}

func TestCorruptSQLiteProjectionIsQuarantinedAndRebuiltFromJournal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "projection recovery")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"projection recovery"},"spec":{"roles":[{"id":"dev","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"dev","title":"A","outcomes":[{"id":"ok","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "import", "dev"); err != nil {
		t.Fatal(err)
	}
	projectionPath := filepath.Join(svc.Project.DataDir, "projection.sqlite")
	if err := os.WriteFile(projectionPath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := recovered.State()
	if err != nil || state.GraphRevision == "" || len(state.Nodes) != 1 || state.Nodes["A"].Status != "planned" {
		t.Fatalf("projection recovery lost journal state: %#v %v", state, err)
	}
	entries, err := os.ReadDir(svc.Project.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	quarantined := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "projection.sqlite.corrupt-") {
			quarantined = true
		}
	}
	if !quarantined {
		t.Fatal("corrupt SQLite projection was not preserved for diagnosis")
	}
}

func TestDeterministicJoinSettlesWithoutExecutor(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "automatic")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "automatic.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"automatic"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"join","kind":"join","title":"join","outcomes":[{"id":"joined","class":"success"}]},{"id":"work","kind":"task","role":"worker","title":"work","outcomes":[{"id":"done","class":"success"}]}],"edges":[{"id":"after-join","from":"join","to":"work","when":{"outcome":"joined"}}]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "import-auto", ""); err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Nodes["join"].Status != "terminal" || state.Nodes["join"].Outcome != "joined" {
		t.Fatalf("join was not settled: %#v", state.Nodes["join"])
	}
	frontier, err := svc.Frontier()
	if err != nil {
		t.Fatal(err)
	}
	if len(frontier.Ready) != 1 || frontier.Ready[0] != "work" {
		t.Fatalf("automatic join did not unlock work: %#v", frontier)
	}
}

func TestCorruptSQLiteProjectionIsRebuiltFromJournal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "sqlite recovery")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"sqlite recovery"},"spec":{"roles":[{"id":"dev","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"dev","title":"A","outcomes":[{"id":"ok","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "import", "dev"); err != nil {
		t.Fatal(err)
	}
	projectionPath := filepath.Join(svc.Project.DataDir, "projection.sqlite")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(projectionPath + suffix)
	}
	if err := os.WriteFile(projectionPath, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	frontier, err := recovered.Frontier()
	if err != nil {
		t.Fatal(err)
	}
	if len(frontier.Ready) != 1 || frontier.Ready[0] != "A" {
		t.Fatalf("projection recovery lost journal state: %#v", frontier)
	}
	matches, err := filepath.Glob(projectionPath + ".corrupt-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("corrupt projection was not preserved: %v %#v", err, matches)
	}
}
