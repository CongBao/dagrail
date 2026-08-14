package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
)

func TestLargeReadyFrontierAlwaysProducesBoundedInspectableContext(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	service, err := Init(root, "scale")
	if err != nil {
		t.Fatal(err)
	}
	graph := scaleGraph(2048)
	graphPath := filepath.Join(root, "graph.json")
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(graphPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := service.ImportGraph(graphPath, "scale-import", ""); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("2048-node import exceeded qualification bound: %s", elapsed)
	}
	for view, budget := range map[string]int{"orchestrator": 12288, "worker": 8192, "reviewer": 12288} {
		context, err := service.Context(view, "", "", budget)
		if err != nil {
			t.Fatalf("%s context: %v", view, err)
		}
		if len(context) > budget {
			t.Fatalf("%s context used %d bytes, budget %d", view, len(context), budget)
		}
		var envelope struct {
			Truncated   bool     `json:"truncated"`
			InspectRefs []string `json:"inspectRefs"`
			Data        struct {
				Frontier struct {
					Ready          []string `json:"ready"`
					ReadyCount     int      `json:"readyCount"`
					ReadyTruncated bool     `json:"readyTruncated"`
				} `json:"frontier"`
			} `json:"data"`
		}
		if err := json.Unmarshal(context, &envelope); err != nil {
			t.Fatal(err)
		}
		if !envelope.Truncated || !envelope.Data.Frontier.ReadyTruncated || envelope.Data.Frontier.ReadyCount != 2048 || len(envelope.Data.Frontier.Ready) == 0 {
			t.Fatalf("%s context lost bounded frontier summary: %#v", view, envelope)
		}
		if len(envelope.InspectRefs) == 0 || envelope.InspectRefs[0] != "frontier" {
			t.Fatalf("%s context has no usable frontier ref: %#v", view, envelope.InspectRefs)
		}
	}
	inspected, err := service.Inspect("frontier")
	if err != nil {
		t.Fatal(err)
	}
	frontier, ok := inspected.(domain.Frontier)
	if !ok || len(frontier.Ready) != 2048 {
		t.Fatalf("frontier inspection returned %T with %d ready", inspected, len(frontier.Ready))
	}
}

func TestDefinitionInputsHaveAnExplicitSizeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversize.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxDefinitionBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeGraph(path); err == nil {
		t.Fatal("oversized graph input was accepted")
	}
	if _, _, err := decodeGraphPatch(path); err == nil {
		t.Fatal("oversized graph patch input was accepted")
	}
}

func FuzzDecodeGraphDefinition(f *testing.F) {
	f.Add([]byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"fuzz"},"spec":{"roles":[],"nodes":[]}}`))
	f.Add([]byte("apiVersion: dagrail.io/v1alpha1\nkind: Graph\nmetadata:\n  name: fuzz\nspec:\n  roles: []\n  nodes: []\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 256*1024 {
			t.Skip()
		}
		graph, err := decodeGraphBytes(raw)
		if err == nil {
			_ = domain.ValidateGraph(graph)
		}
	})
}

func FuzzDecodeGraphPatch(f *testing.F) {
	f.Add([]byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"removeNode","nodeId":"B"}]}`))
	f.Add([]byte("apiVersion: dagrail.io/v1alpha1\nkind: GraphPatch\noperations:\n  - op: removeNode\n    nodeId: B\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 256*1024 {
			t.Skip()
		}
		patch, _, err := decodeGraphPatchBytes(raw)
		if err != nil {
			return
		}
		state := domain.NewState("fuzz")
		graph := scaleGraph(2)
		state.Graph = &graph
		state.Nodes["node-0000"] = domain.NodeRuntime{Status: "planned"}
		state.Nodes["node-0001"] = domain.NodeRuntime{Status: "planned"}
		_, _, _, _ = applyGraphPatch(state, patch)
	})
}

func scaleGraph(count int) domain.GraphDefinition {
	graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "scale"}}
	graph.Spec.Roles = []domain.RoleDefinition{{ID: "dev", Capabilities: []string{"node.run"}}}
	for index := range count {
		graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{
			ID:       fmt.Sprintf("node-%04d", index),
			Kind:     "task",
			Role:     "dev",
			Title:    fmt.Sprintf("Node %d", index),
			Outcomes: []domain.Outcome{{ID: "ok", Class: "success"}},
		})
	}
	return graph
}
