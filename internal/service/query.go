package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
)

type PreWaitAudit struct {
	SafeToWait             bool          `json:"safeToWait"`
	Cursor                 uint64        `json:"cursor"`
	Counts                 PreWaitCounts `json:"counts"`
	Truncated              bool          `json:"truncated"`
	InspectRef             string        `json:"inspectRef,omitempty"`
	ReadyNodes             []string      `json:"readyNodes,omitempty"`
	SubmittedAttempts      []string      `json:"submittedAttempts,omitempty"`
	ActiveAttempts         []string      `json:"activeAttempts,omitempty"`
	WaitingAttempts        []string      `json:"waitingAttempts,omitempty"`
	StaleAttempts          []string      `json:"staleAttempts,omitempty"`
	ExpiredRoles           []string      `json:"expiredRoles,omitempty"`
	PendingEffects         []string      `json:"pendingEffects,omitempty"`
	ActiveResources        []string      `json:"activeResources,omitempty"`
	OrphanedResources      []string      `json:"orphanedResources,omitempty"`
	OpenIncidents          []string      `json:"openIncidents,omitempty"`
	OverdueIncidents       []string      `json:"overdueIncidents,omitempty"`
	CircuitIncidents       []string      `json:"circuitOpenIncidents,omitempty"`
	ZeroReadyCut           []string      `json:"zeroReadyCut,omitempty"`
	Remediations           []Remediation `json:"remediations,omitempty"`
	RemediationCount       int           `json:"remediationCount"`
	RemediationsTruncated  bool          `json:"remediationsTruncated"`
	RemediationsInspectRef string        `json:"remediationsInspectRef,omitempty"`
	Reasons                []string      `json:"reasons,omitempty"`
	pageRef                string
}

type PreWaitCounts struct {
	ReadyNodes        int `json:"readyNodes"`
	SubmittedAttempts int `json:"submittedAttempts"`
	ActiveAttempts    int `json:"activeAttempts"`
	WaitingAttempts   int `json:"waitingAttempts"`
	StaleAttempts     int `json:"staleAttempts"`
	ExpiredRoles      int `json:"expiredRoles"`
	PendingEffects    int `json:"pendingEffects"`
	ActiveResources   int `json:"activeResources"`
	OrphanedResources int `json:"orphanedResources"`
	OpenIncidents     int `json:"openIncidents"`
	OverdueIncidents  int `json:"overdueIncidents"`
	CircuitIncidents  int `json:"circuitOpenIncidents"`
	ZeroReadyCut      int `json:"zeroReadyCut"`
}

type PreWaitPageItem struct {
	Category    string `json:"category"`
	ID          string `json:"id,omitempty"`
	EntityRef   string `json:"entityRef,omitempty"`
	InspectRef  string `json:"inspectRef,omitempty"`
	IDDigest    string `json:"idDigest,omitempty"`
	IDBytes     int    `json:"idBytes,omitempty"`
	IDTruncated bool   `json:"idTruncated,omitempty"`
}

type PreWaitPage struct {
	JournalHead     string            `json:"journalHead"`
	InventoryDigest string            `json:"inventoryDigest"`
	Offset          int               `json:"offset"`
	Total           int               `json:"total"`
	Items           []PreWaitPageItem `json:"items"`
	NextRef         string            `json:"nextRef,omitempty"`
}

type DependencyCutPage struct {
	NodeID         string               `json:"nodeId,omitempty"`
	Offset         int                  `json:"offset"`
	Total          int                  `json:"total"`
	Digest         string               `json:"digest"`
	Items          []string             `json:"items"`
	OversizedItems []BoundedListItemRef `json:"oversizedItems,omitempty"`
	NextRef        string               `json:"nextRef,omitempty"`
}

type BoundedListItemRef struct {
	Index      int    `json:"index"`
	InspectRef string `json:"inspectRef"`
	Digest     string `json:"digest"`
	Bytes      int    `json:"bytes"`
}

type OperationDescriptor struct {
	Kind   string         `json:"kind"`
	Params map[string]any `json:"params"`
}

type Remediation struct {
	ID                  string              `json:"id"`
	Code                string              `json:"code"`
	TargetRef           string              `json:"targetRef,omitempty"`
	OwnerRole           string              `json:"ownerRole,omitempty"`
	NodeID              string              `json:"nodeId,omitempty"`
	DependencyCut       []string            `json:"dependencyCut,omitempty"`
	DependencyCutCount  int                 `json:"dependencyCutCount,omitempty"`
	DependencyCutDigest string              `json:"dependencyCutDigest,omitempty"`
	DependencyCutRef    string              `json:"dependencyCutRef,omitempty"`
	Operation           OperationDescriptor `json:"operation,omitempty"`
	DetailRef           string              `json:"detailRef,omitempty"`
	DetailDigest        string              `json:"detailDigest,omitempty"`
	DetailBytes         int                 `json:"detailBytes,omitempty"`
	DetailsTruncated    bool                `json:"detailsTruncated,omitempty"`
}

type AuthorizationEnvelope struct {
	RoleID             string   `json:"roleId,omitempty"`
	RoleRef            string   `json:"roleRef,omitempty"`
	SessionID          string   `json:"sessionId,omitempty"`
	SessionRef         string   `json:"sessionRef,omitempty"`
	LeaseState         string   `json:"leaseState"`
	ExpiresAt          string   `json:"expiresAt,omitempty"`
	Capabilities       []string `json:"capabilities,omitempty"`
	RoutineActions     []string `json:"routineActions"`
	EscalationRequired []string `json:"escalationRequired"`
}

type EffectContinuity struct {
	ActionID                  string   `json:"actionId"`
	PreparedSequence          uint64   `json:"preparedSequence"`
	CurrentHeadSequence       uint64   `json:"currentHeadSequence"`
	HeadAdvanced              bool     `json:"headAdvanced"`
	DeclarationAvailable      bool     `json:"declarationAvailable"`
	AdapterIDUnchanged        bool     `json:"adapterIdUnchanged"`
	AdapterMetadataAvailable  bool     `json:"adapterMetadataAvailable"`
	AdapterUnchanged          bool     `json:"adapterUnchanged"`
	PreparedAdapterVersion    string   `json:"preparedAdapterVersion,omitempty"`
	CurrentAdapterVersion     string   `json:"currentAdapterVersion,omitempty"`
	PreparedAdapterSchemaHash string   `json:"preparedAdapterSchemaHash,omitempty"`
	CurrentAdapterSchemaHash  string   `json:"currentAdapterSchemaHash,omitempty"`
	RequestUnchanged          bool     `json:"requestUnchanged"`
	PreparedRequestDigest     string   `json:"preparedRequestDigest,omitempty"`
	CurrentRequestDigest      string   `json:"currentRequestDigest,omitempty"`
	CausalContractUnchanged   bool     `json:"causalContractUnchanged"`
	ReconcileAllowed          bool     `json:"reconcileAllowed"`
	ReconcileBlockers         []string `json:"reconcileBlockers,omitempty"`
	Reasons                   []string `json:"reasons"`
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

type IncidentSummary struct {
	ID             string `json:"id,omitempty"`
	IncidentRef    string `json:"incidentRef,omitempty"`
	SourceType     string `json:"sourceType"`
	Status         string `json:"status"`
	Classification string `json:"classification"`
	OwnerRole      string `json:"ownerRole,omitempty"`
	NodeID         string `json:"nodeId,omitempty"`
	Deadline       string `json:"deadline,omitempty"`
	DetailRef      string `json:"detailRef,omitempty"`
	DetailDigest   string `json:"detailDigest,omitempty"`
	DetailBytes    int    `json:"detailBytes,omitempty"`
	Truncated      bool   `json:"truncated,omitempty"`
}

type IncidentIndex struct {
	JournalHead     string            `json:"journalHead"`
	InventoryDigest string            `json:"inventoryDigest"`
	Offset          int               `json:"offset"`
	Total           int               `json:"total"`
	Items           []IncidentSummary `json:"items"`
	NextRef         string            `json:"nextRef,omitempty"`
}

type OperationsIndex struct {
	RoleID                 string                `json:"roleId,omitempty"`
	RoleRef                string                `json:"roleRef,omitempty"`
	IdentityDetailRef      string                `json:"identityDetailRef,omitempty"`
	IdentityDetailDigest   string                `json:"identityDetailDigest,omitempty"`
	IdentityDetailBytes    int                   `json:"identityDetailBytes,omitempty"`
	IdentityTruncated      bool                  `json:"identityTruncated,omitempty"`
	GraphRevision          string                `json:"graphRevision"`
	JournalHead            string                `json:"journalHead"`
	Authorization          AuthorizationEnvelope `json:"authorization"`
	ProjectAllowedActions  []AllowedAction       `json:"projectAllowedActions"`
	ActionCount            int                   `json:"actionCount"`
	ActionsTruncated       bool                  `json:"actionsTruncated"`
	ActionsInspectRef      string                `json:"actionsInspectRef,omitempty"`
	Remediations           []Remediation         `json:"remediations"`
	RemediationCount       int                   `json:"remediationCount"`
	RemediationsTruncated  bool                  `json:"remediationsTruncated"`
	RemediationsInspectRef string                `json:"remediationsInspectRef,omitempty"`
	Truncated              bool                  `json:"truncated"`
}

type OperationsActionPage struct {
	RoleID        string          `json:"roleId,omitempty"`
	RoleRef       string          `json:"roleRef,omitempty"`
	GraphRevision string          `json:"graphRevision"`
	JournalHead   string          `json:"journalHead"`
	Offset        int             `json:"offset"`
	Total         int             `json:"total"`
	Actions       []AllowedAction `json:"actions"`
	NextRef       string          `json:"nextRef,omitempty"`
}

type RemediationPage struct {
	JournalHead     string        `json:"journalHead"`
	InventoryDigest string        `json:"inventoryDigest"`
	Offset          int           `json:"offset"`
	Total           int           `json:"total"`
	Remediations    []Remediation `json:"remediations"`
	NextRef         string        `json:"nextRef,omitempty"`
}

type BoundedDetailChunk struct {
	Digest     string `json:"digest"`
	Offset     int    `json:"offset"`
	TotalBytes int    `json:"totalBytes"`
	Encoding   string `json:"encoding"`
	Chunk      string `json:"chunk"`
	NextRef    string `json:"nextRef,omitempty"`
}

func BoundedGraphImpact(impact GraphImpact) GraphImpact {
	raw, err := json.Marshal(impact)
	if err != nil || len(raw) <= operationsMaxBytes {
		return impact
	}
	sum := sha256.Sum256(raw)
	return GraphImpact{CurrentRevision: impact.CurrentRevision, ProposedRevision: impact.ProposedRevision, Token: impact.Token, ExpiresAt: impact.ExpiresAt,
		AddedNodeCount: len(impact.AddedNodes), UpdatedNodeCount: len(impact.UpdatedNodes), RemovedNodeCount: len(impact.RemovedNodes),
		AddedEdgeCount: len(impact.AddedEdges), RemovedEdgeCount: len(impact.RemovedEdges), AddedRoleCount: len(impact.AddedRoles),
		UpdatedRoleCount: len(impact.UpdatedRoles), RemovedRoleCount: len(impact.RemovedRoles), AddedGroupCount: len(impact.AddedGroups),
		UpdatedGroupCount: len(impact.UpdatedGroups), RemovedGroupCount: len(impact.RemovedGroups), MovedNodeCount: len(impact.MovedNodes), DependencyCutCount: len(impact.DependencyCut),
		ImpactDigest: "sha256:" + hex.EncodeToString(sum[:]), Truncated: true}
}

type EffectActionResult struct {
	Effect       *domain.EffectAction `json:"effect,omitempty"`
	ActionID     string               `json:"actionId,omitempty"`
	EffectRef    string               `json:"effectRef,omitempty"`
	Status       string               `json:"status"`
	Sequence     uint64               `json:"sequence"`
	DetailRef    string               `json:"detailRef,omitempty"`
	DetailDigest string               `json:"detailDigest,omitempty"`
	DetailBytes  int                  `json:"detailBytes,omitempty"`
	Truncated    bool                 `json:"truncated,omitempty"`
}

func (s *Service) BoundedEffectAction(effect domain.EffectAction) (EffectActionResult, error) {
	state, _, err := s.load()
	if err != nil {
		return EffectActionResult{}, err
	}
	current, exists := state.Effects[effect.ID]
	if !exists {
		return EffectActionResult{}, fmt.Errorf("Effect %s is unavailable in the current snapshot", effect.ID)
	}
	raw, err := json.Marshal(current)
	if err != nil {
		return EffectActionResult{}, err
	}
	returnedRaw, err := json.Marshal(effect)
	if err != nil {
		return EffectActionResult{}, err
	}
	if !bytes.Equal(raw, returnedRaw) {
		return EffectActionResult{}, fmt.Errorf("Effect %s changed before its bounded result was encoded; retry the same idempotent operation", effect.ID)
	}
	if len(raw) <= operationsMaxBytes {
		return EffectActionResult{Effect: &current, ActionID: current.ID, Status: current.Status, Sequence: current.Sequence}, nil
	}
	sum := sha256.Sum256(raw)
	return EffectActionResult{EffectRef: storedObjectRef(state, "effect", current.ID), Status: current.Status, Sequence: current.Sequence, DetailRef: storedObjectDetailRef(state, "effect", current.ID, 0), DetailDigest: "sha256:" + hex.EncodeToString(sum[:]), DetailBytes: len(raw), Truncated: true}, nil
}

type allowedActionDetail struct {
	NodeID      string         `json:"nodeId"`
	AttemptID   string         `json:"attemptId,omitempty"`
	TargetRef   string         `json:"targetRef,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (s *Service) ListEvidence(nodeID, attemptID string) (EvidenceIndex, error) {
	return s.ListEvidenceContext(context.Background(), nodeID, attemptID)
}

func (s *Service) ListEvidenceContext(ctx context.Context, nodeID, attemptID string) (EvidenceIndex, error) {
	if err := ctx.Err(); err != nil {
		return EvidenceIndex{}, err
	}
	state, _, err := s.load()
	if err != nil {
		return EvidenceIndex{}, err
	}
	return listEvidenceFromStateContext(ctx, state, nodeID, attemptID)
}

func listEvidenceFromStateContext(ctx context.Context, state domain.State, nodeID, attemptID string) (EvidenceIndex, error) {
	result := EvidenceIndex{}
	packageIDs := map[string]bool{}
	for _, pack := range state.EvidencePackages {
		if err := ctx.Err(); err != nil {
			return EvidenceIndex{}, err
		}
		if (nodeID != "" && pack.NodeID != nodeID) || (attemptID != "" && pack.AttemptID != attemptID) {
			continue
		}
		packageIDs[pack.ID] = true
		result.Packages = append(result.Packages, EvidencePackageSummary{ID: pack.ID, NodeID: pack.NodeID, AttemptID: pack.AttemptID, CoreDigest: pack.CoreDigest, CreatedAt: pack.CreatedAt})
	}
	for _, decision := range state.ReuseDecisions {
		if err := ctx.Err(); err != nil {
			return EvidenceIndex{}, err
		}
		if (nodeID != "" || attemptID != "") && !packageIDs[decision.PackageID] {
			continue
		}
		result.Decisions = append(result.Decisions, ReuseDecisionSummary{ID: decision.ID, PackageID: decision.PackageID, PolicyID: decision.Policy.ID, Result: decision.Result, CreatedAt: decision.CreatedAt})
	}
	sort.Slice(result.Packages, func(i, j int) bool { return result.Packages[i].ID < result.Packages[j].ID })
	sort.Slice(result.Decisions, func(i, j int) bool { return result.Decisions[i].ID < result.Decisions[j].ID })
	return result, nil
}

func statusDetailRef(state domain.State, digest string, offset int) string {
	return fmt.Sprintf("status-detail:%s:%s:%d", state.HeadHash, digest, offset)
}

// BoundedStatusContext keeps the ordinary status shape for small projects and
// replaces an oversized diagnostic with exact aggregate counts plus a
// digest-bound detail stream. Status is a control-plane query, not an export.
func (s *Service) BoundedStatusContext(ctx context.Context) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, _, err := s.load()
	if err != nil {
		return nil, err
	}
	status, err := operationalStatusFromStateContext(ctx, state, s.Now().UTC())
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return nil, err
	}
	if len(raw) <= operationsMaxBytes {
		return status, nil
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	return map[string]any{
		"projectId": status.ProjectID, "graphRevision": status.GraphRevision,
		"headSequence": status.HeadSequence, "headHash": status.HeadHash,
		"nodes": status.Nodes, "attempts": status.Attempts, "effects": status.Effects, "incidents": status.Incidents,
		"overdueIncidentCount": len(status.OverdueIncidents), "expiredRoleLeaseCount": len(status.ExpiredRoleLeases),
		"frontierReadyCount": len(status.Frontier.Ready), "frontierBlockedCount": len(status.Frontier.Blocked),
		"frontierUnreachableCount": len(status.Frontier.Unreachable), "frontierResourceBlockedCount": len(status.Frontier.ResourceBlocked),
		"detailDigest": "sha256:" + digest, "detailBytes": len(raw), "detailRef": statusDetailRef(state, digest, 0), "truncated": true,
	}, nil
}

func (s *Service) statusDetailPage(ctx context.Context, state domain.State, expectedHead, expectedDigest string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale status detail reference: journal head changed")
	}
	if err := ctx.Err(); err != nil {
		return BoundedDetailChunk{}, err
	}
	status, err := operationalStatusFromStateContext(ctx, state, s.Now().UTC())
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if digest != expectedDigest {
		return BoundedDetailChunk{}, fmt.Errorf("stale status detail reference: time-dependent status changed")
	}
	return boundedDetailChunk(raw, "sha256:"+digest, offset, func(next int) string { return statusDetailRef(state, digest, next) })
}

func historyDetailRef(state domain.State, after uint64, limit int, digest string, offset int) string {
	return fmt.Sprintf("history-detail:%s:%d:%d:%s:%d", state.HeadHash, after, limit, digest, offset)
}

func (s *Service) BoundedHistoryContext(ctx context.Context, after uint64, limit int) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, segments, err := s.load()
	if err != nil {
		return nil, err
	}
	page, err := historyFromSegmentsContext(ctx, segments, after, limit)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(page)
	if err != nil {
		return nil, err
	}
	if len(raw) <= operationsMaxBytes {
		return page, nil
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	return map[string]any{
		"after": page.After, "nextCursor": page.NextCursor, "entryCount": len(page.Entries), "truncated": true,
		"moreHistory": page.Truncated, "journalHead": state.HeadHash,
		"detailDigest": "sha256:" + digest, "detailBytes": len(raw), "detailRef": historyDetailRef(state, after, limit, digest, 0),
	}, nil
}

func (s *Service) historyDetailPage(ctx context.Context, state domain.State, expectedHead string, after uint64, limit int, expectedDigest string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale history detail reference: journal head changed")
	}
	if err := ctx.Err(); err != nil {
		return BoundedDetailChunk{}, err
	}
	_, segments, err := s.load()
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	page, err := historyFromSegmentsContext(ctx, segments, after, limit)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	raw, err := json.Marshal(page)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if digest != expectedDigest {
		return BoundedDetailChunk{}, fmt.Errorf("stale history detail reference: page changed")
	}
	return boundedDetailChunk(raw, "sha256:"+digest, offset, func(next int) string {
		return historyDetailRef(state, after, limit, digest, next)
	})
}

func evidenceIndexDetailRef(state domain.State, nodeID, attemptID, digest string, offset int) string {
	nodeKey, attemptKey := "-", "-"
	if nodeID != "" {
		nodeKey = opaqueEntityKey(state, "node", nodeID)
	}
	if attemptID != "" {
		attemptKey = opaqueEntityKey(state, "attempt", attemptID)
	}
	return fmt.Sprintf("evidence-index-detail:%s:%s:%s:%s:%d", state.HeadHash, nodeKey, attemptKey, digest, offset)
}

func (s *Service) BoundedEvidenceContext(ctx context.Context, nodeID, attemptID string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, _, err := s.load()
	if err != nil {
		return nil, err
	}
	index, err := listEvidenceFromStateContext(ctx, state, nodeID, attemptID)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(index)
	if err != nil {
		return nil, err
	}
	if len(raw) <= operationsMaxBytes {
		return index, nil
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	return map[string]any{
		"journalHead": state.HeadHash, "packageCount": len(index.Packages), "reuseDecisionCount": len(index.Decisions),
		"detailDigest": "sha256:" + digest, "detailBytes": len(raw),
		"detailRef": evidenceIndexDetailRef(state, nodeID, attemptID, digest, 0), "truncated": true,
	}, nil
}

func actionListDetailRef(state domain.State, roleID, nodeID, digest string, offset int) string {
	roleKey, nodeKey := "-", "-"
	if roleID != "" {
		roleKey = opaqueEntityKey(state, "role", roleID)
	}
	if nodeID != "" {
		nodeKey = opaqueEntityKey(state, "node", nodeID)
	}
	return fmt.Sprintf("action-list-detail:%s:%s:%s:%s:%d", state.HeadHash, roleKey, nodeKey, digest, offset)
}

func (s *Service) BoundedActionListContext(ctx context.Context, roleID, nodeID string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, _, err := s.load()
	if err != nil {
		return nil, err
	}
	list, err := s.listBoundedActionsFromState(state, roleID, nodeID)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return nil, err
	}
	if len(raw) <= operationsMaxBytes {
		return list, nil
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	return map[string]any{
		"journalHead": state.HeadHash, "actionCount": len(list.Actions),
		"detailDigest": "sha256:" + digest, "detailBytes": len(raw),
		"detailRef": actionListDetailRef(state, roleID, nodeID, digest, 0), "truncated": true,
	}, nil
}

func (s *Service) actionListDetailPage(ctx context.Context, state domain.State, expectedHead, roleKey, nodeKey, expectedDigest string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale action list detail reference: journal head changed")
	}
	roleID, nodeID := "", ""
	if roleKey != "-" {
		var exists bool
		roleID, exists = roleIDForOpaqueKey(state, roleKey)
		if !exists {
			return BoundedDetailChunk{}, fmt.Errorf("unknown action-list Role reference")
		}
	}
	if nodeKey != "-" {
		var exists bool
		nodeID, exists = nodeIDForOpaqueKey(state, nodeKey)
		if !exists {
			return BoundedDetailChunk{}, fmt.Errorf("unknown action-list Node reference")
		}
	}
	if err := ctx.Err(); err != nil {
		return BoundedDetailChunk{}, err
	}
	list, err := s.listBoundedActionsFromState(state, roleID, nodeID)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if digest != expectedDigest {
		return BoundedDetailChunk{}, fmt.Errorf("stale action list detail reference: inventory changed")
	}
	return boundedDetailChunk(raw, "sha256:"+digest, offset, func(next int) string {
		return actionListDetailRef(state, roleID, nodeID, digest, next)
	})
}

func (s *Service) evidenceIndexDetailPage(ctx context.Context, state domain.State, expectedHead, nodeKey, attemptKey, expectedDigest string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale evidence index detail reference: journal head changed")
	}
	nodeID, attemptID := "", ""
	if nodeKey != "-" {
		var exists bool
		nodeID, exists = nodeIDForOpaqueKey(state, nodeKey)
		if !exists {
			return BoundedDetailChunk{}, fmt.Errorf("unknown evidence Node reference")
		}
	}
	if attemptKey != "-" {
		var exists bool
		attemptID, exists = attemptIDForOpaqueKey(state, attemptKey)
		if !exists {
			return BoundedDetailChunk{}, fmt.Errorf("unknown evidence Attempt reference")
		}
	}
	if err := ctx.Err(); err != nil {
		return BoundedDetailChunk{}, err
	}
	index, err := listEvidenceFromStateContext(ctx, state, nodeID, attemptID)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	raw, err := json.Marshal(index)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if digest != expectedDigest {
		return BoundedDetailChunk{}, fmt.Errorf("stale evidence index detail reference: inventory changed")
	}
	return boundedDetailChunk(raw, "sha256:"+digest, offset, func(next int) string {
		return evidenceIndexDetailRef(state, nodeID, attemptID, digest, next)
	})
}

func incidentIndex(state domain.State, offset, limit int) IncidentIndex {
	result, _ := incidentIndexContext(context.Background(), state, "", "", offset, limit)
	return result
}

func incidentInventory(ctx context.Context, state domain.State) ([]IncidentSummary, error) {
	items := make([]IncidentSummary, 0, len(state.Incidents))
	for _, incident := range state.Incidents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if incident.Status == "resolved" {
			continue
		}
		items = append(items, IncidentSummary{ID: incident.ID, SourceType: incident.SourceType, Status: incident.Status, Classification: incident.Classification, OwnerRole: incident.OwnerRole, NodeID: incident.NodeID, Deadline: incident.Deadline})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, ctx.Err()
}

func incidentInventoryDigest(state domain.State, items []IncidentSummary) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(state.ProjectID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(state.HeadHash))
	for _, item := range items {
		raw, _ := json.Marshal(item)
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(raw)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func incidentIndexRef(state domain.State, digest string, offset int) string {
	return fmt.Sprintf("incident-index:%s:%s:%d", state.HeadHash, digest, offset)
}

func incidentRefForIncident(state domain.State, incidentID string) string {
	return "incident-key:" + opaqueEntityKey(state, "incident", incidentID)
}

func incidentDetailRef(state domain.State, incidentID string, offset int) string {
	return fmt.Sprintf("incident-detail:%s:%s:%d", state.HeadHash, opaqueEntityKey(state, "incident", incidentID), offset)
}

func incidentIDForOpaqueKey(state domain.State, key string) (string, bool) {
	for incidentID := range state.Incidents {
		if opaqueEntityKey(state, "incident", incidentID) == key {
			return incidentID, true
		}
	}
	return "", false
}

func boundedIncidentSummary(state domain.State, item IncidentSummary) IncidentSummary {
	raw, _ := json.Marshal(item)
	if len(raw) <= preWaitInlineIDBytes {
		return item
	}
	incidentRaw, _ := json.Marshal(state.Incidents[item.ID])
	sum := sha256.Sum256(incidentRaw)
	return IncidentSummary{IncidentRef: incidentRefForIncident(state, item.ID), SourceType: item.SourceType, Status: item.Status, Classification: item.Classification, DetailRef: incidentDetailRef(state, item.ID, 0), DetailDigest: "sha256:" + hex.EncodeToString(sum[:]), DetailBytes: len(incidentRaw), Truncated: true}
}

func incidentInspectionBytes(state domain.State, incidentID string) ([]byte, error) {
	incident, found := state.Incidents[incidentID]
	if !found {
		return nil, fmt.Errorf("unknown incident %s", incidentID)
	}
	return json.Marshal(incident)
}

func boundedIncidentInspection(state domain.State, incidentID string) (any, error) {
	raw, err := incidentInspectionBytes(state, incidentID)
	if err != nil {
		return nil, err
	}
	if len(raw) <= operationsMaxBytes {
		return state.Incidents[incidentID], nil
	}
	sum := sha256.Sum256(raw)
	idSum := sha256.Sum256([]byte(incidentID))
	return map[string]any{"incidentRef": incidentRefForIncident(state, incidentID), "incidentIdDigest": "sha256:" + hex.EncodeToString(idSum[:]), "incidentIdBytes": len([]byte(incidentID)), "detailDigest": "sha256:" + hex.EncodeToString(sum[:]), "detailBytes": len(raw), "detailRef": incidentDetailRef(state, incidentID, 0), "truncated": true}, nil
}

func incidentDetailPage(state domain.State, expectedHead, incidentKey string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale incident detail reference: journal head changed")
	}
	incidentID, exists := incidentIDForOpaqueKey(state, incidentKey)
	if !exists {
		return BoundedDetailChunk{}, fmt.Errorf("unknown incident detail reference")
	}
	raw, err := incidentInspectionBytes(state, incidentID)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	return boundedDetailChunk(raw, digest, offset, func(next int) string { return incidentDetailRef(state, incidentID, next) })
}

func incidentIndexContext(ctx context.Context, state domain.State, expectedHead, expectedDigest string, offset, limit int) (IncidentIndex, error) {
	if expectedHead != "" && state.HeadHash != expectedHead {
		return IncidentIndex{}, fmt.Errorf("stale incident index reference: journal head changed")
	}
	items, err := incidentInventory(ctx, state)
	if err != nil {
		return IncidentIndex{}, err
	}
	digest := incidentInventoryDigest(state, items)
	if expectedDigest != "" && digest != expectedDigest {
		return IncidentIndex{}, fmt.Errorf("stale incident index reference: incident inventory changed")
	}
	result := IncidentIndex{JournalHead: state.HeadHash, InventoryDigest: "sha256:" + digest, Offset: offset, Total: len(items), Items: []IncidentSummary{}}
	if offset < 0 || offset >= len(items) {
		return result, nil
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	for index := offset; index < end; index++ {
		result.Items = append(result.Items, boundedIncidentSummary(state, items[index]))
	}
	for {
		next := offset + len(result.Items)
		if next < len(items) {
			result.NextRef = incidentIndexRef(state, digest, next)
		} else {
			result.NextRef = ""
		}
		raw, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return IncidentIndex{}, marshalErr
		}
		if len(raw) <= operationsMaxBytes {
			break
		}
		if len(result.Items) <= 1 {
			return IncidentIndex{}, fmt.Errorf("incident index metadata exceeds %d-byte response limit", operationsMaxBytes)
		}
		result.Items = result.Items[:len(result.Items)-1]
	}
	return result, ctx.Err()
}

func preWaitInventoryItems(inventory preWaitInventory) []PreWaitPageItem {
	items := make([]PreWaitPageItem, 0)
	appendItems := func(category string, values []string) {
		for _, id := range values {
			items = append(items, PreWaitPageItem{Category: category, ID: id})
		}
	}
	appendItems("readyNode", inventory.readyNodes)
	appendItems("submittedAttempt", inventory.submittedAttempts)
	appendItems("activeAttempt", inventory.activeAttempts)
	appendItems("waitingAttempt", inventory.waitingAttempts)
	appendItems("staleAttempt", inventory.staleAttempts)
	appendItems("expiredRole", inventory.expiredRoles)
	appendItems("pendingEffect", inventory.pendingEffects)
	appendItems("activeResource", inventory.activeResources)
	appendItems("orphanedResource", inventory.orphanedResources)
	appendItems("openIncident", inventory.openIncidents)
	appendItems("overdueIncident", inventory.overdueIncidents)
	appendItems("circuitOpenIncident", inventory.circuitIncidents)
	appendItems("zeroReadyCut", inventory.zeroReadyCut)
	return items
}

func preWaitInventoryDigest(state domain.State, items []PreWaitPageItem) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(state.ProjectID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(state.HeadHash))
	for _, item := range items {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(item.Category))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(item.ID))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func preWaitPageRef(state domain.State, digest string, offset int) string {
	return fmt.Sprintf("pre-wait-page:%s:%s:%d", state.HeadHash, digest, offset)
}

func preWaitItemRef(state domain.State, inventoryDigest string, index, offset int) string {
	return fmt.Sprintf("pre-wait-item:%s:%s:%d:%d", state.HeadHash, inventoryDigest, index, offset)
}

func boundedPreWaitItem(state domain.State, inventoryDigest string, index int, item PreWaitPageItem) PreWaitPageItem {
	if len([]byte(item.ID)) <= preWaitInlineIDBytes {
		return item
	}
	sum := sha256.Sum256([]byte(item.ID))
	return PreWaitPageItem{Category: item.Category, EntityRef: preWaitEntityRef(state, item), InspectRef: preWaitItemRef(state, inventoryDigest, index, 0), IDDigest: "sha256:" + hex.EncodeToString(sum[:]), IDBytes: len([]byte(item.ID)), IDTruncated: true}
}

func preWaitEntityRef(state domain.State, item PreWaitPageItem) string {
	switch item.Category {
	case "readyNode", "zeroReadyCut":
		return nodeRefForNode(state, item.ID)
	case "expiredRole":
		return roleRefForRole(state, item.ID)
	case "pendingEffect":
		return storedObjectRef(state, "effect", item.ID)
	case "activeResource", "orphanedResource":
		return storedObjectRef(state, "resource", item.ID)
	case "openIncident", "overdueIncident", "circuitOpenIncident":
		return incidentRefForIncident(state, item.ID)
	case "submittedAttempt", "activeAttempt", "waitingAttempt", "staleAttempt":
		return "attempt-key:" + opaqueEntityKey(state, "attempt", item.ID)
	default:
		return ""
	}
}

func (s *Service) preWaitPage(ctx context.Context, state domain.State, expectedHead, expectedDigest string, offset int) (PreWaitPage, error) {
	if state.HeadHash != expectedHead {
		return PreWaitPage{}, fmt.Errorf("stale pre-wait page reference: journal head changed")
	}
	inventory, err := s.collectPreWait(ctx, state)
	if err != nil {
		return PreWaitPage{}, err
	}
	items := preWaitInventoryItems(inventory)
	digest := preWaitInventoryDigest(state, items)
	if digest != expectedDigest {
		return PreWaitPage{}, fmt.Errorf("stale pre-wait page reference: liveness inventory changed")
	}
	result := PreWaitPage{JournalHead: state.HeadHash, InventoryDigest: "sha256:" + digest, Offset: offset, Total: len(items), Items: []PreWaitPageItem{}}
	if offset < 0 || offset >= len(items) {
		return result, nil
	}
	end := offset + preWaitPageLimit
	if end > len(items) {
		end = len(items)
	}
	for index := offset; index < end; index++ {
		result.Items = append(result.Items, boundedPreWaitItem(state, digest, index, items[index]))
	}
	for {
		next := offset + len(result.Items)
		if next < len(items) {
			result.NextRef = preWaitPageRef(state, digest, next)
		} else {
			result.NextRef = ""
		}
		raw, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return PreWaitPage{}, marshalErr
		}
		if len(raw) <= preWaitMaxBytes {
			break
		}
		if len(result.Items) <= 1 {
			return PreWaitPage{}, fmt.Errorf("pre-wait page metadata exceeds %d-byte response limit", preWaitMaxBytes)
		}
		result.Items = result.Items[:len(result.Items)-1]
	}
	return result, nil
}

func (s *Service) preWaitItemPage(ctx context.Context, state domain.State, expectedHead, expectedDigest string, index, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale pre-wait item reference: journal head changed")
	}
	inventory, err := s.collectPreWait(ctx, state)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	items := preWaitInventoryItems(inventory)
	if preWaitInventoryDigest(state, items) != expectedDigest {
		return BoundedDetailChunk{}, fmt.Errorf("stale pre-wait item reference: liveness inventory changed")
	}
	if index < 0 || index >= len(items) {
		return BoundedDetailChunk{}, fmt.Errorf("invalid pre-wait item index")
	}
	raw := []byte(items[index].ID)
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	return boundedDetailChunk(raw, digest, offset, func(next int) string {
		return preWaitItemRef(state, expectedDigest, index, next)
	})
}

func dependencyCutDigest(cut []string) string {
	digest := sha256.Sum256([]byte(strings.Join(cut, "\x00")))
	return hex.EncodeToString(digest[:])
}

func dependencyCutItemRef(digest string, index, offset int) string {
	return fmt.Sprintf("dependency-cut-item:%s:%d:%d", digest, index, offset)
}

func dependencyCutByDigest(ctx context.Context, state domain.State, digest string) ([]string, bool, error) {
	for _, incident := range state.Incidents {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if len(incident.DependencyCut) > 0 && dependencyCutDigest(incident.DependencyCut) == digest {
			return append([]string(nil), incident.DependencyCut...), true, nil
		}
	}
	index := domain.NewDependencyCutIndex(state)
	nodes := map[string]bool{}
	for _, effect := range state.Effects {
		nodes[effect.NodeID] = true
	}
	for _, resource := range state.Resources {
		nodes[resource.NodeID] = true
	}
	for nodeID := range nodes {
		cut, err := index.CutContext(ctx, nodeID)
		if err != nil {
			return nil, false, err
		}
		if err == nil && len(cut) > 0 && dependencyCutDigest(cut) == digest {
			return cut, true, nil
		}
	}
	return nil, false, ctx.Err()
}

func dependencyCutPage(state domain.State, digest string, offset int) (DependencyCutPage, error) {
	return dependencyCutPageContext(context.Background(), state, digest, offset)
}

func dependencyCutPageContext(ctx context.Context, state domain.State, digest string, offset int) (DependencyCutPage, error) {
	if len(digest) != 64 {
		return DependencyCutPage{}, fmt.Errorf("invalid dependency cut digest")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return DependencyCutPage{}, fmt.Errorf("invalid dependency cut digest")
	}
	cut, ok, err := dependencyCutByDigest(ctx, state, digest)
	if err != nil {
		return DependencyCutPage{}, err
	}
	if !ok {
		return DependencyCutPage{}, fmt.Errorf("unknown dependency cut reference")
	}
	result := DependencyCutPage{Offset: offset, Total: len(cut), Digest: "sha256:" + digest, Items: []string{}}
	if offset < 0 || offset >= len(cut) {
		return result, nil
	}
	end := offset + preWaitPageLimit
	if end > len(cut) {
		end = len(cut)
	}
	for index := offset; index < end; index++ {
		item := cut[index]
		if len([]byte(item)) > preWaitInlineIDBytes {
			sum := sha256.Sum256([]byte(item))
			result.Items = append(result.Items, "")
			result.OversizedItems = append(result.OversizedItems, BoundedListItemRef{Index: index, InspectRef: dependencyCutItemRef(digest, index, 0), Digest: "sha256:" + hex.EncodeToString(sum[:]), Bytes: len([]byte(item))})
			continue
		}
		result.Items = append(result.Items, item)
	}
	for {
		next := offset + len(result.Items)
		if next < len(cut) {
			result.NextRef = fmt.Sprintf("dependency-cut:%s:%d", digest, next)
		} else {
			result.NextRef = ""
		}
		raw, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return DependencyCutPage{}, marshalErr
		}
		if len(raw) <= preWaitMaxBytes {
			break
		}
		if len(result.Items) <= 1 {
			return DependencyCutPage{}, fmt.Errorf("dependency cut page metadata exceeds %d-byte response limit", preWaitMaxBytes)
		}
		lastIndex := offset + len(result.Items) - 1
		result.Items = result.Items[:len(result.Items)-1]
		for len(result.OversizedItems) > 0 && result.OversizedItems[len(result.OversizedItems)-1].Index >= lastIndex {
			result.OversizedItems = result.OversizedItems[:len(result.OversizedItems)-1]
		}
	}
	return result, nil
}

func dependencyCutItemPage(ctx context.Context, state domain.State, digest string, index, offset int) (BoundedDetailChunk, error) {
	cut, ok, err := dependencyCutByDigest(ctx, state, digest)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	if !ok || index < 0 || index >= len(cut) {
		return BoundedDetailChunk{}, fmt.Errorf("unknown dependency cut item reference")
	}
	raw := []byte(cut[index])
	sum := sha256.Sum256(raw)
	itemDigest := "sha256:" + hex.EncodeToString(sum[:])
	return boundedDetailChunk(raw, itemDigest, offset, func(next int) string {
		return dependencyCutItemRef(digest, index, next)
	})
}

func operationsActionsDigest(actions []AllowedAction) string {
	hash := sha256.New()
	for _, action := range actions {
		_, _ = hash.Write([]byte(action.ID))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(action.Kind))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(action.TargetRef))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func operationsActionsRef(state domain.State, roleID, digest string, offset int) string {
	return fmt.Sprintf("operations-actions:%s:%s:%s:%d", opaqueEntityKey(state, "role", roleID), state.HeadHash, digest, offset)
}

func operationsActionDetailRef(state domain.State, roleID, inventoryDigest, actionID string, offset int) string {
	return fmt.Sprintf("operations-action-detail:%s:%s:%s:%s:%d", opaqueEntityKey(state, "role", roleID), state.HeadHash, inventoryDigest, actionID, offset)
}

func remediationInventoryDigest(remediations []Remediation) string {
	hash := sha256.New()
	for _, remediation := range remediations {
		raw, _ := json.Marshal(remediation)
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(raw)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func remediationsRef(state domain.State, digest string, offset int) string {
	return fmt.Sprintf("remediations:%s:%s:%d", state.HeadHash, digest, offset)
}

func remediationDetailRef(state domain.State, inventoryDigest, remediationID string, offset int) string {
	return fmt.Sprintf("remediation-detail:%s:%s:%s:%d", state.HeadHash, inventoryDigest, strings.TrimPrefix(remediationID, "rem_"), offset)
}

func boundedRemediation(state domain.State, inventoryDigest string, remediation Remediation) Remediation {
	raw, _ := json.Marshal(remediation)
	if len(raw) <= operationsInlineActionBytes {
		return remediation
	}
	sum := sha256.Sum256(raw)
	return Remediation{ID: remediation.ID, Code: remediation.Code, DetailRef: remediationDetailRef(state, inventoryDigest, remediation.ID, 0), DetailDigest: "sha256:" + hex.EncodeToString(sum[:]), DetailBytes: len(raw), DetailsTruncated: true}
}

func (s *Service) currentRemediations(ctx context.Context, state domain.State) ([]Remediation, error) {
	inventory, err := s.collectPreWait(ctx, state)
	if err != nil {
		return nil, err
	}
	return s.buildRemediationsContext(ctx, state, inventory)
}

func (s *Service) remediationPage(ctx context.Context, state domain.State, expectedHead, expectedDigest string, offset int) (RemediationPage, error) {
	if state.HeadHash != expectedHead {
		return RemediationPage{}, fmt.Errorf("stale remediation reference: journal head changed")
	}
	remediations, err := s.currentRemediations(ctx, state)
	if err != nil {
		return RemediationPage{}, err
	}
	digest := remediationInventoryDigest(remediations)
	if digest != expectedDigest {
		return RemediationPage{}, fmt.Errorf("stale remediation reference: inventory changed")
	}
	page := RemediationPage{JournalHead: state.HeadHash, InventoryDigest: "sha256:" + digest, Offset: offset, Total: len(remediations), Remediations: []Remediation{}}
	if offset < 0 || offset >= len(remediations) {
		return page, nil
	}
	end := offset + operationsPageLimit
	if end > len(remediations) {
		end = len(remediations)
	}
	for index := offset; index < end; index++ {
		page.Remediations = append(page.Remediations, boundedRemediation(state, digest, remediations[index]))
	}
	for {
		next := offset + len(page.Remediations)
		if next < len(remediations) {
			page.NextRef = remediationsRef(state, digest, next)
		} else {
			page.NextRef = ""
		}
		raw, marshalErr := json.Marshal(page)
		if marshalErr != nil {
			return RemediationPage{}, marshalErr
		}
		if len(raw) <= operationsMaxBytes {
			return page, nil
		}
		if len(page.Remediations) <= 1 {
			return RemediationPage{}, fmt.Errorf("remediation metadata exceeds %d-byte response limit", operationsMaxBytes)
		}
		page.Remediations = page.Remediations[:len(page.Remediations)-1]
	}
}

func (s *Service) remediationDetailPage(ctx context.Context, state domain.State, expectedHead, expectedDigest, remediationKey string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale remediation detail reference: journal head changed")
	}
	remediations, err := s.currentRemediations(ctx, state)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	if remediationInventoryDigest(remediations) != expectedDigest {
		return BoundedDetailChunk{}, fmt.Errorf("stale remediation detail reference: inventory changed")
	}
	for _, remediation := range remediations {
		if strings.TrimPrefix(remediation.ID, "rem_") != remediationKey {
			continue
		}
		raw, err := json.Marshal(remediation)
		if err != nil {
			return BoundedDetailChunk{}, err
		}
		sum := sha256.Sum256(raw)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		return boundedDetailChunk(raw, digest, offset, func(next int) string { return remediationDetailRef(state, expectedDigest, remediation.ID, next) })
	}
	return BoundedDetailChunk{}, fmt.Errorf("unknown remediation detail reference")
}

func marshalAllowedActionDetail(action AllowedAction) ([]byte, string, error) {
	raw, err := json.Marshal(allowedActionDetail{NodeID: action.NodeID, AttemptID: action.AttemptID, TargetRef: action.TargetRef, InputSchema: action.InputSchema})
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	return raw, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func boundedAllowedAction(state domain.State, roleID, inventoryDigest string, action AllowedAction) (AllowedAction, error) {
	raw, err := json.Marshal(action)
	if err != nil {
		return AllowedAction{}, err
	}
	if len(raw) <= operationsInlineActionBytes {
		return action, nil
	}
	detailRaw, detailDigest, err := marshalAllowedActionDetail(action)
	if err != nil {
		return AllowedAction{}, err
	}
	return AllowedAction{ID: action.ID, Kind: action.Kind, Ref: action.Ref, NodeRef: nodeRefForNode(state, action.NodeID), DetailRef: operationsActionDetailRef(state, roleID, inventoryDigest, action.ID, 0), DetailDigest: detailDigest, DetailBytes: len(detailRaw), DetailsTruncated: true}, nil
}

func boundedDetailChunk(raw []byte, digest string, offset int, nextRef func(int) string) (BoundedDetailChunk, error) {
	if offset < 0 || offset > len(raw) {
		return BoundedDetailChunk{}, fmt.Errorf("invalid detail offset")
	}
	end := offset + boundedDetailChunkBytes
	if end > len(raw) {
		end = len(raw)
	}
	result := BoundedDetailChunk{Digest: digest, Offset: offset, TotalBytes: len(raw), Encoding: "base64", Chunk: base64.RawStdEncoding.EncodeToString(raw[offset:end])}
	if end < len(raw) {
		result.NextRef = nextRef(end)
	}
	return result, nil
}

func enforceOperationsIndexBound(index OperationsIndex, actionsRef, remediationsRef string) (OperationsIndex, error) {
	for {
		raw, err := json.Marshal(index)
		if err != nil {
			return OperationsIndex{}, err
		}
		if len(raw) <= operationsMaxBytes {
			return index, nil
		}
		if len(index.ProjectAllowedActions) > 0 {
			index.ProjectAllowedActions = index.ProjectAllowedActions[:len(index.ProjectAllowedActions)-1]
			index.ActionsTruncated = index.ActionCount > len(index.ProjectAllowedActions)
			index.ActionsInspectRef = actionsRef
			index.Truncated = true
			continue
		}
		if len(index.Remediations) > 0 {
			index.Remediations = index.Remediations[:len(index.Remediations)-1]
			index.RemediationsTruncated = index.RemediationCount > len(index.Remediations)
			index.RemediationsInspectRef = remediationsRef
			index.Truncated = true
			continue
		}
		return OperationsIndex{}, fmt.Errorf("operations index metadata exceeds %d-byte response limit", operationsMaxBytes)
	}
}

func compactOperationsIdentity(state domain.State, roleID string, index *OperationsIndex) error {
	raw, err := roleInspectionBytes(state, roleID)
	if err != nil {
		return err
	}
	if len(raw) <= preWaitInlineIDBytes {
		return nil
	}
	sum := sha256.Sum256(raw)
	index.RoleID = ""
	index.RoleRef = roleRefForRole(state, roleID)
	index.IdentityDetailRef = roleDetailRef(state, roleID, 0)
	index.IdentityDetailDigest = "sha256:" + hex.EncodeToString(sum[:])
	index.IdentityDetailBytes = len(raw)
	index.IdentityTruncated = true
	index.Authorization.RoleID = ""
	index.Authorization.RoleRef = roleRefForRole(state, roleID)
	if index.Authorization.SessionID != "" {
		index.Authorization.SessionRef = "session-key:" + opaqueEntityKey(state, "session:"+roleID, index.Authorization.SessionID)
		index.Authorization.SessionID = ""
	}
	index.Authorization.Capabilities = nil
	return nil
}

func (s *Service) operationsIndex(ctx context.Context, state domain.State, roleID string) (OperationsIndex, error) {
	if state.Graph == nil {
		return OperationsIndex{}, fmt.Errorf("graph is unavailable")
	}
	knownRole := false
	for _, role := range state.Graph.Spec.Roles {
		knownRole = knownRole || role.ID == roleID
	}
	if !knownRole {
		return OperationsIndex{}, fmt.Errorf("unknown role %s", roleID)
	}
	// An operations index promises a complete projection of currently signable
	// actions. Treat a missing, damaged, or unreadable signing secret as control-
	// plane damage rather than silently reporting an empty action set.
	if _, err := s.actionSecret(); err != nil {
		return OperationsIndex{}, err
	}
	now := s.Now().UTC()
	actions, err := s.projectAllowedActionsContext(ctx, state, roleID, 0, now)
	if err != nil {
		return OperationsIndex{}, err
	}
	actionCount := len(actions)
	actionDigest := operationsActionsDigest(actions)
	if len(actions) > operationsPreviewLimit {
		actions = actions[:operationsPreviewLimit]
	}
	for index := range actions {
		actions[index], err = boundedAllowedAction(state, roleID, actionDigest, actions[index])
		if err != nil {
			return OperationsIndex{}, err
		}
	}
	audit, err := s.preWaitFromStateContext(ctx, state)
	if err != nil {
		return OperationsIndex{}, err
	}
	remediations := append([]Remediation(nil), audit.Remediations...)
	if len(remediations) > operationsPreviewLimit {
		remediations = remediations[:operationsPreviewLimit]
	}
	index := OperationsIndex{RoleID: roleID, GraphRevision: state.GraphRevision, JournalHead: state.HeadHash, Authorization: authorizationForRole(state, roleID, now), ProjectAllowedActions: actions, ActionCount: actionCount, ActionsTruncated: actionCount > len(actions), Remediations: remediations, RemediationCount: audit.RemediationCount, RemediationsTruncated: audit.RemediationCount > len(remediations)}
	if err := compactOperationsIdentity(state, roleID, &index); err != nil {
		return OperationsIndex{}, err
	}
	if index.ActionsTruncated {
		index.ActionsInspectRef = operationsActionsRef(state, roleID, actionDigest, 0)
	}
	if index.RemediationsTruncated {
		index.RemediationsInspectRef = audit.RemediationsInspectRef
	}
	index.Truncated = index.ActionsTruncated || index.RemediationsTruncated
	return enforceOperationsIndexBound(index, operationsActionsRef(state, roleID, actionDigest, 0), audit.RemediationsInspectRef)
}

func (s *Service) operationsActionPage(ctx context.Context, state domain.State, roleID, expectedHead, expectedDigest string, offset int) (OperationsActionPage, error) {
	if state.HeadHash != expectedHead {
		return OperationsActionPage{}, fmt.Errorf("stale operations action reference: journal head changed")
	}
	actions, err := s.projectAllowedActionsContext(ctx, state, roleID, 0, s.Now().UTC())
	if err != nil {
		return OperationsActionPage{}, err
	}
	digest := operationsActionsDigest(actions)
	if digest != expectedDigest {
		return OperationsActionPage{}, fmt.Errorf("stale operations action reference: action inventory changed")
	}
	page := OperationsActionPage{RoleID: roleID, GraphRevision: state.GraphRevision, JournalHead: state.HeadHash, Offset: offset, Total: len(actions), Actions: []AllowedAction{}}
	if len([]byte(roleID)) > preWaitInlineIDBytes {
		page.RoleID = ""
		page.RoleRef = roleRefForRole(state, roleID)
	}
	if offset < 0 || offset >= len(actions) {
		return page, nil
	}
	end := offset + operationsPageLimit
	if end > len(actions) {
		end = len(actions)
	}
	page.Actions = append(page.Actions, actions[offset:end]...)
	for index := range page.Actions {
		page.Actions[index], err = boundedAllowedAction(state, roleID, digest, page.Actions[index])
		if err != nil {
			return OperationsActionPage{}, err
		}
	}
	for {
		if end < len(actions) {
			page.NextRef = operationsActionsRef(state, roleID, digest, end)
		}
		raw, marshalErr := json.Marshal(page)
		if marshalErr != nil {
			return OperationsActionPage{}, marshalErr
		}
		if len(raw) <= operationsMaxBytes {
			return page, nil
		}
		if len(page.Actions) <= 1 {
			return OperationsActionPage{}, fmt.Errorf("allowed action exceeds %d-byte response limit", operationsMaxBytes)
		}
		page.Actions = page.Actions[:len(page.Actions)-1]
		end = offset + len(page.Actions)
	}
}

func (s *Service) operationsActionDetailPage(ctx context.Context, state domain.State, roleID, expectedHead, expectedInventoryDigest, actionID string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale operations action detail reference: journal head changed")
	}
	actions, err := s.projectAllowedActionsContext(ctx, state, roleID, 0, s.Now().UTC())
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	if operationsActionsDigest(actions) != expectedInventoryDigest {
		return BoundedDetailChunk{}, fmt.Errorf("stale operations action detail reference: action inventory changed")
	}
	for _, action := range actions {
		if err := ctx.Err(); err != nil {
			return BoundedDetailChunk{}, err
		}
		if action.ID != actionID {
			continue
		}
		raw, digest, err := marshalAllowedActionDetail(action)
		if err != nil {
			return BoundedDetailChunk{}, err
		}
		return boundedDetailChunk(raw, digest, offset, func(next int) string {
			return operationsActionDetailRef(state, roleID, expectedInventoryDigest, actionID, next)
		})
	}
	return BoundedDetailChunk{}, fmt.Errorf("unknown operations action detail reference")
}

func opaqueEntityKey(state domain.State, kind, id string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{state.ProjectID, kind, id}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func operationsRefForRole(state domain.State, roleID string) string {
	return "operations:" + opaqueEntityKey(state, "role", roleID)
}

func (s *Service) ResolveEntityRef(kind, ref string) (string, error) {
	state, _, err := s.load()
	if err != nil {
		return "", err
	}
	prefix, key, ok := strings.Cut(ref, ":")
	if !ok || key == "" {
		return "", fmt.Errorf("invalid %s reference", kind)
	}
	switch kind {
	case "role":
		if prefix != "role-key" {
			return "", fmt.Errorf("invalid Role reference")
		}
		if id, exists := roleIDForOpaqueKey(state, key); exists {
			return id, nil
		}
	case "node":
		if prefix != "node-key" {
			return "", fmt.Errorf("invalid Node reference")
		}
		if id, exists := nodeIDForOpaqueKey(state, key); exists {
			return id, nil
		}
	case "attempt":
		if prefix != "attempt-key" {
			return "", fmt.Errorf("invalid Attempt reference")
		}
		if id, exists := attemptIDForOpaqueKey(state, key); exists {
			return id, nil
		}
	case "effect":
		if prefix != "effect-key" {
			return "", fmt.Errorf("invalid Effect reference")
		}
		if id, exists := storedObjectIDForOpaqueKey(state, "effect", key); exists {
			return id, nil
		}
	case "resource":
		if prefix != "resource-key" {
			return "", fmt.Errorf("invalid Resource reference")
		}
		if id, exists := storedObjectIDForOpaqueKey(state, "resource", key); exists {
			return id, nil
		}
	case "incident":
		if prefix != "incident-key" {
			return "", fmt.Errorf("invalid Incident reference")
		}
		if id, exists := incidentIDForOpaqueKey(state, key); exists {
			return id, nil
		}
	default:
		return "", fmt.Errorf("unsupported entity reference kind %s", kind)
	}
	return "", fmt.Errorf("unknown %s reference", kind)
}

func (s *Service) EntityRef(kind, id string) (string, error) {
	state, _, err := s.load()
	if err != nil {
		return "", err
	}
	switch kind {
	case "role":
		if _, exists := roleIDForOpaqueKey(state, opaqueEntityKey(state, "role", id)); exists {
			return roleRefForRole(state, id), nil
		}
	case "node":
		if _, exists := nodeIDForOpaqueKey(state, opaqueEntityKey(state, "node", id)); exists {
			return nodeRefForNode(state, id), nil
		}
	case "attempt":
		if _, exists := attemptIDForOpaqueKey(state, opaqueEntityKey(state, "attempt", id)); exists {
			return "attempt-key:" + opaqueEntityKey(state, "attempt", id), nil
		}
	case "effect", "resource":
		if _, exists := storedObject(state, kind, id); exists {
			return storedObjectRef(state, kind, id), nil
		}
	case "incident":
		if _, exists := state.Incidents[id]; exists {
			return incidentRefForIncident(state, id), nil
		}
	default:
		return "", fmt.Errorf("unsupported entity reference kind %s", kind)
	}
	return "", fmt.Errorf("unknown %s %s", kind, id)
}

func roleRefForRole(state domain.State, roleID string) string {
	return "role-key:" + opaqueEntityKey(state, "role", roleID)
}

func nodeRefForNode(state domain.State, nodeID string) string {
	return "node-key:" + opaqueEntityKey(state, "node", nodeID)
}

func nodeDetailRef(state domain.State, nodeID string, offset int) string {
	return fmt.Sprintf("node-detail:%s:%s:%d", state.HeadHash, opaqueEntityKey(state, "node", nodeID), offset)
}

func roleDetailRef(state domain.State, roleID string, offset int) string {
	return fmt.Sprintf("role-detail:%s:%s:%d", state.HeadHash, opaqueEntityKey(state, "role", roleID), offset)
}

func attemptDetailRef(state domain.State, attemptID string, offset int) string {
	return fmt.Sprintf("attempt-detail:%s:%s:%d", state.HeadHash, opaqueEntityKey(state, "attempt", attemptID), offset)
}

func attemptIDDetailRef(state domain.State, attemptID, digest string, offset int) string {
	return fmt.Sprintf("attempt-id-detail:%s:%s:%d", opaqueEntityKey(state, "attempt", attemptID), digest, offset)
}

func outcomeIDDetailRef(state domain.State, nodeID, outcomeKey string, offset int) string {
	return fmt.Sprintf("outcome-id-detail:%s:%s:%s:%d", state.HeadHash, opaqueEntityKey(state, "node", nodeID), outcomeKey, offset)
}

func roleInspectionBytes(state domain.State, roleID string) ([]byte, error) {
	if state.Graph == nil {
		return nil, fmt.Errorf("graph is unavailable")
	}
	for _, role := range state.Graph.Spec.Roles {
		if role.ID == roleID {
			return json.Marshal(map[string]any{"role": role, "lease": state.Leases[roleID]})
		}
	}
	return nil, fmt.Errorf("unknown role %s", roleID)
}

func boundedRoleInspection(state domain.State, roleID string) (any, error) {
	raw, err := roleInspectionBytes(state, roleID)
	if err != nil {
		return nil, err
	}
	if len(raw) <= operationsMaxBytes {
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
		return result, nil
	}
	sum := sha256.Sum256(raw)
	idSum := sha256.Sum256([]byte(roleID))
	return map[string]any{"roleRef": roleRefForRole(state, roleID), "roleIdDigest": "sha256:" + hex.EncodeToString(idSum[:]), "roleIdBytes": len([]byte(roleID)), "detailDigest": "sha256:" + hex.EncodeToString(sum[:]), "detailBytes": len(raw), "detailRef": roleDetailRef(state, roleID, 0), "truncated": true}, nil
}

func roleDetailPage(state domain.State, expectedHead, roleKey string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale role detail reference: journal head changed")
	}
	roleID, exists := roleIDForOpaqueKey(state, roleKey)
	if !exists {
		return BoundedDetailChunk{}, fmt.Errorf("unknown role detail reference")
	}
	raw, err := roleInspectionBytes(state, roleID)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	return boundedDetailChunk(raw, digest, offset, func(next int) string { return roleDetailRef(state, roleID, next) })
}

func attemptInspectionBytes(state domain.State, attemptID string) ([]byte, error) {
	attempt, found := state.Attempts[attemptID]
	if !found {
		return nil, fmt.Errorf("unknown attempt %s", attemptID)
	}
	return json.Marshal(attempt)
}

func boundedAttemptInspection(state domain.State, attemptID string) (any, error) {
	raw, err := attemptInspectionBytes(state, attemptID)
	if err != nil {
		return nil, err
	}
	if len(raw) <= operationsMaxBytes {
		var result domain.Attempt
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
		return result, nil
	}
	sum := sha256.Sum256(raw)
	idSum := sha256.Sum256([]byte(attemptID))
	return map[string]any{"attemptIdDigest": "sha256:" + hex.EncodeToString(idSum[:]), "attemptIdBytes": len([]byte(attemptID)), "detailDigest": "sha256:" + hex.EncodeToString(sum[:]), "detailBytes": len(raw), "detailRef": attemptDetailRef(state, attemptID, 0), "truncated": true}, nil
}

func attemptIDForOpaqueKey(state domain.State, key string) (string, bool) {
	for attemptID := range state.Attempts {
		if opaqueEntityKey(state, "attempt", attemptID) == key {
			return attemptID, true
		}
	}
	return "", false
}

func attemptDetailPage(state domain.State, expectedHead, attemptKey string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale attempt detail reference: journal head changed")
	}
	attemptID, exists := attemptIDForOpaqueKey(state, attemptKey)
	if !exists {
		return BoundedDetailChunk{}, fmt.Errorf("unknown attempt detail reference")
	}
	raw, err := attemptInspectionBytes(state, attemptID)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	return boundedDetailChunk(raw, digest, offset, func(next int) string { return attemptDetailRef(state, attemptID, next) })
}

func attemptIDDetailPage(state domain.State, attemptKey, expectedDigest string, offset int) (BoundedDetailChunk, error) {
	attemptID, exists := attemptIDForOpaqueKey(state, attemptKey)
	if !exists {
		return BoundedDetailChunk{}, fmt.Errorf("unknown attempt identity detail reference")
	}
	raw := []byte(attemptID)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if digest != expectedDigest {
		return BoundedDetailChunk{}, fmt.Errorf("stale attempt identity detail reference")
	}
	return boundedDetailChunk(raw, "sha256:"+digest, offset, func(next int) string {
		return attemptIDDetailRef(state, attemptID, digest, next)
	})
}

func outcomeIDDetailPage(state domain.State, expectedHead, nodeKey, outcomeKey string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale outcome identity detail reference: journal head changed")
	}
	nodeID, exists := nodeIDForOpaqueKey(state, nodeKey)
	if !exists {
		return BoundedDetailChunk{}, fmt.Errorf("unknown outcome identity detail reference")
	}
	node, _ := state.NodeDefinition(nodeID)
	for _, outcome := range node.Outcomes {
		if strings.TrimPrefix(outcomeBindingRef(node.ID, outcome.ID), "outcome:") != outcomeKey {
			continue
		}
		raw := []byte(outcome.ID)
		sum := sha256.Sum256(raw)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		return boundedDetailChunk(raw, digest, offset, func(next int) string {
			return outcomeIDDetailRef(state, node.ID, outcomeKey, next)
		})
	}
	return BoundedDetailChunk{}, fmt.Errorf("unknown outcome identity detail reference")
}

func nodeInspectionBytes(state domain.State, nodeID string) ([]byte, error) {
	node, found := state.NodeDefinition(nodeID)
	if !found {
		return nil, fmt.Errorf("unknown node %s", nodeID)
	}
	return json.Marshal(map[string]any{"node": node, "runtime": state.Nodes[nodeID], "attemptIds": state.NodeAttempts[nodeID]})
}

func boundedNodeInspection(state domain.State, nodeID string) (any, error) {
	raw, err := nodeInspectionBytes(state, nodeID)
	if err != nil {
		return nil, err
	}
	if len(raw) <= operationsMaxBytes {
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
		return result, nil
	}
	sum := sha256.Sum256(raw)
	idSum := sha256.Sum256([]byte(nodeID))
	return map[string]any{"nodeRef": nodeRefForNode(state, nodeID), "nodeIdDigest": "sha256:" + hex.EncodeToString(idSum[:]), "nodeIdBytes": len([]byte(nodeID)), "detailDigest": "sha256:" + hex.EncodeToString(sum[:]), "detailBytes": len(raw), "detailRef": nodeDetailRef(state, nodeID, 0), "truncated": true}, nil
}

func nodeDetailPage(state domain.State, expectedHead, nodeKey string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale node detail reference: journal head changed")
	}
	nodeID, exists := nodeIDForOpaqueKey(state, nodeKey)
	if !exists {
		return BoundedDetailChunk{}, fmt.Errorf("unknown node detail reference")
	}
	raw, err := nodeInspectionBytes(state, nodeID)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	return boundedDetailChunk(raw, digest, offset, func(next int) string { return nodeDetailRef(state, nodeID, next) })
}

var boundedStoredObjectKinds = map[string]bool{
	"checkpoint": true, "decision": true, "action": true, "effect": true,
	"resource": true, "evidence-package": true, "reuse-decision": true,
}

func storedObject(state domain.State, kind, id string) (any, bool) {
	switch kind {
	case "checkpoint":
		value, ok := state.Checkpoints[id]
		return value, ok
	case "decision":
		value, ok := state.Decisions[id]
		return value, ok
	case "action":
		value, ok := state.Actions[id]
		return value, ok
	case "effect":
		value, ok := state.Effects[id]
		return value, ok
	case "resource":
		value, ok := state.Resources[id]
		return value, ok
	case "evidence-package":
		value, ok := state.EvidencePackages[id]
		return value, ok
	case "reuse-decision":
		value, ok := state.ReuseDecisions[id]
		return value, ok
	default:
		return nil, false
	}
}

func storedObjectIDs(state domain.State, kind string) []string {
	ids := []string{}
	switch kind {
	case "checkpoint":
		for id := range state.Checkpoints {
			ids = append(ids, id)
		}
	case "decision":
		for id := range state.Decisions {
			ids = append(ids, id)
		}
	case "action":
		for id := range state.Actions {
			ids = append(ids, id)
		}
	case "effect":
		for id := range state.Effects {
			ids = append(ids, id)
		}
	case "resource":
		for id := range state.Resources {
			ids = append(ids, id)
		}
	case "evidence-package":
		for id := range state.EvidencePackages {
			ids = append(ids, id)
		}
	case "reuse-decision":
		for id := range state.ReuseDecisions {
			ids = append(ids, id)
		}
	}
	return ids
}

func storedObjectIDForOpaqueKey(state domain.State, kind, key string) (string, bool) {
	for _, id := range storedObjectIDs(state, kind) {
		if opaqueEntityKey(state, kind, id) == key {
			return id, true
		}
	}
	return "", false
}

func storedObjectRef(state domain.State, kind, id string) string {
	return kind + "-key:" + opaqueEntityKey(state, kind, id)
}

func storedObjectDetailRef(state domain.State, kind, id string, offset int) string {
	return fmt.Sprintf("object-detail:%s:%s:%s:%d", kind, state.HeadHash, opaqueEntityKey(state, kind, id), offset)
}

func storedObjectBytes(state domain.State, kind, id string) ([]byte, error) {
	value, found := storedObject(state, kind, id)
	if !found {
		return nil, fmt.Errorf("unknown %s %s", kind, id)
	}
	return json.Marshal(value)
}

func boundedStoredObjectInspection(state domain.State, kind, id string) (any, error) {
	raw, err := storedObjectBytes(state, kind, id)
	if err != nil {
		return nil, err
	}
	if len(raw) <= operationsMaxBytes {
		value, _ := storedObject(state, kind, id)
		return value, nil
	}
	sum := sha256.Sum256(raw)
	idSum := sha256.Sum256([]byte(id))
	return map[string]any{"kind": kind, "objectRef": storedObjectRef(state, kind, id), "idDigest": "sha256:" + hex.EncodeToString(idSum[:]), "idBytes": len([]byte(id)), "detailDigest": "sha256:" + hex.EncodeToString(sum[:]), "detailBytes": len(raw), "detailRef": storedObjectDetailRef(state, kind, id, 0), "truncated": true}, nil
}

func storedObjectDetailPage(state domain.State, kind, expectedHead, objectKey string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale %s detail reference: journal head changed", kind)
	}
	id, exists := storedObjectIDForOpaqueKey(state, kind, objectKey)
	if !exists {
		return BoundedDetailChunk{}, fmt.Errorf("unknown %s detail reference", kind)
	}
	raw, err := storedObjectBytes(state, kind, id)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	return boundedDetailChunk(raw, digest, offset, func(next int) string { return storedObjectDetailRef(state, kind, id, next) })
}

func objectReferenceDetailRef(state domain.State, kind, id, digest string, offset int) string {
	return fmt.Sprintf("object-ref-detail:%s:%s:%s:%d", kind, opaqueEntityKey(state, kind, id), digest, offset)
}

func objectReferenceIDForKey(state domain.State, kind, key string) (string, bool) {
	if kind == "incident" {
		return incidentIDForOpaqueKey(state, key)
	}
	if boundedStoredObjectKinds[kind] {
		return storedObjectIDForOpaqueKey(state, kind, key)
	}
	return "", false
}

func objectReferenceDetailPage(state domain.State, kind, objectKey, expectedDigest string, offset int) (BoundedDetailChunk, error) {
	id, exists := objectReferenceIDForKey(state, kind, objectKey)
	if !exists {
		return BoundedDetailChunk{}, fmt.Errorf("unknown object reference detail")
	}
	raw := []byte(kind + ":" + id)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if digest != expectedDigest {
		return BoundedDetailChunk{}, fmt.Errorf("stale object reference detail")
	}
	return boundedDetailChunk(raw, "sha256:"+digest, offset, func(next int) string { return objectReferenceDetailRef(state, kind, id, digest, next) })
}

func frontierDetailRef(state domain.State, digest string, offset int) string {
	return fmt.Sprintf("frontier-detail:%s:%s:%d", state.HeadHash, digest, offset)
}

func boundedFrontierInspection(ctx context.Context, state domain.State) (any, error) {
	frontier, err := domain.ComputeFrontierContext(ctx, state)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(frontier)
	if err != nil {
		return nil, err
	}
	if len(raw) <= operationsMaxBytes {
		return frontier, nil
	}
	sum := sha256.Sum256(raw)
	return map[string]any{
		"graphRevision": frontier.GraphRevision,
		"readyCount":    len(frontier.Ready), "blockedCount": len(frontier.Blocked),
		"unreachableCount": len(frontier.Unreachable), "resourceBlockedCount": len(frontier.ResourceBlocked),
		"detailDigest": "sha256:" + hex.EncodeToString(sum[:]), "detailBytes": len(raw),
		"detailRef": frontierDetailRef(state, hex.EncodeToString(sum[:]), 0), "truncated": true,
	}, nil
}

func frontierDetailPage(ctx context.Context, state domain.State, expectedHead, expectedDigest string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale frontier detail reference: journal head changed")
	}
	frontier, err := domain.ComputeFrontierContext(ctx, state)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	raw, err := json.Marshal(frontier)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if digest != expectedDigest {
		return BoundedDetailChunk{}, fmt.Errorf("stale frontier detail reference: inventory changed")
	}
	return boundedDetailChunk(raw, "sha256:"+digest, offset, func(next int) string { return frontierDetailRef(state, digest, next) })
}

func roleIDForOpaqueKey(state domain.State, key string) (string, bool) {
	if state.Graph == nil {
		return "", false
	}
	for _, role := range state.Graph.Spec.Roles {
		if opaqueEntityKey(state, "role", role.ID) == key {
			return role.ID, true
		}
	}
	return "", false
}

func nodeIDForOpaqueKey(state domain.State, key string) (string, bool) {
	if state.Graph == nil {
		return "", false
	}
	for _, node := range state.Graph.Spec.Nodes {
		if opaqueEntityKey(state, "node", node.ID) == key {
			return node.ID, true
		}
	}
	return "", false
}

func (s *Service) PreWait() (PreWaitAudit, error) {
	return s.PreWaitContext(context.Background())
}

func (s *Service) PreWaitContext(ctx context.Context) (PreWaitAudit, error) {
	state, _, err := s.load()
	if err != nil {
		return PreWaitAudit{}, err
	}
	return s.preWaitFromStateContext(ctx, state)
}

func (s *Service) preWaitFromState(state domain.State) (PreWaitAudit, error) {
	return s.preWaitFromStateContext(context.Background(), state)
}

const (
	preWaitPreviewLimit         = 8
	preWaitRemediationLimit     = 16
	preWaitPageLimit            = 64
	preWaitMaxBytes             = 24 * 1024
	preWaitInlineIDBytes        = 2048
	dependencyCutPreview        = 4
	operationsPreviewLimit      = 8
	operationsPageLimit         = 8
	operationsMaxBytes          = 24 * 1024
	operationsInlineActionBytes = 12 * 1024
	boundedDetailChunkBytes     = 12 * 1024
)

type preWaitInventory struct {
	readyNodes, submittedAttempts, activeAttempts, waitingAttempts, staleAttempts []string
	expiredRoles, pendingEffects, activeResources, orphanedResources              []string
	openIncidents, overdueIncidents, circuitIncidents, zeroReadyCut               []string
}

func (s *Service) collectPreWait(ctx context.Context, state domain.State) (preWaitInventory, error) {
	if err := ctx.Err(); err != nil {
		return preWaitInventory{}, err
	}
	frontier, err := domain.ComputeFrontierSummaryContext(ctx, state)
	if err != nil {
		return preWaitInventory{}, err
	}
	inventory := preWaitInventory{readyNodes: append([]string(nil), frontier.Ready...)}
	now := s.Now().UTC()
	for _, attempt := range state.Attempts {
		if err := ctx.Err(); err != nil {
			return preWaitInventory{}, err
		}
		switch attempt.Status {
		case "submitted":
			inventory.submittedAttempts = append(inventory.submittedAttempts, attempt.ID)
		case "running", "leased":
			inventory.activeAttempts = append(inventory.activeAttempts, attempt.ID)
			updated, parseErr := time.Parse(time.RFC3339Nano, attempt.UpdatedAt)
			if parseErr != nil || now.Sub(updated) > 30*time.Minute {
				inventory.staleAttempts = append(inventory.staleAttempts, attempt.ID)
			}
		case "waiting":
			inventory.activeAttempts = append(inventory.activeAttempts, attempt.ID)
			inventory.waitingAttempts = append(inventory.waitingAttempts, attempt.ID)
		}
	}
	for roleID, lease := range state.Leases {
		if err := ctx.Err(); err != nil {
			return preWaitInventory{}, err
		}
		if !lease.Active {
			continue
		}
		expires, parseErr := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
		if parseErr != nil || !now.Before(expires) {
			inventory.expiredRoles = append(inventory.expiredRoles, roleID)
		}
	}
	for actionID, effect := range state.Effects {
		if err := ctx.Err(); err != nil {
			return preWaitInventory{}, err
		}
		switch effect.Status {
		case "prepared", "dispatched", "unknown", "reconciling":
			inventory.pendingEffects = append(inventory.pendingEffects, actionID)
		}
	}
	for resourceID, lease := range state.Resources {
		if err := ctx.Err(); err != nil {
			return preWaitInventory{}, err
		}
		if lease.Status == "active" {
			inventory.activeResources = append(inventory.activeResources, resourceID)
			attempt, ok := state.Attempts[lease.AttemptID]
			if !ok || attempt.Status == "terminal" {
				inventory.orphanedResources = append(inventory.orphanedResources, resourceID)
			}
		}
	}
	for incidentID, incident := range state.Incidents {
		if err := ctx.Err(); err != nil {
			return preWaitInventory{}, err
		}
		if incident.Status != "resolved" {
			inventory.openIncidents = append(inventory.openIncidents, incidentID)
		}
		if incident.Status == "circuit-open" {
			inventory.circuitIncidents = append(inventory.circuitIncidents, incidentID)
		}
		if incident.Status == "open" {
			if deadline, parseErr := time.Parse(time.RFC3339Nano, incident.Deadline); parseErr == nil && !now.Before(deadline) {
				inventory.overdueIncidents = append(inventory.overdueIncidents, incidentID)
			}
		}
	}
	if len(frontier.Ready) == 0 && len(frontier.Blocked) > 0 && len(inventory.activeAttempts) == 0 && len(inventory.pendingEffects) == 0 {
		inventory.zeroReadyCut = append(inventory.zeroReadyCut, frontier.Blocked...)
	}
	for _, values := range [][]string{inventory.readyNodes, inventory.submittedAttempts, inventory.activeAttempts, inventory.waitingAttempts, inventory.staleAttempts, inventory.expiredRoles, inventory.pendingEffects, inventory.activeResources, inventory.orphanedResources, inventory.openIncidents, inventory.overdueIncidents, inventory.circuitIncidents, inventory.zeroReadyCut} {
		sort.Strings(values)
	}
	return inventory, ctx.Err()
}

func (s *Service) preWaitFromStateContext(ctx context.Context, state domain.State) (PreWaitAudit, error) {
	inventory, err := s.collectPreWait(ctx, state)
	if err != nil {
		return PreWaitAudit{}, err
	}
	counts := PreWaitCounts{ReadyNodes: len(inventory.readyNodes), SubmittedAttempts: len(inventory.submittedAttempts), ActiveAttempts: len(inventory.activeAttempts), WaitingAttempts: len(inventory.waitingAttempts), StaleAttempts: len(inventory.staleAttempts), ExpiredRoles: len(inventory.expiredRoles), PendingEffects: len(inventory.pendingEffects), ActiveResources: len(inventory.activeResources), OrphanedResources: len(inventory.orphanedResources), OpenIncidents: len(inventory.openIncidents), OverdueIncidents: len(inventory.overdueIncidents), CircuitIncidents: len(inventory.circuitIncidents), ZeroReadyCut: len(inventory.zeroReadyCut)}
	pageDigest := preWaitInventoryDigest(state, preWaitInventoryItems(inventory))
	pageRef := preWaitPageRef(state, pageDigest, 0)
	audit := PreWaitAudit{SafeToWait: true, Cursor: state.HeadSequence, Counts: counts, ReadyNodes: previewStrings(inventory.readyNodes), SubmittedAttempts: previewStrings(inventory.submittedAttempts), ActiveAttempts: previewStrings(inventory.activeAttempts), WaitingAttempts: previewStrings(inventory.waitingAttempts), StaleAttempts: previewStrings(inventory.staleAttempts), ExpiredRoles: previewStrings(inventory.expiredRoles), PendingEffects: previewStrings(inventory.pendingEffects), ActiveResources: previewStrings(inventory.activeResources), OrphanedResources: previewStrings(inventory.orphanedResources), OpenIncidents: previewStrings(inventory.openIncidents), OverdueIncidents: previewStrings(inventory.overdueIncidents), CircuitIncidents: previewStrings(inventory.circuitIncidents), ZeroReadyCut: previewStrings(inventory.zeroReadyCut), pageRef: pageRef}
	if counts.ReadyNodes > 0 {
		audit.Reasons = append(audit.Reasons, "ready nodes require assignment or an explicit decision")
	}
	if counts.SubmittedAttempts > 0 {
		audit.Reasons = append(audit.Reasons, "submitted attempts require a terminal action")
	}
	if counts.StaleAttempts > 0 {
		audit.Reasons = append(audit.Reasons, "running attempts have no progress checkpoint within the liveness window")
	}
	if counts.ExpiredRoles > 0 {
		audit.Reasons = append(audit.Reasons, "expired role leases require release or takeover")
	}
	if counts.PendingEffects > 0 {
		audit.Reasons = append(audit.Reasons, "pending effects require confirmation, failure classification or reconciliation")
	}
	if counts.OrphanedResources > 0 {
		audit.Reasons = append(audit.Reasons, "active resource leases have no recoverable attempt")
	}
	if counts.OpenIncidents > 0 {
		audit.Reasons = append(audit.Reasons, "open incidents require ownership, progress or circuit-breaker action")
	}
	if counts.OverdueIncidents > 0 {
		audit.Reasons = append(audit.Reasons, "incident deadlines have expired")
	}
	if counts.CircuitIncidents > 0 {
		audit.Reasons = append(audit.Reasons, "open circuit breakers require resolve, quarantine or a new graph revision")
	}
	if counts.ZeroReadyCut > 0 {
		audit.Reasons = append(audit.Reasons, "zero-ready dependency cut has no active recovery work")
	}
	audit.SafeToWait = len(audit.Reasons) == 0
	remediations, err := s.buildRemediationsContext(ctx, state, inventory)
	if err != nil {
		return PreWaitAudit{}, err
	}
	audit.RemediationCount = len(remediations)
	remediationDigest := remediationInventoryDigest(remediations)
	if len(remediations) > 0 {
		audit.RemediationsInspectRef = remediationsRef(state, remediationDigest, 0)
	}
	if len(remediations) > preWaitRemediationLimit {
		remediations = remediations[:preWaitRemediationLimit]
	}
	for _, remediation := range remediations {
		audit.Remediations = append(audit.Remediations, boundedRemediation(state, remediationDigest, remediation))
	}
	audit.RemediationsTruncated = audit.RemediationCount > len(audit.Remediations)
	audit.Truncated = audit.RemediationsTruncated || preWaitCountsTruncated(audit)
	if audit.Truncated {
		audit.InspectRef = pageRef
	}
	audit = enforcePreWaitBound(audit, pageRef)
	return audit, nil
}

func previewStrings(values []string) []string {
	if len(values) > preWaitPreviewLimit {
		values = values[:preWaitPreviewLimit]
	}
	return append([]string(nil), values...)
}

func preWaitCountsTruncated(audit PreWaitAudit) bool {
	counts := []int{audit.Counts.ReadyNodes, audit.Counts.SubmittedAttempts, audit.Counts.ActiveAttempts, audit.Counts.WaitingAttempts, audit.Counts.StaleAttempts, audit.Counts.ExpiredRoles, audit.Counts.PendingEffects, audit.Counts.ActiveResources, audit.Counts.OrphanedResources, audit.Counts.OpenIncidents, audit.Counts.OverdueIncidents, audit.Counts.CircuitIncidents, audit.Counts.ZeroReadyCut}
	for _, count := range counts {
		if count > preWaitPreviewLimit {
			return true
		}
	}
	return false
}

func enforcePreWaitBound(audit PreWaitAudit, inspectRef string) PreWaitAudit {
	raw, _ := json.Marshal(audit)
	if len(raw) <= preWaitMaxBytes {
		return audit
	}
	audit.ReadyNodes, audit.SubmittedAttempts, audit.ActiveAttempts, audit.WaitingAttempts, audit.StaleAttempts = nil, nil, nil, nil, nil
	audit.ExpiredRoles, audit.PendingEffects, audit.ActiveResources, audit.OrphanedResources = nil, nil, nil, nil
	audit.OpenIncidents, audit.OverdueIncidents, audit.CircuitIncidents, audit.ZeroReadyCut = nil, nil, nil, nil
	if len(audit.Remediations) > 4 {
		audit.Remediations = audit.Remediations[:4]
	}
	audit.Truncated, audit.RemediationsTruncated, audit.InspectRef = true, audit.RemediationCount > len(audit.Remediations), inspectRef
	raw, _ = json.Marshal(audit)
	if len(raw) > preWaitMaxBytes {
		audit.Remediations = nil
		audit.RemediationsTruncated = audit.RemediationCount > 0
	}
	return audit
}

func (s *Service) buildRemediationsContext(ctx context.Context, state domain.State, inventory preWaitInventory) ([]Remediation, error) {
	items := make([]Remediation, 0)
	cuts := domain.NewDependencyCutIndex(state)
	successors, err := buildIncidentSuccessorIndex(ctx, state)
	if err != nil {
		return nil, err
	}
	add := func(code, target, owner, nodeID string, cut []string, operation string, params map[string]any) {
		sum := sha256.Sum256([]byte(strings.Join([]string{state.ProjectID, state.HeadHash, code, target}, "\x00")))
		remediation := Remediation{ID: "rem_" + hex.EncodeToString(sum[:]), Code: code, TargetRef: target, OwnerRole: owner, NodeID: nodeID, Operation: OperationDescriptor{Kind: operation, Params: params}}
		bindDependencyCut(&remediation, cut)
		items = append(items, remediation)
	}
	for _, nodeID := range inventory.readyNodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		node, _ := state.NodeDefinition(nodeID)
		add("assign_ready_node", "node:"+nodeID, node.Role, nodeID, nil, "role.bind_then_node.start", map[string]any{"roleId": node.Role, "nodeId": nodeID})
	}
	for _, attemptID := range inventory.submittedAttempts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		attempt := state.Attempts[attemptID]
		add("complete_submitted_attempt", "attempt:"+attemptID, attempt.RoleID, attempt.NodeID, nil, "action.list", map[string]any{"roleId": attempt.RoleID, "nodeId": attempt.NodeID})
	}
	for _, attemptID := range inventory.staleAttempts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		attempt := state.Attempts[attemptID]
		add("checkpoint_stale_attempt", "attempt:"+attemptID, attempt.RoleID, attempt.NodeID, nil, "action.list", map[string]any{"roleId": attempt.RoleID, "nodeId": attempt.NodeID, "actionKind": "attempt.checkpoint"})
	}
	for _, roleID := range inventory.expiredRoles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lease := state.Leases[roleID]
		add("renew_or_takeover_role", "role:"+roleID, roleID, "", nil, "role.bind", map[string]any{"roleId": roleID, "harness": lease.Harness, "previousSessionId": lease.SessionID, "takeoverRequiredForDifferentSession": true})
	}
	for _, actionID := range inventory.pendingEffects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		effect := state.Effects[actionID]
		cut, err := cuts.CutContext(ctx, effect.NodeID)
		if err != nil {
			return nil, err
		}
		continuity, err := s.effectContinuity(state, actionID)
		continuityRef := effectContinuityRefForEffect(state, actionID)
		if err == nil && continuity.ReconcileAllowed {
			add("reconcile_pending_effect", "effect:"+actionID, effect.OwnerRole, effect.NodeID, cut, "effect.reconcile", map[string]any{"actionId": actionID, "continuityRef": continuityRef})
			continue
		}
		blockers := []string{"continuity_unavailable"}
		if err == nil {
			blockers = append([]string(nil), continuity.ReconcileBlockers...)
		}
		add("resolve_effect_reconcile_blockers", "effect:"+actionID, effect.OwnerRole, effect.NodeID, cut, "inspect.effect-continuity", map[string]any{"actionId": actionID, "continuityRef": continuityRef, "blockers": blockers, "nextStep": effectBlockerNextStep(blockers)})
	}
	for _, resourceID := range inventory.orphanedResources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resource := state.Resources[resourceID]
		cut, err := cuts.CutContext(ctx, resource.NodeID)
		if err != nil {
			return nil, err
		}
		add("close_or_reconcile_orphaned_resource", "resource:"+resourceID, resource.RoleID, resource.NodeID, cut, "action.list", map[string]any{"roleId": resource.RoleID, "nodeId": resource.NodeID, "resourceId": resourceID})
	}
	for _, incidentID := range inventory.openIncidents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		incident := state.Incidents[incidentID]
		if successor := successors[incident.NodeID]; successor != "" && incident.SourceType == "attempt" {
			add("supersede_incident_with_repair", "incident:"+incidentID, incident.OwnerRole, successor, incident.DependencyCut, "incident.supersede", map[string]any{"incidentId": incidentID, "successorNodeId": successor, "actorRole": incident.OwnerRole})
			continue
		}
		add("advance_incident", "incident:"+incidentID, incident.OwnerRole, incident.NodeID, incident.DependencyCut, "incident.progress_or_disposition", map[string]any{"incidentId": incidentID, "actorRole": incident.OwnerRole})
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := remediationPriority(items[i].Code), remediationPriority(items[j].Code)
		if left != right {
			return left < right
		}
		if items[i].Code == items[j].Code {
			return items[i].TargetRef < items[j].TargetRef
		}
		return items[i].Code < items[j].Code
	})
	return items, nil
}

func bindDependencyCut(remediation *Remediation, cut []string) {
	if len(cut) == 0 {
		return
	}
	digest := sha256.Sum256([]byte(strings.Join(cut, "\x00")))
	remediation.DependencyCutCount = len(cut)
	remediation.DependencyCutDigest = "sha256:" + hex.EncodeToString(digest[:])
	remediation.DependencyCutRef = "dependency-cut:" + hex.EncodeToString(digest[:]) + ":0"
	if len(cut) > dependencyCutPreview {
		cut = cut[:dependencyCutPreview]
	}
	remediation.DependencyCut = append([]string(nil), cut...)
}

func remediationPriority(code string) int {
	switch code {
	case "complete_submitted_attempt":
		return 0
	case "reconcile_pending_effect", "resolve_effect_reconcile_blockers":
		return 1
	case "close_or_reconcile_orphaned_resource":
		return 2
	case "supersede_incident_with_repair":
		return 3
	case "advance_incident":
		return 4
	case "checkpoint_stale_attempt":
		return 5
	case "renew_or_takeover_role":
		return 6
	case "assign_ready_node":
		return 7
	}
	return 100
}

func effectBlockerNextStep(blockers []string) string {
	for _, blocker := range blockers {
		switch blocker {
		case "prepared_adapter_metadata_missing", "adapter_metadata_changed", "effect_adapter_unavailable", "causal_contract_changed":
			return "restore_prior_runtime_or_quarantine"
		case "role_capability_missing", "role_lease_invalid":
			return "restore_role_authorization"
		case "incident_circuit-open", "incident_resolved":
			return "resolve_incident_disposition"
		}
	}
	return "inspect_and_escalate"
}

func incidentSuccessor(state domain.State, incident domain.Incident) string {
	index, err := buildIncidentSuccessorIndex(context.Background(), state)
	if err != nil {
		return ""
	}
	return index[incident.NodeID]
}

func buildIncidentSuccessorIndex(ctx context.Context, state domain.State) (map[string]string, error) {
	result := map[string]string{}
	if state.Graph == nil {
		return result, nil
	}
	candidates := map[string][]string{}
	for _, edge := range state.Graph.Spec.Edges {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !domain.PredicateSatisfied(edge.When, state.Nodes[edge.From]) {
			continue
		}
		runtime := state.Nodes[edge.To]
		if runtime.Status == "planned" || runtime.Status == "active" {
			candidates[edge.From] = append(candidates[edge.From], edge.To)
		}
	}
	for _, node := range state.Graph.Spec.Nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if node.Supersedes != "" && (state.Nodes[node.ID].Status == "planned" || state.Nodes[node.ID].Status == "active") {
			candidates[node.Supersedes] = append(candidates[node.Supersedes], node.ID)
		}
	}
	for source, values := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sort.Strings(values)
		if len(values) > 0 {
			result[source] = values[0]
		}
	}
	return result, ctx.Err()
}

func validIncidentSuccessor(state domain.State, incident domain.Incident, successorNodeID string) bool {
	if state.Graph == nil || successorNodeID == "" {
		return false
	}
	runtime := state.Nodes[successorNodeID]
	if runtime.Status != "planned" && runtime.Status != "active" {
		return false
	}
	for _, node := range state.Graph.Spec.Nodes {
		if node.ID == successorNodeID && node.Supersedes == incident.NodeID {
			return true
		}
	}
	source := state.Nodes[incident.NodeID]
	for _, edge := range state.Graph.Spec.Edges {
		if edge.From == incident.NodeID && edge.To == successorNodeID && domain.PredicateSatisfied(edge.When, source) {
			return true
		}
	}
	return false
}

func authorizationForRole(state domain.State, roleID string, now time.Time) AuthorizationEnvelope {
	envelope := AuthorizationEnvelope{RoleID: roleID, LeaseState: "missing", RoutineActions: []string{"read_context", "inspect", "pre_wait"}, EscalationRequired: []string{"irreversible_external_effect", "new_authority", "scope_expansion"}}
	if state.Graph != nil {
		for _, role := range state.Graph.Spec.Roles {
			if role.ID == roleID {
				envelope.Capabilities = append([]string(nil), role.Capabilities...)
				sort.Strings(envelope.Capabilities)
				break
			}
		}
	}
	lease, ok := state.Leases[roleID]
	if !ok || !lease.Active {
		return envelope
	}
	envelope.SessionID, envelope.ExpiresAt = lease.SessionID, lease.ExpiresAt
	expires, err := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
	switch {
	case err != nil || !now.Before(expires):
		envelope.LeaseState = "expired"
	case expires.Sub(now) <= 5*time.Minute:
		envelope.LeaseState = "expiring"
	default:
		envelope.LeaseState = "active"
	}
	if envelope.LeaseState == "active" || envelope.LeaseState == "expiring" {
		envelope.RoutineActions = append(envelope.RoutineActions, "apply_returned_reversible_action_ref")
	}
	return envelope
}

func (s *Service) projectAllowedActions(state domain.State, roleID string, limit int) []AllowedAction {
	actions, _ := s.projectAllowedActionsContext(context.Background(), state, roleID, limit, s.Now().UTC())
	return actions
}

func (s *Service) projectAllowedActionsContext(ctx context.Context, state domain.State, roleID string, limit int, now time.Time) ([]AllowedAction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if roleID == "" {
		return nil, nil
	}
	planning, err := s.buildActionPlanningIndex(ctx, state, roleID, now)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil
	}
	successors, err := buildIncidentSuccessorIndex(ctx, state)
	if err != nil {
		return nil, err
	}
	result := []AllowedAction{}
	incidentIDs := make([]string, 0)
	for incidentID, incident := range state.Incidents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if planning.capabilities[domain.CapabilityIncidentManage] && incident.OwnerRole == roleID && incident.Status != "resolved" && incident.SourceType == "attempt" && successors[incident.NodeID] != "" {
			incidentIDs = append(incidentIDs, incidentID)
		}
	}
	sort.Strings(incidentIDs)
	for _, incidentID := range incidentIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		incident := state.Incidents[incidentID]
		successor := successors[incident.NodeID]
		action, err := s.incidentSupersedeAllowedActionAtWithIndex(state, incident, successor, now, planning)
		if err != nil {
			continue
		}
		result = append(result, action)
		if limit > 0 && len(result) == limit {
			return result, nil
		}
	}
	nodes := []domain.NodeDefinition{}
	if state.Graph != nil {
		for _, node := range state.Graph.Spec.Nodes {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if node.Role == roleID {
				nodes = append(nodes, node)
			}
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	frontier, err := domain.ComputeFrontierSummaryContext(ctx, state)
	if err != nil {
		return nil, err
	}
	ready := make(map[string]bool, len(frontier.Ready))
	for _, nodeID := range frontier.Ready {
		ready[nodeID] = true
	}
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		actions, err := s.listActionsForNodeAtWithReady(state, node, now, ready, planning)
		if err != nil {
			continue
		}
		for _, action := range actions.Actions {
			result = append(result, action)
			if limit > 0 && len(result) == limit {
				return result, nil
			}
		}
	}
	return result, ctx.Err()
}

func (s *Service) incidentSupersedeAllowedAction(state domain.State, incident domain.Incident, successorNodeID string) (AllowedAction, error) {
	return s.incidentSupersedeAllowedActionAt(state, incident, successorNodeID, s.Now().UTC())
}

func (s *Service) incidentSupersedeAllowedActionAt(state domain.State, incident domain.Incident, successorNodeID string, now time.Time) (AllowedAction, error) {
	index, err := s.buildActionPlanningIndex(context.Background(), state, incident.OwnerRole, now)
	if err != nil {
		return AllowedAction{}, err
	}
	return s.incidentSupersedeAllowedActionAtWithIndex(state, incident, successorNodeID, now, index)
}

func (s *Service) incidentSupersedeAllowedActionAtWithIndex(state domain.State, incident domain.Incident, successorNodeID string, now time.Time, index actionPlanningIndex) (AllowedAction, error) {
	if incident.OwnerRole != index.roleID || !index.capabilities[domain.CapabilityIncidentManage] {
		return AllowedAction{}, fmt.Errorf("role %s lacks incident management capability", incident.OwnerRole)
	}
	expires := now.Add(5 * time.Minute)
	if leaseExpiry, parseErr := time.Parse(time.RFC3339Nano, index.lease.ExpiresAt); parseErr == nil && leaseExpiry.Before(expires) {
		expires = leaseExpiry
	}
	providerSet := index.providerSet
	identity := strings.Join([]string{state.ProjectID, state.HeadHash, state.GraphRevision, providerSet, incident.OwnerRole, index.lease.SessionID, incident.ID, successorNodeID, "incident.supersede"}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	payload := actionRefPayload{ActionID: hex.EncodeToString(sum[:]), ProjectID: state.ProjectID, GraphRevision: state.GraphRevision, ProviderSet: providerSet, HeadHash: state.HeadHash, RoleID: incident.OwnerRole, SessionID: index.lease.SessionID, NodeID: successorNodeID, IncidentID: incident.ID, SuccessorNodeID: successorNodeID, Kind: "incident.supersede", ExpiresAt: expires.Format(time.RFC3339Nano)}
	ref, err := signActionRef(payload, index.secret)
	if err != nil {
		return AllowedAction{}, err
	}
	return AllowedAction{ID: payload.ActionID, Kind: payload.Kind, Ref: ref, NodeID: successorNodeID, TargetRef: "incident:" + incident.ID, InputSchema: incidentSupersedeInputSchema()}, nil
}

func (s *Service) effectContinuity(state domain.State, actionID string) (EffectContinuity, error) {
	effect, ok := state.Effects[actionID]
	if !ok {
		return EffectContinuity{}, fmt.Errorf("unknown effect %s", actionID)
	}
	preparedSequence := uint64(0)
	if action, exists := state.Actions[actionID]; exists {
		preparedSequence = action.Sequence
	}
	report := EffectContinuity{ActionID: actionID, PreparedSequence: preparedSequence, CurrentHeadSequence: state.HeadSequence, HeadAdvanced: state.HeadSequence > preparedSequence, Reasons: []string{}}
	node, exists := state.NodeDefinition(effect.NodeID)
	if !exists {
		report.Reasons = append(report.Reasons, "effect_node_no_longer_declared")
		return report, nil
	}
	report.DeclarationAvailable = true
	var declaration effectNodeInput
	if json.Unmarshal(node.Inputs, &declaration) != nil {
		report.Reasons = append(report.Reasons, "effect_declaration_invalid")
		return report, nil
	}
	report.AdapterIDUnchanged = declaration.Adapter == effect.AdapterID
	report.PreparedAdapterVersion = effect.AdapterVersion
	report.PreparedAdapterSchemaHash = effect.AdapterSchemaHash
	adapter, adapterAvailable := s.Providers.Effect(effect.AdapterID)
	if adapterAvailable {
		metadata := adapter.Metadata()
		report.CurrentAdapterVersion = metadata.Version
		report.CurrentAdapterSchemaHash = metadata.SchemaHash
		report.AdapterMetadataAvailable = effect.AdapterVersion != "" && effect.AdapterSchemaHash != ""
		report.AdapterUnchanged = report.AdapterIDUnchanged && report.AdapterMetadataAvailable && metadata.ID == effect.AdapterID && metadata.Version == effect.AdapterVersion && metadata.SchemaHash == effect.AdapterSchemaHash
	}
	preparedDigest, preparedErr := authorityRequestDigest("effect.request", effect.Request)
	currentDigest, currentErr := authorityRequestDigest("effect.request", declaration.Request)
	if preparedErr == nil {
		report.PreparedRequestDigest = preparedDigest
	}
	if currentErr == nil {
		report.CurrentRequestDigest = currentDigest
	}
	report.RequestUnchanged = preparedErr == nil && currentErr == nil && preparedDigest == currentDigest
	report.CausalContractUnchanged = report.AdapterUnchanged && report.RequestUnchanged
	if !report.CausalContractUnchanged {
		report.ReconcileBlockers = append(report.ReconcileBlockers, "causal_contract_changed")
	}
	if !oneOf(effect.Status, "prepared", "dispatched", "unknown", "failed", "reconciling") {
		report.ReconcileBlockers = append(report.ReconcileBlockers, "effect_status_not_reconcilable")
	}
	if !domain.RoleHasCapability(state.Graph, effect.OwnerRole, domain.CapabilityEffectReconcile) && !domain.RoleHasCapability(state.Graph, effect.OwnerRole, domain.CapabilityEffectApply) {
		report.ReconcileBlockers = append(report.ReconcileBlockers, "role_capability_missing")
	}
	if _, err := validLeaseAt(state, effect.OwnerRole, s.Now().UTC()); err != nil {
		report.ReconcileBlockers = append(report.ReconcileBlockers, "role_lease_invalid")
	}
	if incident, exists := state.Incidents["effect:"+actionID]; exists && incident.Status != "open" {
		report.ReconcileBlockers = append(report.ReconcileBlockers, "incident_"+incident.Status)
	}
	if !adapterAvailable {
		report.ReconcileBlockers = append(report.ReconcileBlockers, "effect_adapter_unavailable")
	} else if !report.AdapterMetadataAvailable {
		report.ReconcileBlockers = append(report.ReconcileBlockers, "prepared_adapter_metadata_missing")
	} else if !report.AdapterUnchanged {
		report.ReconcileBlockers = append(report.ReconcileBlockers, "adapter_metadata_changed")
	}
	sort.Strings(report.ReconcileBlockers)
	report.ReconcileAllowed = len(report.ReconcileBlockers) == 0
	if report.HeadAdvanced {
		report.Reasons = append(report.Reasons, "global_head_advanced")
	}
	if !report.AdapterUnchanged {
		report.Reasons = append(report.Reasons, "adapter_changed_or_unbound")
	}
	if !report.RequestUnchanged {
		report.Reasons = append(report.Reasons, "request_changed")
	}
	if report.CausalContractUnchanged {
		report.Reasons = append(report.Reasons, "bounded_effect_contract_unchanged")
	}
	sort.Strings(report.Reasons)
	return report, nil
}

func effectContinuityRefForEffect(state domain.State, actionID string) string {
	return "effect-continuity-key:" + opaqueEntityKey(state, "effect", actionID)
}

func effectContinuityDetailRef(state domain.State, actionID, digest string, offset int) string {
	return fmt.Sprintf("effect-continuity-detail:%s:%s:%s:%d", state.HeadHash, opaqueEntityKey(state, "effect", actionID), digest, offset)
}

func (s *Service) boundedEffectContinuityInspection(state domain.State, actionID string) (any, error) {
	report, err := s.effectContinuity(state, actionID)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	if len(raw) <= operationsMaxBytes {
		return report, nil
	}
	sum := sha256.Sum256(raw)
	return map[string]any{"effectRef": storedObjectRef(state, "effect", actionID), "reconcileAllowed": report.ReconcileAllowed, "detailDigest": "sha256:" + hex.EncodeToString(sum[:]), "detailBytes": len(raw), "detailRef": effectContinuityDetailRef(state, actionID, hex.EncodeToString(sum[:]), 0), "truncated": true}, nil
}

func (s *Service) effectContinuityDetailPage(state domain.State, expectedHead, effectKey, expectedDigest string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale Effect continuity detail reference: journal head changed")
	}
	actionID, exists := storedObjectIDForOpaqueKey(state, "effect", effectKey)
	if !exists {
		return BoundedDetailChunk{}, fmt.Errorf("unknown Effect continuity detail reference")
	}
	report, err := s.effectContinuity(state, actionID)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if digest != expectedDigest {
		return BoundedDetailChunk{}, fmt.Errorf("stale Effect continuity detail reference: report changed")
	}
	return boundedDetailChunk(raw, "sha256:"+digest, offset, func(next int) string { return effectContinuityDetailRef(state, actionID, digest, next) })
}

func evidenceInspectionBytes(state domain.State, evidenceDigest string) ([]byte, error) {
	for _, checkpoint := range state.Checkpoints {
		for _, evidence := range checkpoint.EvidenceRefs {
			if evidence.Digest == evidenceDigest {
				return json.Marshal(map[string]any{"evidence": evidence, "checkpointId": checkpoint.ID, "attemptId": checkpoint.AttemptID})
			}
		}
	}
	for _, pack := range state.EvidencePackages {
		artifacts := append([]domain.ArtifactRef{pack.Candidate, pack.ProspectiveTree}, pack.Artifacts...)
		for _, artifact := range artifacts {
			if artifact.Digest == evidenceDigest {
				return json.Marshal(map[string]any{"artifact": artifact, "packageId": pack.ID, "attemptId": pack.AttemptID})
			}
		}
	}
	return nil, fmt.Errorf("unknown evidence digest %s", evidenceDigest)
}

func evidenceDigestForOpaqueKey(state domain.State, key string) (string, bool) {
	for _, checkpoint := range state.Checkpoints {
		for _, evidence := range checkpoint.EvidenceRefs {
			if opaqueEntityKey(state, "evidence", evidence.Digest) == key {
				return evidence.Digest, true
			}
		}
	}
	for _, pack := range state.EvidencePackages {
		artifacts := append([]domain.ArtifactRef{pack.Candidate, pack.ProspectiveTree}, pack.Artifacts...)
		for _, artifact := range artifacts {
			if opaqueEntityKey(state, "evidence", artifact.Digest) == key {
				return artifact.Digest, true
			}
		}
	}
	return "", false
}

func evidenceDetailRef(state domain.State, evidenceDigest, detailDigest string, offset int) string {
	return fmt.Sprintf("evidence-detail:%s:%s:%s:%d", state.HeadHash, opaqueEntityKey(state, "evidence", evidenceDigest), detailDigest, offset)
}

func boundedEvidenceInspection(state domain.State, evidenceDigest string) (any, error) {
	raw, err := evidenceInspectionBytes(state, evidenceDigest)
	if err != nil {
		return nil, err
	}
	if len(raw) <= operationsMaxBytes {
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
		return result, nil
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	return map[string]any{
		"evidenceRef":  "evidence-key:" + opaqueEntityKey(state, "evidence", evidenceDigest),
		"detailDigest": "sha256:" + digest, "detailBytes": len(raw),
		"detailRef": evidenceDetailRef(state, evidenceDigest, digest, 0), "truncated": true,
	}, nil
}

func evidenceDetailPage(state domain.State, expectedHead, evidenceKey, expectedDigest string, offset int) (BoundedDetailChunk, error) {
	if state.HeadHash != expectedHead {
		return BoundedDetailChunk{}, fmt.Errorf("stale evidence detail reference: journal head changed")
	}
	evidenceDigest, exists := evidenceDigestForOpaqueKey(state, evidenceKey)
	if !exists {
		return BoundedDetailChunk{}, fmt.Errorf("unknown evidence detail reference")
	}
	raw, err := evidenceInspectionBytes(state, evidenceDigest)
	if err != nil {
		return BoundedDetailChunk{}, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if digest != expectedDigest {
		return BoundedDetailChunk{}, fmt.Errorf("stale evidence detail reference: evidence changed")
	}
	return boundedDetailChunk(raw, "sha256:"+digest, offset, func(next int) string {
		return evidenceDetailRef(state, evidenceDigest, digest, next)
	})
}

func (s *Service) Inspect(ref string) (any, error) {
	return s.InspectContext(context.Background(), ref)
}

func (s *Service) InspectContext(ctx context.Context, ref string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, _, err := s.load()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref == "project" {
		return map[string]any{"project": s.Project.Config, "state": map[string]any{"graphRevision": state.GraphRevision, "headSequence": state.HeadSequence, "headHash": state.HeadHash}}, nil
	}
	if ref == "frontier" {
		return boundedFrontierInspection(ctx, state)
	}
	prefix, id, ok := strings.Cut(ref, ":")
	if !ok || id == "" {
		return nil, fmt.Errorf("inspect ref must be project or kind:id")
	}
	if kind, isKey := strings.CutSuffix(prefix, "-key"); isKey && boundedStoredObjectKinds[kind] {
		objectID, exists := storedObjectIDForOpaqueKey(state, kind, id)
		if !exists {
			return nil, fmt.Errorf("unknown %s reference", kind)
		}
		return boundedStoredObjectInspection(state, kind, objectID)
	}
	if prefix == "object-detail" {
		parts := strings.Split(id, ":")
		if len(parts) != 4 || !boundedStoredObjectKinds[parts[0]] || len(parts[1]) != 64 || len(parts[2]) != 64 {
			return nil, fmt.Errorf("invalid object detail reference")
		}
		offset, err := strconv.Atoi(parts[3])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid object detail offset %s", parts[3])
		}
		return storedObjectDetailPage(state, parts[0], parts[1], parts[2], offset)
	}
	if prefix == "object-ref-detail" {
		parts := strings.Split(id, ":")
		if len(parts) != 4 || (parts[0] != "incident" && !boundedStoredObjectKinds[parts[0]]) || len(parts[1]) != 64 || len(parts[2]) != 64 {
			return nil, fmt.Errorf("invalid object reference detail")
		}
		offset, err := strconv.Atoi(parts[3])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid object reference detail offset %s", parts[3])
		}
		return objectReferenceDetailPage(state, parts[0], parts[1], parts[2], offset)
	}
	switch prefix {
	case "status-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 3 || len(parts[0]) != 64 || len(parts[1]) != 64 {
			return nil, fmt.Errorf("invalid status detail reference")
		}
		offset, err := strconv.Atoi(parts[2])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid status detail offset %s", parts[2])
		}
		return s.statusDetailPage(ctx, state, parts[0], parts[1], offset)
	case "history-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 5 || len(parts[0]) != 64 || len(parts[3]) != 64 {
			return nil, fmt.Errorf("invalid history detail reference")
		}
		after, afterErr := strconv.ParseUint(parts[1], 10, 64)
		limit, limitErr := strconv.Atoi(parts[2])
		offset, offsetErr := strconv.Atoi(parts[4])
		if afterErr != nil || limitErr != nil || offsetErr != nil || limit < 1 || limit > 100 || offset < 0 {
			return nil, fmt.Errorf("invalid history detail position")
		}
		return s.historyDetailPage(ctx, state, parts[0], after, limit, parts[3], offset)
	case "evidence-index-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 5 || len(parts[0]) != 64 || (parts[1] != "-" && len(parts[1]) != 64) || (parts[2] != "-" && len(parts[2]) != 64) || len(parts[3]) != 64 {
			return nil, fmt.Errorf("invalid evidence index detail reference")
		}
		offset, err := strconv.Atoi(parts[4])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid evidence index detail offset %s", parts[4])
		}
		return s.evidenceIndexDetailPage(ctx, state, parts[0], parts[1], parts[2], parts[3], offset)
	case "action-list-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 5 || len(parts[0]) != 64 || (parts[1] != "-" && len(parts[1]) != 64) || (parts[2] != "-" && len(parts[2]) != 64) || len(parts[3]) != 64 {
			return nil, fmt.Errorf("invalid action list detail reference")
		}
		offset, err := strconv.Atoi(parts[4])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid action list detail offset %s", parts[4])
		}
		return s.actionListDetailPage(ctx, state, parts[0], parts[1], parts[2], parts[3], offset)
	case "evidence-key":
		evidenceDigest, exists := evidenceDigestForOpaqueKey(state, id)
		if !exists {
			return nil, fmt.Errorf("unknown evidence reference")
		}
		return boundedEvidenceInspection(state, evidenceDigest)
	case "evidence-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 4 || len(parts[0]) != 64 || len(parts[1]) != 64 || len(parts[2]) != 64 {
			return nil, fmt.Errorf("invalid evidence detail reference")
		}
		offset, err := strconv.Atoi(parts[3])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid evidence detail offset %s", parts[3])
		}
		return evidenceDetailPage(state, parts[0], parts[1], parts[2], offset)
	case "pre-wait-page":
		parts := strings.Split(id, ":")
		if len(parts) != 3 || len(parts[0]) != 64 || len(parts[1]) != 64 {
			return nil, fmt.Errorf("invalid pre-wait page reference")
		}
		offset, err := strconv.Atoi(parts[2])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid pre-wait page offset %s", parts[2])
		}
		return s.preWaitPage(ctx, state, parts[0], parts[1], offset)
	case "frontier-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 3 || len(parts[0]) != 64 || len(parts[1]) != 64 {
			return nil, fmt.Errorf("invalid frontier detail reference")
		}
		offset, err := strconv.Atoi(parts[2])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid frontier detail offset %s", parts[2])
		}
		return frontierDetailPage(ctx, state, parts[0], parts[1], offset)
	case "pre-wait-item":
		parts := strings.Split(id, ":")
		if len(parts) != 4 || len(parts[0]) != 64 || len(parts[1]) != 64 {
			return nil, fmt.Errorf("invalid pre-wait item reference")
		}
		index, indexErr := strconv.Atoi(parts[2])
		offset, offsetErr := strconv.Atoi(parts[3])
		if indexErr != nil || offsetErr != nil || index < 0 || offset < 0 {
			return nil, fmt.Errorf("invalid pre-wait item position")
		}
		return s.preWaitItemPage(ctx, state, parts[0], parts[1], index, offset)
	case "dependency-cut":
		digest, offsetText, ok := strings.Cut(id, ":")
		offset, err := strconv.Atoi(offsetText)
		if !ok || err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid dependency cut page reference")
		}
		return dependencyCutPageContext(ctx, state, digest, offset)
	case "dependency-cut-item":
		parts := strings.Split(id, ":")
		if len(parts) != 3 || len(parts[0]) != 64 {
			return nil, fmt.Errorf("invalid dependency cut item reference")
		}
		index, indexErr := strconv.Atoi(parts[1])
		offset, offsetErr := strconv.Atoi(parts[2])
		if indexErr != nil || offsetErr != nil || index < 0 || offset < 0 {
			return nil, fmt.Errorf("invalid dependency cut item position")
		}
		return dependencyCutItemPage(ctx, state, parts[0], index, offset)
	case "incident-index":
		parts := strings.Split(id, ":")
		if len(parts) == 1 {
			offset, err := strconv.Atoi(parts[0])
			if err != nil || offset < 0 {
				return nil, fmt.Errorf("invalid incident index offset %s", id)
			}
			return incidentIndexContext(ctx, state, "", "", offset, 50)
		}
		if len(parts) != 3 || len(parts[0]) != 64 || len(parts[1]) != 64 {
			return nil, fmt.Errorf("invalid incident index reference")
		}
		offset, err := strconv.Atoi(parts[2])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid incident index offset %s", parts[2])
		}
		return incidentIndexContext(ctx, state, parts[0], parts[1], offset, 50)
	case "incident-key":
		incidentID, exists := incidentIDForOpaqueKey(state, id)
		if !exists {
			return nil, fmt.Errorf("unknown incident reference")
		}
		return boundedIncidentInspection(state, incidentID)
	case "incident-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 3 || len(parts[0]) != 64 || len(parts[1]) != 64 {
			return nil, fmt.Errorf("invalid incident detail reference")
		}
		offset, err := strconv.Atoi(parts[2])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid incident detail offset %s", parts[2])
		}
		return incidentDetailPage(state, parts[0], parts[1], offset)
	case "remediations":
		parts := strings.Split(id, ":")
		if len(parts) != 3 || len(parts[0]) != 64 || len(parts[1]) != 64 {
			return nil, fmt.Errorf("invalid remediation page reference")
		}
		offset, err := strconv.Atoi(parts[2])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid remediation page offset %s", parts[2])
		}
		return s.remediationPage(ctx, state, parts[0], parts[1], offset)
	case "remediation-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 4 || len(parts[0]) != 64 || len(parts[1]) != 64 || len(parts[2]) != 64 {
			return nil, fmt.Errorf("invalid remediation detail reference")
		}
		offset, err := strconv.Atoi(parts[3])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid remediation detail offset %s", parts[3])
		}
		return s.remediationDetailPage(ctx, state, parts[0], parts[1], parts[2], offset)
	case "operations":
		roleID, exists := roleIDForOpaqueKey(state, id)
		if !exists {
			return nil, fmt.Errorf("unknown operations reference")
		}
		return s.operationsIndex(ctx, state, roleID)
	case "operations-actions":
		parts := strings.Split(id, ":")
		if len(parts) != 4 || len(parts[0]) != 64 || len(parts[1]) != 64 || len(parts[2]) != 64 {
			return nil, fmt.Errorf("invalid operations action reference")
		}
		roleID, exists := roleIDForOpaqueKey(state, parts[0])
		if !exists {
			return nil, fmt.Errorf("unknown operations action reference")
		}
		offset, err := strconv.Atoi(parts[3])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid operations action offset %s", parts[3])
		}
		return s.operationsActionPage(ctx, state, roleID, parts[1], parts[2], offset)
	case "operations-action-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 5 || len(parts[0]) != 64 || len(parts[1]) != 64 || len(parts[2]) != 64 || len(parts[3]) != 64 {
			return nil, fmt.Errorf("invalid operations action detail reference")
		}
		roleID, exists := roleIDForOpaqueKey(state, parts[0])
		if !exists {
			return nil, fmt.Errorf("unknown operations action detail reference")
		}
		offset, err := strconv.Atoi(parts[4])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid operations action detail offset %s", parts[4])
		}
		return s.operationsActionDetailPage(ctx, state, roleID, parts[1], parts[2], parts[3], offset)
	case "role-key":
		roleID, exists := roleIDForOpaqueKey(state, id)
		if !exists {
			return nil, fmt.Errorf("unknown role reference")
		}
		return boundedRoleInspection(state, roleID)
	case "role-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 3 || len(parts[0]) != 64 || len(parts[1]) != 64 {
			return nil, fmt.Errorf("invalid role detail reference")
		}
		offset, err := strconv.Atoi(parts[2])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid role detail offset %s", parts[2])
		}
		return roleDetailPage(state, parts[0], parts[1], offset)
	case "node-key":
		nodeID, exists := nodeIDForOpaqueKey(state, id)
		if !exists {
			return nil, fmt.Errorf("unknown node reference")
		}
		return boundedNodeInspection(state, nodeID)
	case "node-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 3 || len(parts[0]) != 64 || len(parts[1]) != 64 {
			return nil, fmt.Errorf("invalid node detail reference")
		}
		offset, err := strconv.Atoi(parts[2])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid node detail offset %s", parts[2])
		}
		return nodeDetailPage(state, parts[0], parts[1], offset)
	case "attempt-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 3 || len(parts[0]) != 64 || len(parts[1]) != 64 {
			return nil, fmt.Errorf("invalid attempt detail reference")
		}
		offset, err := strconv.Atoi(parts[2])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid attempt detail offset %s", parts[2])
		}
		return attemptDetailPage(state, parts[0], parts[1], offset)
	case "attempt-key":
		attemptID, exists := attemptIDForOpaqueKey(state, id)
		if !exists {
			return nil, fmt.Errorf("unknown Attempt reference")
		}
		return boundedAttemptInspection(state, attemptID)
	case "attempt-id-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 3 || len(parts[0]) != 64 || len(parts[1]) != 64 {
			return nil, fmt.Errorf("invalid attempt identity detail reference")
		}
		offset, err := strconv.Atoi(parts[2])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid attempt identity detail offset %s", parts[2])
		}
		return attemptIDDetailPage(state, parts[0], parts[1], offset)
	case "outcome-id-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 4 || len(parts[0]) != 64 || len(parts[1]) != 64 || len(parts[2]) != 64 {
			return nil, fmt.Errorf("invalid outcome identity detail reference")
		}
		offset, err := strconv.Atoi(parts[3])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid outcome identity detail offset %s", parts[3])
		}
		return outcomeIDDetailPage(state, parts[0], parts[1], parts[2], offset)
	case "history":
		var cursor uint64
		if _, err := fmt.Sscanf(id, "%d", &cursor); err != nil || cursor > state.HeadSequence {
			return nil, fmt.Errorf("invalid history cursor %s", id)
		}
		return s.BoundedHistoryContext(ctx, cursor, 50)
	case "node":
		return boundedNodeInspection(state, id)
	case "attempt":
		return boundedAttemptInspection(state, id)
	case "checkpoint":
		return boundedStoredObjectInspection(state, prefix, id)
	case "evidence-package":
		return boundedStoredObjectInspection(state, prefix, id)
	case "reuse-decision":
		return boundedStoredObjectInspection(state, prefix, id)
	case "decision":
		return boundedStoredObjectInspection(state, prefix, id)
	case "evidence":
		return boundedEvidenceInspection(state, id)
	case "action":
		return boundedStoredObjectInspection(state, prefix, id)
	case "role":
		return boundedRoleInspection(state, id)
	case "effect":
		return boundedStoredObjectInspection(state, prefix, id)
	case "effect-continuity":
		return s.boundedEffectContinuityInspection(state, id)
	case "effect-continuity-key":
		actionID, exists := storedObjectIDForOpaqueKey(state, "effect", id)
		if !exists {
			return nil, fmt.Errorf("unknown Effect continuity reference")
		}
		return s.boundedEffectContinuityInspection(state, actionID)
	case "effect-continuity-detail":
		parts := strings.Split(id, ":")
		if len(parts) != 4 || len(parts[0]) != 64 || len(parts[1]) != 64 || len(parts[2]) != 64 {
			return nil, fmt.Errorf("invalid Effect continuity detail reference")
		}
		offset, err := strconv.Atoi(parts[3])
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("invalid Effect continuity detail offset %s", parts[3])
		}
		return s.effectContinuityDetailPage(state, parts[0], parts[1], parts[2], offset)
	case "resource":
		return boundedStoredObjectInspection(state, prefix, id)
	case "incident":
		return boundedIncidentInspection(state, id)
	default:
		return nil, fmt.Errorf("unsupported inspect ref %s", ref)
	}
}
