package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPredicateNestingHasAClosedReliabilityLimit(t *testing.T) {
	predicate := Predicate{Outcome: "ok"}
	for range 65 {
		predicate = Predicate{All: []Predicate{predicate}}
	}
	graph := GraphDefinition{
		APIVersion: GraphAPIVersion,
		Kind:       GraphKind,
		Metadata:   GraphMetadata{Name: "deep"},
		Spec: GraphSpec{
			Roles: []RoleDefinition{{ID: "dev", Capabilities: []string{"node.run"}}},
			Nodes: []NodeDefinition{
				{ID: "A", Kind: "task", Role: "dev", Title: "A", Outcomes: []Outcome{{ID: "ok", Class: "success"}}},
				{ID: "B", Kind: "task", Role: "dev", Title: "B", Outcomes: []Outcome{{ID: "ok", Class: "success"}}},
			},
			Edges: []EdgeDefinition{{ID: "A-B", From: "A", To: "B", When: predicate}},
		},
	}
	if err := ValidateGraph(graph); err == nil || !strings.Contains(err.Error(), "nesting exceeds") {
		t.Fatalf("deep predicate error = %v", err)
	}
}

func FuzzValidateGraphJSON(f *testing.F) {
	f.Add([]byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"fuzz"},"spec":{"roles":[{"id":"dev","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"dev","title":"A","outcomes":[{"id":"ok","class":"success"}]}]}}`))
	f.Add([]byte(`{"apiVersion":"future","kind":"Graph"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		var graph GraphDefinition
		if json.Unmarshal(raw, &graph) != nil {
			return
		}
		_ = ValidateGraph(graph)
	})
}
