package service

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/CongBao/dagrail/internal/domain"
)

type ContextEnvelope struct {
	View        string         `json:"view"`
	Cursor      uint64         `json:"cursor"`
	Truncated   bool           `json:"truncated"`
	InspectRefs []string       `json:"inspectRefs,omitempty"`
	Data        map[string]any `json:"data"`
}

func (s *Service) Context(view, roleID, nodeID string, budget int) ([]byte, error) {
	return s.ContextSince(view, roleID, nodeID, budget, 0)
}

func (s *Service) ContextSince(view, roleID, nodeID string, budget int, cursor uint64) ([]byte, error) {
	if budget <= 0 {
		budget = map[string]int{"orchestrator": 12288, "worker": 8192, "reviewer": 12288}[view]
	}
	if budget < 512 {
		return nil, fmt.Errorf("context budget must be at least 512 bytes")
	}
	state, segments, err := s.load()
	if err != nil {
		return nil, err
	}
	frontier := domain.ComputeFrontier(state)
	data := map[string]any{"project": map[string]any{"id": state.ProjectID, "name": s.Project.Config.Name}, "graphRevision": state.GraphRevision, "frontier": frontier}
	refs := []string{"project", "frontier"}
	if cursor > state.HeadSequence {
		return nil, fmt.Errorf("cursor %d is ahead of journal head %d", cursor, state.HeadSequence)
	}
	if cursor > 0 && cursor < state.HeadSequence {
		delta := make([]map[string]any, 0)
		deltaTruncated := false
		for _, segment := range segments[cursor:] {
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
		open := make([]domain.Incident, 0)
		for _, incident := range state.Incidents {
			if incident.Status != "resolved" {
				open = append(open, incident)
				refs = append(refs, "incident:"+incident.ID)
			}
		}
		sort.Slice(open, func(i, j int) bool { return open[i].ID < open[j].ID })
		sort.Strings(refs)
		data["incidents"] = open
	}
	if roleID != "" {
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
		if actions, listErr := s.ListActions(roleID, nodeID); listErr == nil {
			data["allowedActions"] = actions.Actions
		}
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
	envelope.InspectRefs = refs
	delete(data, "allowedActions")
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
	essentialRefs := []string{"frontier"}
	minimal := map[string]any{"graphRevision": state.GraphRevision}
	if nodeID != "" {
		minimal["nodeRef"] = "node:" + nodeID
		minimal["runtime"] = state.Nodes[nodeID]
		essentialRefs = append(essentialRefs, "node:"+nodeID)
	}
	sort.Strings(essentialRefs)
	envelope.Data = minimal
	envelope.InspectRefs = essentialRefs
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
