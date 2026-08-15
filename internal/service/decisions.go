package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	internalproviders "github.com/CongBao/dagrail/internal/providers"
	"github.com/CongBao/dagrail/sdk"
	"github.com/gowebpki/jcs"
)

type completionInput struct {
	Outcome        string                `json:"outcome"`
	Facts          domain.PredicateFacts `json:"facts"`
	EvidenceRefs   []domain.EvidenceRef  `json:"evidenceRefs"`
	Classification string                `json:"classification"`
}

type gateInput struct {
	Input        json.RawMessage      `json:"input"`
	Evidence     []json.RawMessage    `json:"evidence"`
	EvidenceRefs []domain.EvidenceRef `json:"evidenceRefs"`
}

func decodeCompletionInput(raw json.RawMessage) (completionInput, error) {
	var value completionInput
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, fmt.Errorf("decode completion input: %w", err)
	}
	if err := validateEvidenceRefs(value.EvidenceRefs); err != nil {
		return value, err
	}
	return value, nil
}

func validateEvidenceRefs(values []domain.EvidenceRef) error {
	if len(values) > 128 {
		return fmt.Errorf("evidence refs cannot exceed 128 entries")
	}
	seen := map[string]bool{}
	for _, evidence := range values {
		if evidence.Digest == "" || evidence.Type == "" || evidence.Size < 0 || seen[evidence.Digest] {
			return fmt.Errorf("evidence refs require unique digest, type and non-negative size")
		}
		seen[evidence.Digest] = true
	}
	return nil
}

func (s *Service) buildRoleDecision(state domain.State, node domain.NodeDefinition, attempt domain.Attempt, actorRole string, value completionInput, input json.RawMessage, now time.Time) (domain.DecisionRecord, error) {
	contract := node.Decision
	key, source := "verdict", "llm"
	if contract != nil {
		key, source = contract.Key, contract.Source
	}
	if source == "provider" {
		return domain.DecisionRecord{}, fmt.Errorf("provider decisions must use gate.evaluate")
	}
	if value.Facts.Decision == nil {
		value.Facts.Decision = map[string]string{}
	}
	if existing := value.Facts.Decision[key]; existing != "" && existing != value.Outcome {
		return domain.DecisionRecord{}, fmt.Errorf("decision fact %s=%s conflicts with outcome %s", key, existing, value.Outcome)
	}
	value.Facts.Decision[key] = value.Outcome
	digest, err := decisionInputDigest(input)
	if err != nil {
		return domain.DecisionRecord{}, err
	}
	record := domain.DecisionRecord{ProjectID: state.ProjectID, GraphRevision: state.GraphRevision, NodeID: node.ID, AttemptID: attempt.ID, RoleID: actorRole, Key: key, Source: source, Outcome: value.Outcome, Facts: value.Facts, EvidenceRefs: normalizeDecisionEvidence(value.EvidenceRefs), InputDigest: digest, CreatedAt: now.UTC().Format(time.RFC3339Nano)}
	if err := assignDecisionID(&record); err != nil {
		return domain.DecisionRecord{}, err
	}
	return record, nil
}

func (s *Service) buildGateDecision(ctx context.Context, state domain.State, node domain.NodeDefinition, attempt domain.Attempt, actorRole string, raw json.RawMessage, now time.Time) (domain.DecisionRecord, error) {
	if node.Decision == nil || node.Decision.Source != "provider" {
		return domain.DecisionRecord{}, fmt.Errorf("gate node %s has no provider decision contract", node.ID)
	}
	var value gateInput
	if err := json.Unmarshal(raw, &value); err != nil {
		return domain.DecisionRecord{}, fmt.Errorf("decode gate input: %w", err)
	}
	if len(value.Input) == 0 {
		return domain.DecisionRecord{}, fmt.Errorf("gate input is required")
	}
	if err := validateEvidenceRefs(value.EvidenceRefs); err != nil {
		return domain.DecisionRecord{}, err
	}
	_, ok := s.Providers.Policy(node.Decision.ProviderID)
	if !ok {
		return domain.DecisionRecord{}, fmt.Errorf("policy provider %s is not registered", node.Decision.ProviderID)
	}
	providerInput, err := json.Marshal(sdk.PolicyRequest{PolicyID: node.Decision.PolicyID, Input: value.Input, Evidence: value.Evidence})
	if err != nil {
		return domain.DecisionRecord{}, err
	}
	invocation, err := s.ProviderRuntime.Invoke(ctx, internalproviders.Invocation{Kind: internalproviders.KindPolicy, ProviderID: node.Decision.ProviderID, Input: providerInput})
	if err != nil {
		return domain.DecisionRecord{}, err
	}
	var decision sdk.PolicyDecision
	if err := json.Unmarshal(invocation.Output, &decision); err != nil {
		return domain.DecisionRecord{}, fmt.Errorf("decode policy decision: %w", err)
	}
	if !declaresOutcome(node, decision.Outcome) {
		return domain.DecisionRecord{}, fmt.Errorf("policy outcome %s is not declared by gate %s", decision.Outcome, node.ID)
	}
	policyFacts := map[string]string{node.Decision.Key: decision.Outcome}
	if len(decision.Facts) > 0 {
		var extras map[string]string
		if err := json.Unmarshal(decision.Facts, &extras); err != nil {
			return domain.DecisionRecord{}, fmt.Errorf("policy facts must be a string map: %w", err)
		}
		for key, item := range extras {
			if key == "" || item == "" {
				return domain.DecisionRecord{}, fmt.Errorf("policy facts require non-empty keys and values")
			}
			policyFacts[key] = item
		}
	}
	digest, err := decisionInputDigest(raw)
	if err != nil {
		return domain.DecisionRecord{}, err
	}
	record := domain.DecisionRecord{ProjectID: state.ProjectID, GraphRevision: state.GraphRevision, NodeID: node.ID, AttemptID: attempt.ID, RoleID: actorRole, Key: node.Decision.Key, Source: "provider", Outcome: decision.Outcome, Facts: domain.PredicateFacts{Policy: policyFacts}, EvidenceRefs: normalizeDecisionEvidence(value.EvidenceRefs), InputDigest: digest, Provider: &domain.DecisionProviderBinding{ID: invocation.Provider.ID, Version: invocation.Provider.Version, SchemaHash: invocation.Provider.SchemaHash}, CreatedAt: now.UTC().Format(time.RFC3339Nano)}
	if err := assignDecisionID(&record); err != nil {
		return domain.DecisionRecord{}, err
	}
	return record, nil
}

func validateDecisionRecord(record domain.DecisionRecord, state domain.State) error {
	if record.ProjectID != state.ProjectID || record.GraphRevision != state.GraphRevision || record.ID == "" || record.NodeID == "" || record.AttemptID == "" || record.RoleID == "" || record.Key == "" || record.Source == "" || record.Outcome == "" {
		return fmt.Errorf("decision record authority binding is incomplete")
	}
	node, ok := state.NodeDefinition(record.NodeID)
	attempt, attemptOK := state.Attempts[record.AttemptID]
	if !ok || !attemptOK || attempt.NodeID != node.ID || !declaresOutcome(node, record.Outcome) {
		return fmt.Errorf("decision record references an invalid node, attempt or outcome")
	}
	if err := validateEvidenceRefs(record.EvidenceRefs); err != nil {
		return err
	}
	if !strings.HasPrefix(record.InputDigest, "sha256:") || len(record.InputDigest) != 71 {
		return fmt.Errorf("decision input digest is invalid")
	}
	if record.Source == "provider" {
		if record.Provider == nil || record.Provider.ID == "" || record.Provider.Version == "" || record.Provider.SchemaHash == "" {
			return fmt.Errorf("provider decision binding is incomplete")
		}
	} else if record.Provider != nil {
		return fmt.Errorf("non-provider decision cannot carry a provider binding")
	}
	if _, err := time.Parse(time.RFC3339Nano, record.CreatedAt); err != nil {
		return fmt.Errorf("decision createdAt is invalid")
	}
	copy := record
	copy.ID, copy.Sequence = "", 0
	expected, err := decisionRecordID(copy)
	if err != nil || expected != record.ID {
		return fmt.Errorf("decision record ID is invalid")
	}
	return nil
}

func assignDecisionID(record *domain.DecisionRecord) error {
	copy := *record
	copy.ID, copy.Sequence = "", 0
	id, err := decisionRecordID(copy)
	if err != nil {
		return err
	}
	record.ID = id
	return nil
}

func decisionRecordID(record domain.DecisionRecord) (string, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("dagrail-decision-record-v1\x00"), canonical...))
	return "decision_" + hex.EncodeToString(sum[:]), nil
}

func decisionInputDigest(raw json.RawMessage) (string, error) {
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("dagrail-decision-input-v1\x00"), canonical...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func declaresOutcome(node domain.NodeDefinition, outcome string) bool {
	for _, candidate := range node.Outcomes {
		if candidate.ID == outcome {
			return true
		}
	}
	return false
}

func normalizeDecisionEvidence(values []domain.EvidenceRef) []domain.EvidenceRef {
	result := append([]domain.EvidenceRef(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].Digest < result[j].Digest })
	return result
}
