package mcpserver_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/mcpserver"
	"github.com/CongBao/dagrail/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerExposesOnlySixHighLevelTypedTools(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	roleID := strings.Repeat("r", 30_000)
	nodeID := strings.Repeat("n", 30_000)
	graphPath := filepath.Join(root, "graph.json")
	graph := strings.ReplaceAll(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"mcp"},"spec":{"roles":[{"id":"__ROLE__","capabilities":["node.run"]}],"nodes":[{"id":"__NODE__","kind":"task","role":"__ROLE__","title":"task","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`, "__ROLE__", roleID)
	graph = strings.ReplaceAll(graph, "__NODE__", nodeID)
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph/import", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole(roleID, "codex", "session", time.Hour, false, "role/bind"); err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := mcpserver.New(svc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "dagrail-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"dag_context": true, "dag_inspect": true, "dag_apply": true, "dag_graph_change": true, "dag_reconcile": true, "dag_pre_wait": true}
	if len(tools.Tools) != len(want) {
		t.Fatalf("tool count = %d, want %d", len(tools.Tools), len(want))
	}
	inputBytes := 0
	for _, tool := range tools.Tools {
		if !want[tool.Name] {
			t.Fatalf("unexpected tool %s", tool.Name)
		}
		if tool.InputSchema == nil || tool.OutputSchema == nil {
			t.Fatalf("tool %s lacks typed schemas", tool.Name)
		}
		raw, _ := json.Marshal(tool.InputSchema)
		inputBytes += len(raw)
	}
	if inputBytes > 6144 {
		t.Fatalf("MCP input schemas consume %d bytes, budget 6144", inputBytes)
	}
	invalid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "dag_graph_change", Arguments: map[string]any{"mode": "replace", "patch": map[string]any{}}})
	if err == nil && (invalid == nil || !invalid.IsError) {
		t.Fatalf("unknown graph change mode was accepted: result=%+v err=%v", invalid, err)
	}
	for _, arguments := range []map[string]any{
		{"view": "admin", "budget_bytes": 512},
		{"view": "worker", "budget_bytes": 8193},
		{"view": "orchestrator", "budget_bytes": 12289},
	} {
		invalid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "dag_context", Arguments: arguments})
		if err == nil && (invalid == nil || !invalid.IsError) {
			t.Fatalf("unclosed context input was accepted: arguments=%v result=%+v err=%v", arguments, invalid, err)
		}
	}
	before := runtimeBytes(t, svc.Project.DataDir)
	roleRef, err := svc.EntityRef("role", roleID)
	if err != nil {
		t.Fatal(err)
	}
	nodeRef, err := svc.EntityRef("node", nodeID)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "dag_context", Arguments: map[string]any{"view": "worker", "role_ref": roleRef, "node_ref": nodeRef, "budget_bytes": 512}})
	if err != nil || valid == nil || valid.IsError {
		t.Fatalf("valid dag_context failed: result=%+v err=%v", valid, err)
	}
	after := runtimeBytes(t, svc.Project.DataDir)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("dag_context mutated project runtime bytes")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
}

func runtimeBytes(t *testing.T, root string) map[string][sha256.Size]byte {
	t.Helper()
	result := map[string][sha256.Size]byte{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = sha256.Sum256(raw)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}
