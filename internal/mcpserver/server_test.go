package mcpserver_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/controller"
	"github.com/CongBao/dagrail/internal/mcpserver"
	"github.com/CongBao/dagrail/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	contractschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type missingProjectExecutor struct{ calls atomic.Int32 }

func (executor *missingProjectExecutor) Execute(context.Context, []string, []byte) ([]byte, []byte, error) {
	executor.calls.Add(1)
	return nil, nil, &controller.RPCError{Code: "project_not_found", Message: ".dagrail/project.yaml was not found", Hint: "Pass an explicit root or initialize a project."}
}

type recordingExecutor struct {
	mu    sync.Mutex
	calls [][]string
}

func (executor *recordingExecutor) Execute(_ context.Context, args []string, _ []byte) ([]byte, []byte, error) {
	executor.mu.Lock()
	executor.calls = append(executor.calls, append([]string(nil), args...))
	executor.mu.Unlock()
	raw, _ := json.Marshal(service.GraphImpact{})
	return raw, nil, nil
}

func (executor *recordingExecutor) lastCall() []string {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.calls) == 0 {
		return nil
	}
	return append([]string(nil), executor.calls[len(executor.calls)-1]...)
}

func TestRemoteGraphChangeSeparatesPreviewAndApplyCLIContracts(t *testing.T) {
	executor := &recordingExecutor{}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := mcpserver.NewRemote(executor, ".")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "graph-change-transport-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	patch := map[string]any{"apiVersion": "dagrail.io/v1alpha1", "kind": "GraphPatch", "operations": []any{}}

	preview, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "dag_graph_change", Arguments: map[string]any{
		"mode": "preview", "patch": patch, "root": "/tmp/project", "actor_role": "controller", "actor_role_ref": "role-ref-that-preview-must-ignore",
	}})
	if err != nil || preview == nil || preview.IsError {
		t.Fatalf("preview failed before CLI dispatch: result=%+v err=%v", preview, err)
	}
	previewArgs := executor.lastCall()
	if len(previewArgs) != 6 || !reflect.DeepEqual(previewArgs[:5], []string{"graph", "preview-change", "--root", "/tmp/project", "--file"}) {
		t.Fatalf("preview CLI prefix = %v", previewArgs)
	}
	for _, forbidden := range []string{"--token", "--idempotency-key", "--actor-role", "--actor-role-ref"} {
		if slicesContain(previewArgs, forbidden) {
			t.Fatalf("preview forwarded apply-only flag %s: %v", forbidden, previewArgs)
		}
	}

	apply, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "dag_graph_change", Arguments: map[string]any{
		"mode": "apply", "patch": patch, "root": "/tmp/project", "token": "impact-token", "idempotency_key": "graph/apply/1", "actor_role": "controller",
	}})
	if err != nil || apply == nil || apply.IsError {
		t.Fatalf("apply failed before CLI dispatch: result=%+v err=%v", apply, err)
	}
	applyArgs := executor.lastCall()
	if len(applyArgs) != 12 || !reflect.DeepEqual(applyArgs[:5], []string{"graph", "apply-change", "--root", "/tmp/project", "--file"}) || !reflect.DeepEqual(applyArgs[6:], []string{"--token", "impact-token", "--idempotency-key", "graph/apply/1", "--actor-role", "controller"}) {
		t.Fatalf("apply CLI contract = %v", applyArgs)
	}

	_ = session.Close()
	cancel()
	<-done
}

func slicesContain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestRemoteMCPInitializesWithoutOpeningAProject(t *testing.T) {
	executor := &missingProjectExecutor{}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := mcpserver.NewRemote(executor, ".")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, serverTransport) }()
	started := time.Now()
	client := mcp.NewClient(&mcp.Implementation{Name: "lazy-bootstrap-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("project-independent initialize/tools-list exceeded 500ms: %v", elapsed)
	}
	if len(listed.Tools) != 6 || executor.calls.Load() != 0 {
		t.Fatalf("protocol initialization touched project state: tools=%d calls=%d", len(listed.Tools), executor.calls.Load())
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "dag_pre_wait", Arguments: map[string]any{}})
	if err == nil && (result == nil || !result.IsError) {
		t.Fatalf("missing project did not become a bounded tool error: result=%+v err=%v", result, err)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(errString(err)+string(raw), "project_not_found") || executor.calls.Load() != 1 {
		t.Fatalf("missing project diagnostic was not structured or lazy: calls=%d err=%v result=%s", executor.calls.Load(), err, raw)
	}
	_ = session.Close()
	cancel()
	<-done
}

func TestRemoteDagContextSchemaRejectsPerViewBudgetBeforeDispatch(t *testing.T) {
	executor := &missingProjectExecutor{}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := mcpserver.NewRemote(executor, ".")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "context-budget-schema-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "dag_context", Arguments: map[string]any{"view": "worker", "budget_bytes": 12000}})
	if err == nil && (result == nil || !result.IsError) {
		t.Fatalf("worker budget above 8192 was accepted: result=%+v err=%v", result, err)
	}
	if calls := executor.calls.Load(); calls != 0 {
		t.Fatalf("schema-invalid worker budget reached command dispatch: calls=%d", calls)
	}

	for _, arguments := range []map[string]any{
		{"view": "worker", "budget_bytes": 8192},
		{"view": "orchestrator", "budget_bytes": 12000},
		{"view": "reviewer", "budget_bytes": 12000},
	} {
		_, _ = session.CallTool(ctx, &mcp.CallToolParams{Name: "dag_context", Arguments: arguments})
	}
	if calls := executor.calls.Load(); calls != 3 {
		t.Fatalf("schema-valid per-view budgets did not reach command dispatch: calls=%d", calls)
	}

	_ = session.Close()
	cancel()
	<-done
}

func TestAdvertisedDagContextSchemaMatchesEveryRuntimeBudget(t *testing.T) {
	server := mcpserver.NewRemote(&missingProjectExecutor{}, ".")
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "context-budget-contract-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var inputSchema any
	for _, tool := range tools.Tools {
		if tool.Name == "dag_context" {
			inputSchema = tool.InputSchema
			break
		}
	}
	if inputSchema == nil {
		t.Fatal("dag_context input schema was not advertised")
	}
	raw, err := json.Marshal(inputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	compiler := contractschema.NewCompiler()
	compiler.DefaultDraft(contractschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:dag-context", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:dag-context")
	if err != nil {
		t.Fatal(err)
	}

	for _, limit := range service.ContextBudgetLimits() {
		for name, instance := range map[string]map[string]any{
			"default": {"view": limit.View},
			"maximum": {"view": limit.View, "budget_bytes": limit.Bytes},
		} {
			if err := schema.Validate(instance); err != nil {
				t.Fatalf("%s %s budget was rejected by advertised schema: %v", limit.View, name, err)
			}
		}
		if err := schema.Validate(map[string]any{"view": limit.View, "budget_bytes": limit.Bytes + 1}); err == nil {
			t.Fatalf("%s budget above runtime maximum %d was accepted by advertised schema", limit.View, limit.Bytes)
		}
	}
	if err := schema.Validate(map[string]any{"view": "worker", "budget_bytes": service.MinimumContextBudgetBytes - 1}); err == nil {
		t.Fatal("budget below runtime minimum was accepted by advertised schema")
	}

	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

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
