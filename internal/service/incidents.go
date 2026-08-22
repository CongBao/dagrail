package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/google/uuid"
)

const incidentResolutionSupersededByRepair = "superseded_by_repair"
const incidentControlAuthority = "incident.control"

func (s *Service) ProgressIncident(incidentID, actorRole, note string, madeProgress bool, idempotencyKey string) (domain.Incident, error) {
	return s.ProgressIncidentContext(context.Background(), incidentID, actorRole, note, madeProgress, idempotencyKey)
}

func (s *Service) ProgressIncidentContext(ctx context.Context, incidentID, actorRole, note string, madeProgress bool, idempotencyKey string) (domain.Incident, error) {
	if err := validateIncidentText("progress note", note, 1024); err != nil {
		return domain.Incident{}, err
	}
	requestDigest, err := incidentRequestDigest("incident.progress", map[string]any{"note": note, "madeProgress": madeProgress})
	if err != nil {
		return domain.Incident{}, err
	}
	return s.updateIncident(ctx, incidentID, actorRole, idempotencyKey, "incident.progress", requestDigest, func(incident *domain.Incident, _ domain.State, now time.Time) error {
		if incident.Status != "open" {
			return fmt.Errorf("incident %s is not open", incident.ID)
		}
		incident.Attempts++
		incident.LastProgress, incident.LastProgressAt = note, now.Format(time.RFC3339Nano)
		if madeProgress {
			incident.NoProgressAttempts = 0
		} else {
			incident.NoProgressAttempts++
		}
		deadline, deadlineErr := time.Parse(time.RFC3339Nano, incident.Deadline)
		if incident.AttemptBudget > 0 && incident.NoProgressAttempts >= incident.AttemptBudget {
			incident.Status, incident.CircuitReason = "circuit-open", "no_progress_attempt_budget_exhausted"
		} else if deadlineErr == nil && !now.Before(deadline) {
			incident.Status, incident.CircuitReason = "circuit-open", "deadline_exceeded"
		}
		return nil
	})
}

func (s *Service) TripIncident(incidentID, actorRole, reason, idempotencyKey string) (domain.Incident, error) {
	return s.TripIncidentContext(context.Background(), incidentID, actorRole, reason, idempotencyKey)
}

func (s *Service) TripIncidentContext(ctx context.Context, incidentID, actorRole, reason, idempotencyKey string) (domain.Incident, error) {
	if err := validateIncidentText("circuit reason", reason, 512); err != nil {
		return domain.Incident{}, err
	}
	requestDigest, err := incidentRequestDigest("incident.trip", map[string]any{"reason": reason})
	if err != nil {
		return domain.Incident{}, err
	}
	return s.updateIncident(ctx, incidentID, actorRole, idempotencyKey, "incident.trip", requestDigest, func(incident *domain.Incident, _ domain.State, _ time.Time) error {
		if incident.Status == "resolved" {
			return fmt.Errorf("resolved incident cannot be tripped")
		}
		incident.Status, incident.CircuitReason = "circuit-open", reason
		return nil
	})
}

func (s *Service) ResolveIncident(incidentID, actorRole, resolution, idempotencyKey string) (domain.Incident, error) {
	return s.ResolveIncidentContext(context.Background(), incidentID, actorRole, resolution, idempotencyKey)
}

func (s *Service) ResolveIncidentContext(ctx context.Context, incidentID, actorRole, resolution, idempotencyKey string) (domain.Incident, error) {
	if err := validateIncidentText("resolution", resolution, 1024); err != nil {
		return domain.Incident{}, err
	}
	if resolution == incidentResolutionSupersededByRepair {
		return domain.Incident{}, fmt.Errorf("resolution %s is reserved for incident supersede", resolution)
	}
	requestDigest, err := incidentRequestDigest("incident.resolve", map[string]any{"resolution": resolution})
	if err != nil {
		return domain.Incident{}, err
	}
	return s.updateIncident(ctx, incidentID, actorRole, idempotencyKey, "incident.resolve", requestDigest, func(incident *domain.Incident, _ domain.State, _ time.Time) error {
		if incident.Status == "resolved" {
			return fmt.Errorf("incident %s is already resolved", incident.ID)
		}
		if incident.SourceType == "resource" || incident.SourceType == "effect" {
			return fmt.Errorf("%s incident %s can only be resolved by a confirmed observation", incident.SourceType, incident.ID)
		}
		incident.Status, incident.Resolution = "resolved", resolution
		return nil
	})
}

func (s *Service) SetIncidentDisposition(incidentID, actorRole, disposition, note, idempotencyKey string) (domain.Incident, error) {
	return s.SetIncidentDispositionContext(context.Background(), incidentID, actorRole, disposition, note, idempotencyKey)
}

func (s *Service) SetIncidentDispositionContext(ctx context.Context, incidentID, actorRole, disposition, note, idempotencyKey string) (domain.Incident, error) {
	if !domain.ValidIncidentDisposition(disposition) {
		return domain.Incident{}, fmt.Errorf("invalid incident disposition %s", disposition)
	}
	if err := validateIncidentText("disposition note", note, 1024); err != nil {
		return domain.Incident{}, err
	}
	requestDigest, err := incidentRequestDigest("incident.disposition", map[string]any{"disposition": disposition, "note": note})
	if err != nil {
		return domain.Incident{}, err
	}
	return s.updateIncident(ctx, incidentID, actorRole, idempotencyKey, "incident.disposition", requestDigest, func(incident *domain.Incident, _ domain.State, now time.Time) error {
		if incident.Status == "resolved" {
			return fmt.Errorf("resolved incident cannot receive a disposition")
		}
		incident.Disposition = disposition
		incident.DispositionBy = actorRole
		incident.DispositionAt = now.Format(time.RFC3339Nano)
		incident.LastProgress = note
		incident.LastProgressAt = now.Format(time.RFC3339Nano)
		if disposition == "retry" && incident.Status == "circuit-open" {
			incident.Status = "open"
			incident.CircuitReason = ""
			incident.Attempts = 0
			incident.NoProgressAttempts = 0
			incident.Deadline = now.Add(time.Hour).Format(time.RFC3339Nano)
		}
		return nil
	})
}

// ControlResolveIncident applies an exceptional controller-owned closure to a
// terminal Attempt Incident without borrowing or rewriting the original owner
// Role. Ordinary incident.manage mutations remain owner-local.
func (s *Service) ControlResolveIncident(incidentID, actorRole, disposition, resolution, note, idempotencyKey string) (domain.Incident, error) {
	return s.ControlResolveIncidentContext(context.Background(), incidentID, actorRole, disposition, resolution, note, idempotencyKey)
}

func (s *Service) ControlResolveIncidentContext(ctx context.Context, incidentID, actorRole, disposition, resolution, note, idempotencyKey string) (domain.Incident, error) {
	if err := ctx.Err(); err != nil {
		return domain.Incident{}, err
	}
	value := incidentControlResolveInput{Disposition: disposition, Resolution: resolution, Note: note}
	if err := validateIncidentText("controller incident resolution", resolution, 1024); err != nil {
		return domain.Incident{}, err
	}
	if err := validateIncidentText("controller incident note", note, 1024); err != nil {
		return domain.Incident{}, err
	}
	if !domain.ValidIncidentControlDisposition(disposition) {
		return domain.Incident{}, fmt.Errorf("invalid controller incident disposition %s", disposition)
	}
	if resolution == incidentResolutionSupersededByRepair {
		return domain.Incident{}, fmt.Errorf("resolution %s is reserved for incident supersede", resolution)
	}
	requestDigest, err := incidentControlResolveRequestDigest(value)
	if err != nil {
		return domain.Incident{}, err
	}
	if incidentID == "" || actorRole == "" || idempotencyKey == "" {
		return domain.Incident{}, fmt.Errorf("incident, actor role and idempotency key are required")
	}
	state, _, err := s.load()
	if err != nil {
		return domain.Incident{}, err
	}
	if command, exists := state.Commands[idempotencyKey]; exists {
		incident, available := state.Incidents[incidentID]
		if command.Kind != "incident.control-resolve" || command.ActorRole != actorRole || command.ObjectRef != "incident:"+incidentID || (command.RequestDigest != "" && command.RequestDigest != requestDigest) || !available || incident.Control == nil || incident.Control.ActorRole != actorRole || incident.Control.Disposition != disposition || incident.Control.Resolution != resolution || incident.Control.Note != note {
			return domain.Incident{}, fmt.Errorf("idempotency key is already bound to another command")
		}
		return incident, nil
	}
	if _, err := s.requireRoleCapability(state, actorRole, domain.CapabilityIncidentControl); err != nil {
		return domain.Incident{}, err
	}
	now := s.Now().UTC()
	if _, err := validLeaseAt(state, actorRole, now); err != nil {
		return domain.Incident{}, err
	}
	incident, exists := state.Incidents[incidentID]
	if !exists {
		return domain.Incident{}, fmt.Errorf("unknown incident %s", incidentID)
	}
	if err := controlResolveIncidentValue(&incident, state, actorRole, disposition, resolution, note, now); err != nil {
		return domain.Incident{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.Incident{}, err
	}
	incident.UpdatedAt = now.Format(time.RFC3339Nano)
	raw, _ := json.Marshal(incident)
	expectedHead := state.HeadHash
	if _, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "incident.control-resolve", ActorRole: actorRole, IdempotencyKey: idempotencyKey, ObjectRef: "incident:" + incidentID, RequestDigest: requestDigest}, []journal.Event{{Type: "incident.updated", Payload: raw}}, now, &expectedHead); err != nil {
		return domain.Incident{}, err
	}
	state, segments, err := s.load()
	if err != nil {
		return domain.Incident{}, err
	}
	if err := s.Projection.Sync(state, segments); err != nil {
		return domain.Incident{}, err
	}
	return state.Incidents[incidentID], nil
}

func (s *Service) SupersedeIncident(incidentID, successorNodeID, actorRole, note, idempotencyKey string) (domain.Incident, error) {
	return s.SupersedeIncidentContext(context.Background(), incidentID, successorNodeID, actorRole, note, idempotencyKey)
}

func (s *Service) SupersedeIncidentContext(ctx context.Context, incidentID, successorNodeID, actorRole, note, idempotencyKey string) (domain.Incident, error) {
	if strings.TrimSpace(successorNodeID) == "" {
		return domain.Incident{}, fmt.Errorf("successor node is required")
	}
	if err := validateIncidentText("supersede note", note, 1024); err != nil {
		return domain.Incident{}, err
	}
	requestDigest, err := incidentRequestDigest("incident.supersede", map[string]any{"successorNodeId": successorNodeID, "note": note})
	if err != nil {
		return domain.Incident{}, err
	}
	return s.updateIncident(ctx, incidentID, actorRole, idempotencyKey, "incident.supersede", requestDigest, func(incident *domain.Incident, state domain.State, now time.Time) error {
		return supersedeIncidentValue(incident, state, successorNodeID, note, now)
	})
}

func supersedeIncidentValue(incident *domain.Incident, state domain.State, successorNodeID, note string, now time.Time) error {
	if incident.Status == "resolved" {
		return fmt.Errorf("resolved incident cannot be superseded")
	}
	if incident.SourceType != "attempt" {
		return fmt.Errorf("only attempt incidents can be superseded by a repair node")
	}
	if !validIncidentSuccessor(state, *incident, successorNodeID) {
		return fmt.Errorf("node %s is not a declared active repair successor for incident %s", successorNodeID, incident.ID)
	}
	incident.Status = "resolved"
	incident.Resolution = incidentResolutionSupersededByRepair
	incident.RemedyNodeID = successorNodeID
	incident.SupersededAt = now.Format(time.RFC3339Nano)
	incident.LastProgress = note
	incident.LastProgressAt = incident.SupersededAt
	return nil
}

func controlResolveIncidentValue(incident *domain.Incident, state domain.State, actorRole, disposition, resolution, note string, now time.Time) error {
	if err := validateIncidentText("controller incident resolution", resolution, 1024); err != nil {
		return err
	}
	if err := validateIncidentText("controller incident note", note, 1024); err != nil {
		return err
	}
	if err := controlResolvableIncident(state, *incident, actorRole); err != nil {
		return err
	}
	if !domain.ValidIncidentControlDisposition(disposition) {
		return fmt.Errorf("invalid controller incident disposition %s", disposition)
	}
	if resolution == incidentResolutionSupersededByRepair {
		return fmt.Errorf("resolution %s is reserved for incident supersede", resolution)
	}
	appliedAt := now.Format(time.RFC3339Nano)
	incident.Status = "resolved"
	incident.Resolution = resolution
	incident.Disposition = disposition
	incident.DispositionBy = actorRole
	incident.DispositionAt = appliedAt
	incident.LastProgress = note
	incident.LastProgressAt = appliedAt
	incident.Control = &domain.IncidentControl{
		Authority:         incidentControlAuthority,
		ActorRole:         actorRole,
		OriginalOwnerRole: incident.OwnerRole,
		Disposition:       disposition,
		Resolution:        resolution,
		Note:              note,
		AppliedAt:         appliedAt,
	}
	return nil
}

func controlResolvableIncident(state domain.State, incident domain.Incident, actorRole string) error {
	if incident.Status == "resolved" {
		return fmt.Errorf("incident %s is already resolved", incident.ID)
	}
	if incident.SourceType != "attempt" {
		return fmt.Errorf("only terminal Attempt incidents can receive controller disposition")
	}
	if actorRole == incident.OwnerRole {
		return fmt.Errorf("incident owner must use ordinary incident disposition and resolve")
	}
	attempt, exists := state.Attempts[incident.SourceID]
	if !exists || attempt.Status != "terminal" {
		return fmt.Errorf("incident %s does not reference a terminal Attempt", incident.ID)
	}
	runtime := state.Nodes[attempt.NodeID]
	if runtime.Status != "terminal" || (runtime.OutcomeClass != "failure" && runtime.OutcomeClass != "cancelled") {
		return fmt.Errorf("incident %s Attempt is not a terminal failure or cancellation", incident.ID)
	}
	return nil
}

func validateIncidentText(label, value string, maximum int) error {
	if strings.TrimSpace(value) == "" || len([]byte(value)) > maximum {
		return fmt.Errorf("%s must be 1..%d bytes", label, maximum)
	}
	raw, err := json.Marshal(map[string]string{"value": value})
	if err != nil {
		return fmt.Errorf("validate %s: %w", label, err)
	}
	if err := domain.RejectSensitiveFields(raw); err != nil {
		return fmt.Errorf("%s contains prohibited material: %w", label, err)
	}
	return nil
}

func incidentRequestDigest(kind string, value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return authorityRequestDigest(kind, raw)
}

func (s *Service) updateIncident(ctx context.Context, incidentID, actorRole, idempotencyKey, commandKind, requestDigest string, mutate func(*domain.Incident, domain.State, time.Time) error) (domain.Incident, error) {
	if err := ctx.Err(); err != nil {
		return domain.Incident{}, err
	}
	if incidentID == "" || actorRole == "" || idempotencyKey == "" {
		return domain.Incident{}, fmt.Errorf("incident, actor role and idempotency key are required")
	}
	state, _, err := s.load()
	if err != nil {
		return domain.Incident{}, err
	}
	incident, ok := state.Incidents[incidentID]
	if !ok {
		if _, exists := state.Commands[idempotencyKey]; exists {
			return domain.Incident{}, fmt.Errorf("idempotent command references unavailable incident")
		}
		return domain.Incident{}, fmt.Errorf("unknown incident %s", incidentID)
	}
	// Effect observations and their operator-controlled circuit state form one
	// causal stream. Wait for any in-flight dispatch/reconcile observation, then
	// reload below so a trip/disposition cannot be overwritten by a stale receipt.
	if incident.SourceType == "effect" {
		if incident.SourceID == "" {
			return domain.Incident{}, fmt.Errorf("effect incident %s has no source action", incidentID)
		}
		release, lockErr := s.acquireEffectReconcileLock(ctx, incident.SourceID)
		if lockErr != nil {
			return domain.Incident{}, lockErr
		}
		defer release()
		state, _, err = s.load()
		if err != nil {
			return domain.Incident{}, err
		}
		if command, exists := state.Commands[idempotencyKey]; exists {
			if command.Kind != commandKind || command.ActorRole != actorRole || command.ObjectRef != "incident:"+incidentID || (command.RequestDigest != "" && command.RequestDigest != requestDigest) {
				return domain.Incident{}, fmt.Errorf("idempotency key is already bound to another command")
			}
			current, exists := state.Incidents[incidentID]
			if !exists {
				return domain.Incident{}, fmt.Errorf("idempotent command references unavailable incident")
			}
			return current, nil
		}
		incident, ok = state.Incidents[incidentID]
		if !ok || incident.SourceType != "effect" || incident.SourceID == "" {
			return domain.Incident{}, fmt.Errorf("incident %s changed while waiting for its effect observation", incidentID)
		}
	}
	if command, exists := state.Commands[idempotencyKey]; exists {
		if command.Kind != commandKind || command.ActorRole != actorRole || command.ObjectRef != "incident:"+incidentID || (command.RequestDigest != "" && command.RequestDigest != requestDigest) {
			return domain.Incident{}, fmt.Errorf("idempotency key is already bound to another command")
		}
		return incident, nil
	}
	if incident.OwnerRole != "" && incident.OwnerRole != actorRole {
		return domain.Incident{}, fmt.Errorf("incident %s belongs to role %s", incidentID, incident.OwnerRole)
	}
	if _, err := s.requireRoleCapability(state, actorRole, domain.CapabilityIncidentManage); err != nil {
		return domain.Incident{}, err
	}
	now := s.Now().UTC()
	if _, err := validLeaseAt(state, actorRole, now); err != nil {
		return domain.Incident{}, err
	}
	if err := mutate(&incident, state, now); err != nil {
		return domain.Incident{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.Incident{}, err
	}
	incident.UpdatedAt = now.Format(time.RFC3339Nano)
	raw, _ := json.Marshal(incident)
	expectedHead := state.HeadHash
	if _, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: commandKind, ActorRole: actorRole, IdempotencyKey: idempotencyKey, ObjectRef: "incident:" + incidentID, RequestDigest: requestDigest}, []journal.Event{{Type: "incident.updated", Payload: raw}}, now, &expectedHead); err != nil {
		return domain.Incident{}, err
	}
	state, segments, err := s.load()
	if err != nil {
		return domain.Incident{}, err
	}
	if err := s.Projection.Sync(state, segments); err != nil {
		return domain.Incident{}, err
	}
	return state.Incidents[incidentID], nil
}
