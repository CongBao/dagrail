package service

import (
	"bytes"
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
	"sync"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/project"
	"github.com/CongBao/dagrail/sdk"
	"github.com/google/uuid"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type AllowedAction struct {
	ID                    string         `json:"id"`
	Kind                  string         `json:"kind"`
	Ref                   string         `json:"ref"`
	NodeID                string         `json:"nodeId,omitempty"`
	NodeRef               string         `json:"nodeRef,omitempty"`
	AttemptID             string         `json:"attemptId,omitempty"`
	TargetRef             string         `json:"targetRef,omitempty"`
	InputSchema           map[string]any `json:"inputSchema,omitempty"`
	DetailRef             string         `json:"detailRef,omitempty"`
	DetailDigest          string         `json:"detailDigest,omitempty"`
	DetailBytes           int            `json:"detailBytes,omitempty"`
	DetailsTruncated      bool           `json:"detailsTruncated,omitempty"`
	LeaseRemainingSeconds int            `json:"leaseRemainingSeconds"`
	RequiredLeaseSeconds  int            `json:"requiredLeaseSeconds"`
}

type ActionList struct {
	Actions []AllowedAction `json:"actions"`
}

type ActionResult struct {
	ActionID           string       `json:"actionId"`
	Kind               string       `json:"kind"`
	NodeID             string       `json:"nodeId,omitempty"`
	NodeRef            string       `json:"nodeRef,omitempty"`
	AttemptID          string       `json:"attemptId,omitempty"`
	AttemptRef         string       `json:"attemptRef,omitempty"`
	AttemptIDDigest    string       `json:"attemptIdDigest,omitempty"`
	AttemptIDBytes     int          `json:"attemptIdBytes,omitempty"`
	AttemptIDTruncated bool         `json:"attemptIdTruncated,omitempty"`
	ObjectRef          string       `json:"objectRef,omitempty"`
	ObjectInspectRef   string       `json:"objectInspectRef,omitempty"`
	ObjectRefDetailRef string       `json:"objectRefDetailRef,omitempty"`
	ObjectRefDigest    string       `json:"objectRefDigest,omitempty"`
	ObjectRefBytes     int          `json:"objectRefBytes,omitempty"`
	ObjectRefTruncated bool         `json:"objectRefTruncated,omitempty"`
	Status             string       `json:"status"`
	Sequence           uint64       `json:"sequence"`
	HeadSequence       uint64       `json:"headSequence"`
	HeadHash           string       `json:"headHash"`
	GraphRevision      string       `json:"graphRevision"`
	Continuation       Continuation `json:"continuation"`
}

type Continuation struct {
	SafeToWait     bool            `json:"safeToWait"`
	ReasonCodes    []string        `json:"reasonCodes"`
	NextActions    []AllowedAction `json:"nextActions"`
	NextActionsRef string          `json:"nextActionsRef,omitempty"`
	Owner          string          `json:"owner"`
}

func boundedActionResult(state domain.State, result ActionResult) ActionResult {
	if len([]byte(result.NodeID)) > preWaitInlineIDBytes {
		result.NodeRef = nodeRefForNode(state, result.NodeID)
		result.NodeID = ""
	}
	if len([]byte(result.AttemptID)) > preWaitInlineIDBytes {
		sum := sha256.Sum256([]byte(result.AttemptID))
		result.AttemptRef = attemptIDDetailRef(state, result.AttemptID, hex.EncodeToString(sum[:]), 0)
		result.AttemptIDDigest = "sha256:" + hex.EncodeToString(sum[:])
		result.AttemptIDBytes = len([]byte(result.AttemptID))
		result.AttemptIDTruncated = true
		result.AttemptID = ""
	}
	if len([]byte(result.ObjectRef)) > preWaitInlineIDBytes {
		sum := sha256.Sum256([]byte(result.ObjectRef))
		if kind, id, ok := strings.Cut(result.ObjectRef, ":"); ok {
			switch {
			case kind == "incident":
				result.ObjectInspectRef = incidentRefForIncident(state, id)
				result.ObjectRefDetailRef = objectReferenceDetailRef(state, kind, id, hex.EncodeToString(sum[:]), 0)
			case boundedStoredObjectKinds[kind]:
				result.ObjectInspectRef = storedObjectRef(state, kind, id)
				result.ObjectRefDetailRef = objectReferenceDetailRef(state, kind, id, hex.EncodeToString(sum[:]), 0)
			}
		}
		result.ObjectRefDigest = "sha256:" + hex.EncodeToString(sum[:])
		result.ObjectRefBytes = len([]byte(result.ObjectRef))
		result.ObjectRefTruncated = true
		result.ObjectRef = ""
	}
	return result
}

var allowedActionSchemaCache sync.Map

type actionRefPayload struct {
	ActionID        string `json:"actionId"`
	ProjectID       string `json:"projectId"`
	GraphRevision   string `json:"graphRevision"`
	ProviderSet     string `json:"providerSet"`
	HeadHash        string `json:"headHash"`
	RoleID          string `json:"roleId,omitempty"`
	SessionID       string `json:"sessionId,omitempty"`
	NodeID          string `json:"nodeId,omitempty"`
	AttemptID       string `json:"attemptId,omitempty"`
	ResourceID      string `json:"resourceId,omitempty"`
	IncidentID      string `json:"incidentId,omitempty"`
	SuccessorNodeID string `json:"successorNodeId,omitempty"`
	Kind            string `json:"kind"`
	ExpiresAt       string `json:"expiresAt"`
	Compact         bool   `json:"compact,omitempty"`
	RoleKey         string `json:"roleKey,omitempty"`
	SessionKey      string `json:"sessionKey,omitempty"`
	NodeKey         string `json:"nodeKey,omitempty"`
	AttemptKey      string `json:"attemptKey,omitempty"`
	ResourceKey     string `json:"resourceKey,omitempty"`
	IncidentKey     string `json:"incidentKey,omitempty"`
	SuccessorKey    string `json:"successorKey,omitempty"`
}

const maxInlineActionRefPayloadBytes = 8 * 1024

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

func (s *Service) applyIncidentSupersedeAction(ctx context.Context, state domain.State, payload actionRefPayload, input json.RawMessage, idempotencyKey string) (ActionResult, error) {
	if payload.ProjectID != state.ProjectID {
		return ActionResult{}, fmt.Errorf("incident action reference belongs to another project")
	}
	if err := validateAllowedActionInput(incidentSupersedeInputSchema(), input); err != nil {
		return ActionResult{}, err
	}
	var value struct {
		Note string `json:"note"`
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return ActionResult{}, fmt.Errorf("decode incident supersede input: %w", err)
	}
	if value.Note == "" || len(value.Note) > 1024 {
		return ActionResult{}, fmt.Errorf("incident supersede note must be 1..1024 bytes")
	}
	requestDigest, err := incidentRequestDigest("incident.supersede", map[string]any{"successorNodeId": payload.SuccessorNodeID, "note": value.Note})
	if err != nil {
		return ActionResult{}, err
	}
	if command, ok := state.Commands[idempotencyKey]; ok {
		if command.Kind != "incident.supersede" || command.ActorRole != payload.RoleID || command.ObjectRef != "incident:"+payload.IncidentID || (command.RequestDigest != "" && command.RequestDigest != requestDigest) {
			return ActionResult{}, fmt.Errorf("idempotency key is already bound to another command")
		}
		incident, exists := state.Incidents[payload.IncidentID]
		if !exists || incident.RemedyNodeID != payload.SuccessorNodeID || incident.Status != "resolved" {
			return ActionResult{}, fmt.Errorf("idempotent incident successor result is unavailable")
		}
		return ActionResult{ActionID: payload.ActionID, Kind: payload.Kind, NodeID: payload.SuccessorNodeID, ObjectRef: "incident:" + payload.IncidentID, Status: incident.Status, Sequence: command.Sequence}, nil
	}
	if payload.GraphRevision != state.GraphRevision || payload.ProviderSet != providerFingerprint(state.Graph) || payload.HeadHash != state.HeadHash || payload.IncidentID == "" || payload.SuccessorNodeID == "" {
		return ActionResult{}, fmt.Errorf("incident action reference is stale or incomplete")
	}
	expires, err := time.Parse(time.RFC3339Nano, payload.ExpiresAt)
	now := s.Now().UTC()
	if err != nil || !now.Before(expires) {
		return ActionResult{}, fmt.Errorf("incident action reference is expired")
	}
	lease, err := validLeaseAt(state, payload.RoleID, now)
	if err != nil {
		return ActionResult{}, err
	}
	if lease.SessionID != payload.SessionID {
		return ActionResult{}, fmt.Errorf("incident action reference session no longer owns the role")
	}
	if _, err := s.requireRoleCapability(state, payload.RoleID, domain.CapabilityIncidentManage); err != nil {
		return ActionResult{}, err
	}
	incident, exists := state.Incidents[payload.IncidentID]
	if !exists {
		return ActionResult{}, fmt.Errorf("unknown incident %s", payload.IncidentID)
	}
	if incident.OwnerRole != "" && incident.OwnerRole != payload.RoleID {
		return ActionResult{}, fmt.Errorf("incident %s belongs to role %s", payload.IncidentID, incident.OwnerRole)
	}
	if err := supersedeIncidentValue(&incident, state, payload.SuccessorNodeID, value.Note, now); err != nil {
		return ActionResult{}, err
	}
	if s.beforeIncidentSupersedeAppend != nil {
		s.beforeIncidentSupersedeAppend()
	}
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	incident.UpdatedAt = now.Format(time.RFC3339Nano)
	incidentRaw, _ := json.Marshal(incident)
	expectedHead := payload.HeadHash
	segment, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "incident.supersede", ActorRole: payload.RoleID, IdempotencyKey: idempotencyKey, ObjectRef: "incident:" + payload.IncidentID, RequestDigest: requestDigest}, []journal.Event{{Type: "incident.updated", Payload: incidentRaw}}, now, &expectedHead)
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
	return ActionResult{ActionID: payload.ActionID, Kind: payload.Kind, NodeID: payload.SuccessorNodeID, ObjectRef: "incident:" + payload.IncidentID, Status: state.Incidents[payload.IncidentID].Status, Sequence: segment.Sequence}, nil
}

type incidentControlResolveInput struct {
	Disposition string `json:"disposition"`
	Resolution  string `json:"resolution"`
	Note        string `json:"note"`
}

func decodeIncidentControlResolveInput(input json.RawMessage) (incidentControlResolveInput, error) {
	if err := validateAllowedActionInput(incidentControlResolveInputSchema(), input); err != nil {
		return incidentControlResolveInput{}, err
	}
	var value incidentControlResolveInput
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return incidentControlResolveInput{}, fmt.Errorf("decode incident control input: %w", err)
	}
	if err := validateIncidentText("controller incident resolution", value.Resolution, 1024); err != nil {
		return incidentControlResolveInput{}, err
	}
	if err := validateIncidentText("controller incident note", value.Note, 1024); err != nil {
		return incidentControlResolveInput{}, err
	}
	if !domain.ValidIncidentControlDisposition(value.Disposition) {
		return incidentControlResolveInput{}, fmt.Errorf("invalid controller incident disposition %s", value.Disposition)
	}
	if value.Resolution == incidentResolutionSupersededByRepair {
		return incidentControlResolveInput{}, fmt.Errorf("resolution %s is reserved for incident supersede", value.Resolution)
	}
	return value, nil
}

func incidentControlResolveRequestDigest(value incidentControlResolveInput) (string, error) {
	return incidentRequestDigest("incident.control-resolve", map[string]any{"disposition": value.Disposition, "resolution": value.Resolution, "note": value.Note})
}

func (s *Service) applyIncidentControlResolveAction(ctx context.Context, state domain.State, payload actionRefPayload, input json.RawMessage, idempotencyKey string) (ActionResult, error) {
	if payload.ProjectID != state.ProjectID {
		return ActionResult{}, fmt.Errorf("incident action reference belongs to another project")
	}
	value, err := decodeIncidentControlResolveInput(input)
	if err != nil {
		return ActionResult{}, err
	}
	requestDigest, err := incidentControlResolveRequestDigest(value)
	if err != nil {
		return ActionResult{}, err
	}
	if command, ok := state.Commands[idempotencyKey]; ok {
		incident, exists := state.Incidents[payload.IncidentID]
		if command.Kind != "incident.control-resolve" || command.ActorRole != payload.RoleID || command.ObjectRef != "incident:"+payload.IncidentID || (command.RequestDigest != "" && command.RequestDigest != requestDigest) || !exists || incident.Control == nil || incident.Status != "resolved" || incident.Control.ActorRole != payload.RoleID || incident.Control.Disposition != value.Disposition || incident.Control.Resolution != value.Resolution || incident.Control.Note != value.Note {
			return ActionResult{}, fmt.Errorf("idempotency key is already bound to another command")
		}
		return ActionResult{ActionID: payload.ActionID, Kind: payload.Kind, NodeID: incident.NodeID, ObjectRef: "incident:" + payload.IncidentID, Status: incident.Status, Sequence: command.Sequence}, nil
	}
	if payload.GraphRevision != state.GraphRevision || payload.ProviderSet != providerFingerprint(state.Graph) || payload.HeadHash != state.HeadHash || payload.IncidentID == "" {
		return ActionResult{}, fmt.Errorf("incident action reference is stale or incomplete")
	}
	expires, err := time.Parse(time.RFC3339Nano, payload.ExpiresAt)
	now := s.Now().UTC()
	if err != nil || !now.Before(expires) {
		return ActionResult{}, fmt.Errorf("incident action reference is expired")
	}
	lease, err := validLeaseAt(state, payload.RoleID, now)
	if err != nil {
		return ActionResult{}, err
	}
	if lease.SessionID != payload.SessionID {
		return ActionResult{}, fmt.Errorf("incident action reference session no longer owns the role")
	}
	if _, err := s.requireRoleCapability(state, payload.RoleID, domain.CapabilityIncidentControl); err != nil {
		return ActionResult{}, err
	}
	incident, exists := state.Incidents[payload.IncidentID]
	if !exists {
		return ActionResult{}, fmt.Errorf("unknown incident %s", payload.IncidentID)
	}
	if payload.NodeID != incident.NodeID {
		return ActionResult{}, fmt.Errorf("incident action reference target changed")
	}
	if err := controlResolveIncidentValue(&incident, state, payload.RoleID, value.Disposition, value.Resolution, value.Note, now); err != nil {
		return ActionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	incident.UpdatedAt = now.Format(time.RFC3339Nano)
	incidentRaw, _ := json.Marshal(incident)
	expectedHead := payload.HeadHash
	segment, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "incident.control-resolve", ActorRole: payload.RoleID, IdempotencyKey: idempotencyKey, ObjectRef: "incident:" + payload.IncidentID, RequestDigest: requestDigest}, []journal.Event{{Type: "incident.updated", Payload: incidentRaw}}, now, &expectedHead)
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
	return ActionResult{ActionID: payload.ActionID, Kind: payload.Kind, NodeID: incident.NodeID, ObjectRef: "incident:" + payload.IncidentID, Status: "resolved", Sequence: segment.Sequence}, nil
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
	return s.listBoundedActionsFromState(state, roleID, nodeID)
}

func (s *Service) listBoundedActionsFromState(state domain.State, roleID, nodeID string) (ActionList, error) {
	result, err := s.listActions(state, roleID, nodeID)
	if err != nil {
		return ActionList{}, err
	}
	inventoryDigest := ""
	for index := range result.Actions {
		raw, marshalErr := json.Marshal(result.Actions[index])
		if marshalErr != nil {
			return ActionList{}, marshalErr
		}
		if len(raw) <= operationsInlineActionBytes {
			continue
		}
		if inventoryDigest == "" {
			all, listErr := s.projectAllowedActionsContext(context.Background(), state, roleID, 0, s.Now().UTC())
			if listErr != nil {
				return ActionList{}, listErr
			}
			inventoryDigest = operationsActionsDigest(all)
		}
		result.Actions[index], err = boundedAllowedAction(state, roleID, inventoryDigest, result.Actions[index])
		if err != nil {
			return ActionList{}, err
		}
	}
	return result, nil
}

func (s *Service) listActions(state domain.State, roleID, nodeID string) (ActionList, error) {
	return s.listActionsAt(state, roleID, nodeID, s.Now().UTC())
}

func (s *Service) listActionsAt(state domain.State, roleID, nodeID string, now time.Time) (ActionList, error) {
	ready := map[string]bool{}
	if state.Nodes[nodeID].Status == "planned" {
		for _, readyNode := range domain.ComputeFrontier(state).Ready {
			ready[readyNode] = true
		}
	}
	return s.listActionsAtWithReady(state, roleID, nodeID, now, ready)
}

func (s *Service) listActionsAtWithReady(state domain.State, roleID, nodeID string, now time.Time, ready map[string]bool) (ActionList, error) {
	index, err := s.buildActionPlanningIndex(context.Background(), state, roleID, now)
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
	return s.listActionsForNodeAtWithReady(state, node, now, ready, index)
}

type actionPlanningIndex struct {
	roleID             string
	lease              domain.RoleLease
	capabilities       map[string]bool
	resourcesByAttempt map[string][]domain.ResourceLease
	effectsByAttempt   map[string]domain.EffectAction
	secret             []byte
	providerSet        string
}

func (s *Service) buildActionPlanningIndex(ctx context.Context, state domain.State, roleID string, now time.Time) (actionPlanningIndex, error) {
	lease, err := validLeaseAt(state, roleID, now)
	if err != nil {
		return actionPlanningIndex{}, err
	}
	capabilities := map[string]bool{}
	roleFound := false
	if state.Graph != nil {
		for _, role := range state.Graph.Spec.Roles {
			if err := ctx.Err(); err != nil {
				return actionPlanningIndex{}, err
			}
			if role.ID != roleID {
				continue
			}
			roleFound = true
			for _, capability := range role.Capabilities {
				capabilities[capability] = true
			}
			break
		}
	}
	if !roleFound {
		return actionPlanningIndex{}, fmt.Errorf("unknown role %s", roleID)
	}
	resourcesByAttempt := map[string][]domain.ResourceLease{}
	for _, resource := range state.Resources {
		if err := ctx.Err(); err != nil {
			return actionPlanningIndex{}, err
		}
		if resource.Status == "active" {
			resourcesByAttempt[resource.AttemptID] = append(resourcesByAttempt[resource.AttemptID], resource)
		}
	}
	for attemptID := range resourcesByAttempt {
		sort.Slice(resourcesByAttempt[attemptID], func(i, j int) bool {
			return resourcesByAttempt[attemptID][i].ID < resourcesByAttempt[attemptID][j].ID
		})
	}
	effectsByAttempt := map[string]domain.EffectAction{}
	for _, effect := range state.Effects {
		if err := ctx.Err(); err != nil {
			return actionPlanningIndex{}, err
		}
		effectsByAttempt[effect.AttemptID] = effect
	}
	secret, err := s.actionSecret()
	if err != nil {
		return actionPlanningIndex{}, err
	}
	return actionPlanningIndex{roleID: roleID, lease: lease, capabilities: capabilities, resourcesByAttempt: resourcesByAttempt, effectsByAttempt: effectsByAttempt, secret: secret, providerSet: providerFingerprint(state.Graph)}, nil
}

func (s *Service) listActionsForNodeAtWithReady(state domain.State, node domain.NodeDefinition, now time.Time, ready map[string]bool, index actionPlanningIndex) (ActionList, error) {
	roleID := index.roleID
	nodeID := node.ID
	if node.Role != roleID {
		return ActionList{}, fmt.Errorf("node %s belongs to role %s", nodeID, node.Role)
	}
	if !index.capabilities[domain.RequiredNodeCapability(node.Kind)] {
		return ActionList{}, fmt.Errorf("role %s lacks required capability %s", roleID, domain.RequiredNodeCapability(node.Kind))
	}
	runtime := state.Nodes[nodeID]
	type actionCandidate struct{ kind, resourceID string }
	candidates := []actionCandidate{}
	add := func(kind string) { candidates = append(candidates, actionCandidate{kind: kind}) }
	attemptID := ""
	if runtime.Status == "planned" {
		if ready[nodeID] {
			add("node.start")
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
				if effect, exists := index.effectsByAttempt[attempt.ID]; !exists {
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
		if index.capabilities[domain.CapabilityResourceClose] {
			for _, resource := range index.resourcesByAttempt[attempt.ID] {
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
	result := ActionList{Actions: make([]AllowedAction, 0, len(candidates))}
	needsRenewal := false
	for _, candidate := range candidates {
		kind := candidate.kind
		expires := now.Add(5 * time.Minute)
		if leaseExpiry, parseErr := time.Parse(time.RFC3339Nano, index.lease.ExpiresAt); parseErr == nil && leaseExpiry.Before(expires) {
			expires = leaseExpiry
		}
		providerSet := index.providerSet
		identity := strings.Join([]string{state.ProjectID, state.HeadHash, state.GraphRevision, providerSet, roleID, index.lease.SessionID, nodeID, attemptID, candidate.resourceID, kind}, "\x00")
		sum := sha256.Sum256([]byte(identity))
		payload := actionRefPayload{ActionID: hex.EncodeToString(sum[:]), ProjectID: state.ProjectID, GraphRevision: state.GraphRevision, ProviderSet: providerSet, HeadHash: state.HeadHash, RoleID: roleID, SessionID: index.lease.SessionID, NodeID: nodeID, AttemptID: attemptID, ResourceID: candidate.resourceID, Kind: kind, ExpiresAt: expires.Format(time.RFC3339Nano)}
		ref, err := signActionRef(payload, index.secret)
		if err != nil {
			return ActionList{}, err
		}
		targetRef := ""
		if candidate.resourceID != "" {
			targetRef = "resource:" + candidate.resourceID
		}
		remaining := int(expires.Sub(now).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		required := s.requiredLeaseSeconds(kind, node)
		if required > remaining {
			needsRenewal = true
			continue
		}
		result.Actions = append(result.Actions, AllowedAction{ID: payload.ActionID, Kind: kind, Ref: ref, NodeID: nodeID, AttemptID: attemptID, TargetRef: targetRef, InputSchema: actionInputSchemaForState(state, kind, node), LeaseRemainingSeconds: remaining, RequiredLeaseSeconds: required})
	}
	if needsRenewal {
		kind := "role.renew"
		expires := now.Add(5 * time.Minute)
		if leaseExpiry, parseErr := time.Parse(time.RFC3339Nano, index.lease.ExpiresAt); parseErr == nil && leaseExpiry.Before(expires) {
			expires = leaseExpiry
		}
		identity := strings.Join([]string{state.ProjectID, state.HeadHash, state.GraphRevision, index.providerSet, roleID, index.lease.SessionID, nodeID, attemptID, kind}, "\x00")
		sum := sha256.Sum256([]byte(identity))
		payload := actionRefPayload{ActionID: hex.EncodeToString(sum[:]), ProjectID: state.ProjectID, GraphRevision: state.GraphRevision, ProviderSet: index.providerSet, HeadHash: state.HeadHash, RoleID: roleID, SessionID: index.lease.SessionID, NodeID: nodeID, AttemptID: attemptID, Kind: kind, ExpiresAt: expires.Format(time.RFC3339Nano)}
		ref, err := signActionRef(payload, index.secret)
		if err != nil {
			return ActionList{}, err
		}
		remaining := max(0, int(expires.Sub(now).Seconds()))
		result.Actions = append(result.Actions, AllowedAction{ID: payload.ActionID, Kind: kind, Ref: ref, NodeID: nodeID, AttemptID: attemptID, InputSchema: actionInputSchema(kind, node), LeaseRemainingSeconds: remaining, RequiredLeaseSeconds: 0})
	}
	return result, nil
}

func (s *Service) requiredLeaseSeconds(kind string, node domain.NodeDefinition) int {
	providerOperation := ""
	var provider any
	if kind == "effect.prepare" {
		providerOperation = "dispatch"
		var declaration effectNodeInput
		if json.Unmarshal(node.Inputs, &declaration) == nil && declaration.Adapter != "" {
			provider, _ = s.Providers.Effect(declaration.Adapter)
		}
	} else if kind == "gate.evaluate" && node.Decision != nil && node.Decision.ProviderID != "" {
		providerOperation = "decide"
		provider, _ = s.Providers.Policy(node.Decision.ProviderID)
	}
	if budget, ok := provider.(sdk.OperationBudgetProvider); ok {
		if seconds := budget.RequiredLeaseSeconds(providerOperation); seconds > 0 && seconds <= 24*60*60 {
			return seconds
		}
	}
	switch kind {
	case "effect.prepare":
		return 120
	case "resource.close", "resource.reconcile":
		return 60
	case "attempt.finish", "evidence.publish", "decision.record":
		return 30
	default:
		return 5
	}
}

func (s *Service) ApplyAction(ref string, input json.RawMessage, idempotencyKey string) (ActionResult, error) {
	return s.ApplyActionContext(context.Background(), ref, input, idempotencyKey)
}

func (s *Service) ApplyActionContext(ctx context.Context, ref string, input json.RawMessage, idempotencyKey string) (ActionResult, error) {
	result, err := s.applyActionContext(ctx, ref, input, idempotencyKey)
	if err != nil {
		return ActionResult{}, err
	}
	return s.decorateActionResult(ctx, ref, result), nil
}

func (s *Service) applyActionContext(ctx context.Context, ref string, input json.RawMessage, idempotencyKey string) (ActionResult, error) {
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
	if payload.Kind == "incident.supersede" {
		if command, ok := state.Commands[idempotencyKey]; ok {
			incidentID := strings.TrimPrefix(command.ObjectRef, "incident:")
			incident, exists := state.Incidents[incidentID]
			if !exists || incident.Status != "resolved" || incident.RemedyNodeID == "" {
				return ActionResult{}, fmt.Errorf("idempotent incident successor result is unavailable")
			}
			var value struct {
				Note string `json:"note"`
			}
			if err := validateAllowedActionInput(incidentSupersedeInputSchema(), input); err != nil {
				return ActionResult{}, err
			}
			decoder := json.NewDecoder(bytes.NewReader(input))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&value); err != nil {
				return ActionResult{}, fmt.Errorf("decode incident supersede input: %w", err)
			}
			if value.Note == "" || len(value.Note) > 1024 {
				return ActionResult{}, fmt.Errorf("incident supersede note must be 1..1024 bytes")
			}
			incidentDigest, err := incidentRequestDigest("incident.supersede", map[string]any{"successorNodeId": incident.RemedyNodeID, "note": value.Note})
			if err != nil {
				return ActionResult{}, err
			}
			roleMatches := payload.RoleID == command.ActorRole
			incidentMatches := payload.IncidentID == incidentID
			successorMatches := payload.SuccessorNodeID == incident.RemedyNodeID
			if payload.Compact {
				roleMatches = payload.RoleKey == actionBindingKey(state.ProjectID, "role", command.ActorRole)
				incidentMatches = payload.IncidentKey == actionBindingKey(state.ProjectID, "incident", incidentID)
				successorMatches = payload.SuccessorKey == actionBindingKey(state.ProjectID, "node", incident.RemedyNodeID)
			}
			if command.Kind != "incident.supersede" || command.ObjectRef != "incident:"+incidentID || !roleMatches || !incidentMatches || !successorMatches || (command.RequestDigest != "" && command.RequestDigest != incidentDigest) {
				return ActionResult{}, fmt.Errorf("idempotency key is already bound to another command")
			}
			return boundedActionResult(state, ActionResult{ActionID: payload.ActionID, Kind: payload.Kind, NodeID: incident.RemedyNodeID, ObjectRef: command.ObjectRef, Status: incident.Status, Sequence: command.Sequence}), nil
		}
	}
	if payload.Kind == "incident.control-resolve" {
		if command, ok := state.Commands[idempotencyKey]; ok {
			incidentID := strings.TrimPrefix(command.ObjectRef, "incident:")
			incident, exists := state.Incidents[incidentID]
			value, decodeErr := decodeIncidentControlResolveInput(input)
			if decodeErr != nil {
				return ActionResult{}, decodeErr
			}
			controlDigest, digestErr := incidentControlResolveRequestDigest(value)
			if digestErr != nil {
				return ActionResult{}, digestErr
			}
			roleMatches := payload.RoleID == command.ActorRole
			incidentMatches := payload.IncidentID == incidentID
			if payload.Compact {
				roleMatches = payload.RoleKey == actionBindingKey(state.ProjectID, "role", command.ActorRole)
				incidentMatches = payload.IncidentKey == actionBindingKey(state.ProjectID, "incident", incidentID)
			}
			if command.Kind != "incident.control-resolve" || command.ObjectRef != "incident:"+incidentID || !roleMatches || !incidentMatches || (command.RequestDigest != "" && command.RequestDigest != controlDigest) || !exists || incident.Control == nil || incident.Control.ActorRole != command.ActorRole || incident.Control.Disposition != value.Disposition || incident.Control.Resolution != value.Resolution || incident.Control.Note != value.Note {
				return ActionResult{}, fmt.Errorf("idempotency key is already bound to another command")
			}
			return boundedActionResult(state, ActionResult{ActionID: payload.ActionID, Kind: payload.Kind, NodeID: incident.NodeID, ObjectRef: command.ObjectRef, Status: incident.Status, Sequence: command.Sequence}), nil
		}
	}
	if payload.Kind != "incident.supersede" && payload.Kind != "incident.control-resolve" {
		if command, ok := state.Commands[idempotencyKey]; ok {
			result, resultErr := actionResultForSequence(state, command.Sequence)
			if resultErr != nil || result.ActionID != payload.ActionID || result.Kind != payload.Kind || (command.RequestDigest != "" && command.RequestDigest != requestDigest) {
				return ActionResult{}, fmt.Errorf("idempotency key is already bound to another command")
			}
			return boundedActionResult(state, result), nil
		}
	}
	payload, err = resolveCompactActionRef(ctx, state, payload)
	if err != nil {
		return ActionResult{}, err
	}
	if payload.Kind == "incident.supersede" {
		result, applyErr := s.applyIncidentSupersedeAction(ctx, state, payload, input, idempotencyKey)
		return boundedActionResult(state, result), applyErr
	}
	if payload.Kind == "incident.control-resolve" {
		result, applyErr := s.applyIncidentControlResolveAction(ctx, state, payload, input, idempotencyKey)
		return boundedActionResult(state, result), applyErr
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
	if err := validateAllowedActionInput(actionInputSchema(payload.Kind, node), input); err != nil {
		return ActionResult{}, err
	}
	if payload.Kind == "resource.close" || payload.Kind == "resource.reconcile" {
		result, applyErr := s.applyResourceAction(state, payload, input, idempotencyKey, requestDigest)
		return boundedActionResult(state, result), applyErr
	}
	if payload.Kind == "effect.prepare" {
		result, applyErr := s.applyEffectAction(ctx, state, payload, input, idempotencyKey, requestDigest, node)
		return boundedActionResult(state, result), applyErr
	}
	now := authorizedAt.Format(time.RFC3339Nano)
	events := make([]journal.Event, 0, 2)
	attemptID := payload.AttemptID
	actionInput := input
	switch payload.Kind {
	case "role.renew":
		var value struct {
			TTLSeconds int `json:"ttlSeconds"`
		}
		if err := json.Unmarshal(input, &value); err != nil {
			return ActionResult{}, fmt.Errorf("decode role renewal input: %w", err)
		}
		if value.TTLSeconds == 0 {
			value.TTLSeconds = 15 * 60
		}
		renewed := lease
		renewed.BoundAt = now
		renewed.ExpiresAt = authorizedAt.Add(time.Duration(value.TTLSeconds) * time.Second).Format(time.RFC3339Nano)
		renewed.Active = true
		raw, _ := json.Marshal(renewed)
		events = append(events, journal.Event{Type: "role.bound", Payload: raw})
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
			value.Outcome, err = resolveCompletionOutcome(node, value.Outcome, value.OutcomeRef)
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
	return boundedActionResult(state, ActionResult{ActionID: actionRecord.ID, Kind: actionRecord.Kind, NodeID: actionRecord.NodeID, AttemptID: actionRecord.AttemptID, ObjectRef: objectRefForSequence(state, actionRecord.Sequence), Status: actionRecord.Status, Sequence: actionRecord.Sequence}), nil
}

func (s *Service) ApplySelectedActionContext(ctx context.Context, kind, roleID, nodeID string, input json.RawMessage, idempotencyKey string) (ActionResult, error) {
	if strings.TrimSpace(kind) == "" {
		return ActionResult{}, fmt.Errorf("action kind is required")
	}
	actions, err := s.ListActions(roleID, nodeID)
	if err != nil {
		return ActionResult{}, err
	}
	matches := make([]AllowedAction, 0, 2)
	for _, action := range actions.Actions {
		if action.Kind == kind {
			matches = append(matches, action)
		}
	}
	if len(matches) != 1 {
		kinds := make([]string, 0, len(actions.Actions))
		for index, action := range actions.Actions {
			if index == 8 {
				break
			}
			kinds = append(kinds, action.Kind)
		}
		return ActionResult{}, fmt.Errorf("action selector matched %d actions for kind %s; current bounded candidates: %s", len(matches), kind, strings.Join(kinds, ","))
	}
	return s.ApplyActionContext(ctx, matches[0].Ref, input, idempotencyKey)
}

func (s *Service) decorateActionResult(ctx context.Context, ref string, result ActionResult) ActionResult {
	state, _, err := s.load()
	if err != nil {
		result.Continuation = Continuation{SafeToWait: false, ReasonCodes: []string{"continuation_unavailable"}, NextActions: []AllowedAction{}, Owner: "agent"}
		return result
	}
	result.HeadSequence, result.HeadHash, result.GraphRevision = state.HeadSequence, state.HeadHash, state.GraphRevision
	if effect, exists := state.Effects[result.ActionID]; exists {
		result.Status = effect.Status
		result.Sequence = effect.Sequence
	}
	reasons := []string{}
	audit, auditErr := s.preWaitFromStateContext(ctx, state)
	safe := auditErr == nil && audit.SafeToWait
	if auditErr != nil {
		reasons = append(reasons, "pre_wait_unavailable")
	} else {
		if audit.Counts.SubmittedAttempts > 0 {
			reasons = append(reasons, "submitted_attempt_pending")
		}
		if audit.Counts.ReadyNodes > 0 {
			reasons = append(reasons, "ready_frontier_nonempty")
		}
		if audit.Counts.PendingEffects > 0 {
			reasons = append(reasons, "effect_pending")
		}
		if audit.Counts.OpenIncidents > 0 {
			reasons = append(reasons, "incident_open")
		}
		if audit.Counts.StaleAttempts > 0 {
			reasons = append(reasons, "attempt_liveness_stale")
		}
		if audit.Counts.ExpiredRoles > 0 {
			reasons = append(reasons, "role_lease_expired")
		}
		if audit.Counts.OrphanedResources > 0 {
			reasons = append(reasons, "resource_orphaned")
		}
		if audit.Counts.OverdueIncidents > 0 {
			reasons = append(reasons, "incident_overdue")
		}
		if audit.Counts.CircuitIncidents > 0 {
			reasons = append(reasons, "incident_circuit_open")
		}
		if audit.Counts.ZeroReadyCut > 0 {
			reasons = append(reasons, "zero_ready_cut")
		}
	}
	owner := "agent"
	daemonOwnedEffect := result.Kind == "effect.prepare" && (result.Status == "prepared" || result.Status == "dispatched" || result.Status == "reconciling")
	if daemonOwnedEffect {
		owner = "daemon"
		// The authorized Effect itself no longer requires the agent to poll,
		// but unrelated ready/submitted/stale/incident/resource work remains
		// a reason not to yield. More than one pending Effect is not assumed
		// to be owned by this continuation.
		if auditErr == nil && audit.Counts.PendingEffects == 1 && audit.Counts.ReadyNodes == 0 && audit.Counts.SubmittedAttempts == 0 && audit.Counts.StaleAttempts == 0 && audit.Counts.ExpiredRoles == 0 && audit.Counts.OrphanedResources == 0 && audit.Counts.OpenIncidents == 0 && audit.Counts.OverdueIncidents == 0 && audit.Counts.CircuitIncidents == 0 && audit.Counts.ZeroReadyCut == 0 {
			safe = true
			filtered := reasons[:0]
			for _, reason := range reasons {
				if reason != "effect_pending" {
					filtered = append(filtered, reason)
				}
			}
			reasons = filtered
		}
		reasons = append(reasons, "authorized_effect_owned_by_daemon")
	}
	next := []AllowedAction{}
	nextRef := ""
	secret, secretErr := s.actionSecret()
	if secretErr == nil {
		if payload, verifyErr := verifyActionRef(ref, secret); verifyErr == nil {
			if actions, listErr := s.projectAllowedActionsContext(ctx, state, payload.RoleID, 0, s.Now().UTC()); listErr == nil {
				if daemonOwnedEffect {
					filtered := actions[:0]
					for _, action := range actions {
						if action.Kind != "effect.reconcile" || action.NodeID != result.NodeID {
							filtered = append(filtered, action)
						}
					}
					actions = filtered
				}
				// A lease renewal is the closed prerequisite for the next slow
				// operation. Keep it inside the three-action continuation preview
				// and keep the agent active instead of presenting routine short
				// actions while the high-latency operation remains unsafe.
				for index, action := range actions {
					if action.Kind != "role.renew" {
						continue
					}
					safe = false
					foundReason := false
					for _, reason := range reasons {
						if reason == "role_lease_expiring" {
							foundReason = true
							break
						}
					}
					if !foundReason {
						reasons = append(reasons, "role_lease_expiring")
					}
					if index > 0 {
						actions = append([]AllowedAction{action}, append(actions[:index], actions[index+1:]...)...)
					}
					break
				}
				digest := operationsActionsDigest(actions)
				if len(actions) > 3 {
					nextRef = operationsActionsRef(state, payload.RoleID, digest, 0)
				}
				for index := 0; index < len(actions) && index < 3; index++ {
					bounded, boundErr := boundedAllowedAction(state, payload.RoleID, digest, actions[index])
					if boundErr == nil {
						next = append(next, bounded)
					}
				}
			}
		}
	}
	result.Continuation = Continuation{SafeToWait: safe, ReasonCodes: reasons, NextActions: next, NextActionsRef: nextRef, Owner: owner}
	return result
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
		return boundedActionResult(state, ActionResult{ActionID: action.ID, Kind: action.Kind, NodeID: action.NodeID, AttemptID: action.AttemptID, ObjectRef: objectRefForSequence(state, sequence), Status: status, Sequence: resultSequence}), nil
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
		if !s.readOnlyInspection {
			if err := project.SyncDirectory(s.Project.DataDir); err != nil {
				return nil, fmt.Errorf("confirm action secret durability: %w", err)
			}
		}
		return data, nil
	}
	if s.readOnlyInspection {
		return nil, fmt.Errorf("action secret is not initialized; run a writable DAGrail command before requesting signed actions")
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
		if err := project.PublishPathAtomic(tmp, path); err != nil {
			return err
		}
		if err := project.SyncDirectory(s.Project.DataDir); err != nil {
			return fmt.Errorf("confirm action secret durability: %w", err)
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

func actionBindingKey(projectID, kind, value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{projectID, kind, value}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func resolveCompactActionRef(ctx context.Context, state domain.State, payload actionRefPayload) (actionRefPayload, error) {
	if !payload.Compact {
		return payload, nil
	}
	if payload.ProjectID != state.ProjectID || state.Graph == nil || payload.RoleKey == "" || payload.NodeKey == "" {
		return actionRefPayload{}, fmt.Errorf("compact action reference is stale or incomplete")
	}
	resolve := func(kind, key string, values func(func(string) bool)) (string, error) {
		if key == "" {
			return "", nil
		}
		matched := ""
		values(func(value string) bool {
			if ctx.Err() != nil {
				return false
			}
			if actionBindingKey(payload.ProjectID, kind, value) == key {
				if matched != "" && matched != value {
					matched = "!collision"
					return false
				}
				matched = value
			}
			return true
		})
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if matched == "" || matched == "!collision" {
			return "", fmt.Errorf("compact action reference %s binding is stale", kind)
		}
		return matched, nil
	}
	var err error
	payload.RoleID, err = resolve("role", payload.RoleKey, func(yield func(string) bool) {
		for _, role := range state.Graph.Spec.Roles {
			if !yield(role.ID) {
				return
			}
		}
	})
	if err != nil {
		return actionRefPayload{}, err
	}
	lease, exists := state.Leases[payload.RoleID]
	if !exists || actionBindingKey(payload.ProjectID, "session:"+payload.RoleID, lease.SessionID) != payload.SessionKey {
		return actionRefPayload{}, fmt.Errorf("compact action reference session binding is stale")
	}
	payload.SessionID = lease.SessionID
	payload.NodeID, err = resolve("node", payload.NodeKey, func(yield func(string) bool) {
		for _, node := range state.Graph.Spec.Nodes {
			if !yield(node.ID) {
				return
			}
		}
	})
	if err != nil {
		return actionRefPayload{}, err
	}
	payload.AttemptID, err = resolve("attempt", payload.AttemptKey, func(yield func(string) bool) {
		for id := range state.Attempts {
			if !yield(id) {
				return
			}
		}
	})
	if err != nil {
		return actionRefPayload{}, err
	}
	payload.ResourceID, err = resolve("resource", payload.ResourceKey, func(yield func(string) bool) {
		for id := range state.Resources {
			if !yield(id) {
				return
			}
		}
	})
	if err != nil {
		return actionRefPayload{}, err
	}
	payload.IncidentID, err = resolve("incident", payload.IncidentKey, func(yield func(string) bool) {
		for id := range state.Incidents {
			if !yield(id) {
				return
			}
		}
	})
	if err != nil {
		return actionRefPayload{}, err
	}
	payload.SuccessorNodeID, err = resolve("node", payload.SuccessorKey, func(yield func(string) bool) {
		for _, node := range state.Graph.Spec.Nodes {
			if !yield(node.ID) {
				return
			}
		}
	})
	if err != nil {
		return actionRefPayload{}, err
	}
	return payload, nil
}

func compactActionRef(payload actionRefPayload) actionRefPayload {
	return actionRefPayload{
		ActionID:      payload.ActionID,
		ProjectID:     payload.ProjectID,
		GraphRevision: payload.GraphRevision,
		ProviderSet:   payload.ProviderSet,
		HeadHash:      payload.HeadHash,
		Kind:          payload.Kind,
		ExpiresAt:     payload.ExpiresAt,
		Compact:       true,
		RoleKey:       actionBindingKey(payload.ProjectID, "role", payload.RoleID),
		SessionKey:    actionBindingKey(payload.ProjectID, "session:"+payload.RoleID, payload.SessionID),
		NodeKey:       actionBindingKey(payload.ProjectID, "node", payload.NodeID),
		AttemptKey:    actionBindingKey(payload.ProjectID, "attempt", payload.AttemptID),
		ResourceKey:   actionBindingKey(payload.ProjectID, "resource", payload.ResourceID),
		IncidentKey:   actionBindingKey(payload.ProjectID, "incident", payload.IncidentID),
		SuccessorKey:  actionBindingKey(payload.ProjectID, "node", payload.SuccessorNodeID),
	}
}

func signActionRef(payload actionRefPayload, secret []byte) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if len(raw) > maxInlineActionRefPayloadBytes {
		payload = compactActionRef(payload)
		raw, err = json.Marshal(payload)
		if err != nil {
			return "", err
		}
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

func outcomeBindingRef(nodeID, outcomeID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{nodeID, outcomeID}, "\x00")))
	return "outcome:" + hex.EncodeToString(sum[:])
}

func resolveCompletionOutcome(node domain.NodeDefinition, outcomeID, outcomeRef string) (string, error) {
	if (outcomeID == "") == (outcomeRef == "") {
		return "", fmt.Errorf("completion input requires exactly one of outcome or outcomeRef")
	}
	if outcomeID != "" {
		return outcomeID, nil
	}
	matched := ""
	for _, outcome := range node.Outcomes {
		if outcomeBindingRef(node.ID, outcome.ID) != outcomeRef {
			continue
		}
		if matched != "" && matched != outcome.ID {
			return "", fmt.Errorf("outcome reference collision")
		}
		matched = outcome.ID
	}
	if matched == "" {
		return "", fmt.Errorf("outcome reference is not declared by node %s", node.ID)
	}
	return matched, nil
}

func actionInputSchemaForState(state domain.State, kind string, node domain.NodeDefinition) map[string]any {
	schema := actionInputSchema(kind, node)
	if !oneOf(kind, "attempt.finish", "task.complete", "review.resolve", "decision.record", "effect.complete") {
		return schema
	}
	options := make([]map[string]any, 0, len(node.Outcomes))
	for _, outcome := range node.Outcomes {
		ref := outcomeBindingRef(node.ID, outcome.ID)
		option := map[string]any{"ref": ref}
		if len([]byte(outcome.ID)) <= preWaitInlineIDBytes {
			option["id"] = outcome.ID
		} else {
			sum := sha256.Sum256([]byte(outcome.ID))
			option["idDigest"] = "sha256:" + hex.EncodeToString(sum[:])
			option["idBytes"] = len([]byte(outcome.ID))
			option["idDetailRef"] = outcomeIDDetailRef(state, node.ID, strings.TrimPrefix(ref, "outcome:"), 0)
		}
		options = append(options, option)
	}
	schema["x-dagrailOutcomeOptions"] = options
	return schema
}

func actionInputSchema(kind string, node domain.NodeDefinition) map[string]any {
	digest := map[string]any{"type": "string", "pattern": `^sha256:[a-f0-9]{64}$`}
	artifactURI := artifactURIInputSchema()
	evidenceRef := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"digest", "type", "size"}, "properties": map[string]any{"digest": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}, "type": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "size": map[string]any{"type": "integer", "minimum": 0}, "uri": map[string]any{"type": "string", "maxLength": 2048}}}
	provenance := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"producer"}, "properties": map[string]any{"producer": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}, "revision": map[string]any{"type": "string", "maxLength": 256}, "invocationDigest": digest}}
	artifact := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"digest", "type", "size", "provenance"}, "properties": map[string]any{"digest": digest, "type": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "size": map[string]any{"type": "integer", "minimum": 0}, "uri": artifactURI, "provenance": provenance}}
	protectedInput := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"name", "digest"}, "properties": map[string]any{"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "digest": digest}}
	switch kind {
	case "role.renew":
		return map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"ttlSeconds": map[string]any{"type": "integer", "minimum": 60, "maximum": 86400}}}
	case "attempt.checkpoint":
		return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"summary"}, "properties": map[string]any{"summary": map[string]any{"type": "string", "minLength": 1, "maxLength": 2048}, "evidenceRefs": map[string]any{"type": "array", "maxItems": 128, "items": evidenceRef}}}
	case "attempt.finish", "task.complete", "review.resolve", "decision.record", "effect.complete":
		outcomes := make([]string, 0, len(node.Outcomes))
		outcomeRefs := make([]string, 0, len(node.Outcomes))
		for _, outcome := range node.Outcomes {
			if len([]byte(outcome.ID)) <= preWaitInlineIDBytes {
				outcomes = append(outcomes, outcome.ID)
			}
			outcomeRefs = append(outcomeRefs, outcomeBindingRef(node.ID, outcome.ID))
		}
		factMap := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
		facts := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"decision": factMap, "evidence": factMap, "policy": factMap}}
		return map[string]any{"type": "object", "oneOf": []any{map[string]any{"required": []string{"outcome"}}, map[string]any{"required": []string{"outcomeRef"}}}, "additionalProperties": false, "properties": map[string]any{"outcome": map[string]any{"type": "string", "enum": outcomes}, "outcomeRef": map[string]any{"type": "string", "enum": outcomeRefs}, "facts": facts, "evidenceRefs": map[string]any{"type": "array", "maxItems": 128, "items": evidenceRef}, "classification": map[string]any{"enum": []string{"work-product", "policy", "fixture", "infrastructure", "evidence", "external-effect", "unknown"}}}}
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

func artifactURIInputSchema() map[string]any {
	return map[string]any{
		"type":      "string",
		"maxLength": 2048,
		"anyOf": []any{
			map[string]any{"const": ""},
			map[string]any{
				"minLength": 1,
				"format":    "uri",
				"pattern":   `^(?:[Ff][Ii][Ll][Ee]|[Hh][Tt][Tt][Pp]|[Hh][Tt][Tt][Pp][Ss]|[Ss]3|[Gg][Ss]|[Aa][Zz]|[Gg][Ii][Tt]|[Oo][Cc][Ii]):[^?#]*$`,
				"not": map[string]any{
					"pattern": `^[A-Za-z][A-Za-z0-9+.-]*://[^/?#]*@`,
				},
			},
		},
	}
}

func incidentSupersedeInputSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"note"}, "properties": map[string]any{"note": map[string]any{"type": "string", "minLength": 1, "maxLength": 1024}}}
}

func incidentControlResolveInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"disposition", "resolution", "note"},
		"properties": map[string]any{
			"disposition": map[string]any{"enum": []string{"rollback", "lkg", "quarantine", "off-critical-path"}},
			"resolution":  map[string]any{"type": "string", "minLength": 1, "maxLength": 1024},
			"note":        map[string]any{"type": "string", "minLength": 1, "maxLength": 1024},
		},
	}
}

func validateAllowedActionInput(schemaDocument map[string]any, input json.RawMessage) error {
	var instance any
	if err := json.Unmarshal(input, &instance); err != nil {
		return fmt.Errorf("decode action input: %w", err)
	}
	schemaJSON, err := json.Marshal(schemaDocument)
	if err != nil {
		return fmt.Errorf("encode allowed action input schema: %w", err)
	}
	schemaDigest := sha256.Sum256(schemaJSON)
	cacheKey := hex.EncodeToString(schemaDigest[:])
	if cached, ok := allowedActionSchemaCache.Load(cacheKey); ok {
		if err := cached.(*jsonschema.Schema).Validate(instance); err != nil {
			return fmt.Errorf("action input does not match allowed action schema: %w", err)
		}
		return nil
	}
	var normalizedSchema any
	if err := json.Unmarshal(schemaJSON, &normalizedSchema); err != nil {
		return fmt.Errorf("normalize allowed action input schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	resource := "urn:dagrail:allowed-action-runtime:" + cacheKey
	if err := compiler.AddResource(resource, normalizedSchema); err != nil {
		return fmt.Errorf("compile allowed action input schema: %w", err)
	}
	compiled, err := compiler.Compile(resource)
	if err != nil {
		return fmt.Errorf("compile allowed action input schema: %w", err)
	}
	actual, _ := allowedActionSchemaCache.LoadOrStore(cacheKey, compiled)
	compiled = actual.(*jsonschema.Schema)
	if err := compiled.Validate(instance); err != nil {
		return fmt.Errorf("action input does not match allowed action schema: %w", err)
	}
	return nil
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
