package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/domain"
)

func TestSummaryTopologyCoversMaximumCompactGroupDisclosureWithoutJournalFixture(t *testing.T) {
	state := domain.NewState("project")
	graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "compact disclosure"}}
	for index := 0; index < domain.MaxGraphGroups; index++ {
		groupID := fmt.Sprintf("组-%03d-%s", index, strings.Repeat("界", 52))
		nodeID := fmt.Sprintf("node-%03d", index)
		graph.Spec.Groups = append(graph.Spec.Groups, domain.GroupDefinition{ID: groupID, Title: groupID, Kind: "work-unit", SummaryNodeID: nodeID, CollapsedByDefault: true})
		graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: nodeID, Kind: "milestone", Title: nodeID, GroupID: groupID, Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}})
		state.Nodes[nodeID] = domain.NodeRuntime{Status: "planned"}
	}
	state.Graph = &graph

	expanded, err := buildSummaryTopology(state, nodeFilter{}, "", 2, maxTopologyNodes, "expanded", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(expanded.ExpandedGroupIDs) != domain.MaxGraphGroups || len(expanded.Nodes) != domain.MaxGraphGroups {
		t.Fatalf("compact expand-all lost legal groups: groups=%d nodes=%d", len(expanded.ExpandedGroupIDs), len(expanded.Nodes))
	}
	exception, err := buildSummaryTopology(state, nodeFilter{}, "", 2, maxTopologyNodes, "expanded", nil, []string{graph.Spec.Groups[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(exception.ExpandedGroupIDs) != domain.MaxGraphGroups-1 || len(exception.Nodes) != domain.MaxGraphGroups-1 {
		t.Fatalf("per-group exception did not override compact disclosure: expanded=%d nodes=%d", len(exception.ExpandedGroupIDs), len(exception.Nodes))
	}
	collapsed, err := buildSummaryTopology(state, nodeFilter{}, "", 2, maxTopologyNodes, "collapsed", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(collapsed.ExpandedGroupIDs) != 0 || len(collapsed.Nodes) != 0 || len(collapsed.Groups) != domain.MaxGraphGroups {
		t.Fatalf("compact collapse-all lost groups or exposed nodes: groups=%d nodes=%d expanded=%d", len(collapsed.Groups), len(collapsed.Nodes), len(collapsed.ExpandedGroupIDs))
	}
}

func TestDenseAggregateEdgeProjectionIsBoundedWithoutJournalFixture(t *testing.T) {
	state := domain.NewState("project")
	graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "dense edges"}}
	const groupCount = 135
	for index := 0; index < groupCount; index++ {
		groupID := fmt.Sprintf("group-%03d", index)
		nodeID := fmt.Sprintf("node-%03d", index)
		graph.Spec.Groups = append(graph.Spec.Groups, domain.GroupDefinition{ID: groupID, Title: groupID, Kind: "work-unit", SummaryNodeID: nodeID, CollapsedByDefault: true})
		graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: nodeID, Kind: "milestone", Title: nodeID, GroupID: groupID, Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}})
		state.Nodes[nodeID] = domain.NodeRuntime{Status: "planned"}
	}
	for from := 0; from < groupCount; from++ {
		for to := from + 1; to < groupCount; to++ {
			graph.Spec.Edges = append(graph.Spec.Edges, domain.EdgeDefinition{ID: fmt.Sprintf("edge-%03d-%03d", from, to), From: fmt.Sprintf("node-%03d", from), To: fmt.Sprintf("node-%03d", to), When: domain.Predicate{Outcome: "done"}})
		}
	}
	state.Graph = &graph
	topology, err := buildSummaryTopology(state, nodeFilter{}, "", 2, maxTopologyNodes, "collapsed", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := groupCount * (groupCount - 1) / 2
	if topology.AggregateEdgePage == nil || topology.AggregateEdgePage.Total != want || len(topology.AggregateEdges) != maxAggregateEdgePage || topology.AggregateEdgeIndexRef == "" {
		t.Fatalf("dense aggregate projection is not bounded: page=%+v inline=%d ref=%q", topology.AggregateEdgePage, len(topology.AggregateEdges), topology.AggregateEdgeIndexRef)
	}
}

func TestExplorerResponseCapFailsClosedBeforeWriting(t *testing.T) {
	recorder := httptest.NewRecorder()
	err := writeAPIResult(recorder, map[string]string{"value": strings.Repeat("x", maxAPIResponse)}, nil)
	var bounded *explorerError
	if !asExplorerError(err, &bounded) || bounded.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized explorer response did not fail closed: %T %v", err, err)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("oversized explorer response wrote partial bytes: %d", recorder.Body.Len())
	}
}

func TestResourceViewOmitsClosureReceipt(t *testing.T) {
	view := resourceView(domain.ResourceLease{ID: "resource", Kind: "browser", Quantity: 1, NodeID: "node", AttemptID: "attempt", Status: "active", ClosureStatus: "unknown", ClosureReceipt: json.RawMessage(`{"private":"must-not-leak"}`)})
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-not-leak") || strings.Contains(string(raw), "closureReceipt") || !strings.Contains(string(raw), `"closureStatus":"unknown"`) {
		t.Fatalf("resource view leaked receipt or lost typed status: %s", raw)
	}
}

func TestNodeDetailCollectionsAreBounded(t *testing.T) {
	values := make([]int, maxNodeDetailItems+7)
	truncated := map[string]bool{}
	first := capFirst(values, maxNodeDetailItems, truncated, "first")
	last := capLast(values, maxNodeDetailItems, truncated, "last")
	if len(first) != maxNodeDetailItems || len(last) != maxNodeDetailItems || !truncated["first"] || !truncated["last"] {
		t.Fatalf("node detail cap failed: first=%d last=%d truncated=%v", len(first), len(last), truncated)
	}
}
