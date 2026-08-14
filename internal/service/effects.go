package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/sdk"
	"github.com/google/uuid"
)

type effectNodeInput struct {
	Adapter string          `json:"adapter"`
	Request json.RawMessage `json:"request"`
}

func (s *Service) applyEffectAction(state domain.State, payload actionRefPayload, input json.RawMessage, idempotencyKey string, node domain.NodeDefinition) (ActionResult, error) {
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
	if err := domain.RejectSensitiveFields(declaration.Request); err != nil {
		return ActionResult{}, err
	}
	adapter, ok := s.Providers.Effect(declaration.Adapter)
	if !ok {
		return ActionResult{}, fmt.Errorf("effect adapter %s is not registered", declaration.Adapter)
	}
	request := sdk.EffectRequest{ActionID: payload.ActionID, ProjectRoot: s.Project.Root, NodeID: node.ID, AttemptID: attempt.ID, IdempotencyKey: idempotencyKey, Request: declaration.Request}
	prepared, err := adapter.Prepare(context.Background(), request)
	if err != nil {
		return ActionResult{}, err
	}
	if len(prepared.Binding) > 0 {
		if err := domain.ValidateAuthorityJSON(prepared.Binding); err != nil {
			return ActionResult{}, fmt.Errorf("effect preparation returned invalid authority data: %w", err)
		}
	}
	preparedRaw, _ := json.Marshal(prepared)
	now := s.Now().UTC().Format(time.RFC3339Nano)
	effect := domain.EffectAction{ID: payload.ActionID, NodeID: node.ID, AttemptID: attempt.ID, AdapterID: declaration.Adapter, OwnerRole: payload.RoleID, Status: "prepared", Request: declaration.Request, Prepared: preparedRaw, IdempotencyKey: idempotencyKey, PreparedAt: now, UpdatedAt: now}
	effectRaw, _ := json.Marshal(effect)
	action := domain.ActionRecord{ID: payload.ActionID, Kind: payload.Kind, NodeID: node.ID, AttemptID: attempt.ID, Status: "prepared", Input: input}
	actionRaw, _ := json.Marshal(action)
	expectedHead := payload.HeadHash
	segment, created, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: payload.Kind, ActorRole: payload.RoleID, IdempotencyKey: idempotencyKey}, []journal.Event{{Type: "effect.prepared", Payload: effectRaw}, {Type: "action.applied", Payload: actionRaw}}, s.Now(), &expectedHead)
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
	dispatchedAt := s.Now().UTC().Format(time.RFC3339Nano)
	dispatchedRaw, _ := json.Marshal(map[string]string{"actionId": payload.ActionID, "dispatchedAt": dispatchedAt})
	if _, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "effect.dispatch", ActorRole: payload.RoleID, IdempotencyKey: idempotencyKey + "/dispatch"}, []journal.Event{{Type: "effect.dispatched", Payload: dispatchedRaw}}, s.Now(), nil); err != nil {
		return ActionResult{}, err
	}
	state, segments, err = s.load()
	if err != nil {
		return ActionResult{}, err
	}
	if err := s.Projection.Sync(state, segments); err != nil {
		return ActionResult{}, err
	}
	receipt, dispatchErr := adapter.Dispatch(context.Background(), request, prepared)
	if dispatchErr != nil {
		receipt = sdk.EffectReceipt{Status: "unknown", Detail: mustJSON(map[string]string{"error": boundedError(dispatchErr)})}
	}
	if receipt.Status == "" {
		receipt.Status = "unknown"
	}
	observed, observeErr := s.observeEffect(payload.ActionID, receipt, idempotencyKey+"/observe")
	if observeErr != nil {
		return ActionResult{ActionID: payload.ActionID, Kind: payload.Kind, NodeID: node.ID, AttemptID: attempt.ID, Status: "unknown", Sequence: segment.Sequence}, fmt.Errorf("effect may have occurred; reconcile action %s: %w", payload.ActionID, observeErr)
	}
	return ActionResult{ActionID: payload.ActionID, Kind: payload.Kind, NodeID: node.ID, AttemptID: attempt.ID, Status: observed.Status, Sequence: observed.Sequence}, nil
}

func (s *Service) ReconcileEffect(actionID string, evidence json.RawMessage, idempotencyKey string) (domain.EffectAction, error) {
	if actionID == "" || idempotencyKey == "" {
		return domain.EffectAction{}, fmt.Errorf("action and idempotency key are required")
	}
	state, _, err := s.load()
	if err != nil {
		return domain.EffectAction{}, err
	}
	if _, ok := state.Commands[idempotencyKey]; ok {
		effect, exists := state.Effects[actionID]
		if exists {
			return effect, nil
		}
	}
	effect, ok := state.Effects[actionID]
	if !ok {
		return domain.EffectAction{}, fmt.Errorf("unknown effect action %s", actionID)
	}
	if effect.Status == "confirmed" {
		return effect, nil
	}
	if incident, exists := state.Incidents["effect:"+actionID]; exists && incident.Status == "circuit-open" {
		return domain.EffectAction{}, fmt.Errorf("effect incident circuit is open: %s", incident.CircuitReason)
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
	reconcilingAt := s.Now().UTC().Format(time.RFC3339Nano)
	reconcilingRaw, _ := json.Marshal(map[string]string{"actionId": actionID, "reconcilingAt": reconcilingAt})
	expectedHead := state.HeadHash
	if _, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "effect.reconcile", ActorRole: effect.OwnerRole, IdempotencyKey: idempotencyKey + "/begin"}, []journal.Event{{Type: "effect.reconciling", Payload: reconcilingRaw}}, s.Now(), &expectedHead); err != nil {
		return domain.EffectAction{}, err
	}
	receipt, err := adapter.Reconcile(context.Background(), request, prepared, evidence)
	if err != nil {
		return domain.EffectAction{}, err
	}
	return s.observeEffect(actionID, receipt, idempotencyKey)
}

func (s *Service) observeEffect(actionID string, receipt sdk.EffectReceipt, idempotencyKey string) (domain.EffectAction, error) {
	switch receipt.Status {
	case "confirmed", "failed", "unknown", "reconciling":
	default:
		return domain.EffectAction{}, fmt.Errorf("invalid effect receipt status %s", receipt.Status)
	}
	if len(receipt.Detail) > 0 {
		if err := domain.ValidateAuthorityJSON(receipt.Detail); err != nil {
			return domain.EffectAction{}, fmt.Errorf("effect receipt detail: %w", err)
		}
	}
	if err := validateReceiptState("transport", receipt.TransportStatus, "not-attempted", "accepted", "rejected", "unknown"); err != nil {
		return domain.EffectAction{}, err
	}
	if err := validateReceiptState("session", receipt.SessionStatus, "not-created", "created", "unknown"); err != nil {
		return domain.EffectAction{}, err
	}
	if err := validateReceiptState("delivery", receipt.DeliveryStatus, "pending", "visible", "failed", "unknown"); err != nil {
		return domain.EffectAction{}, err
	}
	if err := validateReceiptState("acceptance", receipt.AcceptanceStatus, "pending", "accepted", "returned", "unknown"); err != nil {
		return domain.EffectAction{}, err
	}
	if err := validateReceiptState("completion", receipt.CompletionStatus, "pending", "completed", "failed", "unknown"); err != nil {
		return domain.EffectAction{}, err
	}
	receiptRaw, _ := json.Marshal(receipt)
	now := s.Now().UTC()
	payload, _ := json.Marshal(struct {
		ActionID  string          `json:"actionId"`
		Status    string          `json:"status"`
		Receipt   json.RawMessage `json:"receipt"`
		UpdatedAt string          `json:"updatedAt"`
	}{actionID, receipt.Status, receiptRaw, now.Format(time.RFC3339Nano)})
	events := []journal.Event{{Type: "effect.observed", Payload: payload}}
	stateBefore, _, err := s.load()
	if err != nil {
		return domain.EffectAction{}, err
	}
	effectBefore, exists := stateBefore.Effects[actionID]
	if !exists {
		return domain.EffectAction{}, fmt.Errorf("unknown effect action %s", actionID)
	}
	incidentID := "effect:" + actionID
	if receipt.Status == "unknown" || receipt.Status == "failed" {
		incident := domain.Incident{ID: incidentID, SourceType: "effect", SourceID: actionID, NodeID: effectBefore.NodeID, OwnerRole: effectBefore.OwnerRole, Status: "open", Classification: "effect-" + receipt.Status, Deadline: now.Add(time.Hour).Format(time.RFC3339Nano), AttemptBudget: 2, ProgressMetric: "new external receipt or deterministic reconcile result", DependencyCut: domain.DependencyCut(stateBefore, effectBefore.NodeID), OpenedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
		if existing, ok := stateBefore.Incidents[incidentID]; ok {
			incident.OpenedAt, incident.Attempts = existing.OpenedAt, existing.Attempts+1
			incident.NoProgressAttempts = existing.NoProgressAttempts
			incident.LastProgress, incident.LastProgressAt = existing.LastProgress, existing.LastProgressAt
		}
		if incident.Attempts >= incident.AttemptBudget {
			incident.Status, incident.CircuitReason = "circuit-open", "effect_attempt_budget_exhausted"
		}
		incidentRaw, _ := json.Marshal(incident)
		events = append(events, journal.Event{Type: "incident.opened", Payload: incidentRaw})
	} else if receipt.Status == "confirmed" {
		if _, ok := stateBefore.Incidents[incidentID]; ok {
			resolvedRaw, _ := json.Marshal(map[string]string{"incidentId": incidentID, "resolvedAt": now.Format(time.RFC3339Nano)})
			events = append(events, journal.Event{Type: "incident.resolved", Payload: resolvedRaw})
		}
	}
	if _, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "effect.observe", IdempotencyKey: idempotencyKey}, events, s.Now(), nil); err != nil {
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
func boundedError(err error) string {
	value := err.Error()
	if len(value) > 500 {
		return value[:500]
	}
	return value
}
