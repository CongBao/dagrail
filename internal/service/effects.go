package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/sdk"
	"github.com/gofrs/flock"
	"github.com/google/uuid"
)

var effectReconcileProcessLocks sync.Map

type effectNodeInput struct {
	Adapter string          `json:"adapter"`
	Request json.RawMessage `json:"request"`
}

func (s *Service) applyEffectAction(ctx context.Context, state domain.State, payload actionRefPayload, input json.RawMessage, idempotencyKey, requestDigest string, node domain.NodeDefinition) (ActionResult, error) {
	attempt, err := activeAttempt(state, payload)
	if err != nil {
		return ActionResult{}, err
	}
	if _, exists := state.EffectForAttempt(attempt.ID); exists {
		return ActionResult{}, fmt.Errorf("attempt already has an effect action; reconcile it before another dispatch")
	}
	var declaration effectNodeInput
	if err := json.Unmarshal(node.Inputs, &declaration); err != nil {
		return ActionResult{}, fmt.Errorf("decode effect node inputs: %w", err)
	}
	if declaration.Adapter == "" || len(declaration.Request) == 0 {
		return ActionResult{}, fmt.Errorf("effect node requires adapter and request inputs")
	}
	if isNullAuthorityJSON(declaration.Request) {
		return ActionResult{}, fmt.Errorf("effect node request cannot be null")
	}
	if err := domain.RejectSensitiveFields(declaration.Request); err != nil {
		return ActionResult{}, err
	}
	adapter, ok := s.Providers.Effect(declaration.Adapter)
	if !ok {
		return ActionResult{}, fmt.Errorf("effect adapter %s is not registered", declaration.Adapter)
	}
	request := sdk.EffectRequest{ActionID: payload.ActionID, ProjectRoot: s.Project.Root, NodeID: node.ID, AttemptID: attempt.ID, IdempotencyKey: idempotencyKey, Request: declaration.Request}
	prepared, err := adapter.Prepare(ctx, request)
	if err != nil {
		return ActionResult{}, err
	}
	if prepared.AdapterID != declaration.Adapter {
		return ActionResult{}, fmt.Errorf("effect preparation adapter %q does not match declaration %q", prepared.AdapterID, declaration.Adapter)
	}
	if len(prepared.Binding) > 0 {
		if err := domain.ValidateAuthorityJSON(prepared.Binding); err != nil {
			return ActionResult{}, fmt.Errorf("effect preparation returned invalid authority data: %w", err)
		}
		if err := domain.RejectSensitiveFields(prepared.Binding); err != nil {
			return ActionResult{}, fmt.Errorf("effect preparation returned sensitive authority data: %w", err)
		}
	}
	releaseObservation, err := s.acquireEffectReconcileLock(ctx, payload.ActionID)
	if err != nil {
		return ActionResult{}, err
	}
	defer releaseObservation()
	state, _, err = s.load()
	if err != nil {
		return ActionResult{}, err
	}
	if state.HeadHash != payload.HeadHash {
		if command, ok := state.Commands[idempotencyKey]; ok {
			result, resultErr := actionResultForSequence(state, command.Sequence)
			if resultErr != nil || result.ActionID != payload.ActionID || result.Kind != payload.Kind || command.ActorRole != payload.RoleID || command.ObjectRef != "action:"+payload.ActionID || (command.RequestDigest != "" && command.RequestDigest != requestDigest) {
				return ActionResult{}, fmt.Errorf("idempotency key is already bound to another command")
			}
			return result, nil
		}
		return ActionResult{}, fmt.Errorf("action reference became stale during effect preparation")
	}
	preparedAt := s.Now().UTC()
	lease, err := validLeaseAt(state, payload.RoleID, preparedAt)
	if err != nil || lease.SessionID != payload.SessionID {
		return ActionResult{}, fmt.Errorf("effect preparation finished outside its role lease")
	}
	refExpiresAt, refExpiryErr := time.Parse(time.RFC3339Nano, payload.ExpiresAt)
	if refExpiryErr != nil || !preparedAt.Before(refExpiresAt) {
		return ActionResult{}, fmt.Errorf("action reference expired during effect preparation")
	}
	preparedRaw, _ := json.Marshal(prepared)
	now := preparedAt.Format(time.RFC3339Nano)
	effect := domain.EffectAction{ID: payload.ActionID, NodeID: node.ID, AttemptID: attempt.ID, AdapterID: declaration.Adapter, OwnerRole: payload.RoleID, Status: "prepared", Request: declaration.Request, Prepared: preparedRaw, IdempotencyKey: idempotencyKey, PreparedAt: now, UpdatedAt: now}
	effectRaw, _ := json.Marshal(effect)
	action := domain.ActionRecord{ID: payload.ActionID, Kind: payload.Kind, NodeID: node.ID, AttemptID: attempt.ID, Status: "prepared", Input: input}
	actionRaw, _ := json.Marshal(action)
	expectedHead := state.HeadHash
	segment, created, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: payload.Kind, ActorRole: payload.RoleID, IdempotencyKey: idempotencyKey, ObjectRef: "action:" + payload.ActionID, RequestDigest: requestDigest}, []journal.Event{{Type: "effect.prepared", Payload: effectRaw}, {Type: "action.applied", Payload: actionRaw}}, preparedAt, &expectedHead)
	if err != nil {
		return ActionResult{}, err
	}
	state, segments, err := s.load()
	if err != nil {
		return ActionResult{}, err
	}
	if !created {
		return actionResultForSequence(state, segment.Sequence)
	}
	if err := s.Projection.Sync(state, segments); err != nil {
		return ActionResult{}, err
	}
	dispatchTime := s.Now().UTC()
	dispatchLease, dispatchLeaseErr := validLeaseAt(state, payload.RoleID, dispatchTime)
	if dispatchLeaseErr != nil || dispatchLease.SessionID != payload.SessionID {
		return ActionResult{ActionID: payload.ActionID, Kind: payload.Kind, NodeID: node.ID, AttemptID: attempt.ID, Status: "prepared", Sequence: segment.Sequence}, fmt.Errorf("effect is prepared but dispatch authorization expired; reconcile action %s", payload.ActionID)
	}
	dispatchedAt := dispatchTime.Format(time.RFC3339Nano)
	dispatchedRaw, _ := json.Marshal(map[string]string{"actionId": payload.ActionID, "dispatchedAt": dispatchedAt})
	expectedDispatchHead := state.HeadHash
	if _, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "effect.dispatch", ActorRole: payload.RoleID, IdempotencyKey: idempotencyKey + "/dispatch", ObjectRef: "effect:" + payload.ActionID, RequestDigest: requestDigest}, []journal.Event{{Type: "effect.dispatched", Payload: dispatchedRaw}}, dispatchTime, &expectedDispatchHead); err != nil {
		return ActionResult{}, err
	}
	state, segments, err = s.load()
	if err != nil {
		return ActionResult{}, err
	}
	if err := s.Projection.Sync(state, segments); err != nil {
		return ActionResult{}, err
	}
	receipt, dispatchErr := adapter.Dispatch(ctx, request, prepared)
	if dispatchErr != nil {
		receipt = sdk.EffectReceipt{Status: "unknown", Detail: mustJSON(map[string]string{"error": boundedError(dispatchErr)})}
	}
	if receipt.Status == "" {
		receipt.Status = "unknown"
	}
	observed, observeErr := s.observeEffect(payload.ActionID, payload.RoleID, receipt, idempotencyKey+"/observe", requestDigest)
	if observeErr != nil {
		return ActionResult{ActionID: payload.ActionID, Kind: payload.Kind, NodeID: node.ID, AttemptID: attempt.ID, Status: "unknown", Sequence: segment.Sequence}, fmt.Errorf("effect may have occurred; reconcile action %s: %w", payload.ActionID, observeErr)
	}
	return ActionResult{ActionID: payload.ActionID, Kind: payload.Kind, NodeID: node.ID, AttemptID: attempt.ID, Status: observed.Status, Sequence: observed.Sequence}, nil
}

func (s *Service) ReconcileEffect(actionID string, evidence json.RawMessage, idempotencyKey string) (domain.EffectAction, error) {
	return s.ReconcileEffectContext(context.Background(), actionID, evidence, idempotencyKey)
}

func (s *Service) ReconcileEffectContext(ctx context.Context, actionID string, evidence json.RawMessage, idempotencyKey string) (domain.EffectAction, error) {
	if err := ctx.Err(); err != nil {
		return domain.EffectAction{}, err
	}
	if actionID == "" || idempotencyKey == "" {
		return domain.EffectAction{}, fmt.Errorf("action and idempotency key are required")
	}
	if len(evidence) > 64*1024 {
		return domain.EffectAction{}, fmt.Errorf("reconciliation evidence cannot exceed 64 KiB")
	}
	if len(evidence) > 0 {
		if err := domain.ValidateAuthorityJSON(evidence); err != nil {
			return domain.EffectAction{}, fmt.Errorf("reconciliation evidence: %w", err)
		}
		if err := domain.RejectSensitiveFields(evidence); err != nil {
			return domain.EffectAction{}, fmt.Errorf("reconciliation evidence: %w", err)
		}
	}
	requestDigest, err := authorityRequestDigest("effect.reconcile", evidence)
	if err != nil {
		return domain.EffectAction{}, fmt.Errorf("reconciliation evidence digest: %w", err)
	}
	release, err := s.acquireEffectReconcileLock(ctx, actionID)
	if err != nil {
		return domain.EffectAction{}, err
	}
	defer release()
	state, _, err := s.load()
	if err != nil {
		return domain.EffectAction{}, err
	}
	if command, ok := state.Commands[idempotencyKey]; ok {
		effect, exists := state.Effects[actionID]
		if !exists || command.Kind != "effect.observe" || command.ActorRole != effect.OwnerRole || command.ObjectRef != "effect:"+actionID || (command.RequestDigest != "" && command.RequestDigest != requestDigest) {
			return domain.EffectAction{}, fmt.Errorf("idempotency key is already bound to another command")
		}
		if exists {
			return effect, nil
		}
	}
	effect, ok := state.Effects[actionID]
	if !ok {
		return domain.EffectAction{}, fmt.Errorf("unknown effect action %s", actionID)
	}
	if command, exists := state.Commands[idempotencyKey+"/begin"]; exists {
		if command.Kind != "effect.reconcile" || command.ActorRole != effect.OwnerRole || command.ObjectRef != "effect:"+actionID || (command.RequestDigest != "" && command.RequestDigest != requestDigest) {
			return domain.EffectAction{}, fmt.Errorf("idempotency key is already bound to another reconciliation request")
		}
	}
	if _, err := s.requireRoleCapability(state, effect.OwnerRole, domain.CapabilityEffectReconcile, domain.CapabilityEffectApply); err != nil {
		return domain.EffectAction{}, err
	}
	if _, err := s.validLease(state, effect.OwnerRole); err != nil {
		return domain.EffectAction{}, err
	}
	if effect.Status == "confirmed" {
		return effect, nil
	}
	if incident, exists := state.Incidents["effect:"+actionID]; exists && incident.Status != "open" {
		if incident.Status == "circuit-open" {
			return domain.EffectAction{}, fmt.Errorf("effect incident circuit is open: %s", incident.CircuitReason)
		}
		return domain.EffectAction{}, fmt.Errorf("effect incident is %s and cannot be reopened by reconciliation", incident.Status)
	}
	adapter, ok := s.Providers.Effect(effect.AdapterID)
	if !ok {
		return domain.EffectAction{}, fmt.Errorf("effect adapter %s is not registered", effect.AdapterID)
	}
	var prepared sdk.PreparedEffect
	if err := json.Unmarshal(effect.Prepared, &prepared); err != nil {
		return domain.EffectAction{}, err
	}
	request := sdk.EffectRequest{ActionID: effect.ID, ProjectRoot: s.Project.Root, NodeID: effect.NodeID, AttemptID: effect.AttemptID, IdempotencyKey: effect.IdempotencyKey, Request: effect.Request, PriorReceipt: effect.Receipt}
	reconcilingTime := s.Now().UTC()
	if _, err := validLeaseAt(state, effect.OwnerRole, reconcilingTime); err != nil {
		return domain.EffectAction{}, err
	}
	reconcilingAt := reconcilingTime.Format(time.RFC3339Nano)
	reconcilingRaw, _ := json.Marshal(map[string]string{"actionId": actionID, "reconcilingAt": reconcilingAt})
	expectedHead := state.HeadHash
	if _, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "effect.reconcile", ActorRole: effect.OwnerRole, IdempotencyKey: idempotencyKey + "/begin", ObjectRef: "effect:" + actionID, RequestDigest: requestDigest}, []journal.Event{{Type: "effect.reconciling", Payload: reconcilingRaw}}, reconcilingTime, &expectedHead); err != nil {
		return domain.EffectAction{}, err
	}
	receipt, err := adapter.Reconcile(ctx, request, prepared, evidence)
	if err != nil {
		return domain.EffectAction{}, err
	}
	return s.observeEffect(actionID, effect.OwnerRole, receipt, idempotencyKey, requestDigest)
}

// acquireEffectReconcileLock serializes dispatch and reconcile observations for
// one Effect across goroutines and OS processes without holding the journal writer
// lock during adapter I/O. Apply acquires it before effect.prepared becomes visible,
// so reconcile cannot overtake the original dispatch. The OS releases the lock
// after a crash, so a durable crash prefix never becomes a permanent retry barrier.
func (s *Service) acquireEffectReconcileLock(ctx context.Context, actionID string) (func(), error) {
	sum := sha256.Sum256([]byte(actionID))
	root := filepath.Join(s.Project.DataDir, "effect-locks")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create effect lock directory: %w", err)
	}
	path := filepath.Join(root, hex.EncodeToString(sum[:])+".lock")
	value, _ := effectReconcileProcessLocks.LoadOrStore(path, make(chan struct{}, 1))
	processLock := value.(chan struct{})
	select {
	case processLock <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	fileLock := flock.New(path)
	locked, err := fileLock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil || !locked {
		<-processLock
		if err != nil {
			return nil, fmt.Errorf("acquire effect reconciliation lock: %w", err)
		}
		return nil, fmt.Errorf("effect reconciliation lock was not acquired")
	}
	return func() {
		_ = fileLock.Unlock()
		<-processLock
	}, nil
}

func (s *Service) observeEffect(actionID, actorRole string, receipt sdk.EffectReceipt, idempotencyKey, requestDigest string) (domain.EffectAction, error) {
	receiptRaw, err := validateEffectReceipt(receipt)
	if err != nil {
		return domain.EffectAction{}, err
	}
	stateBefore, _, err := s.load()
	if err != nil {
		return domain.EffectAction{}, err
	}
	effectBefore, exists := stateBefore.Effects[actionID]
	if !exists {
		return domain.EffectAction{}, fmt.Errorf("unknown effect action %s", actionID)
	}
	incidentID := "effect:" + actionID
	if incident, exists := stateBefore.Incidents[incidentID]; exists && incident.Status != "open" {
		return domain.EffectAction{}, fmt.Errorf("effect observation cannot bypass an incident in status %s", incident.Status)
	}
	now := s.Now().UTC()
	payload, _ := json.Marshal(struct {
		ActionID  string          `json:"actionId"`
		Status    string          `json:"status"`
		Receipt   json.RawMessage `json:"receipt"`
		UpdatedAt string          `json:"updatedAt"`
	}{actionID, receipt.Status, receiptRaw, now.Format(time.RFC3339Nano)})
	events := []journal.Event{{Type: "effect.observed", Payload: payload}}
	if receipt.Status == "unknown" || receipt.Status == "failed" {
		incident := domain.Incident{ID: incidentID, SourceType: "effect", SourceID: actionID, NodeID: effectBefore.NodeID, OwnerRole: effectBefore.OwnerRole, Status: "open", Classification: "external-effect", Deadline: now.Add(time.Hour).Format(time.RFC3339Nano), AttemptBudget: 2, ProgressMetric: "new external receipt or deterministic reconcile result", DependencyCut: domain.DependencyCut(stateBefore, effectBefore.NodeID), OpenedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
		if existing, ok := stateBefore.Incidents[incidentID]; ok {
			incident.OpenedAt, incident.Attempts = existing.OpenedAt, existing.Attempts+1
			incident.Deadline = existing.Deadline
			incident.NoProgressAttempts = existing.NoProgressAttempts
			incident.LastProgress, incident.LastProgressAt = existing.LastProgress, existing.LastProgressAt
			incident.Disposition, incident.DispositionBy, incident.DispositionAt = existing.Disposition, existing.DispositionBy, existing.DispositionAt
		}
		if incident.Attempts >= incident.AttemptBudget {
			incident.Status, incident.CircuitReason = "circuit-open", "effect_attempt_budget_exhausted"
		}
		incidentRaw, _ := json.Marshal(incident)
		events = append(events, journal.Event{Type: "incident.opened", Payload: incidentRaw})
	} else if receipt.Status == "confirmed" {
		if incident, ok := stateBefore.Incidents[incidentID]; ok && incident.Status != "resolved" {
			resolvedRaw, _ := json.Marshal(map[string]string{"incidentId": incidentID, "resolvedAt": now.Format(time.RFC3339Nano)})
			events = append(events, journal.Event{Type: "incident.resolved", Payload: resolvedRaw})
		}
	}
	expectedHead := stateBefore.HeadHash
	if _, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "effect.observe", ActorRole: actorRole, IdempotencyKey: idempotencyKey, ObjectRef: "effect:" + actionID, RequestDigest: requestDigest}, events, now, &expectedHead); err != nil {
		return domain.EffectAction{}, err
	}
	state, segments, err := s.load()
	if err != nil {
		return domain.EffectAction{}, err
	}
	if err := s.Projection.Sync(state, segments); err != nil {
		return domain.EffectAction{}, err
	}
	return state.Effects[actionID], nil
}

func validateEffectReceipt(receipt sdk.EffectReceipt) (json.RawMessage, error) {
	switch receipt.Status {
	case "confirmed", "failed", "unknown", "reconciling":
	default:
		return nil, fmt.Errorf("invalid effect receipt status %s", receipt.Status)
	}
	if len(receipt.Detail) > 0 {
		if err := domain.ValidateAuthorityJSON(receipt.Detail); err != nil {
			return nil, fmt.Errorf("effect receipt detail: %w", err)
		}
	}
	if err := validateReceiptState("transport", receipt.TransportStatus, "not-attempted", "accepted", "rejected", "unknown"); err != nil {
		return nil, err
	}
	if err := validateReceiptState("session", receipt.SessionStatus, "not-created", "created", "unknown"); err != nil {
		return nil, err
	}
	if err := validateReceiptState("delivery", receipt.DeliveryStatus, "pending", "visible", "failed", "unknown"); err != nil {
		return nil, err
	}
	if err := validateReceiptState("acceptance", receipt.AcceptanceStatus, "pending", "accepted", "returned", "unknown"); err != nil {
		return nil, err
	}
	if err := validateReceiptState("completion", receipt.CompletionStatus, "pending", "completed", "failed", "unknown"); err != nil {
		return nil, err
	}
	receiptRaw, _ := json.Marshal(receipt)
	if len(receiptRaw) > 64*1024 {
		return nil, fmt.Errorf("effect receipt cannot exceed 64 KiB")
	}
	if err := domain.RejectSensitiveFields(receiptRaw); err != nil {
		return nil, fmt.Errorf("effect receipt: %w", err)
	}
	return receiptRaw, nil
}

func validateReceiptState(name, value string, allowed ...string) error {
	if value == "" {
		return nil
	}
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("invalid %s receipt state %s", name, value)
}

func mustJSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
func isNullAuthorityJSON(value json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}
func boundedError(err error) string {
	value := err.Error()
	if len(value) > 500 {
		return value[:500]
	}
	return value
}
