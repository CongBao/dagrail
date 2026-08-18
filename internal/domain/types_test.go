package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFrontierEvaluatesClosedTypedFacts(t *testing.T) {
	graph := GraphDefinition{
		APIVersion: GraphAPIVersion,
		Kind:       GraphKind,
		Metadata:   GraphMetadata{Name: "facts"},
		Spec: GraphSpec{
			Roles: []RoleDefinition{{ID: "reviewer", Capabilities: []string{CapabilityNodeReview}}},
			Nodes: []NodeDefinition{
				{ID: "review", Kind: "review", Role: "reviewer", Title: "review", Outcomes: []Outcome{{ID: "approved", Class: "success"}}},
				{ID: "merge", Kind: "join", Title: "merge", Outcomes: []Outcome{{ID: "done", Class: "success"}}},
			},
			Edges: []EdgeDefinition{{
				ID: "review-to-merge", From: "review", To: "merge",
				When: Predicate{All: []Predicate{
					{Decision: &ValueMatch{Key: "verdict", Value: "approve"}},
					{Evidence: &ValueMatch{Key: "artifact", Value: "inspected"}},
					{Policy: &ValueMatch{Key: "admission", Value: "pass"}},
				}},
			}},
		},
	}
	if err := ValidateGraph(graph); err != nil {
		t.Fatal(err)
	}
	state := NewState("project")
	state.Graph = &graph
	state.Nodes["review"] = NodeRuntime{Status: "terminal", Outcome: "approved", Facts: PredicateFacts{
		Decision: map[string]string{"verdict": "approve"},
		Evidence: map[string]string{"artifact": "inspected"},
		Policy:   map[string]string{"admission": "pass"},
	}}
	state.Nodes["merge"] = NodeRuntime{Status: "planned"}
	frontier := ComputeFrontier(state)
	if len(frontier.Ready) != 1 || frontier.Ready[0] != "merge" {
		t.Fatalf("typed predicate was not satisfied: %#v", frontier)
	}
}

func TestAuthorityJSONRejectsDuplicateKeysAndExcessiveNesting(t *testing.T) {
	if err := ValidateAuthorityJSON(json.RawMessage(`{"kind":"first","kind":"second"}`)); err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("duplicate authority key was accepted: %v", err)
	}
	nested := strings.Repeat(`{"child":`, MaxAuthorityDepth+2) + `null` + strings.Repeat(`}`, MaxAuthorityDepth+2)
	if err := ValidateAuthorityJSON(json.RawMessage(nested)); err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("excessively nested authority JSON was accepted: %v", err)
	}
}

func TestPredicateFactRequiresKeyAndValue(t *testing.T) {
	source := NodeDefinition{ID: "source", Outcomes: []Outcome{{ID: "ok", Class: "success"}}}
	if err := validatePredicate(Predicate{Decision: &ValueMatch{}}, source); err == nil {
		t.Fatal("empty decision fact was accepted")
	}
}

func TestFrontierHonorsResourceCapacity(t *testing.T) {
	graph := GraphDefinition{
		APIVersion: GraphAPIVersion,
		Kind:       GraphKind,
		Metadata:   GraphMetadata{Name: "resources"},
		Spec: GraphSpec{
			Roles:              []RoleDefinition{{ID: "worker", Capabilities: []string{CapabilityNodeRun, CapabilityResourceClose}}},
			ResourceCapacities: []ResourceCapacity{{Kind: "browser", Capacity: 1}},
			Nodes: []NodeDefinition{
				{ID: "A", Kind: "task", Role: "worker", Title: "A", Resources: []ResourceRequest{{Kind: "browser", Quantity: 1}}, Outcomes: []Outcome{{ID: "done", Class: "success"}}},
				{ID: "B", Kind: "task", Role: "worker", Title: "B", Resources: []ResourceRequest{{Kind: "browser", Quantity: 1}}, Outcomes: []Outcome{{ID: "done", Class: "success"}}},
			},
		},
	}
	if err := ValidateGraph(graph); err != nil {
		t.Fatal(err)
	}
	state := NewState("project")
	state.Graph = &graph
	state.Nodes["A"] = NodeRuntime{Status: "active"}
	state.Nodes["B"] = NodeRuntime{Status: "planned"}
	state.Resources["lease"] = ResourceLease{ID: "lease", Kind: "browser", Quantity: 1, NodeID: "A", AttemptID: "attempt", Status: "active"}
	frontier := ComputeFrontier(state)
	if len(frontier.Ready) != 0 || len(frontier.ResourceBlocked) != 1 || frontier.ResourceBlocked[0] != "B" {
		t.Fatalf("resource exhaustion did not block B: %#v", frontier)
	}
	if len(frontier.Explanations) != 1 || len(frontier.Explanations[0].Reasons) != 1 || frontier.Explanations[0].Reasons[0].Code != "resource_capacity_exhausted" || frontier.Explanations[0].Reasons[0].Available != 0 {
		t.Fatalf("resource explanation is not closed and actionable: %#v", frontier.Explanations)
	}
	lease := state.Resources["lease"]
	lease.Status = "released"
	state.Resources["lease"] = lease
	frontier = ComputeFrontier(state)
	if len(frontier.Ready) != 1 || frontier.Ready[0] != "B" {
		t.Fatalf("released capacity did not unblock B: %#v", frontier)
	}
}

func TestFrontierExplainsEveryUnsatisfiedIncomingEdge(t *testing.T) {
	graph := GraphDefinition{APIVersion: GraphAPIVersion, Kind: GraphKind, Metadata: GraphMetadata{Name: "explain"}, Spec: GraphSpec{
		Nodes: []NodeDefinition{
			{ID: "A", Kind: "join", Title: "A", Outcomes: []Outcome{{ID: "ok", Class: "success"}, {ID: "bad", Class: "failure"}}},
			{ID: "B", Kind: "join", Title: "B", Outcomes: []Outcome{{ID: "ok", Class: "success"}}},
			{ID: "C", Kind: "join", Title: "C", Outcomes: []Outcome{{ID: "done", Class: "success"}}},
		}, Edges: []EdgeDefinition{
			{ID: "A-C", From: "A", To: "C", When: Predicate{Outcome: "ok"}},
			{ID: "B-C", From: "B", To: "C", When: Predicate{Outcome: "ok"}},
		},
	}}
	state := NewState("project")
	state.Graph = &graph
	state.Nodes["A"] = NodeRuntime{Status: "terminal", Outcome: "bad", OutcomeClass: "failure"}
	state.Nodes["B"] = NodeRuntime{Status: "planned"}
	state.Nodes["C"] = NodeRuntime{Status: "planned"}
	frontier := ComputeFrontier(state)
	var explanation ReadinessExplanation
	for _, candidate := range frontier.Explanations {
		if candidate.NodeID == "C" {
			explanation = candidate
		}
	}
	if len(explanation.Reasons) != 2 || explanation.Reasons[0].EdgeID != "A-C" || explanation.Reasons[0].Code != "predicate_unsatisfied" || explanation.Reasons[1].EdgeID != "B-C" || explanation.Reasons[1].Code != "source_not_terminal" {
		t.Fatalf("unexpected readiness explanation: %#v", explanation)
	}
}

func TestDependencyCutSkipsFailureHandlingBranch(t *testing.T) {
	graph := GraphDefinition{
		APIVersion: GraphAPIVersion, Kind: GraphKind, Metadata: GraphMetadata{Name: "cut"},
		Spec: GraphSpec{Nodes: []NodeDefinition{
			{ID: "source", Kind: "join", Title: "source", Outcomes: []Outcome{{ID: "ok", Class: "success"}, {ID: "broken", Class: "failure"}}},
			{ID: "success-only", Kind: "join", Title: "success", Outcomes: []Outcome{{ID: "done", Class: "success"}}},
			{ID: "failure-handler", Kind: "join", Title: "handler", Outcomes: []Outcome{{ID: "done", Class: "success"}}},
			{ID: "downstream", Kind: "join", Title: "downstream", Outcomes: []Outcome{{ID: "done", Class: "success"}}},
		}, Edges: []EdgeDefinition{
			{ID: "success", From: "source", To: "success-only", When: Predicate{Outcome: "ok"}},
			{ID: "failure", From: "source", To: "failure-handler", When: Predicate{Outcome: "broken"}},
			{ID: "downstream", From: "success-only", To: "downstream", When: Predicate{Outcome: "done"}},
		}},
	}
	state := NewState("project")
	state.Graph = &graph
	state.Nodes["source"] = NodeRuntime{Status: "terminal", Outcome: "broken", OutcomeClass: "failure"}
	for _, id := range []string{"success-only", "failure-handler", "downstream"} {
		state.Nodes[id] = NodeRuntime{Status: "planned"}
	}
	cut := DependencyCut(state, "source")
	if len(cut) != 2 || cut[0] != "downstream" || cut[1] != "success-only" {
		t.Fatalf("unexpected dependency cut: %#v", cut)
	}
}

func TestGraphRejectsHierarchyCycleAndFloatingAuthorityNumber(t *testing.T) {
	graph := GraphDefinition{
		APIVersion: GraphAPIVersion, Kind: GraphKind, Metadata: GraphMetadata{Name: "invalid"},
		Spec: GraphSpec{Nodes: []NodeDefinition{
			{ID: "A", Kind: "join", Title: "A", Parent: "B", Inputs: json.RawMessage(`{"ratio":1.5}`), Outcomes: []Outcome{{ID: "done", Class: "success"}}},
			{ID: "B", Kind: "join", Title: "B", Parent: "A", Outcomes: []Outcome{{ID: "done", Class: "success"}}},
		}},
	}
	err := ValidateGraph(graph)
	if err == nil || (!strings.Contains(err.Error(), "floating") && !strings.Contains(err.Error(), "hierarchy")) {
		t.Fatalf("invalid authority was accepted: %v", err)
	}
}

func TestSensitiveMaterialIsRejectedEvenUnderInnocuousFieldNames(t *testing.T) {
	cases := []json.RawMessage{
		json.RawMessage(`{"endpoint":"https://user:password@example.com/path"}`),
		json.RawMessage(`{"endpoint":"https://example.com/path?access_token=value"}`),
		json.RawMessage(`{"note":"Bearer abcdefghijklmnopqrstuvwxyz"}`),
		json.RawMessage(`{"value":"github_pat_abcdefghijklmnopqrstuvwxyz"}`),
		json.RawMessage(`{"document":"-----BEGIN PRIVATE KEY-----\\nmaterial"}`),
	}
	for _, raw := range cases {
		if err := RejectSensitiveFields(raw); err == nil {
			t.Fatalf("sensitive material was accepted: %s", raw)
		}
	}
	legitimate := json.RawMessage(`{"endpoint":"https://example.com/artifact?id=42&signal=healthy","digest":"sha256:0123456789abcdef","note":"token budget"}`)
	if err := RejectSensitiveFields(legitimate); err != nil {
		t.Fatalf("legitimate metadata was rejected: %v", err)
	}
}

func TestGraphRejectsSensitiveMaterialOutsideNodeInputs(t *testing.T) {
	graph := GraphDefinition{
		APIVersion: GraphAPIVersion,
		Kind:       GraphKind,
		Metadata: GraphMetadata{
			Name:         "unsafe",
			ExternalRefs: []ExternalRef{{System: "web", Type: "requirement", ID: "one", URL: "https://example.com/item?access_token=secret"}},
		},
		Spec: GraphSpec{Nodes: []NodeDefinition{{ID: "done", Kind: "join", Title: "done", Outcomes: []Outcome{{ID: "ok", Class: "success"}}}}},
	}
	if err := ValidateGraph(graph); err == nil || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("sensitive graph metadata was accepted: %v", err)
	}
}

func TestFrontierResourceCapacityIsIndexedOnceForLargeGraphs(t *testing.T) {
	state := NewState("resource-scale")
	graph := GraphDefinition{APIVersion: GraphAPIVersion, Kind: GraphKind, Metadata: GraphMetadata{Name: "resource scale"}, Spec: GraphSpec{Roles: []RoleDefinition{{ID: "worker", Capabilities: []string{CapabilityNodeRun}}}, ResourceCapacities: []ResourceCapacity{{Kind: "slot", Capacity: 12000}}}}
	for index := 0; index < 6000; index++ {
		nodeID := fmt.Sprintf("node-%05d", index)
		graph.Spec.Nodes = append(graph.Spec.Nodes, NodeDefinition{ID: nodeID, Kind: "task", Role: "worker", Title: "work", Outcomes: []Outcome{{ID: "done", Class: "success"}}, Resources: []ResourceRequest{{Kind: "slot", Quantity: 1}}})
		state.Nodes[nodeID] = NodeRuntime{Status: "planned"}
		resourceID := fmt.Sprintf("resource-%05d", index)
		state.Resources[resourceID] = ResourceLease{ID: resourceID, Kind: "slot", Quantity: 1, Status: "active"}
	}
	state.Graph = &graph
	started := time.Now()
	frontier, err := ComputeFrontierSummaryContext(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if len(frontier.Ready) != 6000 {
		t.Fatalf("resource capacity summary lost ready nodes: %d", len(frontier.Ready))
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("frontier rebuilt resource capacity per node: %s", elapsed)
	}
}
