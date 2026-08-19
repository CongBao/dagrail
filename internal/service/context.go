package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/CongBao/dagrail/internal/domain"
)

type ContextEnvelope struct {
	View        string         `json:"view"`
	Cursor      uint64         `json:"cursor"`
	Truncated   bool           `json:"truncated"`
	InspectRefs []string       `json:"inspectRefs,omitempty"`
	Data        map[string]any `json:"data"`
}

const MinimumContextBudgetBytes = 512

type ContextBudgetLimit struct {
	View  string
	Bytes int
}

var contextBudgetLimits = []ContextBudgetLimit{
	{View: "orchestrator", Bytes: 12288},
	{View: "reviewer", Bytes: 12288},
	{View: "worker", Bytes: 8192},
}

func ContextBudgetLimits() []ContextBudgetLimit {
	return append([]ContextBudgetLimit(nil), contextBudgetLimits...)
}

func ContextBudgetForView(view string) (int, bool) {
	for _, limit := range contextBudgetLimits {
		if limit.View == view {
			return limit.Bytes, true
		}
	}
	return 0, false
}

func (s *Service) Context(view, roleID, nodeID string, budget int) ([]byte, error) {
	return s.ContextSinceContext(context.Background(), view, roleID, nodeID, budget, 0)
}

func (s *Service) ContextSince(view, roleID, nodeID string, budget int, cursor uint64) ([]byte, error) {
	return s.ContextSinceContext(context.Background(), view, roleID, nodeID, budget, cursor)
}

func (s *Service) ContextSinceContext(ctx context.Context, view, roleID, nodeID string, budget int, cursor uint64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	maximum, ok := ContextBudgetForView(view)
	if !ok {
		return nil, fmt.Errorf("context view must be orchestrator, worker, or reviewer")
	}
	if budget == 0 {
		budget = maximum
	}
	if budget < MinimumContextBudgetBytes {
		return nil, fmt.Errorf("context budget must be at least 512 bytes")
	}
	if budget > maximum {
		return nil, fmt.Errorf("context budget for %s cannot exceed %d bytes", view, maximum)
	}
	state, segments, err := s.load()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if roleID != "" {
		if _, err := s.actionSecret(); err != nil {
			return nil, err
		}
	}
	frontier, err := domain.ComputeFrontierContext(ctx, state)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"project": map[string]any{"id": state.ProjectID, "name": s.Project.Config.Name}, "graphRevision": state.GraphRevision, "frontier": frontier}
	refs := []string{"project", "frontier"}
	if cursor > state.HeadSequence {
		return nil, fmt.Errorf("cursor %d is ahead of journal head %d", cursor, state.HeadSequence)
	}
	if cursor > 0 && cursor < state.HeadSequence {
		delta := make([]map[string]any, 0)
		deltaTruncated := false
		for _, segment := range segments[cursor:] {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			types := make([]string, 0, len(segment.Events))
			for _, event := range segment.Events {
				types = append(types, event.Type)
			}
			candidate := append(delta, map[string]any{"sequence": segment.Sequence, "command": segment.Command.Kind, "events": types})
			encoded, _ := json.Marshal(candidate)
			if len(encoded) > 4096 {
				deltaTruncated = true
				break
			}
			delta = candidate
		}
		data["delta"] = delta
		if deltaTruncated {
			data["deltaTruncated"] = true
			refs = append(refs, fmt.Sprintf("history:%d", cursor))
		}
	}
	if view == "orchestrator" && len(state.Incidents) > 0 {
		index := incidentIndex(state, 0, 8)
		if index.Total > 0 {
			indexRef := incidentIndexRef(state, strings.TrimPrefix(index.InventoryDigest, "sha256:"), 0)
			data["incidentSummary"] = map[string]any{"openCount": index.Total, "items": index.Items, "truncated": index.NextRef != "", "indexRef": indexRef}
			refs = append(refs, indexRef)
			for _, incident := range index.Items {
				if incident.IncidentRef != "" {
					refs = append(refs, incident.IncidentRef)
				} else {
					refs = append(refs, "incident:"+incident.ID)
				}
			}
		}
	}
	if roleID != "" {
		data["authorization"] = authorizationForRole(state, roleID, s.Now().UTC())
		data["operationsRef"] = operationsRefForRole(state, roleID)
		refs = append(refs, operationsRefForRole(state, roleID))
		if lease, ok := state.Leases[roleID]; ok {
			data["roleLease"] = lease
			refs = append(refs, "role:"+roleID)
		}
	}
	if nodeID != "" {
		node, ok := state.NodeDefinition(nodeID)
		if !ok {
			return nil, fmt.Errorf("unknown node %s", nodeID)
		}
		data["node"] = node
		data["runtime"] = state.Nodes[nodeID]
		refs = append(refs, "node:"+nodeID)
		if attempt, ok := state.LatestAttempt(nodeID); ok {
			data["attempt"] = attempt
			refs = append(refs, "attempt:"+attempt.ID)
			if checkpoint, exists := state.Checkpoints[attempt.CheckpointID]; exists {
				data["checkpoint"] = checkpoint
				refs = append(refs, "checkpoint:"+checkpoint.ID)
			}
			if decisionIDs := state.AttemptDecisions[attempt.ID]; len(decisionIDs) > 0 {
				latest := state.Decisions[decisionIDs[len(decisionIDs)-1]]
				data["decision"] = latest
				refs = append(refs, "decision:"+latest.ID)
			}
			resourceRefs := []string{}
			for _, resource := range state.Resources {
				if resource.AttemptID == attempt.ID {
					resourceRefs = append(resourceRefs, "resource:"+resource.ID)
				}
			}
			sort.Strings(resourceRefs)
			if len(resourceRefs) > 0 {
				data["resourceRefs"] = resourceRefs
				refs = append(refs, resourceRefs...)
			}
			if packageIDs := state.AttemptPackages[attempt.ID]; len(packageIDs) > 0 {
				packageRefs := make([]string, 0, len(packageIDs))
				for _, packageID := range packageIDs {
					packageRef := "evidence-package:" + packageID
					packageRefs = append(packageRefs, packageRef)
					refs = append(refs, packageRef)
				}
				data["executionPackageRefs"] = packageRefs
				latestPackageID := packageIDs[len(packageIDs)-1]
				if decisionIDs := state.PackageDecisions[latestPackageID]; len(decisionIDs) > 0 {
					latest := state.ReuseDecisions[decisionIDs[len(decisionIDs)-1]]
					data["latestReuseDecision"] = ReuseDecisionSummary{ID: latest.ID, PackageID: latest.PackageID, PolicyID: latest.Policy.ID, Result: latest.Result, CreatedAt: latest.CreatedAt}
					refs = append(refs, "reuse-decision:"+latest.ID)
				}
			}
		}
	}
	if roleID != "" && nodeID != "" {
		if actions, listErr := s.listActions(state, roleID, nodeID); listErr == nil {
			data["allowedActions"] = actions.Actions
		}
	}
	if view == "orchestrator" && roleID != "" {
		actions, actionsErr := s.projectAllowedActionsContext(ctx, state, roleID, 7, s.Now().UTC())
		if actionsErr != nil {
			return nil, actionsErr
		}
		if len(actions) > 0 {
			if len(actions) > 6 {
				data["projectAllowedActionsTruncated"] = true
				actions = actions[:6]
			}
			data["projectAllowedActions"] = actions
		}
		audit, auditErr := s.preWaitFromStateContext(ctx, state)
		if auditErr == nil {
			data["remediationCount"] = audit.RemediationCount
			if len(audit.Remediations) > 8 {
				data["remediationsTruncated"] = true
				audit.Remediations = audit.Remediations[:8]
			}
			data["remediations"] = audit.Remediations
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Strings(refs)
	envelope := ContextEnvelope{View: view, Cursor: state.HeadSequence, Data: data}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	if len(raw) <= budget {
		return raw, nil
	}
	envelope.Truncated = true
	envelope.InspectRefs = boundedInspectRefs(refs, 32)
	delete(data, "delta")
	delete(data, "roleLease")
	if actions, ok := data["allowedActions"].([]AllowedAction); ok && len(actions) > 1 {
		data["allowedActions"] = actions[:1]
		data["allowedActionsTruncated"] = true
	}
	if actions, ok := data["projectAllowedActions"].([]AllowedAction); ok && len(actions) > 1 {
		data["projectAllowedActions"] = actions[:1]
		data["projectAllowedActionsTruncated"] = true
	}
	if remediations, ok := data["remediations"].([]Remediation); ok && len(remediations) > 2 {
		data["remediations"] = remediations[:2]
		data["remediationsTruncated"] = true
	}
	if summary, ok := data["incidentSummary"].(map[string]any); ok {
		delete(summary, "items")
	}
	if value, ok := data["frontier"].(domain.Frontier); ok {
		value.Blocked = nil
		data["frontier"] = value
	}
	raw, err = json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	if len(raw) <= budget {
		return raw, nil
	}
	essentialRefs := []string{"project", "frontier"}
	minimal := map[string]any{"projectRef": "project", "graphRevision": state.GraphRevision}
	if roleID != "" {
		authorization := authorizationForRole(state, roleID, s.Now().UTC())
		minimal["authorization"] = map[string]any{"roleRef": roleRefForRole(state, authorization.RoleID), "leaseState": authorization.LeaseState}
		minimal["operationsRef"] = operationsRefForRole(state, roleID)
		essentialRefs = append(essentialRefs, operationsRefForRole(state, roleID))
	}
	if index := incidentIndex(state, 0, 1); view == "orchestrator" && index.Total > 0 {
		indexRef := incidentIndexRef(state, strings.TrimPrefix(index.InventoryDigest, "sha256:"), 0)
		minimal["incidents"] = map[string]any{"openCount": index.Total, "indexRef": indexRef}
		essentialRefs = append(essentialRefs, indexRef)
	}
	if nodeID != "" {
		minimal["nodeRef"] = nodeRefForNode(state, nodeID)
		minimal["runtime"] = state.Nodes[nodeID]
		essentialRefs = append(essentialRefs, nodeRefForNode(state, nodeID))
	}
	envelope.Data = minimal
	envelope.InspectRefs = boundedInspectRefs(essentialRefs, 8)
	marshalWithReady := func(count int) ([]byte, error) {
		minimal["frontier"] = map[string]any{
			"ready":          frontier.Ready[:count],
			"readyCount":     len(frontier.Ready),
			"readyTruncated": count < len(frontier.Ready),
		}
		return json.Marshal(envelope)
	}
	empty, err := marshalWithReady(0)
	if err != nil {
		return nil, err
	}
	if len(empty) > budget {
		delete(minimal, "runtime")
		envelope.InspectRefs = nil
		empty, err = marshalWithReady(0)
		if err != nil {
			return nil, err
		}
	}
	if len(empty) > budget {
		delete(minimal, "incidents")
		delete(minimal, "projectRef")
		empty, err = marshalWithReady(0)
		if err != nil {
			return nil, err
		}
	}
	if len(empty) > budget {
		return nil, fmt.Errorf("context invariant set exceeds byte budget")
	}
	low, high := 0, len(frontier.Ready)
	best := empty
	for low <= high {
		middle := low + (high-low)/2
		candidate, marshalErr := marshalWithReady(middle)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if len(candidate) <= budget {
			best = candidate
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best, nil
}

func boundedInspectRefs(refs []string, limit int) []string {
	seen := map[string]bool{}
	result := make([]string, 0, limit)
	for _, ref := range refs {
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		result = append(result, ref)
	}
	sort.Strings(result)
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}
