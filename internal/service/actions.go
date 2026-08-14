package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	state, _, err := s.load()
	if err != nil {
		return domain.RoleLease{}, err
	}
	if _, ok := state.Commands[idempotencyKey]; ok {
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
	if _, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "role.bind", ActorRole: roleID, IdempotencyKey: idempotencyKey}, []journal.Event{{Type: "role.bound", Payload: payload}}, now, &expectedHead); err != nil {
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
	state, _, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := state.Commands[idempotencyKey]; ok {
		return nil
	}
	lease, ok := state.Leases[roleID]
	if !ok || !lease.Active || lease.SessionID != sessionID {
		return fmt.Errorf("active role lease does not match session")
	}
	payload, _ := json.Marshal(map[string]string{"roleId": roleID, "releasedAt": s.Now().UTC().Format(time.RFC3339Nano)})
	expectedHead := state.HeadHash
	if _, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "role.release", ActorRole: roleID, IdempotencyKey: idempotencyKey}, []journal.Event{{Type: "role.released", Payload: payload}}, s.Now(), &expectedHead); err != nil {
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
	runtime := state.Nodes[nodeID]
	kinds := []string{}
	attemptID := ""
	if runtime.Status == "planned" {
		frontier := domain.ComputeFrontier(state)
		for _, ready := range frontier.Ready {
			if ready == nodeID {
				kinds = append(kinds, "node.start")
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
					kinds = []string{"attempt.checkpoint", "attempt.wait", "effect.prepare"}
				} else if effect.Status == "confirmed" {
					kinds = []string{"attempt.checkpoint", "attempt.finish"}
				} else if effect.Status == "failed" {
					kinds = []string{"attempt.checkpoint", "attempt.finish", "attempt.wait"}
				} else {
					kinds = []string{"attempt.checkpoint", "attempt.wait"}
				}
			} else {
				kinds = []string{"attempt.checkpoint", "attempt.wait", "attempt.submit", "attempt.finish"}
			}
		case "waiting":
			kinds = []string{"attempt.checkpoint", "attempt.resume", "attempt.finish"}
		case "submitted":
			kinds = []string{"attempt.checkpoint", "attempt.finish"}
		}
	}
	sort.Strings(kinds)
	secret, err := s.actionSecret()
	if err != nil {
		return ActionList{}, err
	}
	result := ActionList{Actions: make([]AllowedAction, 0, len(kinds))}
	for _, kind := range kinds {
		expires := s.Now().UTC().Add(5 * time.Minute)
		if leaseExpiry, parseErr := time.Parse(time.RFC3339Nano, lease.ExpiresAt); parseErr == nil && leaseExpiry.Before(expires) {
			expires = leaseExpiry
		}
		providerSet := providerFingerprint(state.Graph)
		identity := strings.Join([]string{state.ProjectID, state.HeadHash, state.GraphRevision, providerSet, roleID, lease.SessionID, nodeID, attemptID, kind}, "\x00")
		sum := sha256.Sum256([]byte(identity))
		payload := actionRefPayload{ActionID: hex.EncodeToString(sum[:]), ProjectID: state.ProjectID, GraphRevision: state.GraphRevision, ProviderSet: providerSet, HeadHash: state.HeadHash, RoleID: roleID, SessionID: lease.SessionID, NodeID: nodeID, AttemptID: attemptID, Kind: kind, ExpiresAt: expires.Format(time.RFC3339Nano)}
		ref, err := signActionRef(payload, secret)
		if err != nil {
			return ActionList{}, err
		}
		result.Actions = append(result.Actions, AllowedAction{ID: payload.ActionID, Kind: kind, Ref: ref, InputSchema: actionInputSchema(kind, node)})
	}
	return result, nil
}

func (s *Service) ApplyAction(ref string, input json.RawMessage, idempotencyKey string) (ActionResult, error) {
	if idempotencyKey == "" {
		return ActionResult{}, fmt.Errorf("idempotency key is required")
	}
	if err := domain.ValidateAuthorityJSON(input); err != nil {
		return ActionResult{}, fmt.Errorf("action input: %w", err)
	}
	if err := domain.RejectSensitiveFields(input); err != nil {
		return ActionResult{}, fmt.Errorf("action input: %w", err)
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
		for _, action := range state.Actions {
			if action.Sequence == command.Sequence {
				if effect, exists := state.Effects[action.ID]; exists {
					return ActionResult{ActionID: action.ID, Kind: action.Kind, NodeID: action.NodeID, AttemptID: action.AttemptID, Status: effect.Status, Sequence: effect.Sequence}, nil
				}
				return ActionResult{ActionID: action.ID, Kind: action.Kind, NodeID: action.NodeID, AttemptID: action.AttemptID, Status: action.Status, Sequence: action.Sequence}, nil
			}
		}
		return ActionResult{}, fmt.Errorf("idempotency key belongs to another command")
	}
	if payload.ProjectID != state.ProjectID || payload.GraphRevision != state.GraphRevision || payload.ProviderSet != providerFingerprint(state.Graph) || payload.HeadHash != state.HeadHash {
		return ActionResult{}, fmt.Errorf("action reference is stale")
	}
	expires, err := time.Parse(time.RFC3339Nano, payload.ExpiresAt)
	if err != nil || !s.Now().UTC().Before(expires) {
		return ActionResult{}, fmt.Errorf("action reference is expired")
	}
	lease, err := s.validLease(state, payload.RoleID)
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
	if payload.Kind == "effect.prepare" {
		return s.applyEffectAction(state, payload, input, idempotencyKey, node)
	}
	now := s.Now().UTC().Format(time.RFC3339Nano)
	events := make([]journal.Event, 0, 2)
	attemptID := payload.AttemptID
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
			lease := domain.ResourceLease{ID: uuid.NewString(), Kind: request.Kind, Quantity: request.Quantity, NodeID: node.ID, AttemptID: attemptID, RoleID: payload.RoleID, Status: "active", LeasedAt: now}
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
	case "attempt.finish":
		attempt, err := activeAttempt(state, payload)
		if err != nil {
			return ActionResult{}, err
		}
		var value struct {
			Outcome string                `json:"outcome"`
			Facts   domain.PredicateFacts `json:"facts"`
		}
		if err := json.Unmarshal(input, &value); err != nil {
			return ActionResult{}, fmt.Errorf("decode finish input: %w", err)
		}
		outcomeClass := ""
		for _, outcome := range node.Outcomes {
			if outcome.ID == value.Outcome {
				outcomeClass = outcome.Class
			}
		}
		if outcomeClass == "" {
			return ActionResult{}, fmt.Errorf("outcome %s is not declared by node %s", value.Outcome, node.ID)
		}
		if outcomeClass == "retryable" && attempt.Number > node.RetryBudget {
			return ActionResult{}, fmt.Errorf("node %s retry budget %d is exhausted at attempt %d", node.ID, node.RetryBudget, attempt.Number)
		}
		raw, _ := json.Marshal(struct {
			AttemptID    string                `json:"attemptId"`
			Outcome      string                `json:"outcome"`
			OutcomeClass string                `json:"outcomeClass"`
			Facts        domain.PredicateFacts `json:"facts,omitempty"`
			UpdatedAt    string                `json:"updatedAt"`
		}{attempt.ID, value.Outcome, outcomeClass, value.Facts, now})
		events = append(events, journal.Event{Type: "attempt.finished", Payload: raw})
		for _, lease := range state.Resources {
			if lease.AttemptID != attempt.ID || lease.Status != "active" {
				continue
			}
			releaseRaw, _ := json.Marshal(map[string]string{"resourceId": lease.ID, "releasedAt": now})
			events = append(events, journal.Event{Type: "resource.released", Payload: releaseRaw})
		}
		if outcomeClass == "failure" || outcomeClass == "cancelled" {
			postState := state
			postState.Nodes = make(map[string]domain.NodeRuntime, len(state.Nodes))
			for nodeID, runtime := range state.Nodes {
				postState.Nodes[nodeID] = runtime
			}
			postState.Nodes[node.ID] = domain.NodeRuntime{Status: "terminal", Outcome: value.Outcome, OutcomeClass: outcomeClass, Facts: value.Facts}
			incident := domain.Incident{ID: "attempt:" + attempt.ID, SourceType: "attempt", SourceID: attempt.ID, NodeID: node.ID, OwnerRole: payload.RoleID, Status: "open", Classification: "node-failure", Deadline: s.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), AttemptBudget: 2, ProgressMetric: "new candidate or changed failure classification", DependencyCut: domain.DependencyCut(postState, node.ID), OpenedAt: now, UpdatedAt: now}
			incidentRaw, _ := json.Marshal(incident)
			events = append(events, journal.Event{Type: "incident.opened", Payload: incidentRaw})
		}
	default:
		return ActionResult{}, fmt.Errorf("unsupported action kind %s", payload.Kind)
	}
	action := domain.ActionRecord{ID: payload.ActionID, Kind: payload.Kind, NodeID: payload.NodeID, AttemptID: attemptID, Status: "confirmed", Input: input}
	actionRaw, _ := json.Marshal(action)
	events = append(events, journal.Event{Type: "action.applied", Payload: actionRaw})
	expectedHead := payload.HeadHash
	segment, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: payload.Kind, ActorRole: payload.RoleID, IdempotencyKey: idempotencyKey}, events, s.Now(), &expectedHead)
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
	return ActionResult{ActionID: actionRecord.ID, Kind: actionRecord.Kind, NodeID: actionRecord.NodeID, AttemptID: actionRecord.AttemptID, Status: actionRecord.Status, Sequence: actionRecord.Sequence}, nil
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
		return ActionResult{ActionID: action.ID, Kind: action.Kind, NodeID: action.NodeID, AttemptID: action.AttemptID, Status: status, Sequence: resultSequence}, nil
	}
	return ActionResult{}, fmt.Errorf("journal command at sequence %d has no action result", sequence)
}

func (s *Service) validLease(state domain.State, roleID string) (domain.RoleLease, error) {
	lease, ok := state.Leases[roleID]
	if !ok || !lease.Active {
		return domain.RoleLease{}, fmt.Errorf("role %s has no active lease", roleID)
	}
	expires, err := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
	if err != nil || !s.Now().UTC().Before(expires) {
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
	if data, err := os.ReadFile(path); err == nil && len(data) == 32 {
		return data, nil
	}
	var result []byte
	err := s.Journal.WithLock(func() error {
		if existing, readErr := os.ReadFile(path); readErr == nil && len(existing) == 32 {
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
	switch kind {
	case "attempt.checkpoint":
		return map[string]any{"type": "object", "required": []string{"summary"}, "properties": map[string]any{"summary": map[string]any{"type": "string", "maxLength": 2048}, "evidenceRefs": map[string]any{"type": "array"}}}
	case "attempt.finish":
		outcomes := make([]string, 0, len(node.Outcomes))
		for _, outcome := range node.Outcomes {
			outcomes = append(outcomes, outcome.ID)
		}
		factMap := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
		facts := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"decision": factMap, "evidence": factMap, "policy": factMap}}
		return map[string]any{"type": "object", "required": []string{"outcome"}, "additionalProperties": false, "properties": map[string]any{"outcome": map[string]any{"type": "string", "enum": outcomes}, "facts": facts}}
	default:
		return map[string]any{"type": "object", "additionalProperties": false}
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
