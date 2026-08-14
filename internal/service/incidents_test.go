package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIncidentProgressTripsCircuitAndCanResolve(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "incidents")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"incidents"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"worker","title":"A","outcomes":[{"id":"ok","class":"success"},{"id":"broken","class":"failure"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	start := findActionRef(t, svc, "worker", "A", "node.start")
	started, err := svc.ApplyAction(start, json.RawMessage(`{}`), "start")
	if err != nil {
		t.Fatal(err)
	}
	finish := findActionRef(t, svc, "worker", "A", "attempt.finish")
	if _, err := svc.ApplyAction(finish, json.RawMessage(`{"outcome":"broken"}`), "finish"); err != nil {
		t.Fatal(err)
	}
	incidentID := "attempt:" + started.AttemptID
	first, err := svc.ProgressIncident(incidentID, "worker", "same failure", false, "progress-1")
	if err != nil || first.Status != "open" || first.NoProgressAttempts != 1 {
		t.Fatalf("first progress: %+v %v", first, err)
	}
	second, err := svc.ProgressIncident(incidentID, "worker", "still same failure", false, "progress-2")
	if err != nil || second.Status != "circuit-open" || second.CircuitReason != "no_progress_attempt_budget_exhausted" {
		t.Fatalf("circuit did not trip: %+v %v", second, err)
	}
	audit, err := svc.PreWait()
	if err != nil || len(audit.CircuitIncidents) != 1 || audit.CircuitIncidents[0] != incidentID || audit.SafeToWait {
		t.Fatalf("pre-wait did not expose open circuit: %+v %v", audit, err)
	}
	resolved, err := svc.ResolveIncident(incidentID, "worker", "quarantined failing path", "resolve")
	if err != nil || resolved.Status != "resolved" {
		t.Fatalf("resolve: %+v %v", resolved, err)
	}
	state, err := svc.State()
	if err != nil || state.Incidents[incidentID].Resolution == "" {
		t.Fatalf("incident did not replay: %v %+v", err, state.Incidents[incidentID])
	}
}

func findActionRef(t *testing.T, svc *Service, role, node, kind string) string {
	t.Helper()
	actions, err := svc.ListActions(role, node)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range actions.Actions {
		if action.Kind == kind {
			return action.Ref
		}
	}
	t.Fatalf("action %s unavailable", kind)
	return ""
}
