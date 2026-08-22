package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
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

func TestControllerCanClosePassiveReturnedAttemptIncidentWithoutOwnerImpersonation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "controller incident disposition")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"controller-incident-disposition"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","incident.manage"]},{"id":"controller","capabilities":["incident.control"]},{"id":"other-manager","capabilities":["incident.manage"]}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"work","outcomes":[{"id":"returned","class":"failure"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "controller"); err != nil {
		t.Fatal(err)
	}
	initial, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "worker-session", time.Hour, false, "bind-worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("controller", "codex", "controller-session", time.Hour, false, "bind-controller"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("other-manager", "codex", "other-session", time.Hour, false, "bind-other"); err != nil {
		t.Fatal(err)
	}
	started, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "node.start"), json.RawMessage(`{}`), "start")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "task.complete"), json.RawMessage(`{"outcome":"returned"}`), "return"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReleaseRole("worker", "worker-session", "release-worker"); err != nil {
		t.Fatal(err)
	}
	incidentID := "attempt:" + started.AttemptID
	before, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if before.Incidents[incidentID].OwnerRole != "worker" || before.Attempts[started.AttemptID].Status != "terminal" {
		t.Fatalf("fixture is not a passive terminal Attempt incident: %#v %#v", before.Incidents[incidentID], before.Attempts[started.AttemptID])
	}
	audit, err := svc.preWaitFromState(before)
	if err != nil {
		t.Fatal(err)
	}
	controlRemediation := false
	for _, remediation := range audit.Remediations {
		if remediation.Code == "control_terminal_attempt_incident" && remediation.OwnerRole == "controller" && remediation.Operation.Kind == "incident.control-resolve" {
			controlRemediation = true
		}
	}
	if !controlRemediation {
		t.Fatalf("pre-wait omitted the exact controller remediation: %#v", audit.Remediations)
	}
	if _, err := svc.ResolveIncident(incidentID, "other-manager", "cross-owner", "ordinary-cross-owner"); err == nil || !strings.Contains(err.Error(), "belongs to role worker") {
		t.Fatalf("ordinary incident.manage became a cross-owner wildcard: %v", err)
	}
	var control AllowedAction
	for _, action := range svc.projectAllowedActions(before, "controller", 24) {
		if action.Kind == "incident.control-resolve" && action.TargetRef == "incident:"+incidentID {
			control = action
			break
		}
	}
	if control.Ref == "" {
		t.Fatal("controller incident action was not exposed")
	}
	if _, err := svc.BindRole("controller", "codex", "controller-session", time.Hour, false, "renew-controller"); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"disposition":"off-critical-path","resolution":"terminal sender delivered; preserve returned evidence","note":"remove the closed failure path from the critical lane"}`)
	if _, err := svc.ApplyAction(control.Ref, input, "stale-control-resolve"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale controller incident ref was accepted: %v", err)
	}
	refreshed, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	control = AllowedAction{}
	for _, action := range svc.projectAllowedActions(refreshed, "controller", 24) {
		if action.Kind == "incident.control-resolve" && action.TargetRef == "incident:"+incidentID {
			control = action
			break
		}
	}
	realNow := svc.Now
	refIssuedAt := realNow().UTC()
	svc.Now = func() time.Time { return refIssuedAt.Add(6 * time.Minute) }
	if _, err := svc.ApplyAction(control.Ref, input, "expired-control-resolve"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired controller incident ref was accepted: %v", err)
	}
	svc.Now = realNow
	result, err := svc.ApplyAction(control.Ref, input, "control-resolve")
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "incident.control-resolve" || result.Status != "resolved" || result.ObjectRef != "incident:"+incidentID {
		t.Fatalf("unexpected control result: %#v", result)
	}
	after, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	incident := after.Incidents[incidentID]
	if incident.OwnerRole != "worker" || incident.Disposition != "off-critical-path" || incident.DispositionBy != "controller" || incident.Status != "resolved" {
		t.Fatalf("controller disposition rewrote ownership or failed to close: %#v", incident)
	}
	if after.Nodes["work"].Status != "terminal" || after.Nodes["work"].Outcome != "returned" || after.Nodes["work"].OutcomeClass != "failure" || len(after.NodeAttempts["work"]) != 1 {
		t.Fatalf("controller incident closure changed the failed work product or started new work: runtime=%#v attempts=%#v", after.Nodes["work"], after.NodeAttempts["work"])
	}
	command := after.Commands["control-resolve"]
	if command.Kind != "incident.control-resolve" || command.ActorRole != "controller" || command.ObjectRef != "incident:"+incidentID {
		t.Fatalf("journal command did not preserve the truthful actor: %#v", command)
	}
	raw, _ := json.Marshal(incident)
	for _, expected := range []string{`"authority":"incident.control"`, `"actorRole":"controller"`, `"originalOwnerRole":"worker"`} {
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("incident omitted replay-verifiable control field %s: %s", expected, raw)
		}
	}
	head := after.HeadSequence
	replayed, err := svc.ApplyAction(control.Ref, input, "control-resolve")
	if err != nil || replayed.Sequence != result.Sequence {
		t.Fatalf("exact idempotent controller retry failed: result=%#v err=%v", replayed, err)
	}
	afterRetry, _ := svc.State()
	if afterRetry.HeadSequence != head {
		t.Fatalf("idempotent controller retry appended authority: before=%d after=%d", head, afterRetry.HeadSequence)
	}
	changed := json.RawMessage(`{"disposition":"quarantine","resolution":"changed semantics","note":"changed semantics"}`)
	if _, err := svc.ApplyAction(control.Ref, changed, "control-resolve"); err == nil || !strings.Contains(err.Error(), "another command") {
		t.Fatalf("same-key controller retry accepted changed input: %v", err)
	}
	assertLifecycleWriterPrefixes(t, svc, initial)
	forgedRecords := lifecycleRecordsFromWriter(t, svc, initial.HeadSequence)
	mutateLifecycleEventPayload(t, forgedRecords, "incident.updated", func(payload map[string]any) {
		control := payload["control"].(map[string]any)
		control["actorRole"] = "other-manager"
		payload["dispositionBy"] = "other-manager"
	})
	if err := validateLifecycleRecordsManifest(t, svc, initial, forgedRecords); err == nil || !strings.Contains(err.Error(), "lacks capability incident.control") {
		t.Fatalf("migration accepted a cross-owner actor without incident.control: %v", err)
	}
	for name, invalidText := range map[string]string{
		"blank":           " ",
		"over-byte-limit": strings.Repeat("界", 400),
	} {
		t.Run("migration-rejects-control-text-"+name, func(t *testing.T) {
			for _, apiVersion := range []string{LifecycleMigrationAPIVersion, LifecycleMigrationBundleAPIVersion} {
				t.Run(apiVersion, func(t *testing.T) {
					records := lifecycleRecordsFromWriter(t, svc, initial.HeadSequence)
					mutateLifecycleEventPayload(t, records, "incident.updated", func(payload map[string]any) {
						control := payload["control"].(map[string]any)
						control["resolution"] = invalidText
						control["note"] = invalidText
						payload["resolution"] = invalidText
						payload["lastProgress"] = invalidText
					})
					if err := validateLifecycleRecordsManifestAuthorityVersion(t, svc, initial, records, apiVersion); err == nil {
						t.Fatalf("%s migration accepted controller text that the public writer rejects", apiVersion)
					}
				})
			}
		})
	}
	backup, report, err := svc.CreateBackup()
	if err != nil || !report.Valid {
		t.Fatalf("controller incident backup creation failed: report=%#v err=%v", report, err)
	}
	verified, err := svc.VerifyBackup(backup)
	if err != nil || !verified.Valid || verified.HeadHash != afterRetry.HeadHash {
		t.Fatalf("controller incident backup verification failed: report=%#v err=%v", verified, err)
	}
	if err := svc.RebuildProjection(); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := svc.State()
	if err != nil || !reflect.DeepEqual(rebuilt.Incidents[incidentID], incident) {
		t.Fatalf("controller incident projection rebuild drifted: incident=%#v err=%v", rebuilt.Incidents[incidentID], err)
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

func TestIncidentControlEligibilityDoesNotBroadenResourceEffectOrOwnerSemantics(t *testing.T) {
	state := domain.State{
		Attempts: map[string]domain.Attempt{"attempt": {ID: "attempt", NodeID: "work", RoleID: "worker", Status: "terminal"}},
		Nodes:    map[string]domain.NodeRuntime{"work": {Status: "terminal", OutcomeClass: "failure"}},
	}
	checks := []domain.Incident{
		{ID: "effect:action", SourceType: "effect", SourceID: "action", OwnerRole: "worker", Status: "open"},
		{ID: "resource:lease", SourceType: "resource", SourceID: "lease", OwnerRole: "worker", Status: "open"},
		{ID: "attempt:attempt", SourceType: "attempt", SourceID: "attempt", NodeID: "work", OwnerRole: "controller", Status: "open"},
	}
	for _, incident := range checks {
		if err := controlResolvableIncident(state, incident, "controller"); err == nil {
			t.Fatalf("controller eligibility broadened a forbidden incident path: %#v", incident)
		}
	}
	valid := domain.Incident{ID: "attempt:attempt", SourceType: "attempt", SourceID: "attempt", NodeID: "work", OwnerRole: "worker", Status: "open"}
	for name, invalidText := range map[string]string{
		"blank":           " ",
		"over-byte-limit": strings.Repeat("界", 400),
		"sensitive":       "github_pat_abcdefghijklmnopqrstuvwxyz",
	} {
		t.Run("shared-control-text-validator-"+name, func(t *testing.T) {
			incident := valid
			if err := controlResolveIncidentValue(&incident, state, "controller", "off-critical-path", invalidText, invalidText, time.Now()); err == nil {
				t.Fatal("shared controller mutation accepted text that the public writer rejects")
			}
		})
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
		func() error {
			_, err := svc.ControlResolveIncidentContext(ctx, "incident", "controller", "quarantine", "resolved", "note", "control")
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
