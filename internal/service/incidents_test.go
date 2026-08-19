package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type observedDoneContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (ctx *observedDoneContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.entered) })
	return ctx.Context.Done()
}

func TestIncidentProgressTripsCircuitAndCanResolve(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "incidents")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"incidents"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","incident.manage"]}],"nodes":[{"id":"A","kind":"task","role":"worker","title":"A","outcomes":[{"id":"ok","class":"success"},{"id":"broken","class":"failure"}]}],"edges":[]}}`
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
	finish := findActionRef(t, svc, "worker", "A", "task.complete")
	if _, err := svc.ApplyAction(finish, json.RawMessage(`{"outcome":"broken"}`), "finish"); err != nil {
		t.Fatal(err)
	}
	incidentID := "attempt:" + started.AttemptID
	first, err := svc.ProgressIncident(incidentID, "worker", "same failure", false, "progress-1")
	if err != nil || first.Status != "open" || first.NoProgressAttempts != 1 {
		t.Fatalf("first progress: %+v %v", first, err)
	}
	if _, err := svc.ProgressIncident(incidentID, "worker", "different observation", true, "progress-1"); err == nil || !strings.Contains(err.Error(), "another command") {
		t.Fatalf("incident idempotency key accepted different progress: %v", err)
	}
	second, err := svc.ProgressIncident(incidentID, "worker", "still same failure", false, "progress-2")
	if err != nil || second.Status != "circuit-open" || second.CircuitReason != "no_progress_attempt_budget_exhausted" {
		t.Fatalf("circuit did not trip: %+v %v", second, err)
	}
	dispositioned, err := svc.SetIncidentDisposition(incidentID, "worker", "quarantine", "move the failed path off the critical lane", "disposition")
	if err != nil || dispositioned.Disposition != "quarantine" || dispositioned.DispositionBy != "worker" {
		t.Fatalf("disposition: %+v %v", dispositioned, err)
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

func TestIncidentTextRejectsSensitiveMaterialBeforeStateAccess(t *testing.T) {
	svc := &Service{}
	if _, err := svc.ProgressIncident("incident", "worker", "Bearer abcdefghijklmnopqrstuvwxyz", false, "progress"); err == nil || !strings.Contains(err.Error(), "prohibited") {
		t.Fatalf("sensitive incident note was accepted: %v", err)
	}
	if _, err := svc.TripIncident("incident", "worker", "github_pat_abcdefghijklmnopqrstuvwxyz", "trip"); err == nil || !strings.Contains(err.Error(), "prohibited") {
		t.Fatalf("sensitive circuit reason was accepted: %v", err)
	}
}

func TestIncidentContextMethodsHonorCancellationBeforeStateAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := &Service{}
	checks := []func() error{
		func() error {
			_, err := svc.ProgressIncidentContext(ctx, "incident", "worker", "progress", false, "progress")
			return err
		},
		func() error {
			_, err := svc.TripIncidentContext(ctx, "incident", "worker", "circuit", "trip")
			return err
		},
		func() error {
			_, err := svc.ResolveIncidentContext(ctx, "incident", "worker", "resolved", "resolve")
			return err
		},
		func() error {
			_, err := svc.SetIncidentDispositionContext(ctx, "incident", "worker", "retry", "retry", "disposition")
			return err
		},
	}
	for index, check := range checks {
		if err := check(); !errors.Is(err, context.Canceled) {
			t.Fatalf("incident context method %d ignored cancellation: %v", index, err)
		}
	}
}

func TestEffectIncidentContextCancellationWhileWaitingDoesNotCommit(t *testing.T) {
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"incident-wait-cancellation"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply","effect.reconcile","incident.manage"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"manual","request":true},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	svc, _ := lifecycleWriterService(t, "incident-wait-cancellation", graph)
	_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
	_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
	prepared, err := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare")
	if err != nil || prepared.Status != "unknown" {
		t.Fatalf("manual Effect did not open an Incident: %+v %v", prepared, err)
	}
	if prepared.Continuation.SafeToWait || prepared.Continuation.Owner != "agent" || !contains(prepared.Continuation.ReasonCodes, "effect_pending") || !contains(prepared.Continuation.ReasonCodes, "incident_open") {
		t.Fatalf("unknown Effect continuation was not actionable: %+v", prepared.Continuation)
	}
	incidentID := "effect:" + prepared.ActionID
	release, err := svc.acquireEffectReconcileLock(context.Background(), prepared.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := svc.State()
	base, cancel := context.WithCancel(context.Background())
	ctx := &observedDoneContext{Context: base, entered: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		_, tripErr := svc.TripIncidentContext(ctx, incidentID, "worker", "operator", "cancelled-trip")
		result <- tripErr
	}()
	select {
	case <-ctx.entered:
	case <-time.After(time.Second):
		cancel()
		release()
		<-result
		t.Fatal("Effect Incident mutation never consulted the caller context while waiting for its lock")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			release()
			t.Fatalf("lock wait returned the wrong cancellation: %v", err)
		}
	case <-time.After(time.Second):
		release()
		<-result
		t.Fatal("Effect Incident mutation ignored cancellation while waiting for its lock")
	}
	afterCancel, _ := svc.State()
	if afterCancel.HeadHash != before.HeadHash || afterCancel.HeadSequence != before.HeadSequence {
		release()
		t.Fatalf("cancelled Effect Incident mutation committed authority: before=%d/%s after=%d/%s", before.HeadSequence, before.HeadHash, afterCancel.HeadSequence, afterCancel.HeadHash)
	}
	release()
	if _, err := svc.TripIncidentContext(context.Background(), incidentID, "worker", "operator", "cancelled-trip"); err != nil {
		t.Fatal(err)
	}
	afterRetry, _ := svc.State()
	if afterRetry.HeadSequence != before.HeadSequence+1 {
		t.Fatalf("same-key retry did not commit exactly once: before=%d after=%d", before.HeadSequence, afterRetry.HeadSequence)
	}
	if _, err := svc.TripIncidentContext(context.Background(), incidentID, "worker", "operator", "cancelled-trip"); err != nil {
		t.Fatal(err)
	}
	afterIdempotent, _ := svc.State()
	if afterIdempotent.HeadSequence != afterRetry.HeadSequence {
		t.Fatalf("idempotent retry appended another command: before=%d after=%d", afterRetry.HeadSequence, afterIdempotent.HeadSequence)
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
