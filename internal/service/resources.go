package service

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/google/uuid"
)

type resourceClosureInput struct {
	Status  string          `json:"status"`
	Receipt json.RawMessage `json:"receipt"`
}

func (s *Service) applyResourceAction(state domain.State, payload actionRefPayload, input json.RawMessage, idempotencyKey, requestDigest string) (ActionResult, error) {
	if _, err := s.requireRoleCapability(state, payload.RoleID, domain.CapabilityResourceClose); err != nil {
		return ActionResult{}, err
	}
	resource, ok := state.Resources[payload.ResourceID]
	if !ok || resource.Status != "active" || resource.AttemptID != payload.AttemptID || resource.NodeID != payload.NodeID || resource.RoleID != payload.RoleID {
		return ActionResult{}, fmt.Errorf("resource action target is invalid or no longer active")
	}
	if payload.Kind == "resource.close" && resource.ClosureStatus != "" && resource.ClosureStatus != "pending" {
		return ActionResult{}, fmt.Errorf("resource closure already requires reconciliation")
	}
	if payload.Kind == "resource.reconcile" && resource.ClosureStatus != "unknown" && resource.ClosureStatus != "failed" && resource.ClosureStatus != "reconciling" {
		return ActionResult{}, fmt.Errorf("resource closure is not reconcilable")
	}
	incidentID := "resource:" + resource.ID
	if incident, exists := state.Incidents[incidentID]; exists && incident.Status != "open" {
		if incident.Status == "circuit-open" {
			return ActionResult{}, fmt.Errorf("resource incident circuit is open: %s", incident.CircuitReason)
		}
		return ActionResult{}, fmt.Errorf("resource incident is %s and cannot be reopened by reconciliation", incident.Status)
	}
	var value resourceClosureInput
	if err := json.Unmarshal(input, &value); err != nil {
		return ActionResult{}, fmt.Errorf("decode resource closure input: %w", err)
	}
	if value.Status != "confirmed" && value.Status != "failed" && value.Status != "unknown" {
		return ActionResult{}, fmt.Errorf("resource closure status must be confirmed, failed or unknown")
	}
	if len(value.Receipt) == 0 || isNullAuthorityJSON(value.Receipt) {
		return ActionResult{}, fmt.Errorf("resource closure receipt is required")
	}
	observedAt := s.Now().UTC()
	if _, err := validLeaseAt(state, payload.RoleID, observedAt); err != nil {
		return ActionResult{}, err
	}
	now := observedAt.Format(time.RFC3339Nano)
	observed, _ := json.Marshal(map[string]any{"resourceId": resource.ID, "status": value.Status, "receipt": value.Receipt, "updatedAt": now})
	events := []journal.Event{{Type: "resource.closure-observed", Payload: observed}}
	if value.Status == "confirmed" {
		released, _ := json.Marshal(map[string]string{"resourceId": resource.ID, "releasedAt": now})
		events = append(events, journal.Event{Type: "resource.released", Payload: released})
		if incident, exists := state.Incidents[incidentID]; exists && incident.Status != "resolved" {
			resolved, _ := json.Marshal(map[string]string{"incidentId": incidentID, "resolvedAt": now})
			events = append(events, journal.Event{Type: "incident.resolved", Payload: resolved})
		}
	} else {
		incident := domain.Incident{ID: incidentID, SourceType: "resource", SourceID: resource.ID, NodeID: resource.NodeID, OwnerRole: resource.RoleID, Status: "open", Classification: "infrastructure", Deadline: observedAt.Add(time.Hour).Format(time.RFC3339Nano), AttemptBudget: 2, Attempts: 1, NoProgressAttempts: 1, ProgressMetric: "new closure receipt or changed observation", DependencyCut: domain.DependencyCut(state, resource.NodeID), OpenedAt: now, UpdatedAt: now}
		if existing, exists := state.Incidents[incidentID]; exists {
			incident.OpenedAt = existing.OpenedAt
			incident.Deadline = existing.Deadline
			incident.Attempts = existing.Attempts + 1
			incident.NoProgressAttempts = existing.NoProgressAttempts + 1
			incident.LastProgress = existing.LastProgress
			incident.LastProgressAt = existing.LastProgressAt
			incident.Disposition = existing.Disposition
			incident.DispositionBy = existing.DispositionBy
			incident.DispositionAt = existing.DispositionAt
			if incident.NoProgressAttempts >= incident.AttemptBudget {
				incident.Status, incident.CircuitReason = "circuit-open", "resource_closure_attempt_budget_exhausted"
			}
			raw, _ := json.Marshal(incident)
			events = append(events, journal.Event{Type: "incident.updated", Payload: raw})
		} else {
			raw, _ := json.Marshal(incident)
			events = append(events, journal.Event{Type: "incident.opened", Payload: raw})
		}
	}
	actionInput, _ := json.Marshal(map[string]string{"resourceId": resource.ID})
	action := domain.ActionRecord{ID: payload.ActionID, Kind: payload.Kind, NodeID: payload.NodeID, AttemptID: payload.AttemptID, Status: value.Status, Input: actionInput}
	actionRaw, _ := json.Marshal(action)
	events = append(events, journal.Event{Type: "action.applied", Payload: actionRaw})
	expectedHead := payload.HeadHash
	segment, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: payload.Kind, ActorRole: payload.RoleID, IdempotencyKey: idempotencyKey, ObjectRef: "action:" + payload.ActionID, RequestDigest: requestDigest}, events, observedAt, &expectedHead)
	if err != nil {
		return ActionResult{}, err
	}
	state, segments, err := s.load()
	if err != nil {
		return ActionResult{}, err
	}
	if err := s.Projection.Sync(state, segments); err != nil {
		return ActionResult{}, err
	}
	return ActionResult{ActionID: payload.ActionID, Kind: payload.Kind, NodeID: payload.NodeID, AttemptID: payload.AttemptID, ObjectRef: "resource:" + resource.ID, Status: value.Status, Sequence: segment.Sequence}, nil
}
