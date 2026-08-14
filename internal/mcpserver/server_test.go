package mcpserver_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

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
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
}
