package service

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
)

const roleTransferGraph = `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"role-transfer"},"spec":{"roles":[{"id":"controller","capabilities":["role.control"]},{"id":"observer","capabilities":["graph.change"]},{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"work","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`

func TestRoleControlTransferPreservesAuditAndInvalidatesPriorSession(t *testing.T) {
	svc, _ := governanceService(t, roleTransferGraph)
	base := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return base }
	if _, err := svc.BindRole("controller", "codex", "control-session", time.Hour, false, "bind/controller"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "old-session", time.Hour, false, "bind/worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "new-session", 30*time.Minute, true, "unsafe/takeover"); err == nil || !strings.Contains(err.Error(), "active lease") {
		t.Fatalf("ordinary takeover replaced an unexpired lease: %v", err)
	}
	start := findActionRef(t, svc, "worker", "work", "node.start")
	started, err := svc.ApplyAction(start, json.RawMessage(`{}`), "worker/start")
	if err != nil {
		t.Fatal(err)
	}
	priorAction := findActionRef(t, svc, "worker", "work", "attempt.checkpoint")
	checkpointed, err := svc.ApplyAction(priorAction, json.RawMessage(`{"summary":"successor recovery point"}`), "worker/checkpoint")
	if err != nil || checkpointed.AttemptID != started.AttemptID {
		t.Fatalf("failed to establish durable predecessor checkpoint: start=%#v checkpoint=%#v err=%v", started, checkpointed, err)
	}
	before, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := svc.TransferRole("controller", "control-session", "worker", "old-session", "codex", "new-session", 30*time.Minute, "replace an unavailable executor without impersonation", "transfer/1")
	if err != nil {
		t.Fatal(err)
	}
	if transfer.Authority != roleControlAuthority || transfer.ActorRole != "controller" || transfer.ActorSessionID != "control-session" || transfer.Previous.SessionID != "old-session" || transfer.Next.SessionID != "new-session" || transfer.Previous.RoleID != "worker" || transfer.Next.RoleID != "worker" {
		t.Fatalf("role transfer lost audit identity: %#v", transfer)
	}
	after, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if after.HeadSequence != before.HeadSequence+1 || after.Leases["worker"].SessionID != "new-session" || !after.Leases["worker"].Active {
		t.Fatalf("role transfer did not atomically replace the lease: before=%d after=%d lease=%#v", before.HeadSequence, after.HeadSequence, after.Leases["worker"])
	}
	if attempt := after.Attempts[started.AttemptID]; attempt.ID == "" || attempt.RoleID != "worker" || attempt.CheckpointID == "" {
		t.Fatalf("role transfer lost the active attempt or its checkpoint: %#v", attempt)
	}
	if _, err := svc.ApplyAction(priorAction, json.RawMessage(`{}`), "old-session/start"); err == nil {
		t.Fatal("action ref issued to the previous session survived role transfer")
	}
	resumedCheckpoint, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "attempt.checkpoint"), json.RawMessage(`{"summary":"successor resumed from the durable checkpoint"}`), "worker/successor-checkpoint")
	if err != nil || resumedCheckpoint.AttemptID != started.AttemptID {
		t.Fatalf("successor could not continue the existing attempt after transfer: result=%#v err=%v", resumedCheckpoint, err)
	}
	sequence := mustState(t, svc).HeadSequence
	retry, err := svc.TransferRole("controller", "control-session", "worker", "old-session", "codex", "new-session", 30*time.Minute, "replace an unavailable executor without impersonation", "transfer/1")
	if err != nil || !reflect.DeepEqual(retry, transfer) {
		t.Fatalf("exact role transfer retry did not return the original receipt: retry=%#v err=%v", retry, err)
	}
	afterRetry, _ := svc.State()
	if afterRetry.HeadSequence != sequence {
		t.Fatalf("exact role transfer retry appended another event: %d -> %d", sequence, afterRetry.HeadSequence)
	}
	if _, err := svc.TransferRole("controller", "control-session", "worker", "old-session", "codex", "different-session", 30*time.Minute, "changed intent", "transfer/1"); err == nil || !strings.Contains(err.Error(), "another command") {
		t.Fatalf("role transfer idempotency key accepted changed intent: %v", err)
	}
	projectionBefore, err := svc.LifecycleProjection()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RebuildProjection(); err != nil {
		t.Fatal(err)
	}
	projectionAfter, err := svc.LifecycleProjection()
	if err != nil || projectionAfter.ProjectionDigest != projectionBefore.ProjectionDigest {
		t.Fatalf("role transfer projection rebuild drifted: before=%s after=%s err=%v", projectionBefore.ProjectionDigest, projectionAfter.ProjectionDigest, err)
	}
	backup, created, err := svc.CreateBackup()
	if err != nil || !created.Valid {
		t.Fatalf("role transfer backup creation failed: report=%#v err=%v", created, err)
	}
	verified, err := svc.VerifyBackup(backup)
	if err != nil || !verified.Valid || verified.HeadHash != created.HeadHash || verified.Segments != created.Segments {
		t.Fatalf("role transfer backup verification drifted: created=%#v verified=%#v err=%v", created, verified, err)
	}
}

func TestRoleControlTransferExpiryAndCompactBindings(t *testing.T) {
	t.Run("expired target uses takeover", func(t *testing.T) {
		svc, _ := governanceService(t, roleTransferGraph)
		base := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)
		svc.Now = func() time.Time { return base }
		_, _ = svc.BindRole("controller", "codex", "control-session", time.Hour, false, "bind/controller")
		_, _ = svc.BindRole("worker", "codex", "old-session", time.Minute, false, "bind/worker")
		svc.Now = func() time.Time { return base.Add(2 * time.Minute) }
		if _, err := svc.TransferRole("controller", "control-session", "worker", "old-session", "codex", "new-session", 30*time.Minute, "expired target", "transfer/expired"); err == nil || !strings.Contains(err.Error(), "use role takeover") {
			t.Fatalf("controller transfer replaced an expired lease instead of preserving takeover semantics: %v", err)
		}
	})

	t.Run("expired controller is unauthorized", func(t *testing.T) {
		svc, _ := governanceService(t, roleTransferGraph)
		base := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)
		svc.Now = func() time.Time { return base }
		_, _ = svc.BindRole("controller", "codex", "control-session", time.Minute, false, "bind/controller")
		_, _ = svc.BindRole("worker", "codex", "old-session", time.Hour, false, "bind/worker")
		svc.Now = func() time.Time { return base.Add(2 * time.Minute) }
		if _, err := svc.TransferRole("controller", "control-session", "worker", "old-session", "codex", "new-session", 30*time.Minute, "expired controller", "transfer/expired-controller"); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("expired controller lease authorized a transfer: %v", err)
		}
	})

	t.Run("future target lease is not transferable", func(t *testing.T) {
		svc, _ := governanceService(t, roleTransferGraph)
		base := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)
		svc.Now = func() time.Time { return base.Add(-2 * time.Minute) }
		_, _ = svc.BindRole("controller", "codex", "control-session", time.Hour, false, "bind/controller")
		svc.Now = func() time.Time { return base }
		_, _ = svc.BindRole("worker", "codex", "old-session", time.Hour, false, "bind/worker")
		svc.Now = func() time.Time { return base.Add(-time.Minute) }
		if _, err := svc.TransferRole("controller", "control-session", "worker", "old-session", "codex", "new-session", 30*time.Minute, "future target must not be transferred", "transfer/future-target"); err == nil || !strings.Contains(err.Error(), "not begun") {
			t.Fatalf("controller transferred a future target lease: %v", err)
		}
		actions, err := svc.projectAllowedActionsContext(t.Context(), mustState(t, svc), "controller", 0, base.Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		for _, action := range actions {
			if action.Kind == "role.control-transfer" && action.TargetRef == "role:worker" {
				t.Fatal("operations advertised transfer for a future target lease")
			}
		}
	})

	t.Run("large session identity uses compact ref", func(t *testing.T) {
		svc, _ := governanceService(t, roleTransferGraph)
		base := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)
		svc.Now = func() time.Time { return base }
		oldSession := strings.Repeat("old-session/", 900)
		_, _ = svc.BindRole("controller", "codex", "control-session", time.Hour, false, "bind/controller")
		_, _ = svc.BindRole("worker", "codex", oldSession, time.Hour, false, "bind/worker")
		actions, err := svc.projectAllowedActionsContext(t.Context(), mustState(t, svc), "controller", 0, base)
		if err != nil {
			t.Fatal(err)
		}
		var action AllowedAction
		for _, candidate := range actions {
			if candidate.Kind == "role.control-transfer" && candidate.TargetRef == "role:worker" {
				action = candidate
				break
			}
		}
		secret, err := svc.actionSecret()
		if err != nil {
			t.Fatal(err)
		}
		payload, err := verifyActionRef(action.Ref, secret)
		if err != nil || !payload.Compact || payload.PreviousSessionKey == "" || payload.PreviousSessionID != "" {
			t.Fatalf("large transfer identity did not use a compact signed binding: payload=%#v err=%v", payload, err)
		}
		input := json.RawMessage(`{"harness":"codex","sessionId":"successor-session","ttlSeconds":1800,"reason":"replace unavailable executor through compact binding"}`)
		if _, err := svc.ApplyAction(action.Ref, input, "transfer/compact"); err != nil {
			t.Fatal(err)
		}
		sequence := mustState(t, svc).HeadSequence
		if _, err := svc.ApplyAction(action.Ref, input, "transfer/compact"); err != nil || mustState(t, svc).HeadSequence != sequence {
			t.Fatalf("compact exact retry was not idempotent: sequence=%d err=%v", sequence, err)
		}
	})

	t.Run("successor session cannot alias another active role", func(t *testing.T) {
		svc, _ := governanceService(t, roleTransferGraph)
		base := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)
		svc.Now = func() time.Time { return base }
		_, _ = svc.BindRole("controller", "codex", "control-session", time.Hour, false, "bind/controller")
		_, _ = svc.BindRole("observer", "codex", "observer-session", time.Hour, false, "bind/observer")
		_, _ = svc.BindRole("worker", "codex", "old-session", time.Hour, false, "bind/worker")
		if _, err := svc.TransferRole("controller", "control-session", "worker", "old-session", "codex", "control-session", 30*time.Minute, "must not reuse controller identity", "transfer/controller-session"); err == nil || !strings.Contains(err.Error(), "controller session") {
			t.Fatalf("controller session was reused as worker successor: %v", err)
		}
		if _, err := svc.TransferRole("controller", "control-session", "worker", "old-session", "codex", "observer-session", 30*time.Minute, "must not reuse another active role identity", "transfer/observer-session"); err == nil || !strings.Contains(err.Error(), "already bound") {
			t.Fatalf("another active Role session was reused as worker successor: %v", err)
		}
	})

	t.Run("controller session cannot already own target lease", func(t *testing.T) {
		svc, _ := governanceService(t, roleTransferGraph)
		base := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)
		svc.Now = func() time.Time { return base }
		_, _ = svc.BindRole("controller", "codex", "shared-session", time.Hour, false, "bind/controller")
		_, _ = svc.BindRole("worker", "codex", "shared-session", time.Hour, false, "bind/worker")
		if _, err := svc.TransferRole("controller", "shared-session", "worker", "shared-session", "codex", "successor-session", 30*time.Minute, "ambiguous controller identity must not authorize transfer", "transfer/shared-prior"); err == nil || !strings.Contains(err.Error(), "also own") {
			t.Fatalf("one session controlled and owned the transferred target Role: %v", err)
		}
		actions, err := svc.projectAllowedActionsContext(t.Context(), mustState(t, svc), "controller", 0, base)
		if err != nil {
			t.Fatal(err)
		}
		for _, action := range actions {
			if action.Kind == "role.control-transfer" && action.TargetRef == "role:worker" {
				t.Fatal("operations advertised controller transfer for a session that also owns the target")
			}
		}
	})
}

func TestRoleControlTransferReasonSchemaRuntimeParity(t *testing.T) {
	svc, _ := governanceService(t, roleTransferGraph)
	base := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return base }
	_, _ = svc.BindRole("controller", "codex", "control-session", time.Hour, false, "bind/controller")
	_, _ = svc.BindRole("worker", "codex", "old-session", time.Hour, false, "bind/worker")
	allowed := strings.Repeat("界", 1024)
	if _, err := svc.TransferRole("controller", "control-session", "worker", "old-session", "codex", "new-session", 30*time.Minute, allowed, "transfer/unicode"); err != nil {
		t.Fatalf("schema-valid 1024-code-point reason was rejected: %v", err)
	}
	if _, err := decodeRoleControlTransferInput(json.RawMessage(`{"harness":"codex","sessionId":"next","ttlSeconds":1800,"reason":"   "}`)); err == nil {
		t.Fatal("blank role transfer reason was accepted")
	}
	tooLong, _ := json.Marshal(roleControlTransferInput{Harness: "codex", SessionID: "next", TTLSeconds: 1800, Reason: strings.Repeat("界", 1025)})
	if _, err := decodeRoleControlTransferInput(tooLong); err == nil {
		t.Fatalf("overlong Unicode role transfer reason was accepted: %v", err)
	}
	if _, err := svc.TransferRole("controller", "control-session", "worker", "new-session", "codex", "later-session", 1500*time.Millisecond, "subsecond TTL must not alias another request", "transfer/subsecond"); err == nil || !strings.Contains(err.Error(), "whole number of seconds") {
		t.Fatalf("subsecond role transfer TTL was accepted: %v", err)
	}
}

func TestRoleRenewIsStrictlyOwnerLocalAndUnexpired(t *testing.T) {
	newService := func(t *testing.T) (*Service, time.Time) {
		t.Helper()
		svc, _ := governanceService(t, roleTransferGraph)
		base := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)
		svc.Now = func() time.Time { return base }
		return svc, base
	}
	t.Run("success", func(t *testing.T) {
		svc, base := newService(t)
		if _, err := svc.BindRole("worker", "codex", "worker-session", time.Hour, false, "bind/worker"); err != nil {
			t.Fatal(err)
		}
		svc.Now = func() time.Time { return base.Add(5 * time.Minute) }
		renewed, err := svc.RenewRole("worker", "codex", "worker-session", 30*time.Minute, "renew/worker")
		if err != nil || renewed.SessionID != "worker-session" || renewed.Harness != "codex" || renewed.BoundAt != base.Add(5*time.Minute).Format(time.RFC3339Nano) {
			t.Fatalf("same-session renewal failed: lease=%#v err=%v", renewed, err)
		}
		svc.Now = func() time.Time { return base.Add(2 * time.Hour) }
		replayed, err := svc.RenewRole("worker", "codex", "worker-session", 30*time.Minute, "renew/worker")
		if err != nil || !reflect.DeepEqual(replayed, renewed) {
			t.Fatalf("exact renewal retry did not recover its original receipt after expiry: lease=%#v err=%v", replayed, err)
		}
	})
	t.Run("missing expired or different identity", func(t *testing.T) {
		svc, base := newService(t)
		if _, err := svc.RenewRole("worker", "codex", "worker-session", 15*time.Minute, "renew/missing"); err == nil || !strings.Contains(err.Error(), "no active lease") {
			t.Fatalf("renew created a missing lease: %v", err)
		}
		if _, err := svc.BindRole("worker", "codex", "worker-session", time.Minute, false, "bind/worker"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.RenewRole("worker", "claude", "worker-session", 15*time.Minute, "renew/harness"); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("renew changed harness identity: %v", err)
		}
		if _, err := svc.RenewRole("worker", "codex", "other-session", 15*time.Minute, "renew/session"); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("renew changed session identity: %v", err)
		}
		svc.Now = func() time.Time { return base.Add(2 * time.Minute) }
		if _, err := svc.RenewRole("worker", "codex", "worker-session", 15*time.Minute, "renew/expired"); err == nil || !strings.Contains(err.Error(), "takeover") {
			t.Fatalf("renew revived an expired lease: %v", err)
		}
	})
}

func TestRoleControlTransferConcurrentCAS(t *testing.T) {
	newService := func(t *testing.T) *Service {
		t.Helper()
		svc, _ := governanceService(t, roleTransferGraph)
		base := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)
		svc.Now = func() time.Time { return base }
		_, _ = svc.BindRole("controller", "codex", "control-session", time.Hour, false, "bind/controller")
		_, _ = svc.BindRole("worker", "codex", "old-session", time.Hour, false, "bind/worker")
		return svc
	}

	t.Run("same intent converges", func(t *testing.T) {
		svc := newService(t)
		baseline := mustState(t, svc).HeadSequence
		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make([]domain.RoleTransfer, 2)
		errs := make([]error, 2)
		for index := range results {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				<-start
				results[index], errs[index] = svc.TransferRole("controller", "control-session", "worker", "old-session", "codex", "new-session", 30*time.Minute, "one concurrent transfer intent", "transfer/concurrent-same")
			}(index)
		}
		close(start)
		wg.Wait()
		if errs[0] != nil || errs[1] != nil || !reflect.DeepEqual(results[0], results[1]) {
			t.Fatalf("same concurrent transfer intent did not converge: results=%#v errors=%v", results, errs)
		}
		state := mustState(t, svc)
		if state.Leases["worker"].SessionID != "new-session" || state.HeadSequence != baseline+1 {
			t.Fatalf("same concurrent transfer appended more than one event: head=%d lease=%#v", state.HeadSequence, state.Leases["worker"])
		}
	})

	t.Run("different intents cannot split brain", func(t *testing.T) {
		svc := newService(t)
		baseline := mustState(t, svc).HeadSequence
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for index := range errs {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				<-start
				session := fmt.Sprintf("successor-%d", index)
				_, errs[index] = svc.TransferRole("controller", "control-session", "worker", "old-session", "codex", session, 30*time.Minute, "competing transfer intent", fmt.Sprintf("transfer/concurrent-%d", index))
			}(index)
		}
		close(start)
		wg.Wait()
		successes := 0
		for _, err := range errs {
			if err == nil {
				successes++
			}
		}
		state := mustState(t, svc)
		if successes != 1 || state.HeadSequence != baseline+1 || !oneOf(state.Leases["worker"].SessionID, "successor-0", "successor-1") {
			t.Fatalf("competing transfers did not fail closed: successes=%d errors=%v head=%d lease=%#v", successes, errs, state.HeadSequence, state.Leases["worker"])
		}
	})
}

func TestRoleControlTransferAllowedActionAndAuthorization(t *testing.T) {
	svc, _ := governanceService(t, roleTransferGraph)
	base := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return base }
	for _, binding := range []struct{ role, session, key string }{{"controller", "control-session", "bind/controller"}, {"observer", "observer-session", "bind/observer"}, {"worker", "old-session", "bind/worker"}} {
		if _, err := svc.BindRole(binding.role, "codex", binding.session, time.Hour, false, binding.key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.TransferRole("observer", "observer-session", "worker", "old-session", "codex", "forbidden", 30*time.Minute, "must not pass", "transfer/unauthorized"); err == nil || !strings.Contains(err.Error(), "role control capability") {
		t.Fatalf("role without role.control transferred another lease: %v", err)
	}
	if _, err := svc.TransferRole("controller", "wrong-control-session", "worker", "old-session", "codex", "forbidden", 30*time.Minute, "must not pass", "transfer/wrong-actor-session"); err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("stale controller session transferred a lease: %v", err)
	}
	if _, err := svc.TransferRole("controller", "control-session", "worker", "different-old-session", "codex", "forbidden", 30*time.Minute, "must not pass", "transfer/stale-target"); err == nil || !strings.Contains(err.Error(), "session changed") {
		t.Fatalf("stale target-session CAS transferred a lease: %v", err)
	}
	actions, err := svc.projectAllowedActionsContext(t.Context(), mustState(t, svc), "controller", 0, base)
	if err != nil {
		t.Fatal(err)
	}
	var transferAction AllowedAction
	for _, action := range actions {
		if action.Kind == "role.control-transfer" && action.TargetRef == "role:observer" {
			t.Fatal("controller received a transfer action for an idle Role with no live responsibility")
		}
		if action.Kind == "role.control-transfer" && action.TargetRef == "role:worker" {
			transferAction = action
			break
		}
	}
	if transferAction.Ref == "" {
		t.Fatalf("controller operations omitted typed role transfer action: %#v", actions)
	}
	remediations, err := svc.buildRemediationsContext(t.Context(), mustState(t, svc), preWaitInventory{readyNodes: []string{"work"}})
	if err != nil {
		t.Fatal(err)
	}
	remediationFound := false
	for _, remediation := range remediations {
		if remediation.Code == "control_transfer_active_role" && remediation.TargetRef == "role:worker" && remediation.OwnerRole == "controller" {
			remediationFound = true
		}
	}
	if !remediationFound {
		t.Fatalf("pre-wait omitted active-role controller transfer remediation: %#v", remediations)
	}
	input := json.RawMessage(`{"harness":"codex","sessionId":"successor-session","ttlSeconds":1800,"reason":"central-authorized successor replaces unavailable executor"}`)
	if _, err := svc.BindRole("observer", "codex", "observer-session", time.Hour, false, "renew/observer"); err != nil {
		t.Fatal(err)
	}
	staleSequence := mustState(t, svc).HeadSequence
	if _, err := svc.ApplyAction(transferAction.Ref, input, "transfer/stale-ref"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("role transfer accepted a stale head-bound action ref: %v", err)
	}
	if mustState(t, svc).HeadSequence != staleSequence {
		t.Fatal("rejected stale role transfer mutated the journal")
	}
	if _, err := svc.transferRole("controller", "control-session", "worker", "old-session", "codex", "successor-session", 30*time.Minute, "simulate a head advance between signed-action admission and transfer reload", "transfer/interleaved-stale", transferActionHead(t, svc, transferAction.Ref)); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("role transfer crossed a head advance after signed-action admission: %v", err)
	}
	actions, err = svc.projectAllowedActionsContext(t.Context(), mustState(t, svc), "controller", 0, base)
	if err != nil {
		t.Fatal(err)
	}
	transferAction = AllowedAction{}
	for _, action := range actions {
		if action.Kind == "role.control-transfer" && action.TargetRef == "role:worker" {
			transferAction = action
			break
		}
	}
	if transferAction.Ref == "" {
		t.Fatal("fresh role transfer action was unavailable after unrelated head advancement")
	}
	result, err := svc.ApplyAction(transferAction.Ref, input, "transfer/action")
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "role.control-transfer" || result.Status != "transferred" || result.ObjectRef != "role:worker" {
		t.Fatalf("unexpected role transfer action result: %#v", result)
	}
	state := mustState(t, svc)
	if state.Leases["worker"].SessionID != "successor-session" {
		t.Fatalf("typed action did not install successor lease: %#v", state.Leases["worker"])
	}
	sequence := state.HeadSequence
	if _, err := svc.ApplyAction(transferAction.Ref, input, "transfer/action"); err != nil {
		t.Fatalf("exact stale-ref retry did not recover role transfer result: %v", err)
	}
	if current := mustState(t, svc); current.HeadSequence != sequence {
		t.Fatalf("idempotent action retry appended another transfer: %d -> %d", sequence, current.HeadSequence)
	}
}

func TestLifecycleValidationAcceptsOnlyWriterEquivalentRoleTransfer(t *testing.T) {
	svc, _ := governanceService(t, roleTransferGraph)
	base := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return base }
	graphOnly := mustState(t, svc)
	if _, err := svc.BindRole("controller", "codex", "control-session", time.Hour, false, "bind/controller"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "old-session", time.Hour, false, "bind/worker"); err != nil {
		t.Fatal(err)
	}
	initial := mustState(t, svc)
	if _, err := svc.TransferRole("controller", "control-session", "worker", "old-session", "codex", "new-session", 30*time.Minute, "verified controller transfer", "transfer/lifecycle"); err != nil {
		t.Fatal(err)
	}
	segments, err := svc.Journal.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	segment := segments[len(segments)-1]
	record := LifecycleMigrationRecord{SourceEventID: "role-transfer", OccurredAt: segment.CommittedAt, Events: []LifecycleMigrationEvent{{Type: segment.Events[0].Type, Payload: segment.Events[0].Payload}}}
	if err := validateLifecycleEventSequence(initial, []LifecycleMigrationRecord{record}); err != nil {
		t.Fatalf("migration rejected current writer role transfer: %v", err)
	}
	writerRecords := lifecycleRecordsFromWriter(t, svc, graphOnly.HeadSequence)
	if err := validateLifecycleRecordsManifestVersion(t, svc, graphOnly, writerRecords, LifecycleMigrationAPIVersion); err != nil {
		t.Fatalf("v1alpha1 migration/schema rejected current writer role transfer: %v", err)
	}
	if err := validateLifecycleRecordsManifestVersion(t, svc, graphOnly, writerRecords, LifecycleMigrationBundleAPIVersion); err != nil {
		t.Fatalf("v1beta1 migration/schema rejected current writer role transfer: %v", err)
	}
	var forged domain.RoleTransfer
	if err := json.Unmarshal(segment.Events[0].Payload, &forged); err != nil {
		t.Fatal(err)
	}
	lateRecord := record
	lateRecord.OccurredAt = base.Add(2 * time.Hour).Format(time.RFC3339Nano)
	if err := validateLifecycleEventSequence(initial, []LifecycleMigrationRecord{lateRecord}); err == nil || !strings.Contains(err.Error(), "timestamps") {
		t.Fatalf("migration admitted a backdated transfer after controller lease expiry: %v", err)
	}
	reusedActorSession := forged
	reusedActorSession.Next.SessionID = forged.ActorSessionID
	reusedActorRaw, _ := json.Marshal(reusedActorSession)
	reusedActorRecord := record
	reusedActorRecord.Events = append([]LifecycleMigrationEvent(nil), record.Events...)
	reusedActorRecord.Events[0].Payload = reusedActorRaw
	if err := validateLifecycleEventSequence(initial, []LifecycleMigrationRecord{reusedActorRecord}); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("migration admitted controller session reuse by the successor Role: %v", err)
	}
	reusedPriorSession := forged
	reusedPriorSession.ActorSessionID = forged.Previous.SessionID
	reusedPriorRaw, _ := json.Marshal(reusedPriorSession)
	reusedPriorRecord := record
	reusedPriorRecord.Events = append([]LifecycleMigrationEvent(nil), record.Events...)
	reusedPriorRecord.Events[0].Payload = reusedPriorRaw
	if err := validateLifecycleEventSequence(initial, []LifecycleMigrationRecord{reusedPriorRecord}); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("migration admitted one session as controller and target owner: %v", err)
	}
	forged.ActorSessionID = "stale-controller-session"
	forgedRaw, _ := json.Marshal(forged)
	record.Events[0].Payload = forgedRaw
	if err := validateLifecycleEventSequence(initial, []LifecycleMigrationRecord{record}); err == nil || !strings.Contains(err.Error(), "controller authority") {
		t.Fatalf("migration admitted role transfer outside controller lease: %v", err)
	}
	for name, reason := range map[string]string{
		"blank":     "   ",
		"oversized": strings.Repeat("界", 1025),
		"secret":    "github_pat_abcdefghijklmnopqrstuvwxyz",
	} {
		t.Run(name+" reason", func(t *testing.T) {
			invalid := forged
			invalid.ActorSessionID = "control-session"
			invalid.Reason = reason
			raw, _ := json.Marshal(invalid)
			invalidRecord := record
			invalidRecord.Events = append([]LifecycleMigrationEvent(nil), record.Events...)
			invalidRecord.Events[0].Payload = raw
			if err := validateLifecycleEventSequence(initial, []LifecycleMigrationRecord{invalidRecord}); err == nil {
				t.Fatalf("migration admitted %s role transfer reason", name)
			}
		})
	}
}

func TestLifecycleMigrationRejectsRoleRenewalAfterPriorLeaseExpiry(t *testing.T) {
	svc, _ := governanceService(t, roleTransferGraph)
	base := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return base }
	graphOnly := mustState(t, svc)
	if _, err := svc.BindRole("worker", "codex", "worker-session", 3*time.Second, false, "bind/worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "role.renew"), json.RawMessage(`{"ttlSeconds":600}`), "renew/writer"); err != nil {
		t.Fatal(err)
	}
	records := lifecycleRecordsFromWriter(t, svc, graphOnly.HeadSequence)
	if len(records) != 2 {
		t.Fatalf("expected role bind and renewal writer records, got %d", len(records))
	}
	forgedBound := base.Add(time.Hour)
	records[1].OccurredAt = forgedBound.Format(time.RFC3339Nano)
	for index, event := range records[1].Events {
		if event.Type != "role.bound" {
			continue
		}
		var lease domain.RoleLease
		if err := json.Unmarshal(event.Payload, &lease); err != nil {
			t.Fatal(err)
		}
		lease.BoundAt = forgedBound.Format(time.RFC3339Nano)
		lease.ExpiresAt = forgedBound.Add(10 * time.Minute).Format(time.RFC3339Nano)
		records[1].Events[index].Payload, _ = json.Marshal(lease)
	}
	for _, apiVersion := range []string{LifecycleMigrationAPIVersion, LifecycleMigrationBundleAPIVersion} {
		if err := validateLifecycleRecordsManifestVersion(t, svc, graphOnly, records, apiVersion); err == nil || !strings.Contains(err.Error(), "renewal action") {
			t.Fatalf("%s migration admitted renewal after prior lease expiry: %v", apiVersion, err)
		}
	}
}

func mustState(t *testing.T, svc *Service) domain.State {
	t.Helper()
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func transferActionHead(t *testing.T, svc *Service, ref string) string {
	t.Helper()
	secret, err := svc.actionSecret()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := verifyActionRef(ref, secret)
	if err != nil {
		t.Fatal(err)
	}
	return payload.HeadHash
}
