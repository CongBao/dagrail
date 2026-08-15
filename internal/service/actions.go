package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/google/uuid"
)

type AllowedAction struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Ref         string         `json:"ref"`
	TargetRef   string         `json:"targetRef,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

type ActionList struct {
	Actions []AllowedAction `json:"actions"`
}

type ActionResult struct {
	ActionID  string `json:"actionId"`
	Kind      string `json:"kind"`
	NodeID    string `json:"nodeId"`
	AttemptID string `json:"attemptId,omitempty"`
	ObjectRef string `json:"objectRef,omitempty"`
	Status    string `json:"status"`
	Sequence  uint64 `json:"sequence"`
}

type actionRefPayload struct {
	ActionID      string `json:"actionId"`
	ProjectID     string `json:"projectId"`
	GraphRevision string `json:"graphRevision"`
	ProviderSet   string `json:"providerSet"`
	HeadHash      string `json:"headHash"`
	RoleID        string `json:"roleId"`
	SessionID     string `json:"sessionId"`
	NodeID        string `json:"nodeId"`
	AttemptID     string `json:"attemptId,omitempty"`
	ResourceID    string `json:"resourceId,omitempty"`
	Kind          string `json:"kind"`
	ExpiresAt     string `json:"expiresAt"`
}

func (s *Service) BindRole(roleID, harness, sessionID string, ttl time.Duration, takeover bool, idempotencyKey string) (domain.RoleLease, error) {
	if idempotencyKey == "" {
		return domain.RoleLease{}, fmt.Errorf("idempotency key is required")
	}
	if roleID == "" || harness == "" || sessionID == "" {
		return domain.RoleLease{}, fmt.Errorf("role, harness and session are required")
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if ttl > 24*time.Hour {
		return domain.RoleLease{}, fmt.Errorf("lease TTL cannot exceed 24 hours")
	}
	bindingRaw, err := json.Marshal(struct {
		RoleID     string `json:"roleId"`
		Harness    string `json:"harness"`
		SessionID  string `json:"sessionId"`
		TTLSeconds int64  `json:"ttlSeconds"`
		Takeover   bool   `json:"takeover"`
	}{roleID, harness, sessionID, int64(ttl / time.Second), takeover})
	if err != nil {
		return domain.RoleLease{}, err
	}
	if err := domain.RejectSensitiveFields(bindingRaw); err != nil {
		return domain.RoleLease{}, fmt.Errorf("role binding contains prohibited material: %w", err)
	}
	requestDigest, err := authorityRequestDigest("role.bind", bindingRaw)
	if err != nil {
		return domain.RoleLease{}, err
	}
	state, _, err := s.load()
	if err != nil {
		return domain.RoleLease{}, err
	}
	if command, ok := state.Commands[idempotencyKey]; ok {
		if command.Kind != "role.bind" || command.ActorRole != roleID || command.ObjectRef != "role:"+roleID || (command.RequestDigest != "" && command.RequestDigest != requestDigest) {
			return domain.RoleLease{}, fmt.Errorf("idempotency key is already bound to another command")
		}
		lease, exists := state.Leases[roleID]
		if exists {
			return lease, nil
		}
	}
	found := false
	if state.Graph != nil {
		for _, role := range state.Graph.Spec.Roles {
			found = found || role.ID == roleID
		}
	}
	if !found {
		return domain.RoleLease{}, fmt.Errorf("unknown role %s", roleID)
	}
	now := s.Now().UTC()
	if current, ok := state.Leases[roleID]; ok && current.Active {
		expires, parseErr := time.Parse(time.RFC3339Nano, current.ExpiresAt)
		if parseErr != nil {
			return domain.RoleLease{}, parseErr
		}
		if now.Before(expires) && current.SessionID != sessionID {
			return domain.RoleLease{}, fmt.Errorf("role %s already has an active lease", roleID)
		}
		if takeover && now.Before(expires) {
			return domain.RoleLease{}, fmt.Errorf("role %s lease has not expired", roleID)
		}
	}
	lease := domain.RoleLease{RoleID: roleID, Harness: harness, SessionID: sessionID, BoundAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(ttl).Format(time.RFC3339Nano), Active: true}
	payload, _ := json.Marshal(lease)
	expectedHead := state.HeadHash
	if _, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "role.bind", ActorRole: roleID, IdempotencyKey: idempotencyKey, ObjectRef: "role:" + roleID, RequestDigest: requestDigest}, []journal.Event{{Type: "role.bound", Payload: payload}}, now, &expectedHead); err != nil {
		return domain.RoleLease{}, err
	}
	state, segments, err := s.load()
	if err != nil {
		return domain.RoleLease{}, err
	}
	if err := s.Projection.Sync(state, segments); err != nil {
		return domain.RoleLease{}, err
	}
	return state.Leases[roleID], nil
}

func (s *Service) ReleaseRole(roleID, sessionID, idempotencyKey string) error {
	if idempotencyKey == "" {
		return fmt.Errorf("idempotency key is required")
	}
	requestRaw, err := json.Marshal(map[string]string{"roleId": roleID, "sessionId": sessionID})
	if err != nil {
		return err
	}
	requestDigest, err := authorityRequestDigest("role.release", requestRaw)
	if err != nil {
		return err
	}
	state, _, err := s.load()
	if err != nil {
		return err
	}
	if command, ok := state.Commands[idempotencyKey]; ok {
		if command.Kind != "role.release" || command.ActorRole != roleID || command.ObjectRef != "role:"+roleID || (command.RequestDigest != "" && command.RequestDigest != requestDigest) {
			return fmt.Errorf("idempotency key is already bound to another command")
		}
		return nil
	}
	lease, ok := state.Leases[roleID]
	if !ok || !lease.Active || lease.SessionID != sessionID {
		return fmt.Errorf("active role lease does not match session")
	}
	payload, _ := json.Marshal(map[string]string{"roleId": roleID, "releasedAt": s.Now().UTC().Format(time.RFC3339Nano)})
	expectedHead := state.HeadHash
	if _, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "role.release", ActorRole: roleID, IdempotencyKey: idempotencyKey, ObjectRef: "role:" + roleID, RequestDigest: requestDigest}, []journal.Event{{Type: "role.released", Payload: payload}}, s.Now(), &expectedHead); err != nil {
		return err
	}
	state, segments, err := s.load()
	if err != nil {
		return err
	}
	return s.Projection.Sync(state, segments)
}

func (s *Service) ListActions(roleID, nodeID string) (ActionList, error) {
	state, _, err := s.load()
	if err != nil {
		return ActionList{}, err
	}
	return s.listActions(state, roleID, nodeID)
}

func (s *Service) listActions(state domain.State, roleID, nodeID string) (ActionList, error) {
	lease, err := s.validLease(state, roleID)
	if err != nil {
		return ActionList{}, err
	}
	node, ok := state.NodeDefinition(nodeID)
	if !ok {
		return ActionList{}, fmt.Errorf("unknown node %s", nodeID)
	}
	if node.Role != roleID {
		return ActionList{}, fmt.Errorf("node %s belongs to role %s", nodeID, node.Role)
	}
	if _, err := s.requireRoleCapability(state, roleID, domain.RequiredNodeCapability(node.Kind)); err != nil {
		return ActionList{}, err
	}
	runtime := state.Nodes[nodeID]
	type actionCandidate struct{ kind, resourceID string }
	candidates := []actionCandidate{}
	add := func(kind string) { candidates = append(candidates, actionCandidate{kind: kind}) }
	attemptID := ""
	if runtime.Status == "planned" {
		frontier := domain.ComputeFrontier(state)
		for _, ready := range frontier.Ready {
			if ready == nodeID {
				add("node.start")
			}
		}
	} else if runtime.Status == "active" {
		attempt, found := state.LatestAttempt(nodeID)
		if !found {
			return ActionList{}, fmt.Errorf("active node has no attempt")
		}
		attemptID = attempt.ID
		switch attempt.Status {
		case "running":
			if node.Kind == "effect" {
				if effect, exists := state.EffectForAttempt(attempt.ID); !exists {
					for _, kind := range []string{"attempt.checkpoint", "attempt.wait", "effect.prepare"} {
						add(kind)
					}
				} else if effect.Status == "confirmed" {
					for _, kind := range []string{"attempt.checkpoint", "effect.complete"} {
						add(kind)
					}
				} else if effect.Status == "failed" {
					for _, kind := range []string{"attempt.checkpoint", "effect.complete", "attempt.wait"} {
						add(kind)
					}
				} else {
					for _, kind := range []string{"attempt.checkpoint", "attempt.wait"} {
						add(kind)
					}
				}
			} else {
				for _, kind := range []string{"attempt.checkpoint", "attempt.wait", completionAction(node)} {
					add(kind)
				}
				if node.Kind == "task" {
					add("attempt.submit")
				}
			}
		case "waiting":
			for _, kind := range []string{"attempt.checkpoint", "attempt.resume"} {
				add(kind)
			}
		case "submitted":
			for _, kind := range []string{"attempt.checkpoint", completionAction(node)} {
				add(kind)
			}
		}
		if attempt.Status == "running" || attempt.Status == "waiting" || attempt.Status == "submitted" {
			add("evidence.publish")
			if len(state.EvidencePackages) > 0 {
				add("evidence.assess-reuse")
			}
		}
		if domain.RoleHasCapability(state.Graph, roleID, domain.CapabilityResourceClose) {
			for _, resource := range state.Resources {
				if resource.AttemptID != attempt.ID || resource.Status != "active" {
					continue
				}
				kind := "resource.close"
				if resource.ClosureStatus == "unknown" || resource.ClosureStatus == "failed" || resource.ClosureStatus == "reconciling" {
					kind = "resource.reconcile"
				}
				candidates = append(candidates, actionCandidate{kind: kind, resourceID: resource.ID})
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].kind == candidates[j].kind {
			return candidates[i].resourceID < candidates[j].resourceID
		}
		return candidates[i].kind < candidates[j].kind
	})
	secret, err := s.actionSecret()
	if err != nil {
		return ActionList{}, err
	}
	result := ActionList{Actions: make([]AllowedAction, 0, len(candidates))}
	for _, candidate := range candidates {
		kind := candidate.kind
		expires := s.Now().UTC().Add(5 * time.Minute)
		if leaseExpiry, parseErr := time.Parse(time.RFC3339Nano, lease.ExpiresAt); parseErr == nil && leaseExpiry.Before(expires) {
			expires = leaseExpiry
		}
		providerSet := providerFingerprint(state.Graph)
		identity := strings.Join([]string{state.ProjectID, state.HeadHash, state.GraphRevision, providerSet, roleID, lease.SessionID, nodeID, attemptID, candidate.resourceID, kind}, "\x00")
		sum := sha256.Sum256([]byte(identity))
		payload := actionRefPayload{ActionID: hex.EncodeToString(sum[:]), ProjectID: state.ProjectID, GraphRevision: state.GraphRevision, ProviderSet: providerSet, HeadHash: state.HeadHash, RoleID: roleID, SessionID: lease.SessionID, NodeID: nodeID, AttemptID: attemptID, ResourceID: candidate.resourceID, Kind: kind, ExpiresAt: expires.Format(time.RFC3339Nano)}
		ref, err := signActionRef(payload, secret)
		if err != nil {
			return ActionList{}, err
		}
		targetRef := ""
		if candidate.resourceID != "" {
			targetRef = "resource:" + candidate.resourceID
		}
		result.Actions = append(result.Actions, AllowedAction{ID: payload.ActionID, Kind: kind, Ref: ref, TargetRef: targetRef, InputSchema: actionInputSchema(kind, node)})
	}
	return result, nil
}

func (s *Service) ApplyAction(ref string, input json.RawMessage, idempotencyKey string) (ActionResult, error) {
	return s.ApplyActionContext(context.Background(), ref, input, idempotencyKey)
}

func (s *Service) ApplyActionContext(ctx context.Context, ref string, input json.RawMessage, idempotencyKey string) (ActionResult, error) {
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	if idempotencyKey == "" {
		return ActionResult{}, fmt.Errorf("idempotency key is required")
	}
	if len(input) > 64*1024 {
		return ActionResult{}, fmt.Errorf("action input cannot exceed 64 KiB")
	}
	if err := domain.ValidateAuthorityJSON(input); err != nil {
		return ActionResult{}, fmt.Errorf("action input: %w", err)
	}
	if err := domain.RejectSensitiveFields(input); err != nil {
		return ActionResult{}, fmt.Errorf("action input: %w", err)
	}
	requestDigest, err := authorityRequestDigest("action.apply", input)
	if err != nil {
		return ActionResult{}, fmt.Errorf("action input digest: %w", err)
	}
	secret, err := s.actionSecret()
	if err != nil {
		return ActionResult{}, err
	}
	payload, err := verifyActionRef(ref, secret)
	if err != nil {
		return ActionResult{}, err
	}
	state, _, err := s.load()
	if err != nil {
		return ActionResult{}, err
	}
	if command, ok := state.Commands[idempotencyKey]; ok {
		result, resultErr := actionResultForSequence(state, command.Sequence)
		if resultErr != nil || result.ActionID != payload.ActionID || result.Kind != payload.Kind || (command.RequestDigest != "" && command.RequestDigest != requestDigest) {
			return ActionResult{}, fmt.Errorf("idempotency key is already bound to another command")
		}
		return result, nil
	}
	if payload.ProjectID != state.ProjectID || payload.GraphRevision != state.GraphRevision || payload.ProviderSet != providerFingerprint(state.Graph) || payload.HeadHash != state.HeadHash {
		return ActionResult{}, fmt.Errorf("action reference is stale")
	}
	expires, err := time.Parse(time.RFC3339Nano, payload.ExpiresAt)
	authorizedAt := s.Now().UTC()
	if err != nil || !authorizedAt.Before(expires) {
		return ActionResult{}, fmt.Errorf("action reference is expired")
	}
	lease, err := validLeaseAt(state, payload.RoleID, authorizedAt)
	if err != nil {
		return ActionResult{}, err
	}
	if lease.SessionID != payload.SessionID {
		return ActionResult{}, fmt.Errorf("action reference session no longer owns the role")
	}
	node, ok := state.NodeDefinition(payload.NodeID)
	if !ok || node.Role != payload.RoleID {
		return ActionResult{}, fmt.Errorf("action node or role is invalid")
	}
	if _, err := s.requireRoleCapability(state, payload.RoleID, domain.RequiredNodeCapability(node.Kind)); err != nil {
		return ActionResult{}, err
	}
	if payload.Kind == "resource.close" || payload.Kind == "resource.reconcile" {
		return s.applyResourceAction(state, payload, input, idempotencyKey, requestDigest)
	}
	if payload.Kind == "effect.prepare" {
		return s.applyEffectAction(ctx, state, payload, input, idempotencyKey, requestDigest, node)
	}
	now := authorizedAt.Format(time.RFC3339Nano)
	events := make([]journal.Event, 0, 2)
	attemptID := payload.AttemptID
	actionInput := input
	switch payload.Kind {
	case "node.start":
		if state.Nodes[node.ID].Status != "planned" || !contains(domain.ComputeFrontier(state).Ready, node.ID) {
			return ActionResult{}, fmt.Errorf("node is not ready")
		}
		attemptID = uuid.NewString()
		attempt := domain.Attempt{ID: attemptID, NodeID: node.ID, RoleID: payload.RoleID, Number: len(state.NodeAttempts[node.ID]) + 1, Status: "leased", StartedAt: now, UpdatedAt: now}
		raw, _ := json.Marshal(attempt)
		events = append(events, journal.Event{Type: "attempt.leased", Payload: raw})
		runningRaw, _ := json.Marshal(map[string]string{"attemptId": attempt.ID, "status": "running", "updatedAt": now})
		events = append(events, journal.Event{Type: "attempt.status-changed", Payload: runningRaw})
		for _, request := range node.Resources {
			lease := domain.ResourceLease{ID: uuid.NewString(), Kind: request.Kind, Quantity: request.Quantity, NodeID: node.ID, AttemptID: attemptID, RoleID: payload.RoleID, Status: "active", ClosureStatus: "pending", LeasedAt: now}
			leaseRaw, _ := json.Marshal(lease)
			events = append(events, journal.Event{Type: "resource.leased", Payload: leaseRaw})
		}
	case "attempt.checkpoint":
		attempt, err := activeAttempt(state, payload)
		if err != nil {
			return ActionResult{}, err
		}
		var value struct {
			Summary      string               `json:"summary"`
			EvidenceRefs []domain.EvidenceRef `json:"evidenceRefs"`
		}
		if err := json.Unmarshal(input, &value); err != nil {
			return ActionResult{}, fmt.Errorf("decode checkpoint input: %w", err)
		}
		if strings.TrimSpace(value.Summary) == "" || len([]byte(value.Summary)) > 2048 {
			return ActionResult{}, fmt.Errorf("checkpoint summary must be 1..2048 bytes")
		}
		for _, evidence := range value.EvidenceRefs {
			if evidence.Digest == "" || evidence.Type == "" || evidence.Size < 0 {
				return ActionResult{}, fmt.Errorf("evidence refs require digest, type and non-negative size")
			}
		}
		checkpoint := domain.Checkpoint{ID: uuid.NewString(), AttemptID: attempt.ID, Summary: value.Summary, EvidenceRefs: value.EvidenceRefs, CreatedAt: now}
		raw, _ := json.Marshal(checkpoint)
		events = append(events, journal.Event{Type: "attempt.checkpointed", Payload: raw})
	case "evidence.publish":
		attempt, err := activeAttempt(state, payload)
		if err != nil {
			return ActionResult{}, err
		}
		pack, err := s.buildExecutionPackage(state, node, attempt, input, authorizedAt)
		if err != nil {
			return ActionResult{}, err
		}
		if _, exists := state.EvidencePackages[pack.ID]; exists {
			return ActionResult{}, fmt.Errorf("execution package %s is already published", pack.ID)
		}
		raw, _ := json.Marshal(pack)
		events = append(events, journal.Event{Type: "evidence.package-published", Payload: raw})
		actionInput, _ = json.Marshal(map[string]string{"packageId": pack.ID})
	case "evidence.assess-reuse":
		attempt, err := activeAttempt(state, payload)
		if err != nil {
			return ActionResult{}, err
		}
		decision, err := s.buildReuseDecision(state, attempt, input, authorizedAt)
		if err != nil {
			return ActionResult{}, err
		}
		if _, exists := state.ReuseDecisions[decision.ID]; exists {
			return ActionResult{}, fmt.Errorf("reuse decision %s is already recorded", decision.ID)
		}
		raw, _ := json.Marshal(decision)
		events = append(events, journal.Event{Type: "evidence.reuse-assessed", Payload: raw})
		actionInput, _ = json.Marshal(map[string]string{"decisionId": decision.ID})
	case "attempt.wait", "attempt.resume", "attempt.submit":
		attempt, err := activeAttempt(state, payload)
		if err != nil {
			return ActionResult{}, err
		}
		target := map[string]string{"attempt.wait": "waiting", "attempt.resume": "running", "attempt.submit": "submitted"}[payload.Kind]
		if payload.Kind == "attempt.resume" && attempt.Status != "waiting" {
			return ActionResult{}, fmt.Errorf("only a waiting attempt can resume")
		}
		raw, _ := json.Marshal(map[string]string{"attemptId": attempt.ID, "status": target, "updatedAt": now})
		events = append(events, journal.Event{Type: "attempt.status-changed", Payload: raw})
	case "task.complete", "review.resolve", "decision.record", "effect.complete", "attempt.finish", "gate.evaluate":
		attempt, err := activeAttempt(state, payload)
		if err != nil {
			return ActionResult{}, err
		}
		if payload.Kind != completionAction(node) {
			return ActionResult{}, fmt.Errorf("action %s cannot complete node kind %s", payload.Kind, node.Kind)
		}
		if activeResourceIDs(state, attempt.ID) != nil {
			return ActionResult{}, fmt.Errorf("attempt %s has active resources; close or reconcile them before completion", attempt.ID)
		}
		value := completionInput{}
		var decision *domain.DecisionRecord
		if payload.Kind == "gate.evaluate" {
			record, decisionErr := s.buildGateDecision(ctx, state, node, attempt, payload.RoleID, input, authorizedAt)
			if decisionErr != nil {
				return ActionResult{}, decisionErr
			}
			refreshed, commitAt, refreshErr := s.revalidateActionAuthorization(payload, expires)
			if refreshErr != nil {
				return ActionResult{}, fmt.Errorf("gate decision finished outside its action authorization: %w", refreshErr)
			}
			state, authorizedAt, now = refreshed, commitAt, commitAt.Format(time.RFC3339Nano)
			record.CreatedAt = now
			if assignErr := assignDecisionID(&record); assignErr != nil {
				return ActionResult{}, assignErr
			}
			decision = &record
			value.Outcome, value.Facts, value.EvidenceRefs = record.Outcome, record.Facts, record.EvidenceRefs
		} else {
			value, err = decodeCompletionInput(input)
			if err != nil {
				return ActionResult{}, err
			}
			if node.Kind == "task" && outcomeClass(node, value.Outcome) == "success" && attempt.Status != "submitted" {
				return ActionResult{}, fmt.Errorf("successful task completion requires a submitted attempt")
			}
			if node.Kind == "effect" {
				effect, exists := state.EffectForAttempt(attempt.ID)
				class := outcomeClass(node, value.Outcome)
				if !exists || (class == "success" && effect.Status != "confirmed") || (class == "failure" && effect.Status != "failed") {
					return ActionResult{}, fmt.Errorf("effect outcome is inconsistent with its observed receipt state")
				}
			}
			if node.Kind == "review" || node.Kind == "decision" {
				record, decisionErr := s.buildRoleDecision(state, node, attempt, payload.RoleID, value, input, authorizedAt)
				if decisionErr != nil {
					return ActionResult{}, decisionErr
				}
				decision = &record
				value.Facts = record.Facts
			}
		}
		outcomeClass := outcomeClass(node, value.Outcome)
		if outcomeClass == "" {
			return ActionResult{}, fmt.Errorf("outcome %s is not declared by node %s", value.Outcome, node.ID)
		}
		if outcomeClass == "retryable" && attempt.Number > node.RetryBudget {
			return ActionResult{}, fmt.Errorf("node %s retry budget %d is exhausted at attempt %d", node.ID, node.RetryBudget, attempt.Number)
		}
		if decision != nil {
			raw, _ := json.Marshal(decision)
			events = append(events, journal.Event{Type: "decision.recorded", Payload: raw})
			actionInput, _ = json.Marshal(map[string]string{"decisionId": decision.ID})
		}
		raw, _ := json.Marshal(struct {
			AttemptID    string                `json:"attemptId"`
			Outcome      string                `json:"outcome"`
			OutcomeClass string                `json:"outcomeClass"`
			Facts        domain.PredicateFacts `json:"facts,omitempty"`
			UpdatedAt    string                `json:"updatedAt"`
		}{attempt.ID, value.Outcome, outcomeClass, value.Facts, now})
		events = append(events, journal.Event{Type: "attempt.finished", Payload: raw})
		if outcomeClass == "failure" || outcomeClass == "cancelled" {
			classification := value.Classification
			if classification == "" {
				classification = "work-product"
			}
			if !domain.ValidIncidentClassification(classification) {
				return ActionResult{}, fmt.Errorf("invalid incident classification %s", classification)
			}
			postState := state
			postState.Nodes = make(map[string]domain.NodeRuntime, len(state.Nodes))
			for nodeID, runtime := range state.Nodes {
				postState.Nodes[nodeID] = runtime
			}
			postState.Nodes[node.ID] = domain.NodeRuntime{Status: "terminal", Outcome: value.Outcome, OutcomeClass: outcomeClass, Facts: value.Facts}
			incident := domain.Incident{ID: "attempt:" + attempt.ID, SourceType: "attempt", SourceID: attempt.ID, NodeID: node.ID, OwnerRole: payload.RoleID, Status: "open", Classification: classification, Deadline: authorizedAt.Add(time.Hour).Format(time.RFC3339Nano), AttemptBudget: 2, ProgressMetric: "new candidate or changed failure classification", DependencyCut: domain.DependencyCut(postState, node.ID), OpenedAt: now, UpdatedAt: now}
			incidentRaw, _ := json.Marshal(incident)
			events = append(events, journal.Event{Type: "incident.opened", Payload: incidentRaw})
		}
	default:
		return ActionResult{}, fmt.Errorf("unsupported action kind %s", payload.Kind)
	}
	action := domain.ActionRecord{ID: payload.ActionID, Kind: payload.Kind, NodeID: payload.NodeID, AttemptID: attemptID, Status: "confirmed", Input: actionInput}
	actionRaw, _ := json.Marshal(action)
	events = append(events, journal.Event{Type: "action.applied", Payload: actionRaw})
	expectedHead := payload.HeadHash
	segment, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: payload.Kind, ActorRole: payload.RoleID, IdempotencyKey: idempotencyKey, ObjectRef: "action:" + payload.ActionID, RequestDigest: requestDigest}, events, authorizedAt, &expectedHead)
	if err != nil {
		return ActionResult{}, err
	}
	if err := s.settleAutomatic(); err != nil {
		return ActionResult{}, err
	}
	state, segments, err := s.load()
	if err != nil {
		return ActionResult{}, err
	}
	if err := s.Projection.Sync(state, segments); err != nil {
		return ActionResult{}, err
	}
	actionRecord, ok := state.Actions[payload.ActionID]
	if !ok || actionRecord.Sequence != segment.Sequence {
		return actionResultForSequence(state, segment.Sequence)
	}
	return ActionResult{ActionID: actionRecord.ID, Kind: actionRecord.Kind, NodeID: actionRecord.NodeID, AttemptID: actionRecord.AttemptID, ObjectRef: objectRefForSequence(state, actionRecord.Sequence), Status: actionRecord.Status, Sequence: actionRecord.Sequence}, nil
}

// revalidateActionAuthorization closes the window created by a slow semantic
// provider. Provider output is only a proposal until the original journal head,
// graph/provider binding, action-ref expiry, and Role lease are all rechecked at
// the exact time that will be written into the authoritative events.
func (s *Service) revalidateActionAuthorization(payload actionRefPayload, expires time.Time) (domain.State, time.Time, error) {
	state, _, err := s.load()
	if err != nil {
		return domain.State{}, time.Time{}, err
	}
	if payload.ProjectID != state.ProjectID || payload.GraphRevision != state.GraphRevision || payload.ProviderSet != providerFingerprint(state.Graph) || payload.HeadHash != state.HeadHash {
		return domain.State{}, time.Time{}, fmt.Errorf("action reference is stale")
	}
	commitAt := s.Now().UTC()
	if !commitAt.Before(expires) {
		return domain.State{}, time.Time{}, fmt.Errorf("action reference is expired")
	}
	lease, err := validLeaseAt(state, payload.RoleID, commitAt)
	if err != nil {
		return domain.State{}, time.Time{}, err
	}
	if lease.SessionID != payload.SessionID {
		return domain.State{}, time.Time{}, fmt.Errorf("action reference session no longer owns the role")
	}
	return state, commitAt, nil
}

func actionResultForSequence(state domain.State, sequence uint64) (ActionResult, error) {
	for _, action := range state.Actions {
		if action.Sequence != sequence {
			continue
		}
		status, resultSequence := action.Status, action.Sequence
		if effect, ok := state.Effects[action.ID]; ok {
			status, resultSequence = effect.Status, effect.Sequence
		}
		return ActionResult{ActionID: action.ID, Kind: action.Kind, NodeID: action.NodeID, AttemptID: action.AttemptID, ObjectRef: objectRefForSequence(state, sequence), Status: status, Sequence: resultSequence}, nil
	}
	return ActionResult{}, fmt.Errorf("journal command at sequence %d has no action result", sequence)
}

func objectRefForSequence(state domain.State, sequence uint64) string {
	for _, action := range state.Actions {
		if action.Sequence != sequence || (action.Kind != "resource.close" && action.Kind != "resource.reconcile") {
			continue
		}
		var value struct {
			ResourceID string `json:"resourceId"`
		}
		if json.Unmarshal(action.Input, &value) == nil && value.ResourceID != "" {
			return "resource:" + value.ResourceID
		}
	}
	for _, decision := range state.Decisions {
		if decision.Sequence == sequence {
			return "decision:" + decision.ID
		}
	}
	for _, pack := range state.EvidencePackages {
		if pack.Sequence == sequence {
			return "evidence-package:" + pack.ID
		}
	}
	for _, decision := range state.ReuseDecisions {
		if decision.Sequence == sequence {
			return "reuse-decision:" + decision.ID
		}
	}
	return ""
}

func outcomeClass(node domain.NodeDefinition, outcomeID string) string {
	for _, outcome := range node.Outcomes {
		if outcome.ID == outcomeID {
			return outcome.Class
		}
	}
	return ""
}

func activeResourceIDs(state domain.State, attemptID string) []string {
	result := []string{}
	for _, resource := range state.Resources {
		if resource.AttemptID == attemptID && resource.Status == "active" {
			result = append(result, resource.ID)
		}
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil
	}
	return result
}

func (s *Service) validLease(state domain.State, roleID string) (domain.RoleLease, error) {
	return validLeaseAt(state, roleID, s.Now().UTC())
}

func validLeaseAt(state domain.State, roleID string, instant time.Time) (domain.RoleLease, error) {
	lease, ok := state.Leases[roleID]
	if !ok || !lease.Active {
		return domain.RoleLease{}, fmt.Errorf("role %s has no active lease", roleID)
	}
	expires, err := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
	bound, boundErr := time.Parse(time.RFC3339Nano, lease.BoundAt)
	if err != nil || boundErr != nil || instant.Before(bound) || !instant.Before(expires) {
		return domain.RoleLease{}, fmt.Errorf("role %s lease is expired", roleID)
	}
	return lease, nil
}

func activeAttempt(state domain.State, payload actionRefPayload) (domain.Attempt, error) {
	attempt, ok := state.Attempts[payload.AttemptID]
	if !ok || attempt.NodeID != payload.NodeID || attempt.Status == "terminal" {
		return domain.Attempt{}, fmt.Errorf("attempt is not active")
	}
	return attempt, nil
}

func (s *Service) actionSecret() ([]byte, error) {
	path := filepath.Join(s.Project.DataDir, "action-secret")
	if data, exists, err := readActionSecret(path); err != nil {
		return nil, err
	} else if exists {
		return data, nil
	}
	var result []byte
	err := s.Journal.WithLock(func() error {
		if existing, exists, readErr := readActionSecret(path); readErr != nil {
			return readErr
		} else if exists {
			result = existing
			return nil
		}
		data := make([]byte, 32)
		if _, err := rand.Read(data); err != nil {
			return err
		}
		file, err := os.CreateTemp(s.Project.DataDir, ".action-secret-*.tmp")
		if err != nil {
			return err
		}
		tmp := file.Name()
		defer os.Remove(tmp)
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return err
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
		result = data
		return nil
	})
	return result, err
}

func readActionSecret(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != 32 {
		return nil, false, fmt.Errorf("action secret must be a regular non-symlink 32-byte file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, false, fmt.Errorf("action secret permissions must not allow group or other access")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	if len(data) != 32 {
		return nil, false, fmt.Errorf("action secret changed while being read")
	}
	return data, true, nil
}

func signActionRef(payload actionRefPayload, secret []byte) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyActionRef(ref string, secret []byte) (actionRefPayload, error) {
	parts := strings.Split(ref, ".")
	if len(parts) != 2 {
		return actionRefPayload{}, fmt.Errorf("invalid action reference")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return actionRefPayload{}, fmt.Errorf("invalid action reference")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return actionRefPayload{}, fmt.Errorf("invalid action reference")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return actionRefPayload{}, fmt.Errorf("invalid action reference signature")
	}
	var payload actionRefPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, err
	}
	return payload, nil
}

func actionInputSchema(kind string, node domain.NodeDefinition) map[string]any {
	digest := map[string]any{"type": "string", "pattern": `^sha256:[a-f0-9]{64}$`}
	provenance := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"producer"}, "properties": map[string]any{"producer": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}, "revision": map[string]any{"type": "string", "maxLength": 256}, "invocationDigest": digest}}
	artifact := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"digest", "type", "size", "provenance"}, "properties": map[string]any{"digest": digest, "type": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "size": map[string]any{"type": "integer", "minimum": 0}, "uri": map[string]any{"type": "string", "maxLength": 2048}, "provenance": provenance}}
	protectedInput := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"name", "digest"}, "properties": map[string]any{"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "digest": digest}}
	switch kind {
	case "attempt.checkpoint":
		return map[string]any{"type": "object", "required": []string{"summary"}, "properties": map[string]any{"summary": map[string]any{"type": "string", "maxLength": 2048}, "evidenceRefs": map[string]any{"type": "array"}}}
	case "attempt.finish", "task.complete", "review.resolve", "decision.record", "effect.complete":
		outcomes := make([]string, 0, len(node.Outcomes))
		for _, outcome := range node.Outcomes {
			outcomes = append(outcomes, outcome.ID)
		}
		factMap := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
		facts := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"decision": factMap, "evidence": factMap, "policy": factMap}}
		return map[string]any{"type": "object", "required": []string{"outcome"}, "additionalProperties": false, "properties": map[string]any{"outcome": map[string]any{"type": "string", "enum": outcomes}, "facts": facts, "evidenceRefs": map[string]any{"type": "array"}, "classification": map[string]any{"enum": []string{"work-product", "policy", "fixture", "infrastructure", "evidence", "external-effect", "unknown"}}}}
	case "gate.evaluate":
		return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"input"}, "properties": map[string]any{"input": map[string]any{}, "evidence": map[string]any{"type": "array", "maxItems": 128}, "evidenceRefs": map[string]any{"type": "array", "maxItems": 128}}}
	case "resource.close", "resource.reconcile":
		return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"status", "receipt"}, "properties": map[string]any{"status": map[string]any{"enum": []string{"confirmed", "failed", "unknown"}}, "receipt": map[string]any{"not": map[string]any{"type": "null"}}}}
	case "evidence.publish":
		observations := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"exact", "clean", "depthComplete", "sourceUnmodified", "resourcesClosed"}, "properties": map[string]any{"exact": map[string]any{"type": "boolean"}, "clean": map[string]any{"type": "boolean"}, "depthComplete": map[string]any{"type": "boolean"}, "sourceUnmodified": map[string]any{"type": "boolean"}, "resourcesClosed": map[string]any{"type": "boolean"}}}
		return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"candidate", "prospectiveTree", "commandGraphDigest", "protectedInputs", "observations", "artifacts"}, "properties": map[string]any{"candidate": artifact, "prospectiveTree": artifact, "commandGraphDigest": digest, "protectedInputs": map[string]any{"type": "array", "minItems": 1, "maxItems": 128, "items": protectedInput}, "observations": observations, "artifacts": map[string]any{"type": "array", "maxItems": 256, "items": artifact}}}
	case "evidence.assess-reuse":
		policy := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "version", "schemaHash"}, "properties": map[string]any{"id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "version": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "schemaHash": digest}}
		return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"packageId", "policy", "candidateDigest", "prospectiveTreeDigest", "commandGraphDigest", "protectedInputs"}, "properties": map[string]any{"packageId": map[string]any{"type": "string", "pattern": `^epkg_[a-f0-9]{64}$`}, "policy": policy, "candidateDigest": digest, "prospectiveTreeDigest": digest, "commandGraphDigest": digest, "protectedInputs": map[string]any{"type": "array", "minItems": 1, "maxItems": 128, "items": protectedInput}}}
	default:
		return map[string]any{"type": "object", "additionalProperties": false}
	}
}

func completionAction(node domain.NodeDefinition) string {
	switch node.Kind {
	case "task":
		return "task.complete"
	case "review":
		return "review.resolve"
	case "decision":
		return "decision.record"
	case "gate":
		if node.Decision == nil {
			return "attempt.finish"
		}
		return "gate.evaluate"
	case "effect":
		return "effect.complete"
	default:
		return "attempt.finish"
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
