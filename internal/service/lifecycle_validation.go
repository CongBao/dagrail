package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/sdk"
	"github.com/gowebpki/jcs"
)

type lifecycleStatusChange struct {
	AttemptID string `json:"attemptId"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updatedAt"`
}

type lifecycleAttemptFinish struct {
	AttemptID    string                `json:"attemptId"`
	Outcome      string                `json:"outcome"`
	OutcomeClass string                `json:"outcomeClass"`
	Facts        domain.PredicateFacts `json:"facts"`
	UpdatedAt    string                `json:"updatedAt"`
}

type lifecycleResourceObservation struct {
	ResourceID string          `json:"resourceId"`
	Status     string          `json:"status"`
	Receipt    json.RawMessage `json:"receipt"`
	UpdatedAt  string          `json:"updatedAt"`
}

type lifecycleEffectObservation struct {
	ActionID  string          `json:"actionId"`
	Status    string          `json:"status"`
	Receipt   json.RawMessage `json:"receipt"`
	UpdatedAt string          `json:"updatedAt"`
}

type lifecycleRecordContext struct {
	eventTypes             []string
	actionCount            int
	actionKind             string
	actionID               string
	attempts               map[string]domain.Attempt
	statuses               map[string]lifecycleStatusChange
	checkpoints            map[string]domain.Checkpoint
	packages               map[string]domain.ExecutionPackage
	reuseDecisions         map[string]domain.ReuseDecision
	decisions              map[string]domain.DecisionRecord
	resourceLeases         map[string]domain.ResourceLease
	resourceBefore         map[string]domain.ResourceLease
	resourceObservations   map[string]lifecycleResourceObservation
	resourceIncidentBefore map[string]*domain.Incident
	resourceReleases       map[string]bool
	effects                map[string]domain.EffectAction
	effectObservations     map[string]lifecycleEffectObservation
	effectIncidentBefore   map[string]*domain.Incident
	finishes               map[string]lifecycleAttemptFinish
	incidentEvents         map[string]lifecycleIncidentEvent
	incidentResolutions    map[string]string
	effectAdmissions       map[string]bool
}

type lifecycleIncidentEvent struct {
	Type     string
	Incident domain.Incident
	Prior    *domain.Incident
}

func newLifecycleRecordContext(effectAdmissions map[string]bool) *lifecycleRecordContext {
	return &lifecycleRecordContext{
		attempts: map[string]domain.Attempt{}, statuses: map[string]lifecycleStatusChange{},
		checkpoints: map[string]domain.Checkpoint{}, packages: map[string]domain.ExecutionPackage{},
		reuseDecisions: map[string]domain.ReuseDecision{}, decisions: map[string]domain.DecisionRecord{},
		resourceLeases: map[string]domain.ResourceLease{}, resourceBefore: map[string]domain.ResourceLease{}, resourceObservations: map[string]lifecycleResourceObservation{}, resourceIncidentBefore: map[string]*domain.Incident{}, resourceReleases: map[string]bool{},
		effects: map[string]domain.EffectAction{}, effectObservations: map[string]lifecycleEffectObservation{}, effectIncidentBefore: map[string]*domain.Incident{},
		finishes: map[string]lifecycleAttemptFinish{}, incidentEvents: map[string]lifecycleIncidentEvent{},
		incidentResolutions: map[string]string{}, effectAdmissions: effectAdmissions,
	}
}

// validateLifecycleEventSequence applies the command-level invariants that a
// normal DAGrail writer enforces before emitting each native event. The journal
// reducer intentionally trusts already-committed events, so migration cannot
// use final-map replay alone as admission validation.
func validateLifecycleEventSequence(initial domain.State, records []LifecycleMigrationRecord) error {
	_, err := simulateLifecycleEventSequence(initial, records)
	return err
}

func simulateLifecycleEventSequence(initial domain.State, records []LifecycleMigrationRecord) (domain.State, error) {
	raw, err := json.Marshal(initial)
	if err != nil {
		return domain.State{}, err
	}
	var state domain.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return domain.State{}, err
	}
	effectAdmissions := map[string]bool{}
	for _, record := range records {
		if !validTimestamp(record.OccurredAt) {
			return domain.State{}, fmt.Errorf("source event %s occurredAt is invalid", record.SourceEventID)
		}
		context := newLifecycleRecordContext(effectAdmissions)
		for _, event := range record.Events {
			if err := applyValidatedLifecycleEvent(&state, event, record.OccurredAt, context); err != nil {
				return domain.State{}, fmt.Errorf("source event %s native event %s: %w", record.SourceEventID, event.Type, err)
			}
		}
		if err := validateLifecycleRecordClosed(&state, context); err != nil {
			return domain.State{}, fmt.Errorf("source event %s command shape: %w", record.SourceEventID, err)
		}
	}
	if err := validateMigratedState(state); err != nil {
		return domain.State{}, err
	}
	return state, nil
}

func applyValidatedLifecycleEvent(state *domain.State, event LifecycleMigrationEvent, occurredAt string, context *lifecycleRecordContext) error {
	context.eventTypes = append(context.eventTypes, event.Type)
	switch event.Type {
	case "role.bound":
		var lease domain.RoleLease
		if err := json.Unmarshal(event.Payload, &lease); err != nil {
			return err
		}
		if !graphHasRole(*state, lease.RoleID) || !lease.Active || lease.Harness == "" || lease.SessionID == "" {
			return fmt.Errorf("role binding is incomplete or inactive")
		}
		bound, boundErr := time.Parse(time.RFC3339Nano, lease.BoundAt)
		expires, expiresErr := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
		if boundErr != nil || expiresErr != nil || !expires.After(bound) || expires.Sub(bound) > 24*time.Hour || !timestampAtOrBefore(lease.BoundAt, occurredAt) {
			return fmt.Errorf("role binding timestamps are invalid")
		}
		if current, ok := state.Leases[lease.RoleID]; ok && current.Active {
			currentExpiry, err := time.Parse(time.RFC3339Nano, current.ExpiresAt)
			currentBound, boundErr := time.Parse(time.RFC3339Nano, current.BoundAt)
			if err != nil || boundErr != nil || bound.Before(currentBound) || (currentExpiry.After(bound) && current.SessionID != lease.SessionID) {
				return fmt.Errorf("role %s has overlapping active bindings", lease.RoleID)
			}
		}
		state.Leases[lease.RoleID] = lease
	case "role.released":
		var value struct {
			RoleID     string `json:"roleId"`
			ReleasedAt string `json:"releasedAt"`
		}
		_ = json.Unmarshal(event.Payload, &value)
		lease, ok := state.Leases[value.RoleID]
		if !ok || !lease.Active || lease.RoleID != value.RoleID || !timestampAtOrAfter(value.ReleasedAt, lease.BoundAt) || !timestampAtOrBefore(value.ReleasedAt, occurredAt) {
			return fmt.Errorf("role release does not reference an active binding")
		}
		lease.Active = false
		state.Leases[value.RoleID] = lease
	case "attempt.started", "attempt.leased":
		var attempt domain.Attempt
		_ = json.Unmarshal(event.Payload, &attempt)
		node, nodeOK := state.NodeDefinition(attempt.NodeID)
		started, startedErr := time.Parse(time.RFC3339Nano, attempt.StartedAt)
		updated, updatedErr := time.Parse(time.RFC3339Nano, attempt.UpdatedAt)
		lease, leaseOK := state.Leases[attempt.RoleID]
		if attempt.ID == "" || !nodeOK || node.Role != attempt.RoleID || attempt.Status != "leased" || startedErr != nil || updatedErr != nil || updated.Before(started) || !timestampAtOrBefore(attempt.UpdatedAt, occurredAt) || !leaseOK || !lease.Active {
			return fmt.Errorf("attempt introduction is not a legal leased attempt")
		}
		bound, boundErr := time.Parse(time.RFC3339Nano, lease.BoundAt)
		expires, expiresErr := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
		if boundErr != nil || expiresErr != nil || started.Before(bound) || !started.Before(expires) {
			return fmt.Errorf("attempt starts outside its role binding")
		}
		requiredCapability := domain.RequiredNodeCapability(node.Kind)
		if requiredCapability == "" || !domain.RoleHasCapability(state.Graph, attempt.RoleID, requiredCapability) {
			return fmt.Errorf("attempt role lacks the node capability")
		}
		if _, exists := state.Attempts[attempt.ID]; exists || state.Nodes[attempt.NodeID].Status != "planned" || !contains(domain.ComputeFrontier(*state).Ready, attempt.NodeID) || attempt.Number != len(state.NodeAttempts[attempt.NodeID])+1 {
			return fmt.Errorf("attempt identity, number, or node state is invalid")
		}
		state.Attempts[attempt.ID] = attempt
		state.NodeAttempts[attempt.NodeID] = append(state.NodeAttempts[attempt.NodeID], attempt.ID)
		state.Nodes[attempt.NodeID] = domain.NodeRuntime{Status: "active"}
		context.attempts[attempt.ID] = attempt
	case "attempt.status-changed":
		var value lifecycleStatusChange
		_ = json.Unmarshal(event.Payload, &value)
		attempt, ok := state.Attempts[value.AttemptID]
		if !ok || attempt.Status == "terminal" || !legalAttemptStatusTransition(attempt.Status, value.Status) || !timestampAtOrAfter(value.UpdatedAt, attempt.UpdatedAt) || !timestampAtOrBefore(value.UpdatedAt, occurredAt) || !roleLeaseCovers(*state, attempt.RoleID, value.UpdatedAt) {
			return fmt.Errorf("attempt status transition is invalid")
		}
		attempt.Status, attempt.UpdatedAt = value.Status, value.UpdatedAt
		state.Attempts[attempt.ID] = attempt
		context.statuses[attempt.ID] = value
	case "attempt.checkpointed":
		var checkpoint domain.Checkpoint
		_ = json.Unmarshal(event.Payload, &checkpoint)
		attempt, ok := state.Attempts[checkpoint.AttemptID]
		if checkpoint.ID == "" || !ok || !oneOf(attempt.Status, "running", "waiting", "submitted") || checkpoint.Summary == "" || len([]byte(checkpoint.Summary)) > 2048 || !timestampAtOrAfter(checkpoint.CreatedAt, attempt.UpdatedAt) || !timestampAtOrBefore(checkpoint.CreatedAt, occurredAt) || !roleLeaseCovers(*state, attempt.RoleID, checkpoint.CreatedAt) {
			return fmt.Errorf("checkpoint does not reference an active attempt")
		}
		if _, exists := state.Checkpoints[checkpoint.ID]; exists {
			return fmt.Errorf("checkpoint identity is duplicated")
		}
		for _, evidence := range checkpoint.EvidenceRefs {
			if !validDigest(evidence.Digest) || evidence.Type == "" || evidence.Size < 0 {
				return fmt.Errorf("checkpoint evidence reference is invalid")
			}
		}
		state.Checkpoints[checkpoint.ID] = checkpoint
		attempt.CheckpointID, attempt.UpdatedAt = checkpoint.ID, checkpoint.CreatedAt
		state.Attempts[attempt.ID] = attempt
		context.checkpoints[checkpoint.ID] = checkpoint
	case "evidence.package-published":
		var pack domain.ExecutionPackage
		_ = json.Unmarshal(event.Payload, &pack)
		attempt, ok := state.Attempts[pack.AttemptID]
		node, nodeOK := state.NodeDefinition(pack.NodeID)
		if !ok || !oneOf(attempt.Status, "running", "waiting", "submitted") || !nodeOK || attempt.NodeID != pack.NodeID || pack.ProjectID != state.ProjectID || pack.GraphRevision != state.GraphRevision || !timestampAtOrAfter(pack.CreatedAt, attempt.UpdatedAt) || !timestampAtOrBefore(pack.CreatedAt, occurredAt) || !roleLeaseCovers(*state, attempt.RoleID, pack.CreatedAt) {
			return fmt.Errorf("execution package does not reference an active attempt")
		}
		if _, exists := state.EvidencePackages[pack.ID]; exists {
			return fmt.Errorf("execution package identity is duplicated")
		}
		if err := validateExecutionPackageRecord(pack, node); err != nil {
			return err
		}
		state.EvidencePackages[pack.ID] = pack
		state.AttemptPackages[pack.AttemptID] = append(state.AttemptPackages[pack.AttemptID], pack.ID)
		context.packages[pack.ID] = pack
	case "evidence.reuse-assessed":
		var decision domain.ReuseDecision
		_ = json.Unmarshal(event.Payload, &decision)
		pack, packOK := state.EvidencePackages[decision.PackageID]
		assessor, assessorOK := state.Attempts[decision.AssessedByAttempt]
		node, nodeOK := state.NodeDefinition(pack.NodeID)
		if !packOK || !assessorOK || !oneOf(assessor.Status, "running", "waiting", "submitted") || !nodeOK || !timestampAtOrAfter(decision.CreatedAt, assessor.UpdatedAt) || !timestampAtOrAfter(decision.CreatedAt, pack.CreatedAt) || !timestampAtOrBefore(decision.CreatedAt, occurredAt) || !roleLeaseCovers(*state, assessor.RoleID, decision.CreatedAt) {
			return fmt.Errorf("reuse decision references unavailable evidence or assessor")
		}
		if _, exists := state.ReuseDecisions[decision.ID]; exists {
			return fmt.Errorf("reuse decision identity is duplicated")
		}
		if err := validateReuseDecisionRecord(decision, pack, node); err != nil {
			return err
		}
		state.ReuseDecisions[decision.ID] = decision
		state.PackageDecisions[decision.PackageID] = append(state.PackageDecisions[decision.PackageID], decision.ID)
		context.reuseDecisions[decision.ID] = decision
	case "decision.recorded":
		var decision domain.DecisionRecord
		_ = json.Unmarshal(event.Payload, &decision)
		attempt, ok := state.Attempts[decision.AttemptID]
		if !ok || !oneOf(attempt.Status, "running", "waiting", "submitted") || !timestampAtOrAfter(decision.CreatedAt, attempt.UpdatedAt) || !timestampAtOrBefore(decision.CreatedAt, occurredAt) || !roleLeaseCovers(*state, attempt.RoleID, decision.CreatedAt) {
			return fmt.Errorf("decision does not reference an active attempt")
		}
		if _, exists := state.Decisions[decision.ID]; exists || len(state.AttemptDecisions[decision.AttemptID]) != 0 {
			return fmt.Errorf("decision identity is duplicated")
		}
		if err := validateDecisionRecord(decision, *state); err != nil {
			return err
		}
		state.Decisions[decision.ID] = decision
		state.AttemptDecisions[decision.AttemptID] = append(state.AttemptDecisions[decision.AttemptID], decision.ID)
		context.decisions[decision.ID] = decision
	case "resource.leased":
		var resource domain.ResourceLease
		_ = json.Unmarshal(event.Payload, &resource)
		attempt, ok := state.Attempts[resource.AttemptID]
		node, nodeOK := state.NodeDefinition(resource.NodeID)
		if resource.ID == "" || !ok || attempt.Status != "running" || !nodeOK || attempt.NodeID != resource.NodeID || attempt.RoleID != resource.RoleID || resource.Status != "active" || resource.ClosureStatus != "pending" || resource.Quantity < 1 || !timestampAtOrAfter(resource.LeasedAt, attempt.UpdatedAt) || !timestampAtOrBefore(resource.LeasedAt, occurredAt) || !roleLeaseCovers(*state, attempt.RoleID, resource.LeasedAt) {
			return fmt.Errorf("resource lease is not bound to an active attempt")
		}
		if _, exists := state.Resources[resource.ID]; exists || !nodeRequestsResource(node, resource.Kind, resource.Quantity) || resourceForAttempt(*state, resource.AttemptID, resource.Kind) {
			return fmt.Errorf("resource identity or request does not match the node contract")
		}
		if activeResourceQuantity(*state, resource.Kind)+resource.Quantity > resourceCapacity(*state, resource.Kind) {
			return fmt.Errorf("resource lease exceeds declared capacity")
		}
		state.Resources[resource.ID] = resource
		context.resourceLeases[resource.ID] = resource
	case "resource.closure-observed":
		var value lifecycleResourceObservation
		_ = json.Unmarshal(event.Payload, &value)
		resource, ok := state.Resources[value.ResourceID]
		if ok {
			context.resourceBefore[value.ResourceID] = resource
		}
		if incident, exists := state.Incidents["resource:"+value.ResourceID]; exists {
			copy := incident
			context.resourceIncidentBefore[value.ResourceID] = &copy
		} else {
			context.resourceIncidentBefore[value.ResourceID] = nil
		}
		if !ok || resource.Status != "active" || !oneOf(resource.ClosureStatus, "pending", "unknown", "failed", "reconciling") || !oneOf(value.Status, "confirmed", "failed", "unknown") || len(value.Receipt) == 0 || isNullAuthorityJSON(value.Receipt) || !timestampAtOrAfter(value.UpdatedAt, latestTimestamp(resource.LeasedAt, resource.ClosureUpdatedAt)) || !timestampAtOrBefore(value.UpdatedAt, occurredAt) || !roleLeaseCovers(*state, resource.RoleID, value.UpdatedAt) {
			return fmt.Errorf("resource closure transition is invalid")
		}
		resource.ClosureStatus, resource.ClosureReceipt, resource.ClosureUpdatedAt = value.Status, value.Receipt, value.UpdatedAt
		state.Resources[resource.ID] = resource
		context.resourceObservations[resource.ID] = value
	case "resource.released":
		var value struct {
			ResourceID string `json:"resourceId"`
			ReleasedAt string `json:"releasedAt"`
		}
		_ = json.Unmarshal(event.Payload, &value)
		resource, ok := state.Resources[value.ResourceID]
		if !ok || resource.Status != "active" || resource.ClosureStatus != "confirmed" || !timestampAtOrAfter(value.ReleasedAt, resource.ClosureUpdatedAt) || !timestampAtOrBefore(value.ReleasedAt, occurredAt) {
			return fmt.Errorf("resource release requires a confirmed closure")
		}
		resource.Status, resource.ReleasedAt = "released", value.ReleasedAt
		state.Resources[resource.ID] = resource
		context.resourceReleases[resource.ID] = true
	case "action.applied":
		var action domain.ActionRecord
		_ = json.Unmarshal(event.Payload, &action)
		if err := validateLifecycleAction(action, *state, context); err != nil {
			return err
		}
		context.actionCount++
		context.actionKind = action.Kind
		context.actionID = action.ID
		if context.actionCount != 1 {
			return fmt.Errorf("one source command cannot apply more than one action")
		}
		if _, exists := state.Actions[action.ID]; exists {
			return fmt.Errorf("action identity is duplicated")
		}
		if action.AttemptID != "" {
			attempt, ok := state.Attempts[action.AttemptID]
			if !ok || action.NodeID == "" || attempt.NodeID != action.NodeID {
				return fmt.Errorf("action target is inconsistent")
			}
		}
		state.Actions[action.ID] = action
	case "effect.prepared":
		var effect domain.EffectAction
		_ = json.Unmarshal(event.Payload, &effect)
		attempt, ok := state.Attempts[effect.AttemptID]
		node, nodeOK := state.NodeDefinition(effect.NodeID)
		if effect.ID == "" || !ok || attempt.Status == "terminal" || !nodeOK || node.Kind != "effect" || attempt.NodeID != effect.NodeID || effect.OwnerRole != attempt.RoleID || effect.AdapterID == "" || effect.Status != "prepared" || effect.IdempotencyKey == "" || len(effect.Request) == 0 || len(effect.Prepared) == 0 || !timestampAtOrAfter(effect.UpdatedAt, effect.PreparedAt) || !timestampAtOrAfter(effect.PreparedAt, attempt.UpdatedAt) || !timestampAtOrBefore(effect.UpdatedAt, occurredAt) || !roleLeaseCovers(*state, attempt.RoleID, effect.PreparedAt) {
			return fmt.Errorf("effect preparation is inconsistent")
		}
		if _, exists := state.Effects[effect.ID]; exists || effectForAttempt(*state, effect.AttemptID) || state.Actions[effect.ID].ID != "" {
			return fmt.Errorf("effect identity or attempt is duplicated")
		}
		if err := validateLifecycleEffectDeclaration(effect, node); err != nil {
			return err
		}
		state.Effects[effect.ID] = effect
		context.effects[effect.ID] = effect
	case "effect.dispatched", "effect.reconciling":
		var value struct {
			ActionID      string `json:"actionId"`
			DispatchedAt  string `json:"dispatchedAt"`
			ReconcilingAt string `json:"reconcilingAt"`
		}
		_ = json.Unmarshal(event.Payload, &value)
		effect, ok := state.Effects[value.ActionID]
		timestamp := value.DispatchedAt
		if event.Type == "effect.reconciling" {
			timestamp = value.ReconcilingAt
		}
		action, actionExists := state.Actions[value.ActionID]
		allowed := event.Type == "effect.dispatched" && effect.Status == "prepared" && actionExists && action.Kind == "effect.prepare"
		if event.Type == "effect.reconciling" && actionExists && action.Kind == "effect.prepare" && oneOf(effect.Status, "prepared", "dispatched", "unknown", "failed", "reconciling") {
			incident, incidentExists := state.Incidents["effect:"+value.ActionID]
			allowed = !incidentExists || incident.Status == "open"
		}
		hasCapability := domain.RoleHasCapability(state.Graph, effect.OwnerRole, domain.CapabilityEffectApply)
		if event.Type == "effect.reconciling" {
			hasCapability = hasCapability || domain.RoleHasCapability(state.Graph, effect.OwnerRole, domain.CapabilityEffectReconcile)
		}
		if !ok || !allowed || !hasCapability || !roleLeaseCovers(*state, effect.OwnerRole, timestamp) || !timestampAtOrAfter(timestamp, effect.UpdatedAt) || !timestampAtOrBefore(timestamp, occurredAt) {
			return fmt.Errorf("effect transition is invalid")
		}
		if event.Type == "effect.dispatched" {
			effect.Status = "dispatched"
		} else {
			effect.Status = "reconciling"
			action.Status = "reconciling"
			state.Actions[action.ID] = action
		}
		effect.UpdatedAt = timestamp
		state.Effects[effect.ID] = effect
		context.effectAdmissions[effect.ID] = true
	case "effect.observed":
		var value lifecycleEffectObservation
		_ = json.Unmarshal(event.Payload, &value)
		var receipt sdk.EffectReceipt
		if err := decodeStrictAuthorityJSON(value.Receipt, &receipt); err != nil || receipt.Status != value.Status {
			return fmt.Errorf("effect observation receipt contradicts its status")
		}
		if _, err := validateEffectReceipt(receipt); err != nil {
			return fmt.Errorf("effect observation receipt is invalid: %w", err)
		}
		effect, ok := state.Effects[value.ActionID]
		if incident, exists := state.Incidents["effect:"+value.ActionID]; exists {
			copy := incident
			context.effectIncidentBefore[value.ActionID] = &copy
		} else {
			context.effectIncidentBefore[value.ActionID] = nil
		}
		if !ok || !context.effectAdmissions[value.ActionID] || !oneOf(effect.Status, "dispatched", "reconciling") || !oneOf(value.Status, "confirmed", "failed", "unknown", "reconciling") || len(value.Receipt) == 0 || string(value.Receipt) == "null" || !timestampAtOrAfter(value.UpdatedAt, effect.UpdatedAt) || !timestampAtOrBefore(value.UpdatedAt, occurredAt) {
			return fmt.Errorf("effect observation is invalid")
		}
		delete(context.effectAdmissions, value.ActionID)
		effect.Status, effect.Receipt, effect.UpdatedAt = value.Status, value.Receipt, value.UpdatedAt
		state.Effects[effect.ID] = effect
		if action, exists := state.Actions[effect.ID]; exists {
			action.Status = value.Status
			state.Actions[action.ID] = action
		}
		context.effectObservations[value.ActionID] = value
	case "attempt.finished":
		var value lifecycleAttemptFinish
		_ = json.Unmarshal(event.Payload, &value)
		attempt, ok := state.Attempts[value.AttemptID]
		node, nodeOK := state.NodeDefinition(attempt.NodeID)
		if !ok || !oneOf(attempt.Status, "running", "waiting", "submitted") || !nodeOK || outcomeClass(node, value.Outcome) != value.OutcomeClass || !timestampAtOrAfter(value.UpdatedAt, attempt.UpdatedAt) || !timestampAtOrBefore(value.UpdatedAt, occurredAt) || !roleLeaseCovers(*state, attempt.RoleID, value.UpdatedAt) || activeResourceIDs(*state, attempt.ID) != nil {
			return fmt.Errorf("attempt completion is inconsistent")
		}
		if node.Kind == "task" && value.OutcomeClass == "success" && attempt.Status != "submitted" {
			return fmt.Errorf("successful task completion requires a submitted attempt")
		}
		if value.OutcomeClass == "retryable" && attempt.Number > node.RetryBudget {
			return fmt.Errorf("retry budget is exhausted")
		}
		if nodeRequiresDecision(node) {
			decisionIDs := state.AttemptDecisions[attempt.ID]
			if len(decisionIDs) != 1 {
				return fmt.Errorf("semantic completion requires exactly one decision record")
			}
			decision := state.Decisions[decisionIDs[0]]
			if decision.AttemptID != attempt.ID || decision.RoleID != attempt.RoleID || decision.Outcome != value.Outcome || !reflect.DeepEqual(decision.Facts, value.Facts) {
				return fmt.Errorf("semantic completion contradicts its decision record")
			}
		}
		if node.Kind == "effect" {
			effect, exists := state.EffectForAttempt(attempt.ID)
			if !exists || (value.OutcomeClass == "success" && effect.Status != "confirmed") || (value.OutcomeClass == "failure" && effect.Status != "failed") {
				return fmt.Errorf("effect completion does not match its receipt")
			}
		}
		attempt.Status, attempt.Outcome, attempt.UpdatedAt = "terminal", value.Outcome, value.UpdatedAt
		state.Attempts[attempt.ID] = attempt
		status := "terminal"
		if value.OutcomeClass == "retryable" {
			status = "planned"
		}
		state.Nodes[attempt.NodeID] = domain.NodeRuntime{Status: status, Outcome: value.Outcome, OutcomeClass: value.OutcomeClass, Facts: value.Facts}
		context.finishes[attempt.ID] = value
	case "incident.opened", "incident.updated":
		var incident domain.Incident
		_ = json.Unmarshal(event.Payload, &incident)
		var prior *domain.Incident
		if current, exists := state.Incidents[incident.ID]; exists {
			copy := current
			prior = &copy
		}
		if err := validateLifecycleIncident(incident, *state, event.Type, occurredAt, context); err != nil {
			return err
		}
		current, exists := state.Incidents[incident.ID]
		if event.Type == "incident.opened" && exists {
			if current.Status != "open" || current.SourceType != "effect" || current.SourceType != incident.SourceType || current.SourceID != incident.SourceID || current.NodeID != incident.NodeID || current.OwnerRole != incident.OwnerRole || current.Classification != incident.Classification || current.OpenedAt != incident.OpenedAt || !timestampAtOrAfter(incident.UpdatedAt, current.UpdatedAt) {
				return fmt.Errorf("reopened incident changes immutable identity or time")
			}
		}
		if event.Type == "incident.updated" {
			if !exists || current.Status == "resolved" || current.SourceType != incident.SourceType || current.SourceID != incident.SourceID || current.NodeID != incident.NodeID || current.OwnerRole != incident.OwnerRole || current.Classification != incident.Classification || current.OpenedAt != incident.OpenedAt || !timestampAtOrAfter(incident.UpdatedAt, current.UpdatedAt) {
				return fmt.Errorf("incident update changes immutable identity or time")
			}
		}
		state.Incidents[incident.ID] = incident
		if _, duplicate := context.incidentEvents[incident.ID]; duplicate {
			return fmt.Errorf("one source command cannot emit multiple incident bodies for %s", incident.ID)
		}
		context.incidentEvents[incident.ID] = lifecycleIncidentEvent{Type: event.Type, Incident: incident, Prior: prior}
	case "incident.resolved":
		var value struct {
			IncidentID string `json:"incidentId"`
			ResolvedAt string `json:"resolvedAt"`
		}
		_ = json.Unmarshal(event.Payload, &value)
		incident, ok := state.Incidents[value.IncidentID]
		causedByConfirmedObservation := false
		switch incident.SourceType {
		case "resource":
			observation, observedHere := context.resourceObservations[incident.SourceID]
			resource, resourceOK := state.Resources[incident.SourceID]
			causedByConfirmedObservation = resourceOK && observedHere && observation.Status == "confirmed" && context.resourceReleases[incident.SourceID] && resource.Status == "released"
		case "effect":
			observation, observedHere := context.effectObservations[incident.SourceID]
			causedByConfirmedObservation = observedHere && observation.Status == "confirmed"
		}
		if !ok || incident.Status == "resolved" || !causedByConfirmedObservation || !timestampAtOrAfter(value.ResolvedAt, incident.UpdatedAt) || !timestampAtOrBefore(value.ResolvedAt, occurredAt) {
			return fmt.Errorf("incident resolution is invalid")
		}
		incident.Status, incident.UpdatedAt = "resolved", value.ResolvedAt
		state.Incidents[incident.ID] = incident
		if _, duplicate := context.incidentResolutions[value.IncidentID]; duplicate {
			return fmt.Errorf("one source command cannot resolve incident %s more than once", value.IncidentID)
		}
		context.incidentResolutions[value.IncidentID] = value.ResolvedAt
	default:
		return fmt.Errorf("unsupported native event")
	}
	return nil
}

func legalAttemptStatusTransition(from, to string) bool {
	return (from == "leased" && to == "running") || (from == "running" && (to == "waiting" || to == "submitted")) || (from == "waiting" && to == "running")
}

func timestampAtOrAfter(value, minimum string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return false
	}
	if minimum == "" {
		return true
	}
	base, err := time.Parse(time.RFC3339Nano, minimum)
	return err == nil && !parsed.Before(base)
}

func timestampAtOrBefore(value, maximum string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return false
	}
	limit, err := time.Parse(time.RFC3339Nano, maximum)
	return err == nil && !parsed.After(limit)
}

func roleLeaseCovers(state domain.State, roleID, at string) bool {
	lease, ok := state.Leases[roleID]
	if !ok || !lease.Active || !timestampAtOrAfter(at, lease.BoundAt) {
		return false
	}
	instant, instantErr := time.Parse(time.RFC3339Nano, at)
	expires, expiresErr := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
	return instantErr == nil && expiresErr == nil && instant.Before(expires)
}

func latestTimestamp(first, second string) string {
	if second == "" || timestampAtOrAfter(first, second) {
		return first
	}
	return second
}

func graphHasRole(state domain.State, roleID string) bool {
	if state.Graph == nil {
		return false
	}
	for _, role := range state.Graph.Spec.Roles {
		if role.ID == roleID {
			return true
		}
	}
	return false
}

func nodeRequestsResource(node domain.NodeDefinition, kind string, quantity int) bool {
	for _, request := range node.Resources {
		if request.Kind == kind && request.Quantity == quantity {
			return true
		}
	}
	return false
}

func resourceCapacity(state domain.State, kind string) int {
	for _, capacity := range state.Graph.Spec.ResourceCapacities {
		if capacity.Kind == kind {
			return capacity.Capacity
		}
	}
	return 0
}

func activeResourceQuantity(state domain.State, kind string) int {
	total := 0
	for _, resource := range state.Resources {
		if resource.Kind == kind && resource.Status == "active" {
			total += resource.Quantity
		}
	}
	return total
}

func resourceForAttempt(state domain.State, attemptID, kind string) bool {
	for _, resource := range state.Resources {
		if resource.AttemptID == attemptID && resource.Kind == kind {
			return true
		}
	}
	return false
}

func effectForAttempt(state domain.State, attemptID string) bool {
	for _, effect := range state.Effects {
		if effect.AttemptID == attemptID {
			return true
		}
	}
	return false
}

func validateLifecycleEffectDeclaration(effect domain.EffectAction, node domain.NodeDefinition) error {
	var declaration effectNodeInput
	if err := json.Unmarshal(node.Inputs, &declaration); err != nil || declaration.Adapter == "" || len(declaration.Request) == 0 || isNullAuthorityJSON(declaration.Request) {
		return fmt.Errorf("effect node declaration is invalid")
	}
	if effect.AdapterID != declaration.Adapter || !validEffectAdapterMetadataPair(effect) || !canonicalAuthorityJSONEqual(effect.Request, declaration.Request) {
		return fmt.Errorf("effect preparation does not match its graph declaration")
	}
	var prepared sdk.PreparedEffect
	if err := decodeStrictAuthorityJSON(effect.Prepared, &prepared); err != nil || prepared.AdapterID != effect.AdapterID {
		return fmt.Errorf("effect preparation binding is invalid")
	}
	return nil
}

func validEffectAdapterMetadataPair(effect domain.EffectAction) bool {
	return (effect.AdapterVersion == "") == (effect.AdapterSchemaHash == "")
}

func canonicalAuthorityJSONEqual(first, second json.RawMessage) bool {
	left, leftErr := jcs.Transform(first)
	right, rightErr := jcs.Transform(second)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func validateLifecycleAction(action domain.ActionRecord, state domain.State, context *lifecycleRecordContext) error {
	if action.ID == "" || action.NodeID == "" || action.AttemptID == "" || len(action.Input) == 0 || domain.ValidateAuthorityJSON(action.Input) != nil {
		return fmt.Errorf("action record is incomplete")
	}
	attempt, ok := state.Attempts[action.AttemptID]
	node, nodeOK := state.NodeDefinition(action.NodeID)
	if !ok || !nodeOK || attempt.NodeID != action.NodeID {
		return fmt.Errorf("action target is inconsistent")
	}
	if err := validateAllowedActionInput(persistedActionInputSchema(action.Kind, node), action.Input); err != nil {
		return fmt.Errorf("action input is outside its writer schema: %w", err)
	}
	confirmed := lifecycleConfirmedActionKinds()
	completionKinds := map[string]bool{"task.complete": true, "review.resolve": true, "decision.record": true, "effect.complete": true, "attempt.finish": true, "gate.evaluate": true}
	if completionKinds[action.Kind] && action.Kind != completionAction(node) {
		return fmt.Errorf("completion action does not match node kind")
	}
	if confirmed[action.Kind] && action.Status != "confirmed" {
		return fmt.Errorf("confirmed action status is invalid")
	}
	switch action.Kind {
	case "node.start":
		introduced, introducedHere := context.attempts[action.AttemptID]
		status, startedHere := context.statuses[action.AttemptID]
		if attempt.Number < 1 || attempt.Status != "running" || !introducedHere || introduced.NodeID != action.NodeID || !startedHere || status.Status != "running" {
			return fmt.Errorf("node start action does not reference an introduced attempt")
		}
		resourceCount := 0
		for resourceID, resource := range context.resourceLeases {
			if resource.AttemptID == action.AttemptID {
				delete(context.resourceLeases, resourceID)
				resourceCount++
			}
		}
		if resourceCount != len(node.Resources) {
			return fmt.Errorf("node start action does not contain its declared resource leases")
		}
		delete(context.attempts, action.AttemptID)
		delete(context.statuses, action.AttemptID)
	case "attempt.checkpoint":
		var input struct {
			Summary      string               `json:"summary"`
			EvidenceRefs []domain.EvidenceRef `json:"evidenceRefs"`
		}
		checkpoint, exists := context.checkpoints[attempt.CheckpointID]
		if attempt.CheckpointID == "" || !exists || json.Unmarshal(action.Input, &input) != nil || input.Summary != checkpoint.Summary || !reflect.DeepEqual(input.EvidenceRefs, checkpoint.EvidenceRefs) {
			return fmt.Errorf("checkpoint action has no checkpoint event")
		}
		delete(context.checkpoints, attempt.CheckpointID)
	case "attempt.wait":
		status, exists := context.statuses[action.AttemptID]
		if attempt.Status != "waiting" || !exists || status.Status != "waiting" {
			return fmt.Errorf("wait action has no matching status transition")
		}
		delete(context.statuses, action.AttemptID)
	case "attempt.resume":
		status, exists := context.statuses[action.AttemptID]
		if attempt.Status != "running" || !exists || status.Status != "running" {
			return fmt.Errorf("resume action has no matching status transition")
		}
		delete(context.statuses, action.AttemptID)
	case "attempt.submit":
		status, exists := context.statuses[action.AttemptID]
		if attempt.Status != "submitted" || !exists || status.Status != "submitted" {
			return fmt.Errorf("submit action has no matching status transition")
		}
		delete(context.statuses, action.AttemptID)
	case "evidence.publish":
		var input struct {
			PackageID string `json:"packageId"`
		}
		if decodeStrictAuthorityJSON(action.Input, &input) != nil {
			return fmt.Errorf("evidence action input is invalid")
		}
		pack, exists := context.packages[input.PackageID]
		if !exists || pack.AttemptID != action.AttemptID {
			return fmt.Errorf("evidence action has no execution package event")
		}
		delete(context.packages, input.PackageID)
	case "evidence.assess-reuse":
		var input struct {
			DecisionID string `json:"decisionId"`
		}
		if decodeStrictAuthorityJSON(action.Input, &input) != nil {
			return fmt.Errorf("reuse action input is invalid")
		}
		decision, exists := context.reuseDecisions[input.DecisionID]
		if !exists || decision.AssessedByAttempt != action.AttemptID {
			return fmt.Errorf("reuse action has no decision event")
		}
		delete(context.reuseDecisions, input.DecisionID)
	case "effect.prepare":
		if node.Kind != "effect" || action.Status != "prepared" {
			return fmt.Errorf("effect action kind or status is invalid")
		}
		effect, exists := state.Effects[action.ID]
		prepared, preparedHere := context.effects[action.ID]
		if !exists || !preparedHere || prepared.NodeID != action.NodeID || prepared.AttemptID != action.AttemptID || effect.NodeID != action.NodeID || effect.AttemptID != action.AttemptID || effect.Status != "prepared" {
			return fmt.Errorf("effect action does not match its prepared effect")
		}
		delete(context.effects, action.ID)
	case "resource.close", "resource.reconcile":
		if !oneOf(action.Status, "confirmed", "failed", "unknown") {
			return fmt.Errorf("resource action status is invalid")
		}
		var input struct {
			ResourceID string `json:"resourceId"`
		}
		if decodeStrictAuthorityJSON(action.Input, &input) != nil || input.ResourceID == "" {
			return fmt.Errorf("resource action target is invalid")
		}
		resource, exists := state.Resources[input.ResourceID]
		before, hadBefore := context.resourceBefore[input.ResourceID]
		observation, observedHere := context.resourceObservations[input.ResourceID]
		kindMatches := hadBefore && ((action.Kind == "resource.close" && before.ClosureStatus == "pending") || (action.Kind == "resource.reconcile" && oneOf(before.ClosureStatus, "unknown", "failed", "reconciling")))
		if !exists || !observedHere || !kindMatches || observation.Status != action.Status || resource.NodeID != action.NodeID || resource.AttemptID != action.AttemptID || resource.ClosureStatus != action.Status || (action.Status == "confirmed" && (!context.resourceReleases[input.ResourceID] || resource.Status != "released")) || (action.Status != "confirmed" && resource.Status != "active") {
			return fmt.Errorf("resource action does not match its closure events")
		}
		if err := consumeResourceIncidentEvent(state, context, resource, observation); err != nil {
			return err
		}
		delete(context.resourceObservations, input.ResourceID)
		delete(context.resourceBefore, input.ResourceID)
		delete(context.resourceIncidentBefore, input.ResourceID)
		delete(context.resourceReleases, input.ResourceID)
	default:
		if !confirmed[action.Kind] || action.Status != "confirmed" {
			return fmt.Errorf("action kind or status is outside the closed lifecycle vocabulary")
		}
		if completionKinds[action.Kind] {
			finish, finishedHere := context.finishes[action.AttemptID]
			if attempt.Status != "terminal" || !finishedHere {
				return fmt.Errorf("completion action has no matching terminal event")
			}
			expectedClassification := ""
			if nodeRequiresDecision(node) {
				var input struct {
					DecisionID string `json:"decisionId"`
				}
				if decodeStrictAuthorityJSON(action.Input, &input) != nil {
					return fmt.Errorf("semantic completion action input is invalid")
				}
				decision, decisionHere := context.decisions[input.DecisionID]
				if !decisionHere || decision.AttemptID != action.AttemptID || decision.Outcome != finish.Outcome || !reflect.DeepEqual(decision.Facts, finish.Facts) {
					return fmt.Errorf("semantic completion action does not match its decision event")
				}
				delete(context.decisions, input.DecisionID)
				if node.Kind == "gate" {
					expectedClassification = "work-product"
				}
			} else {
				input, inputErr := decodeCompletionInput(action.Input)
				if inputErr == nil {
					input.Outcome, inputErr = resolveCompletionOutcome(node, input.Outcome, input.OutcomeRef)
				}
				if inputErr != nil || input.Outcome != finish.Outcome || !reflect.DeepEqual(input.Facts, finish.Facts) {
					return fmt.Errorf("completion action input contradicts its terminal event")
				}
				expectedClassification = input.Classification
				if expectedClassification == "" {
					expectedClassification = "work-product"
				}
			}
			if err := consumeCompletionIncidentEvent(state, context, node, action, finish, expectedClassification); err != nil {
				return err
			}
			delete(context.finishes, action.AttemptID)
		}
	}
	return nil
}

func persistedActionInputSchema(kind string, node domain.NodeDefinition) map[string]any {
	closedRef := func(name string) map[string]any {
		return map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{name},
			"properties": map[string]any{
				name: map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
			},
		}
	}
	switch kind {
	case "node.start", "attempt.checkpoint", "attempt.wait", "attempt.resume", "attempt.submit", "task.complete", "effect.prepare", "effect.complete", "attempt.finish":
		// Released pre-v0.23 writers kept these caller inputs verbatim and either
		// ignored them or decoded only their known semantic fields. Migration
		// validates those fields against the companion native events below; it
		// must not retroactively apply the newer closed caller schema to immutable
		// history (including previously ignored fields or scalar no-op inputs).
		return map[string]any{}
	case "evidence.publish":
		return closedRef("packageId")
	case "evidence.assess-reuse", "review.resolve", "decision.record", "gate.evaluate":
		return closedRef("decisionId")
	case "resource.close", "resource.reconcile":
		return closedRef("resourceId")
	default:
		return actionInputSchema(kind, node)
	}
}

func validateLifecycleRecordClosed(state *domain.State, context *lifecycleRecordContext) error {
	if context.actionCount > 1 {
		return fmt.Errorf("one source command contains multiple actions")
	}
	if context.actionCount == 1 {
		if err := validateLifecycleActionEventShape(*state, context); err != nil {
			return err
		}
		return ensureLifecycleProofLedgerEmpty(context)
	}

	if len(context.effectObservations) != 0 {
		if len(context.effectObservations) != 1 {
			return fmt.Errorf("one effect observation command must identify exactly one effect")
		}
		expected := map[string]int{"effect.observed": 1}
		for actionID, observation := range context.effectObservations {
			prior := context.effectIncidentBefore[actionID]
			if oneOf(observation.Status, "unknown", "failed") {
				expected["incident.opened"] = 1
			} else if observation.Status == "confirmed" && prior != nil {
				expected["incident.resolved"] = 1
			}
			if err := consumeEffectIncidentEvent(*state, context, actionID, observation); err != nil {
				return err
			}
			delete(context.effectObservations, actionID)
			delete(context.effectIncidentBefore, actionID)
		}
		if !reflect.DeepEqual(lifecycleEventCounts(context.eventTypes), expected) {
			return fmt.Errorf("effect observation command contains unrelated native events")
		}
		return ensureLifecycleProofLedgerEmpty(context)
	}

	if len(context.eventTypes) == 1 && context.eventTypes[0] == "incident.updated" && len(context.incidentEvents) == 1 {
		for incidentID, event := range context.incidentEvents {
			if event.Type != "incident.updated" || event.Prior == nil {
				return fmt.Errorf("standalone incident command is not an update")
			}
			delete(context.incidentEvents, incidentID)
		}
		return ensureLifecycleProofLedgerEmpty(context)
	}

	if len(context.eventTypes) == 1 && oneOf(context.eventTypes[0], "role.bound", "role.released", "effect.dispatched", "effect.reconciling") {
		return ensureLifecycleProofLedgerEmpty(context)
	}
	return fmt.Errorf("native events do not form one closed current-writer command")
}

func validateLifecycleActionEventShape(state domain.State, context *lifecycleRecordContext) error {
	action, ok := state.Actions[context.actionID]
	if !ok || action.Kind != context.actionKind {
		return fmt.Errorf("applied action is unavailable at record end")
	}
	node, _ := state.NodeDefinition(action.NodeID)
	expected := map[string]int{"action.applied": 1}
	actual := lifecycleEventCounts(context.eventTypes)
	switch action.Kind {
	case "node.start":
		expected["attempt.leased"] = 1
		expected["attempt.status-changed"] = 1
		if len(node.Resources) != 0 {
			expected["resource.leased"] = len(node.Resources)
		}
	case "attempt.checkpoint":
		expected["attempt.checkpointed"] = 1
	case "attempt.wait", "attempt.resume", "attempt.submit":
		expected["attempt.status-changed"] = 1
	case "evidence.publish":
		expected["evidence.package-published"] = 1
	case "evidence.assess-reuse":
		expected["evidence.reuse-assessed"] = 1
	case "effect.prepare":
		expected["effect.prepared"] = 1
	case "resource.close", "resource.reconcile":
		expected["resource.closure-observed"] = 1
		var input struct {
			ResourceID string `json:"resourceId"`
		}
		_ = json.Unmarshal(action.Input, &input)
		observation := state.Resources[input.ResourceID]
		if observation.Status == "released" {
			expected["resource.released"] = 1
		}
		for _, eventType := range []string{"incident.opened", "incident.updated", "incident.resolved"} {
			if actual[eventType] != 0 {
				expected[eventType] = actual[eventType]
			}
		}
	case "task.complete", "review.resolve", "decision.record", "gate.evaluate", "effect.complete", "attempt.finish":
		expected["attempt.finished"] = 1
		if nodeRequiresDecision(node) {
			expected["decision.recorded"] = 1
		}
		if actual["incident.opened"] != 0 {
			expected["incident.opened"] = actual["incident.opened"]
		}
	default:
		return fmt.Errorf("action kind is outside the closed writer command shapes")
	}
	if actual["attempt.started"] == 1 && expected["attempt.leased"] == 1 {
		delete(actual, "attempt.started")
		actual["attempt.leased"] = 1
	}
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("action %s native event shape is not writer-equivalent", action.Kind)
	}
	return nil
}

func lifecycleEventCounts(events []string) map[string]int {
	result := map[string]int{}
	for _, event := range events {
		result[event]++
	}
	return result
}

func ensureLifecycleProofLedgerEmpty(context *lifecycleRecordContext) error {
	if len(context.attempts)+len(context.statuses)+len(context.checkpoints)+len(context.packages)+len(context.reuseDecisions)+len(context.decisions)+len(context.resourceLeases)+len(context.resourceBefore)+len(context.resourceObservations)+len(context.resourceIncidentBefore)+len(context.resourceReleases)+len(context.effects)+len(context.effectObservations)+len(context.effectIncidentBefore)+len(context.finishes)+len(context.incidentEvents)+len(context.incidentResolutions) != 0 {
		return fmt.Errorf("source command has unconsumed, duplicated, or missing lifecycle proof")
	}
	return nil
}

func consumeCompletionIncidentEvent(state domain.State, context *lifecycleRecordContext, node domain.NodeDefinition, action domain.ActionRecord, finish lifecycleAttemptFinish, expectedClassification string) error {
	incidentID := "attempt:" + action.AttemptID
	event, exists := context.incidentEvents[incidentID]
	needsIncident := finish.OutcomeClass == "failure" || finish.OutcomeClass == "cancelled"
	if !needsIncident {
		if exists {
			return fmt.Errorf("successful or retryable completion cannot open an attempt incident")
		}
		return nil
	}
	if !exists || event.Type != "incident.opened" || event.Prior != nil {
		return fmt.Errorf("failed completion requires one new attempt incident")
	}
	attempt := state.Attempts[action.AttemptID]
	completedAt, err := time.Parse(time.RFC3339Nano, finish.UpdatedAt)
	if err != nil {
		return fmt.Errorf("completion incident time is invalid")
	}
	if expectedClassification == "" {
		expectedClassification = event.Incident.Classification
	}
	expected := domain.Incident{
		ID: incidentID, SourceType: "attempt", SourceID: action.AttemptID, NodeID: node.ID, OwnerRole: attempt.RoleID,
		Status: "open", Classification: expectedClassification, Deadline: completedAt.Add(time.Hour).Format(time.RFC3339Nano),
		AttemptBudget: 2, ProgressMetric: "new candidate or changed failure classification",
		DependencyCut: domain.DependencyCut(state, node.ID), OpenedAt: finish.UpdatedAt, UpdatedAt: finish.UpdatedAt,
	}
	normalizeIncidentForWriterComparison(&expected)
	if !reflect.DeepEqual(event.Incident, expected) {
		return fmt.Errorf("attempt incident is not the deterministic writer companion: got %+v want %+v", event.Incident, expected)
	}
	delete(context.incidentEvents, incidentID)
	return nil
}

func consumeResourceIncidentEvent(state domain.State, context *lifecycleRecordContext, resource domain.ResourceLease, observation lifecycleResourceObservation) error {
	incidentID := "resource:" + resource.ID
	prior := context.resourceIncidentBefore[resource.ID]
	if prior != nil && prior.Status != "open" {
		return fmt.Errorf("resource action cannot bypass an incident circuit")
	}
	if observation.Status == "confirmed" {
		if _, extra := context.incidentEvents[incidentID]; extra {
			return fmt.Errorf("confirmed resource closure cannot update an incident body")
		}
		resolvedAt, resolved := context.incidentResolutions[incidentID]
		if prior == nil && resolved {
			return fmt.Errorf("resource closure resolved an incident that was not open")
		}
		if prior != nil && (!resolved || resolvedAt != observation.UpdatedAt) {
			return fmt.Errorf("confirmed resource closure must resolve its existing incident at the observation time")
		}
		delete(context.incidentResolutions, incidentID)
		return nil
	}
	if _, resolved := context.incidentResolutions[incidentID]; resolved {
		return fmt.Errorf("unconfirmed resource closure cannot resolve an incident")
	}
	event, exists := context.incidentEvents[incidentID]
	if !exists {
		return fmt.Errorf("unconfirmed resource closure requires an incident companion")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, observation.UpdatedAt)
	if err != nil {
		return fmt.Errorf("resource observation time is invalid")
	}
	expected := domain.Incident{
		ID: incidentID, SourceType: "resource", SourceID: resource.ID, NodeID: resource.NodeID, OwnerRole: resource.RoleID,
		Status: "open", Classification: "infrastructure", Deadline: observedAt.Add(time.Hour).Format(time.RFC3339Nano),
		AttemptBudget: 2, Attempts: 1, NoProgressAttempts: 1,
		ProgressMetric: "new closure receipt or changed observation", DependencyCut: domain.DependencyCut(state, resource.NodeID),
		OpenedAt: observation.UpdatedAt, UpdatedAt: observation.UpdatedAt,
	}
	normalizeIncidentForWriterComparison(&expected)
	expectedType := "incident.opened"
	if prior != nil {
		expectedType = "incident.updated"
		expected.OpenedAt = prior.OpenedAt
		expected.Deadline = prior.Deadline
		expected.Attempts = prior.Attempts + 1
		expected.NoProgressAttempts = prior.NoProgressAttempts + 1
		expected.LastProgress, expected.LastProgressAt = prior.LastProgress, prior.LastProgressAt
		expected.Disposition, expected.DispositionBy, expected.DispositionAt = prior.Disposition, prior.DispositionBy, prior.DispositionAt
		if expected.NoProgressAttempts >= expected.AttemptBudget {
			expected.Status, expected.CircuitReason = "circuit-open", "resource_closure_attempt_budget_exhausted"
		}
	}
	if event.Type != expectedType || !reflect.DeepEqual(event.Incident, expected) {
		return fmt.Errorf("resource incident is not the deterministic writer companion: got %s %+v want %s %+v", event.Type, event.Incident, expectedType, expected)
	}
	delete(context.incidentEvents, incidentID)
	return nil
}

func consumeEffectIncidentEvent(state domain.State, context *lifecycleRecordContext, actionID string, observation lifecycleEffectObservation) error {
	incidentID := "effect:" + actionID
	prior := context.effectIncidentBefore[actionID]
	if prior != nil && prior.Status != "open" {
		return fmt.Errorf("effect observation cannot bypass an incident circuit")
	}
	if observation.Status == "confirmed" {
		if _, extra := context.incidentEvents[incidentID]; extra {
			return fmt.Errorf("confirmed effect observation cannot update an incident body")
		}
		resolvedAt, resolved := context.incidentResolutions[incidentID]
		if prior == nil && resolved {
			return fmt.Errorf("effect observation resolved an incident that was not open")
		}
		if prior != nil && (!resolved || resolvedAt != observation.UpdatedAt) {
			return fmt.Errorf("confirmed effect observation must resolve its existing incident at the observation time")
		}
		delete(context.incidentResolutions, incidentID)
		return nil
	}
	if observation.Status == "reconciling" {
		if _, incident := context.incidentEvents[incidentID]; incident {
			return fmt.Errorf("reconciling receipt cannot mutate an incident")
		}
		if _, resolved := context.incidentResolutions[incidentID]; resolved {
			return fmt.Errorf("reconciling receipt cannot resolve an incident")
		}
		return nil
	}
	if _, resolved := context.incidentResolutions[incidentID]; resolved {
		return fmt.Errorf("unconfirmed effect observation cannot resolve an incident")
	}
	event, exists := context.incidentEvents[incidentID]
	effect := state.Effects[actionID]
	if !exists || effect.ID == "" {
		return fmt.Errorf("unknown or failed effect observation requires an incident companion")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, observation.UpdatedAt)
	if err != nil {
		return fmt.Errorf("effect observation time is invalid")
	}
	expected := domain.Incident{
		ID: incidentID, SourceType: "effect", SourceID: actionID, NodeID: effect.NodeID, OwnerRole: effect.OwnerRole,
		Status: "open", Classification: "external-effect", Deadline: observedAt.Add(time.Hour).Format(time.RFC3339Nano),
		AttemptBudget: 2, ProgressMetric: "new external receipt or deterministic reconcile result",
		DependencyCut: domain.DependencyCut(state, effect.NodeID), OpenedAt: observation.UpdatedAt, UpdatedAt: observation.UpdatedAt,
	}
	normalizeIncidentForWriterComparison(&expected)
	if prior != nil {
		expected.OpenedAt, expected.Attempts = prior.OpenedAt, prior.Attempts+1
		expected.Deadline = prior.Deadline
		expected.NoProgressAttempts = prior.NoProgressAttempts
		expected.LastProgress, expected.LastProgressAt = prior.LastProgress, prior.LastProgressAt
		expected.Disposition, expected.DispositionBy, expected.DispositionAt = prior.Disposition, prior.DispositionBy, prior.DispositionAt
	}
	if expected.Attempts >= expected.AttemptBudget {
		expected.Status, expected.CircuitReason = "circuit-open", "effect_attempt_budget_exhausted"
	}
	if event.Type != "incident.opened" || !reflect.DeepEqual(event.Incident, expected) {
		return fmt.Errorf("effect incident is not the deterministic writer companion: got %s %+v want incident.opened %+v", event.Type, event.Incident, expected)
	}
	delete(context.incidentEvents, incidentID)
	return nil
}

func normalizeIncidentForWriterComparison(incident *domain.Incident) {
	if len(incident.DependencyCut) == 0 {
		incident.DependencyCut = nil
	}
}

func nodeRequiresDecision(node domain.NodeDefinition) bool {
	return node.Kind == "review" || node.Kind == "decision" || (node.Kind == "gate" && node.Decision != nil)
}

func lifecycleConfirmedActionKinds() map[string]bool {
	return map[string]bool{
		"node.start": true, "attempt.checkpoint": true, "evidence.publish": true,
		"evidence.assess-reuse": true, "attempt.wait": true, "attempt.resume": true,
		"attempt.submit": true, "task.complete": true, "review.resolve": true,
		"decision.record": true, "effect.complete": true, "attempt.finish": true,
		"gate.evaluate": true,
	}
}

func validateLifecycleActionFinal(action domain.ActionRecord, state domain.State) error {
	attempt, ok := state.Attempts[action.AttemptID]
	node, nodeOK := state.NodeDefinition(action.NodeID)
	if action.ID == "" || action.NodeID == "" || action.AttemptID == "" || !ok || !nodeOK || attempt.NodeID != action.NodeID {
		return fmt.Errorf("action target is inconsistent")
	}
	completionKinds := map[string]bool{"task.complete": true, "review.resolve": true, "decision.record": true, "effect.complete": true, "attempt.finish": true, "gate.evaluate": true}
	if completionKinds[action.Kind] && action.Kind != completionAction(node) {
		return fmt.Errorf("completion action does not match node kind")
	}
	switch action.Kind {
	case "effect.prepare":
		if node.Kind != "effect" || !oneOf(action.Status, "prepared", "confirmed", "failed", "unknown", "reconciling") {
			return fmt.Errorf("effect action kind or status is invalid")
		}
	case "resource.close", "resource.reconcile":
		if !oneOf(action.Status, "confirmed", "failed", "unknown") {
			return fmt.Errorf("resource action status is invalid")
		}
	default:
		if !lifecycleConfirmedActionKinds()[action.Kind] || action.Status != "confirmed" {
			return fmt.Errorf("action kind or status is outside the closed lifecycle vocabulary")
		}
	}
	return nil
}

func lifecycleEffectActionStatusCompatible(effectStatus, actionStatus string) bool {
	switch effectStatus {
	case "prepared", "dispatched":
		return actionStatus == "prepared"
	case "confirmed", "failed", "unknown", "reconciling":
		return actionStatus == effectStatus
	default:
		return false
	}
}

func validateLifecycleIncident(incident domain.Incident, state domain.State, eventType, occurredAt string, context *lifecycleRecordContext) error {
	allowedStatus := oneOf(incident.Status, "open", "circuit-open")
	if eventType == "incident.updated" {
		allowedStatus = oneOf(incident.Status, "open", "circuit-open", "resolved")
	}
	if incident.ID == "" || !oneOf(incident.SourceType, "attempt", "effect", "resource") || incident.SourceID == "" || !allowedStatus || !domain.ValidIncidentClassification(incident.Classification) || incident.AttemptBudget < 1 || incident.Attempts < 0 || incident.NoProgressAttempts < 0 || !validTimestamp(incident.OpenedAt) || !timestampAtOrAfter(incident.UpdatedAt, incident.OpenedAt) || !timestampAtOrBefore(incident.UpdatedAt, occurredAt) {
		return fmt.Errorf("incident fields are invalid")
	}
	if incident.Status == "resolved" && incident.Resolution == "" {
		return fmt.Errorf("resolved incident requires a resolution")
	}
	if incident.LastProgressAt != "" && (!timestampAtOrAfter(incident.LastProgressAt, incident.OpenedAt) || !timestampAtOrBefore(incident.LastProgressAt, incident.UpdatedAt)) {
		return fmt.Errorf("incident progress time is invalid")
	}
	if incident.DispositionAt != "" && (!timestampAtOrAfter(incident.DispositionAt, incident.OpenedAt) || !timestampAtOrBefore(incident.DispositionAt, incident.UpdatedAt)) {
		return fmt.Errorf("incident disposition time is invalid")
	}
	hasRepairBinding := incident.RemedyNodeID != "" || incident.SupersededAt != ""
	if incident.Resolution == incidentResolutionSupersededByRepair && hasRepairBinding {
		if incident.SourceType != "attempt" || incident.Status != "resolved" || incident.RemedyNodeID == "" || incident.SupersededAt != incident.UpdatedAt || !validIncidentSuccessor(state, incident, incident.RemedyNodeID) {
			return fmt.Errorf("incident repair successor is invalid")
		}
	} else if hasRepairBinding {
		return fmt.Errorf("incident repair binding requires the reserved successor resolution")
	}
	if incident.NodeID != "" {
		if _, ok := state.NodeDefinition(incident.NodeID); !ok {
			return fmt.Errorf("incident references an unknown node")
		}
	}
	if incident.OwnerRole != "" && !graphHasRole(state, incident.OwnerRole) {
		return fmt.Errorf("incident references an unknown role")
	}
	if eventType == "incident.updated" {
		observation, automaticResourceUpdate := context.resourceObservations[incident.SourceID]
		automaticResourceUpdate = automaticResourceUpdate && incident.SourceType == "resource" && oneOf(observation.Status, "failed", "unknown") && observation.UpdatedAt == incident.UpdatedAt
		capability := domain.CapabilityIncidentManage
		if automaticResourceUpdate {
			capability = domain.CapabilityResourceClose
		} else if incident.OwnerRole == "" || !roleLeaseCovers(state, incident.OwnerRole, incident.UpdatedAt) {
			return fmt.Errorf("incident update is outside its owner role binding")
		}
		if !domain.RoleHasCapability(state.Graph, incident.OwnerRole, capability) {
			return fmt.Errorf("incident update owner lacks capability %s", capability)
		}
		if !automaticResourceUpdate {
			prior, exists := state.Incidents[incident.ID]
			if !exists || !validExplicitIncidentUpdate(prior, incident) {
				return fmt.Errorf("incident update is not a current writer transition")
			}
		}
	}
	if incident.Deadline != "" && !timestampAtOrAfter(incident.Deadline, incident.OpenedAt) {
		return fmt.Errorf("incident deadline is invalid")
	}
	switch incident.SourceType {
	case "attempt":
		attempt, ok := state.Attempts[incident.SourceID]
		if !ok || (incident.NodeID != "" && incident.NodeID != attempt.NodeID) || (incident.OwnerRole != "" && incident.OwnerRole != attempt.RoleID) || !timestampAtOrAfter(incident.OpenedAt, attempt.StartedAt) {
			return fmt.Errorf("incident references an unknown attempt")
		}
	case "effect":
		effect, ok := state.Effects[incident.SourceID]
		if !ok || (incident.NodeID != "" && incident.NodeID != effect.NodeID) || (incident.OwnerRole != "" && incident.OwnerRole != effect.OwnerRole) || !timestampAtOrAfter(incident.OpenedAt, effect.PreparedAt) {
			return fmt.Errorf("incident references an unknown effect")
		}
	case "resource":
		resource, ok := state.Resources[incident.SourceID]
		if !ok || (incident.NodeID != "" && incident.NodeID != resource.NodeID) || (incident.OwnerRole != "" && incident.OwnerRole != resource.RoleID) || !timestampAtOrAfter(incident.OpenedAt, resource.LeasedAt) {
			return fmt.Errorf("incident references an unknown resource")
		}
	default:
		return fmt.Errorf("incident source type is outside the closed lifecycle vocabulary")
	}
	return nil
}

func validExplicitIncidentUpdate(prior, next domain.Incident) bool {
	base := prior
	base.UpdatedAt = next.UpdatedAt
	candidates := []domain.Incident{}

	hasRepairBinding := next.RemedyNodeID != "" || next.SupersededAt != ""
	if prior.Status != "resolved" && prior.SourceType == "attempt" && next.Resolution != "" && (next.Resolution != incidentResolutionSupersededByRepair || !hasRepairBinding) {
		candidate := base
		candidate.Status, candidate.Resolution = "resolved", next.Resolution
		candidates = append(candidates, candidate)
	}
	if prior.Status != "resolved" && prior.SourceType == "attempt" && next.Resolution == incidentResolutionSupersededByRepair && next.RemedyNodeID != "" && next.SupersededAt == next.UpdatedAt && next.LastProgress != "" && next.LastProgressAt == next.UpdatedAt {
		candidate := base
		candidate.Status = "resolved"
		candidate.Resolution = incidentResolutionSupersededByRepair
		candidate.RemedyNodeID = next.RemedyNodeID
		candidate.SupersededAt = next.SupersededAt
		candidate.LastProgress = next.LastProgress
		candidate.LastProgressAt = next.LastProgressAt
		candidates = append(candidates, candidate)
	}
	if prior.Status != "resolved" && next.CircuitReason != "" {
		candidate := base
		candidate.Status, candidate.CircuitReason = "circuit-open", next.CircuitReason
		candidates = append(candidates, candidate)
	}
	if prior.Status == "open" && next.LastProgress != "" && next.LastProgressAt == next.UpdatedAt {
		for _, madeProgress := range []bool{true, false} {
			candidate := base
			candidate.Attempts++
			candidate.LastProgress, candidate.LastProgressAt = next.LastProgress, next.UpdatedAt
			if madeProgress {
				candidate.NoProgressAttempts = 0
			} else {
				candidate.NoProgressAttempts++
			}
			deadline, deadlineErr := time.Parse(time.RFC3339Nano, candidate.Deadline)
			updated, updatedErr := time.Parse(time.RFC3339Nano, next.UpdatedAt)
			if candidate.AttemptBudget > 0 && candidate.NoProgressAttempts >= candidate.AttemptBudget {
				candidate.Status, candidate.CircuitReason = "circuit-open", "no_progress_attempt_budget_exhausted"
			} else if deadlineErr == nil && updatedErr == nil && !updated.Before(deadline) {
				candidate.Status, candidate.CircuitReason = "circuit-open", "deadline_exceeded"
			}
			candidates = append(candidates, candidate)
		}
	}
	if prior.Status != "resolved" && domain.ValidIncidentDisposition(next.Disposition) && next.DispositionBy == next.OwnerRole && next.DispositionAt == next.UpdatedAt && next.LastProgress != "" && next.LastProgressAt == next.UpdatedAt {
		candidate := base
		candidate.Disposition, candidate.DispositionBy, candidate.DispositionAt = next.Disposition, next.DispositionBy, next.DispositionAt
		candidate.LastProgress, candidate.LastProgressAt = next.LastProgress, next.LastProgressAt
		if next.Disposition == "retry" && prior.Status == "circuit-open" {
			updated, err := time.Parse(time.RFC3339Nano, next.UpdatedAt)
			if err == nil {
				candidate.Status = "open"
				candidate.CircuitReason = ""
				candidate.Attempts = 0
				candidate.NoProgressAttempts = 0
				candidate.Deadline = updated.Add(time.Hour).Format(time.RFC3339Nano)
			}
		}
		candidates = append(candidates, candidate)
	}
	for _, candidate := range candidates {
		if reflect.DeepEqual(candidate, next) {
			return true
		}
	}
	return false
}
