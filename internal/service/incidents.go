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
	if strings.TrimSpace(note) == "" || len(note) > 1024 {
		return domain.Incident{}, fmt.Errorf("progress note must be 1..1024 bytes")
	}
	return s.updateIncident(incidentID, actorRole, idempotencyKey, "incident.progress", func(incident *domain.Incident, now time.Time) error {
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
	if strings.TrimSpace(reason) == "" || len(reason) > 512 {
		return domain.Incident{}, fmt.Errorf("circuit reason must be 1..512 bytes")
	}
	return s.updateIncident(incidentID, actorRole, idempotencyKey, "incident.trip", func(incident *domain.Incident, _ time.Time) error {
		if incident.Status == "resolved" {
			return fmt.Errorf("resolved incident cannot be tripped")
		}
		incident.Status, incident.CircuitReason = "circuit-open", reason
		return nil
	})
}

func (s *Service) ResolveIncident(incidentID, actorRole, resolution, idempotencyKey string) (domain.Incident, error) {
	if strings.TrimSpace(resolution) == "" || len(resolution) > 1024 {
		return domain.Incident{}, fmt.Errorf("resolution must be 1..1024 bytes")
	}
	return s.updateIncident(incidentID, actorRole, idempotencyKey, "incident.resolve", func(incident *domain.Incident, _ time.Time) error {
		if incident.Status == "resolved" {
			return nil
		}
		incident.Status, incident.Resolution = "resolved", resolution
		return nil
	})
}

func (s *Service) updateIncident(incidentID, actorRole, idempotencyKey, commandKind string, mutate func(*domain.Incident, time.Time) error) (domain.Incident, error) {
	if incidentID == "" || actorRole == "" || idempotencyKey == "" {
		return domain.Incident{}, fmt.Errorf("incident, actor role and idempotency key are required")
	}
	state, _, err := s.load()
	if err != nil {
		return domain.Incident{}, err
	}
	if _, ok := state.Commands[idempotencyKey]; ok {
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
	if _, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: commandKind, ActorRole: actorRole, IdempotencyKey: idempotencyKey}, []journal.Event{{Type: "incident.updated", Payload: raw}}, now, &expectedHead); err != nil {
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
