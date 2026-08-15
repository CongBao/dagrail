package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/google/uuid"
)

func (s *Service) ProgressIncident(incidentID, actorRole, note string, madeProgress bool, idempotencyKey string) (domain.Incident, error) {
	if err := validateIncidentText("progress note", note, 1024); err != nil {
		return domain.Incident{}, err
	}
	requestDigest, err := incidentRequestDigest("incident.progress", map[string]any{"note": note, "madeProgress": madeProgress})
	if err != nil {
		return domain.Incident{}, err
	}
	return s.updateIncident(incidentID, actorRole, idempotencyKey, "incident.progress", requestDigest, func(incident *domain.Incident, now time.Time) error {
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
	if err := validateIncidentText("circuit reason", reason, 512); err != nil {
		return domain.Incident{}, err
	}
	requestDigest, err := incidentRequestDigest("incident.trip", map[string]any{"reason": reason})
	if err != nil {
		return domain.Incident{}, err
	}
	return s.updateIncident(incidentID, actorRole, idempotencyKey, "incident.trip", requestDigest, func(incident *domain.Incident, _ time.Time) error {
		if incident.Status == "resolved" {
			return fmt.Errorf("resolved incident cannot be tripped")
		}
		incident.Status, incident.CircuitReason = "circuit-open", reason
		return nil
	})
}

func (s *Service) ResolveIncident(incidentID, actorRole, resolution, idempotencyKey string) (domain.Incident, error) {
	if err := validateIncidentText("resolution", resolution, 1024); err != nil {
		return domain.Incident{}, err
	}
	requestDigest, err := incidentRequestDigest("incident.resolve", map[string]any{"resolution": resolution})
	if err != nil {
		return domain.Incident{}, err
	}
	return s.updateIncident(incidentID, actorRole, idempotencyKey, "incident.resolve", requestDigest, func(incident *domain.Incident, _ time.Time) error {
		if incident.Status == "resolved" {
			return nil
		}
		incident.Status, incident.Resolution = "resolved", resolution
		return nil
	})
}

func (s *Service) SetIncidentDisposition(incidentID, actorRole, disposition, note, idempotencyKey string) (domain.Incident, error) {
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
	return s.updateIncident(incidentID, actorRole, idempotencyKey, "incident.disposition", requestDigest, func(incident *domain.Incident, now time.Time) error {
		if incident.Status == "resolved" {
			return fmt.Errorf("resolved incident cannot receive a disposition")
		}
		incident.Disposition = disposition
		incident.DispositionBy = actorRole
		incident.DispositionAt = now.Format(time.RFC3339Nano)
		incident.LastProgress = note
		incident.LastProgressAt = now.Format(time.RFC3339Nano)
		return nil
	})
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

func (s *Service) updateIncident(incidentID, actorRole, idempotencyKey, commandKind, requestDigest string, mutate func(*domain.Incident, time.Time) error) (domain.Incident, error) {
	if incidentID == "" || actorRole == "" || idempotencyKey == "" {
		return domain.Incident{}, fmt.Errorf("incident, actor role and idempotency key are required")
	}
	state, _, err := s.load()
	if err != nil {
		return domain.Incident{}, err
	}
	if command, ok := state.Commands[idempotencyKey]; ok {
		if command.Kind != commandKind || command.ActorRole != actorRole || command.ObjectRef != "incident:"+incidentID || (command.RequestDigest != "" && command.RequestDigest != requestDigest) {
			return domain.Incident{}, fmt.Errorf("idempotency key is already bound to another command")
		}
		incident, exists := state.Incidents[incidentID]
		if !exists {
			return domain.Incident{}, fmt.Errorf("idempotent command references unavailable incident")
		}
		return incident, nil
	}
	incident, ok := state.Incidents[incidentID]
	if !ok {
		return domain.Incident{}, fmt.Errorf("unknown incident %s", incidentID)
	}
	if incident.OwnerRole != "" && incident.OwnerRole != actorRole {
		return domain.Incident{}, fmt.Errorf("incident %s belongs to role %s", incidentID, incident.OwnerRole)
	}
	if _, err := s.requireRoleCapability(state, actorRole, domain.CapabilityIncidentManage); err != nil {
		return domain.Incident{}, err
	}
	if _, err := s.validLease(state, actorRole); err != nil {
		return domain.Incident{}, err
	}
	now := s.Now().UTC()
	if err := mutate(&incident, now); err != nil {
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
