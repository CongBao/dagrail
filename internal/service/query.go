package service

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
)

type PreWaitAudit struct {
	SafeToWait        bool     `json:"safeToWait"`
	Cursor            uint64   `json:"cursor"`
	ReadyNodes        []string `json:"readyNodes,omitempty"`
	SubmittedAttempts []string `json:"submittedAttempts,omitempty"`
	ActiveAttempts    []string `json:"activeAttempts,omitempty"`
	WaitingAttempts   []string `json:"waitingAttempts,omitempty"`
	StaleAttempts     []string `json:"staleAttempts,omitempty"`
	ExpiredRoles      []string `json:"expiredRoles,omitempty"`
	PendingEffects    []string `json:"pendingEffects,omitempty"`
	ActiveResources   []string `json:"activeResources,omitempty"`
	OrphanedResources []string `json:"orphanedResources,omitempty"`
	OpenIncidents     []string `json:"openIncidents,omitempty"`
	ZeroReadyCut      []string `json:"zeroReadyCut,omitempty"`
	Reasons           []string `json:"reasons,omitempty"`
}

type EvidenceIndex struct {
	Packages  []EvidencePackageSummary `json:"packages"`
	Decisions []ReuseDecisionSummary   `json:"reuseDecisions"`
}

type EvidencePackageSummary struct {
	ID         string `json:"id"`
	NodeID     string `json:"nodeId"`
	AttemptID  string `json:"attemptId"`
	CoreDigest string `json:"coreDigest"`
	CreatedAt  string `json:"createdAt"`
}

type ReuseDecisionSummary struct {
	ID        string `json:"id"`
	PackageID string `json:"packageId"`
	PolicyID  string `json:"policyId"`
	Result    string `json:"result"`
	CreatedAt string `json:"createdAt"`
}

func (s *Service) ListEvidence(nodeID, attemptID string) (EvidenceIndex, error) {
	state, _, err := s.load()
	if err != nil {
		return EvidenceIndex{}, err
	}
	result := EvidenceIndex{}
	packageIDs := map[string]bool{}
	for _, pack := range state.EvidencePackages {
		if (nodeID != "" && pack.NodeID != nodeID) || (attemptID != "" && pack.AttemptID != attemptID) {
			continue
		}
		packageIDs[pack.ID] = true
		result.Packages = append(result.Packages, EvidencePackageSummary{ID: pack.ID, NodeID: pack.NodeID, AttemptID: pack.AttemptID, CoreDigest: pack.CoreDigest, CreatedAt: pack.CreatedAt})
	}
	for _, decision := range state.ReuseDecisions {
		if (nodeID != "" || attemptID != "") && !packageIDs[decision.PackageID] {
			continue
		}
		result.Decisions = append(result.Decisions, ReuseDecisionSummary{ID: decision.ID, PackageID: decision.PackageID, PolicyID: decision.Policy.ID, Result: decision.Result, CreatedAt: decision.CreatedAt})
	}
	sort.Slice(result.Packages, func(i, j int) bool { return result.Packages[i].ID < result.Packages[j].ID })
	sort.Slice(result.Decisions, func(i, j int) bool { return result.Decisions[i].ID < result.Decisions[j].ID })
	return result, nil
}

func (s *Service) PreWait() (PreWaitAudit, error) {
	state, _, err := s.load()
	if err != nil {
		return PreWaitAudit{}, err
	}
	audit := PreWaitAudit{SafeToWait: true, Cursor: state.HeadSequence, ReadyNodes: domainFrontier(state)}
	now := s.Now().UTC()
	if len(audit.ReadyNodes) > 0 {
		audit.Reasons = append(audit.Reasons, "ready nodes require assignment or an explicit decision")
	}
	for _, attempt := range state.Attempts {
		switch attempt.Status {
		case "submitted":
			audit.SubmittedAttempts = append(audit.SubmittedAttempts, attempt.ID)
		case "running", "leased":
			audit.ActiveAttempts = append(audit.ActiveAttempts, attempt.ID)
			updated, parseErr := time.Parse(time.RFC3339Nano, attempt.UpdatedAt)
			if parseErr != nil || now.Sub(updated) > 30*time.Minute {
				audit.StaleAttempts = append(audit.StaleAttempts, attempt.ID)
			}
		case "waiting":
			audit.ActiveAttempts = append(audit.ActiveAttempts, attempt.ID)
			audit.WaitingAttempts = append(audit.WaitingAttempts, attempt.ID)
		}
	}
	for roleID, lease := range state.Leases {
		if !lease.Active {
			continue
		}
		expires, parseErr := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
		if parseErr != nil || !now.Before(expires) {
			audit.ExpiredRoles = append(audit.ExpiredRoles, roleID)
		}
	}
	for actionID, effect := range state.Effects {
		switch effect.Status {
		case "prepared", "dispatched", "unknown", "reconciling":
			audit.PendingEffects = append(audit.PendingEffects, actionID)
		}
	}
	for resourceID, lease := range state.Resources {
		if lease.Status == "active" {
			audit.ActiveResources = append(audit.ActiveResources, resourceID)
			attempt, ok := state.Attempts[lease.AttemptID]
			if !ok || attempt.Status == "terminal" {
				audit.OrphanedResources = append(audit.OrphanedResources, resourceID)
			}
		}
	}
	for incidentID, incident := range state.Incidents {
		if incident.Status != "resolved" {
			audit.OpenIncidents = append(audit.OpenIncidents, incidentID)
		}
	}
	sort.Strings(audit.SubmittedAttempts)
	sort.Strings(audit.ActiveAttempts)
	sort.Strings(audit.WaitingAttempts)
	sort.Strings(audit.StaleAttempts)
	sort.Strings(audit.ExpiredRoles)
	sort.Strings(audit.PendingEffects)
	sort.Strings(audit.ActiveResources)
	sort.Strings(audit.OrphanedResources)
	sort.Strings(audit.OpenIncidents)
	if len(audit.SubmittedAttempts) > 0 {
		audit.Reasons = append(audit.Reasons, "submitted attempts require a terminal action")
	}
	if len(audit.StaleAttempts) > 0 {
		audit.Reasons = append(audit.Reasons, "running attempts have no progress checkpoint within the liveness window")
	}
	if len(audit.ExpiredRoles) > 0 {
		audit.Reasons = append(audit.Reasons, "expired role leases require release or takeover")
	}
	if len(audit.PendingEffects) > 0 {
		audit.Reasons = append(audit.Reasons, "pending effects require confirmation, failure classification or reconciliation")
	}
	if len(audit.OrphanedResources) > 0 {
		audit.Reasons = append(audit.Reasons, "active resource leases have no recoverable attempt")
	}
	if len(audit.OpenIncidents) > 0 {
		audit.Reasons = append(audit.Reasons, "open incidents require ownership, progress or circuit-breaker action")
	}
	frontier := domain.ComputeFrontier(state)
	if len(frontier.Ready) == 0 && len(frontier.Blocked) > 0 && len(audit.ActiveAttempts) == 0 && len(audit.PendingEffects) == 0 {
		audit.ZeroReadyCut = append(audit.ZeroReadyCut, frontier.Blocked...)
		audit.Reasons = append(audit.Reasons, "zero-ready dependency cut has no active recovery work")
	}
	audit.SafeToWait = len(audit.Reasons) == 0
	return audit, nil
}

func domainFrontier(state domain.State) []string { return domain.ComputeFrontier(state).Ready }

func (s *Service) Inspect(ref string) (any, error) {
	state, segments, err := s.load()
	if err != nil {
		return nil, err
	}
	if ref == "project" {
		return map[string]any{"project": s.Project.Config, "state": map[string]any{"graphRevision": state.GraphRevision, "headSequence": state.HeadSequence, "headHash": state.HeadHash}}, nil
	}
	prefix, id, ok := strings.Cut(ref, ":")
	if !ok || id == "" {
		return nil, fmt.Errorf("inspect ref must be project or kind:id")
	}
	switch prefix {
	case "history":
		var cursor uint64
		if _, err := fmt.Sscanf(id, "%d", &cursor); err != nil || cursor > state.HeadSequence {
			return nil, fmt.Errorf("invalid history cursor %s", id)
		}
		end := cursor + 50
		if end > uint64(len(segments)) {
			end = uint64(len(segments))
		}
		return map[string]any{"fromCursor": cursor, "toCursor": end, "segments": segments[cursor:end], "truncated": end < uint64(len(segments))}, nil
	case "node":
		node, found := state.NodeDefinition(id)
		if !found {
			return nil, fmt.Errorf("unknown node %s", id)
		}
		return map[string]any{"node": node, "runtime": state.Nodes[id], "attemptIds": state.NodeAttempts[id]}, nil
	case "attempt":
		value, found := state.Attempts[id]
		if !found {
			return nil, fmt.Errorf("unknown attempt %s", id)
		}
		return value, nil
	case "checkpoint":
		value, found := state.Checkpoints[id]
		if !found {
			return nil, fmt.Errorf("unknown checkpoint %s", id)
		}
		return value, nil
	case "evidence-package":
		value, found := state.EvidencePackages[id]
		if !found {
			return nil, fmt.Errorf("unknown execution package %s", id)
		}
		return value, nil
	case "reuse-decision":
		value, found := state.ReuseDecisions[id]
		if !found {
			return nil, fmt.Errorf("unknown reuse decision %s", id)
		}
		return value, nil
	case "evidence":
		for _, checkpoint := range state.Checkpoints {
			for _, evidence := range checkpoint.EvidenceRefs {
				if evidence.Digest == id {
					return map[string]any{"evidence": evidence, "checkpointId": checkpoint.ID, "attemptId": checkpoint.AttemptID}, nil
				}
			}
		}
		for _, pack := range state.EvidencePackages {
			artifacts := append([]domain.ArtifactRef{pack.Candidate, pack.ProspectiveTree}, pack.Artifacts...)
			for _, artifact := range artifacts {
				if artifact.Digest == id {
					return map[string]any{"artifact": artifact, "packageId": pack.ID, "attemptId": pack.AttemptID}, nil
				}
			}
		}
		return nil, fmt.Errorf("unknown evidence digest %s", id)
	case "action":
		value, found := state.Actions[id]
		if !found {
			return nil, fmt.Errorf("unknown action %s", id)
		}
		return value, nil
	case "role":
		value, found := state.Leases[id]
		if !found {
			return nil, fmt.Errorf("unknown role lease %s", id)
		}
		return value, nil
	case "effect":
		value, found := state.Effects[id]
		if !found {
			return nil, fmt.Errorf("unknown effect %s", id)
		}
		return value, nil
	case "resource":
		value, found := state.Resources[id]
		if !found {
			return nil, fmt.Errorf("unknown resource lease %s", id)
		}
		return value, nil
	case "incident":
		value, found := state.Incidents[id]
		if !found {
			return nil, fmt.Errorf("unknown incident %s", id)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported inspect ref %s", ref)
	}
}
