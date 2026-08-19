package grouping

import (
	"reflect"
	"testing"

	"github.com/CongBao/dagrail/internal/domain"
)

func groupedState() domain.State {
	state := domain.NewState("project")
	state.GraphRevision = "revision"
	state.HeadHash = "head"
	state.Graph = &domain.GraphDefinition{
		APIVersion: domain.GraphAPIVersion,
		Kind:       domain.GraphKind,
		Metadata:   domain.GraphMetadata{Name: "grouped"},
		Spec: domain.GraphSpec{
			Groups: []domain.GroupDefinition{
				{ID: "phase", Title: "Phase", Kind: "custom"},
				{ID: "alpha", Title: "Alpha", Kind: "work-unit", ParentGroupID: "phase", SummaryNodeID: "alpha-done", CollapsedByDefault: true},
				{ID: "beta", Title: "Beta", Kind: "work-unit", ParentGroupID: "phase", SummaryNodeID: "beta-done", CollapsedByDefault: true},
			},
			Nodes: []domain.NodeDefinition{
				{ID: "alpha-work", Kind: "task", Title: "Alpha work", GroupID: "alpha", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}},
				{ID: "alpha-done", Kind: "milestone", Title: "Alpha done", GroupID: "alpha", Outcomes: []domain.Outcome{{ID: "completed", Class: "success"}}},
				{ID: "beta-work", Kind: "task", Title: "Beta work", GroupID: "beta", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}},
				{ID: "beta-done", Kind: "milestone", Title: "Beta done", GroupID: "beta", Outcomes: []domain.Outcome{{ID: "completed", Class: "success"}}},
			},
			Edges: []domain.EdgeDefinition{
				{ID: "alpha-beta-1", From: "alpha-work", To: "beta-work", When: domain.Predicate{Outcome: "done"}},
				{ID: "alpha-beta-2", From: "alpha-done", To: "beta-work", When: domain.Predicate{Outcome: "completed"}},
				{ID: "alpha-internal", From: "alpha-work", To: "alpha-done", When: domain.Predicate{Outcome: "done"}},
			},
		},
	}
	state.Nodes["alpha-work"] = domain.NodeRuntime{Status: "terminal", Outcome: "done", OutcomeClass: "success"}
	state.Nodes["alpha-done"] = domain.NodeRuntime{Status: "terminal", Outcome: "completed", OutcomeClass: "success"}
	state.Nodes["beta-work"] = domain.NodeRuntime{Status: "planned"}
	state.Nodes["beta-done"] = domain.NodeRuntime{Status: "planned"}
	return state
}

func TestBuildSeparatesLifecycleFromOperationalHealth(t *testing.T) {
	state := groupedState()
	state.Attempts["attempt"] = domain.Attempt{ID: "attempt", NodeID: "alpha-work", Number: 3, Status: "terminal"}
	state.Incidents["incident"] = domain.Incident{ID: "incident", NodeID: "alpha-work", Status: "open"}
	state.Effects["effect"] = domain.EffectAction{ID: "effect", NodeID: "alpha-work", Status: "unknown"}
	state.Resources["resource"] = domain.ResourceLease{ID: "resource", NodeID: "alpha-work", Status: "active", ClosureStatus: "unknown"}

	projection, err := Build(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	alpha := groupByID(t, projection, "alpha")
	if alpha.LifecycleStatus != "completed" || alpha.HealthStatus != "blocked" || alpha.DisplayStatus == "completed" {
		t.Fatalf("unsafe group roll-up: %#v", alpha)
	}
	if alpha.NodeCount != 2 || alpha.AttemptCount != 1 || alpha.CurrentCycle != 3 || alpha.OpenIncidentCount != 1 || alpha.UncertainEffectCount != 1 || alpha.UnclosedResourceCount != 1 {
		t.Fatalf("group counters are incomplete: %#v", alpha)
	}
	if alpha.MembershipDigest == "" || projection.ProjectionDigest == "" {
		t.Fatalf("projection provenance is not bound: %#v", projection)
	}
}

func TestBuildFallbackReportsBlockedInternalWork(t *testing.T) {
	state := groupedState()
	state.Graph.Spec.Groups[1].SummaryNodeID = ""
	state.Nodes["alpha-work"] = domain.NodeRuntime{Status: "terminal", Outcome: "failed", OutcomeClass: "failure"}
	state.Nodes["alpha-done"] = domain.NodeRuntime{Status: "planned"}
	projection, err := Build(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	alpha := groupByID(t, projection, "alpha")
	if alpha.LifecycleStatus != "blocked" || alpha.DisplayStatus != "blocked" {
		t.Fatalf("blocked internal work was hidden by fallback rollup: %#v", alpha)
	}
}

func TestBuildAggregatesCollapsedCrossGroupEdgesAndPreservesExactMembers(t *testing.T) {
	projection, err := Build(groupedState(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.VisibleNodes) != 0 || len(projection.AggregateEdges) != 1 {
		t.Fatalf("collapsed projection leaked internal detail: %#v", projection)
	}
	edge := projection.AggregateEdges[0]
	if edge.FromRef != "group:alpha" || edge.ToRef != "group:beta" || edge.Count != 2 || !reflect.DeepEqual(edge.EdgeIDs, []string{"alpha-beta-1", "alpha-beta-2"}) || edge.InspectRef == "" {
		t.Fatalf("cross-group aggregation lost exact evidence: %#v", edge)
	}

	expanded, err := Build(groupedState(), map[string]bool{"phase": true, "alpha": true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expanded.VisibleNodes, []string{"alpha-done", "alpha-work"}) {
		t.Fatalf("single-group expansion is unstable: %v", expanded.VisibleNodes)
	}
	if len(expanded.AggregateEdges) != 3 {
		t.Fatalf("expanded detail did not restore exact endpoints: %#v", expanded.AggregateEdges)
	}
}

func TestBuildIsDeterministicAcrossRuntimeMapOrder(t *testing.T) {
	first := groupedState()
	second := groupedState()
	second.Nodes = map[string]domain.NodeRuntime{
		"beta-done":  second.Nodes["beta-done"],
		"alpha-done": second.Nodes["alpha-done"],
		"beta-work":  second.Nodes["beta-work"],
		"alpha-work": second.Nodes["alpha-work"],
	}
	a, err := Build(first, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.ProjectionDigest != b.ProjectionDigest || !reflect.DeepEqual(a, b) {
		t.Fatalf("group projection depends on map iteration: a=%#v b=%#v", a, b)
	}
}

func TestBuildAssignsVisibleGroupsAndGlobalNodesToGenericLanes(t *testing.T) {
	state := groupedState()
	state.Graph.Spec.Groups = append(state.Graph.Spec.Groups,
		domain.GroupDefinition{ID: "governance", Title: "Governance", Kind: "governance", CollapsedByDefault: true},
		domain.GroupDefinition{ID: "external", Title: "External", Kind: "external", CollapsedByDefault: true},
	)
	state.Graph.Spec.Nodes = append(state.Graph.Spec.Nodes,
		domain.NodeDefinition{ID: "repair", Kind: "task", Title: "Repair", GroupID: "governance", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}},
		domain.NodeDefinition{ID: "publish", Kind: "effect", Title: "Publish", GroupID: "external", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}},
		domain.NodeDefinition{ID: "roadmap-gate", Kind: "gate", Title: "Roadmap gate", Outcomes: []domain.Outcome{{ID: "passed", Class: "success"}}},
	)
	state.Nodes["repair"] = domain.NodeRuntime{Status: "planned"}
	state.Nodes["publish"] = domain.NodeRuntime{Status: "planned"}
	state.Nodes["roadmap-gate"] = domain.NodeRuntime{Status: "planned"}

	projection, err := Build(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Lane{}
	for _, lane := range projection.Lanes {
		got[lane.ID] = lane
	}
	if !reflect.DeepEqual(got["work-units"].GroupRefs, []string{"group:alpha", "group:beta", "group:phase"}) {
		t.Fatalf("work-unit lane should retain the visible hierarchy: %#v", got["work-units"])
	}
	if !reflect.DeepEqual(got["global-governance"].GroupRefs, []string{"group:governance"}) {
		t.Fatalf("governance group was not isolated: %#v", got["global-governance"])
	}
	if !reflect.DeepEqual(got["external-actions"].GroupRefs, []string{"group:external"}) {
		t.Fatalf("external group was not isolated: %#v", got["external-actions"])
	}
	if !reflect.DeepEqual(got["milestones-gates"].NodeRefs, []string{"node:roadmap-gate"}) {
		t.Fatalf("ungrouped gate was not isolated: %#v", got["milestones-gates"])
	}
}

func TestBuildHonorsExplicitCustomLanesAndGroupInheritance(t *testing.T) {
	state := groupedState()
	state.Graph.Spec.Lanes = []domain.LaneDefinition{{ID: "delivery", Title: "Delivery", Order: 25}, {ID: "assurance", Title: "Assurance", Order: 50}}
	state.Graph.Spec.Groups[1].LaneID = "delivery"
	state.Graph.Spec.Nodes = append(state.Graph.Spec.Nodes,
		domain.NodeDefinition{ID: "global-review", Kind: "review", Title: "Global review", LaneID: "assurance", Outcomes: []domain.Outcome{{ID: "approved", Class: "success"}}},
	)
	state.Nodes["global-review"] = domain.NodeRuntime{Status: "planned"}

	projection, err := Build(state, map[string]bool{"alpha": true})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Lane{}
	order := []string{}
	for _, lane := range projection.Lanes {
		got[lane.ID] = lane
		order = append(order, lane.ID)
	}
	if len(order) < 2 || order[0] != "delivery" || order[1] != "assurance" {
		t.Fatalf("custom lane order is not deterministic: %v", order)
	}
	if !reflect.DeepEqual(got["delivery"].GroupRefs, []string{"group:alpha"}) || !reflect.DeepEqual(got["delivery"].NodeRefs, []string{"node:alpha-done", "node:alpha-work"}) {
		t.Fatalf("expanded group members did not inherit their explicit lane: %#v", got["delivery"])
	}
	if !reflect.DeepEqual(got["assurance"].NodeRefs, []string{"node:global-review"}) {
		t.Fatalf("explicit global node lane was ignored: %#v", got["assurance"])
	}
	if groupByID(t, projection, "alpha").LaneID != "delivery" {
		t.Fatalf("group summary omitted its effective lane: %#v", groupByID(t, projection, "alpha"))
	}
}

func groupByID(t *testing.T, projection Projection, id string) GroupSummary {
	t.Helper()
	for _, group := range projection.Groups {
		if group.ID == id {
			return group
		}
	}
	t.Fatalf("group %s is missing", id)
	return GroupSummary{}
}
