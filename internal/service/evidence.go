package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/gowebpki/jcs"
)

var sha256DigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type executionObservationsInput struct {
	Exact            *bool `json:"exact"`
	Clean            *bool `json:"clean"`
	DepthComplete    *bool `json:"depthComplete"`
	SourceUnmodified *bool `json:"sourceUnmodified"`
	ResourcesClosed  *bool `json:"resourcesClosed"`
}

type publishExecutionPackageInput struct {
	Candidate          domain.ArtifactRef         `json:"candidate"`
	ProspectiveTree    domain.ArtifactRef         `json:"prospectiveTree"`
	CommandGraphDigest string                     `json:"commandGraphDigest"`
	ProtectedInputs    []domain.ProtectedInput    `json:"protectedInputs"`
	Observations       executionObservationsInput `json:"observations"`
	Artifacts          []domain.ArtifactRef       `json:"artifacts"`
}

type reuseAssessmentInput struct {
	PackageID             string                  `json:"packageId"`
	Policy                domain.PolicyBinding    `json:"policy"`
	CandidateDigest       string                  `json:"candidateDigest"`
	ProspectiveTreeDigest string                  `json:"prospectiveTreeDigest"`
	CommandGraphDigest    string                  `json:"commandGraphDigest"`
	ProtectedInputs       []domain.ProtectedInput `json:"protectedInputs"`
}

func (s *Service) buildExecutionPackage(state domain.State, node domain.NodeDefinition, attempt domain.Attempt, input json.RawMessage, now time.Time) (domain.ExecutionPackage, error) {
	if len(input) > 64*1024 {
		return domain.ExecutionPackage{}, fmt.Errorf("execution package metadata cannot exceed 64 KiB")
	}
	var value publishExecutionPackageInput
	if err := json.Unmarshal(input, &value); err != nil {
		return domain.ExecutionPackage{}, fmt.Errorf("decode execution package input: %w", err)
	}
	if err := validateArtifact(value.Candidate); err != nil {
		return domain.ExecutionPackage{}, fmt.Errorf("candidate: %w", err)
	}
	if err := validateArtifact(value.ProspectiveTree); err != nil {
		return domain.ExecutionPackage{}, fmt.Errorf("prospective tree: %w", err)
	}
	if err := validateDigest(value.CommandGraphDigest); err != nil {
		return domain.ExecutionPackage{}, fmt.Errorf("command graph digest: %w", err)
	}
	protected, err := normalizeProtectedInputs(value.ProtectedInputs)
	if err != nil {
		return domain.ExecutionPackage{}, err
	}
	artifacts, err := normalizeArtifacts(value.Artifacts)
	if err != nil {
		return domain.ExecutionPackage{}, err
	}
	if value.Observations.Exact == nil || value.Observations.Clean == nil || value.Observations.DepthComplete == nil || value.Observations.SourceUnmodified == nil || value.Observations.ResourcesClosed == nil {
		return domain.ExecutionPackage{}, fmt.Errorf("execution observations require exact, clean, depthComplete, sourceUnmodified and resourcesClosed")
	}
	nodeDigest, err := nodeContractDigest(node)
	if err != nil {
		return domain.ExecutionPackage{}, err
	}
	core := domain.ExecutionCore{
		CandidateDigest:       value.Candidate.Digest,
		ProspectiveTreeDigest: value.ProspectiveTree.Digest,
		CommandGraphDigest:    value.CommandGraphDigest,
		NodeContractDigest:    nodeDigest,
		ProtectedInputs:       protected,
	}
	coreDigest, err := authorityDigest("dagrail-execution-core-v1\x00", core)
	if err != nil {
		return domain.ExecutionPackage{}, err
	}
	result := domain.ExecutionPackage{
		APIVersion:         domain.ExecutionPackageAPIVersion,
		Kind:               domain.ExecutionPackageKind,
		ProjectID:          state.ProjectID,
		GraphRevision:      state.GraphRevision,
		NodeID:             node.ID,
		AttemptID:          attempt.ID,
		NodeContractDigest: nodeDigest,
		Candidate:          value.Candidate,
		ProspectiveTree:    value.ProspectiveTree,
		CommandGraphDigest: value.CommandGraphDigest,
		ProtectedInputs:    protected,
		Observations: domain.ExecutionObservations{
			Exact:            *value.Observations.Exact,
			Clean:            *value.Observations.Clean,
			DepthComplete:    *value.Observations.DepthComplete,
			SourceUnmodified: *value.Observations.SourceUnmodified,
			ResourcesClosed:  *value.Observations.ResourcesClosed,
		},
		Artifacts:  artifacts,
		CoreDigest: coreDigest,
		CreatedAt:  now.UTC().Format(time.RFC3339Nano),
	}
	identity := result
	identity.CreatedAt = ""
	id, err := authorityDigest("dagrail-execution-package-id-v1\x00", identity)
	if err != nil {
		return domain.ExecutionPackage{}, err
	}
	result.ID = "epkg_" + strings.TrimPrefix(id, "sha256:")
	if err := validateExecutionPackageRecord(result, node); err != nil {
		return domain.ExecutionPackage{}, err
	}
	return result, nil
}

func (s *Service) buildReuseDecision(state domain.State, assessor domain.Attempt, input json.RawMessage, now time.Time) (domain.ReuseDecision, error) {
	if len(input) > 32*1024 {
		return domain.ReuseDecision{}, fmt.Errorf("reuse assessment metadata cannot exceed 32 KiB")
	}
	var value reuseAssessmentInput
	if err := json.Unmarshal(input, &value); err != nil {
		return domain.ReuseDecision{}, fmt.Errorf("decode reuse assessment input: %w", err)
	}
	pack, ok := state.EvidencePackages[value.PackageID]
	if !ok {
		return domain.ReuseDecision{}, fmt.Errorf("unknown execution package %s", value.PackageID)
	}
	if strings.TrimSpace(value.Policy.ID) == "" || strings.TrimSpace(value.Policy.Version) == "" {
		return domain.ReuseDecision{}, fmt.Errorf("policy binding requires id and version")
	}
	if len(value.Policy.ID) > 128 || len(value.Policy.Version) > 128 {
		return domain.ReuseDecision{}, fmt.Errorf("policy id and version cannot exceed 128 bytes")
	}
	if err := validateDigest(value.Policy.SchemaHash); err != nil {
		return domain.ReuseDecision{}, fmt.Errorf("policy schema hash: %w", err)
	}
	for label, digest := range map[string]string{
		"candidate": value.CandidateDigest, "prospective tree": value.ProspectiveTreeDigest, "command graph": value.CommandGraphDigest,
	} {
		if err := validateDigest(digest); err != nil {
			return domain.ReuseDecision{}, fmt.Errorf("%s digest: %w", label, err)
		}
	}
	protected, err := normalizeProtectedInputs(value.ProtectedInputs)
	if err != nil {
		return domain.ReuseDecision{}, err
	}
	node, ok := state.NodeDefinition(pack.NodeID)
	if !ok {
		return domain.ReuseDecision{}, fmt.Errorf("execution package node %s is no longer declared", pack.NodeID)
	}
	nodeDigest, err := nodeContractDigest(node)
	if err != nil {
		return domain.ReuseDecision{}, err
	}
	currentCore := domain.ExecutionCore{
		CandidateDigest:       value.CandidateDigest,
		ProspectiveTreeDigest: value.ProspectiveTreeDigest,
		CommandGraphDigest:    value.CommandGraphDigest,
		NodeContractDigest:    nodeDigest,
		ProtectedInputs:       protected,
	}
	currentDigest, err := authorityDigest("dagrail-execution-core-v1\x00", currentCore)
	if err != nil {
		return domain.ReuseDecision{}, err
	}
	result, reasons := reuseResult(pack, currentCore)
	decision := domain.ReuseDecision{
		APIVersion:         domain.ExecutionPackageAPIVersion,
		Kind:               domain.ReuseDecisionKind,
		PackageID:          pack.ID,
		AssessedByAttempt:  assessor.ID,
		Policy:             value.Policy,
		OriginalCoreDigest: pack.CoreDigest,
		CurrentCore:        currentCore,
		CurrentCoreDigest:  currentDigest,
		Result:             result,
		Reasons:            reasons,
		CreatedAt:          now.UTC().Format(time.RFC3339Nano),
	}
	identity := decision
	identity.CreatedAt = ""
	id, err := authorityDigest("dagrail-reuse-decision-id-v1\x00", identity)
	if err != nil {
		return domain.ReuseDecision{}, err
	}
	decision.ID = "reuse_" + strings.TrimPrefix(id, "sha256:")
	if err := validateReuseDecisionRecord(decision, pack, node); err != nil {
		return domain.ReuseDecision{}, err
	}
	return decision, nil
}

func reuseResult(pack domain.ExecutionPackage, current domain.ExecutionCore) (string, []string) {
	reasons := make([]string, 0, 5)
	if current.CandidateDigest != pack.Candidate.Digest {
		reasons = append(reasons, "candidate_changed")
	}
	if current.ProspectiveTreeDigest != pack.ProspectiveTree.Digest {
		reasons = append(reasons, "prospective_tree_changed")
	}
	if current.CommandGraphDigest != pack.CommandGraphDigest {
		reasons = append(reasons, "command_graph_changed")
	}
	if !reflect.DeepEqual(current.ProtectedInputs, pack.ProtectedInputs) {
		reasons = append(reasons, "protected_inputs_changed")
	}
	if current.NodeContractDigest != pack.NodeContractDigest {
		reasons = append(reasons, "node_contract_changed")
	}
	if len(reasons) == 0 {
		return "reuse_execution", []string{"protected_core_unchanged"}
	}
	return "rerun_required", reasons
}

func validateExecutionPackageRecord(pack domain.ExecutionPackage, node domain.NodeDefinition) error {
	if pack.APIVersion != domain.ExecutionPackageAPIVersion || pack.Kind != domain.ExecutionPackageKind {
		return fmt.Errorf("execution package has unsupported apiVersion or kind")
	}
	if err := validateArtifact(pack.Candidate); err != nil {
		return fmt.Errorf("candidate: %w", err)
	}
	if err := validateArtifact(pack.ProspectiveTree); err != nil {
		return fmt.Errorf("prospective tree: %w", err)
	}
	if err := validateDigest(pack.CommandGraphDigest); err != nil {
		return fmt.Errorf("command graph digest: %w", err)
	}
	protected, err := normalizeProtectedInputs(pack.ProtectedInputs)
	if err != nil {
		return fmt.Errorf("execution package protected inputs: %w", err)
	}
	if !reflect.DeepEqual(protected, pack.ProtectedInputs) {
		return fmt.Errorf("execution package protected inputs are not canonical")
	}
	artifacts, err := normalizeArtifacts(pack.Artifacts)
	if err != nil {
		return fmt.Errorf("execution package artifacts: %w", err)
	}
	if !reflect.DeepEqual(artifacts, pack.Artifacts) {
		return fmt.Errorf("execution package artifacts are not canonical")
	}
	nodeDigest, err := nodeContractDigest(node)
	if err != nil {
		return err
	}
	if nodeDigest != pack.NodeContractDigest {
		return fmt.Errorf("execution package node contract digest is invalid")
	}
	core := domain.ExecutionCore{CandidateDigest: pack.Candidate.Digest, ProspectiveTreeDigest: pack.ProspectiveTree.Digest, CommandGraphDigest: pack.CommandGraphDigest, NodeContractDigest: pack.NodeContractDigest, ProtectedInputs: pack.ProtectedInputs}
	coreDigest, err := authorityDigest("dagrail-execution-core-v1\x00", core)
	if err != nil || coreDigest != pack.CoreDigest {
		return fmt.Errorf("execution package core digest is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, pack.CreatedAt); err != nil {
		return fmt.Errorf("execution package createdAt is invalid")
	}
	identity := pack
	identity.ID, identity.CreatedAt, identity.Sequence = "", "", 0
	id, err := authorityDigest("dagrail-execution-package-id-v1\x00", identity)
	if err != nil || pack.ID != "epkg_"+strings.TrimPrefix(id, "sha256:") {
		return fmt.Errorf("execution package ID is invalid")
	}
	return nil
}

func validateReuseDecisionRecord(decision domain.ReuseDecision, pack domain.ExecutionPackage, node domain.NodeDefinition) error {
	if decision.APIVersion != domain.ExecutionPackageAPIVersion || decision.Kind != domain.ReuseDecisionKind {
		return fmt.Errorf("reuse decision has unsupported apiVersion or kind")
	}
	if strings.TrimSpace(decision.Policy.ID) == "" || strings.TrimSpace(decision.Policy.Version) == "" {
		return fmt.Errorf("reuse decision policy binding requires id and version")
	}
	if len(decision.Policy.ID) > 128 || len(decision.Policy.Version) > 128 {
		return fmt.Errorf("reuse decision policy id and version cannot exceed 128 bytes")
	}
	if err := validateDigest(decision.Policy.SchemaHash); err != nil {
		return fmt.Errorf("reuse decision policy schema hash: %w", err)
	}
	for label, digest := range map[string]string{
		"candidate": decision.CurrentCore.CandidateDigest, "prospective tree": decision.CurrentCore.ProspectiveTreeDigest,
		"command graph": decision.CurrentCore.CommandGraphDigest, "node contract": decision.CurrentCore.NodeContractDigest,
	} {
		if err := validateDigest(digest); err != nil {
			return fmt.Errorf("reuse decision %s digest: %w", label, err)
		}
	}
	protected, err := normalizeProtectedInputs(decision.CurrentCore.ProtectedInputs)
	if err != nil {
		return fmt.Errorf("reuse decision protected inputs: %w", err)
	}
	if !reflect.DeepEqual(protected, decision.CurrentCore.ProtectedInputs) {
		return fmt.Errorf("reuse decision protected inputs are not canonical")
	}
	nodeDigest, err := nodeContractDigest(node)
	if err != nil {
		return err
	}
	if nodeDigest != decision.CurrentCore.NodeContractDigest {
		return fmt.Errorf("reuse decision node contract digest is invalid")
	}
	currentDigest, err := authorityDigest("dagrail-execution-core-v1\x00", decision.CurrentCore)
	if err != nil || currentDigest != decision.CurrentCoreDigest || pack.CoreDigest != decision.OriginalCoreDigest {
		return fmt.Errorf("reuse decision core digest is invalid")
	}
	result, reasons := reuseResult(pack, decision.CurrentCore)
	if result != decision.Result || !reflect.DeepEqual(reasons, decision.Reasons) {
		return fmt.Errorf("reuse decision result or reasons are invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, decision.CreatedAt); err != nil {
		return fmt.Errorf("reuse decision createdAt is invalid")
	}
	identity := decision
	identity.ID, identity.CreatedAt, identity.Sequence = "", "", 0
	id, err := authorityDigest("dagrail-reuse-decision-id-v1\x00", identity)
	if err != nil || decision.ID != "reuse_"+strings.TrimPrefix(id, "sha256:") {
		return fmt.Errorf("reuse decision ID is invalid")
	}
	return nil
}

func normalizeProtectedInputs(values []domain.ProtectedInput) ([]domain.ProtectedInput, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one protected input is required")
	}
	if len(values) > 128 {
		return nil, fmt.Errorf("protected inputs cannot exceed 128 entries")
	}
	result := append([]domain.ProtectedInput(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	for index, value := range result {
		if strings.TrimSpace(value.Name) == "" || len(value.Name) > 128 {
			return nil, fmt.Errorf("protected input name must be 1..128 bytes")
		}
		if err := validateDigest(value.Digest); err != nil {
			return nil, fmt.Errorf("protected input %s: %w", value.Name, err)
		}
		if index > 0 && result[index-1].Name == value.Name {
			return nil, fmt.Errorf("protected input %s is duplicated", value.Name)
		}
	}
	return result, nil
}

func normalizeArtifacts(values []domain.ArtifactRef) ([]domain.ArtifactRef, error) {
	if len(values) > 256 {
		return nil, fmt.Errorf("artifacts cannot exceed 256 entries")
	}
	result := make([]domain.ArtifactRef, len(values))
	copy(result, values)
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i].Digest+"\x00"+result[i].Type+"\x00"+result[i].URI, result[j].Digest+"\x00"+result[j].Type+"\x00"+result[j].URI
		return left < right
	})
	for index, value := range result {
		if err := validateArtifact(value); err != nil {
			return nil, fmt.Errorf("artifact %d: %w", index, err)
		}
		if index > 0 && result[index-1].Digest == value.Digest && result[index-1].Type == value.Type && result[index-1].URI == value.URI {
			return nil, fmt.Errorf("artifact %s is duplicated", value.Digest)
		}
	}
	return result, nil
}

func validateArtifact(value domain.ArtifactRef) error {
	if err := validateDigest(value.Digest); err != nil {
		return err
	}
	if strings.TrimSpace(value.Type) == "" || len(value.Type) > 128 || value.Size < 0 || strings.TrimSpace(value.Provenance.Producer) == "" || len(value.Provenance.Producer) > 256 || len(value.Provenance.Revision) > 256 {
		return fmt.Errorf("artifact requires type, non-negative size and provenance producer")
	}
	if value.Provenance.InvocationDigest != "" {
		if err := validateDigest(value.Provenance.InvocationDigest); err != nil {
			return fmt.Errorf("invocation digest: %w", err)
		}
	}
	if value.URI != "" {
		if len(value.URI) > 2048 {
			return fmt.Errorf("artifact URI cannot exceed 2048 bytes")
		}
		parsed, err := url.Parse(value.URI)
		if err != nil || !parsed.IsAbs() || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("artifact URI must be absolute and cannot contain userinfo, query or fragment")
		}
		allowedSchemes := map[string]bool{"file": true, "http": true, "https": true, "s3": true, "gs": true, "az": true, "git": true, "oci": true}
		if !allowedSchemes[strings.ToLower(parsed.Scheme)] {
			return fmt.Errorf("artifact URI scheme %s is not allowed", parsed.Scheme)
		}
	}
	return nil
}

func validateDigest(value string) error {
	if !sha256DigestPattern.MatchString(value) {
		return fmt.Errorf("digest must be lowercase sha256:<64 hex>")
	}
	return nil
}

func nodeContractDigest(node domain.NodeDefinition) (string, error) {
	contract := struct {
		ID        string                   `json:"id"`
		Kind      string                   `json:"kind"`
		Objective string                   `json:"objective"`
		Inputs    json.RawMessage          `json:"inputs,omitempty"`
		Outcomes  []domain.Outcome         `json:"outcomes"`
		Resources []domain.ResourceRequest `json:"resources,omitempty"`
	}{node.ID, node.Kind, node.Objective, node.Inputs, node.Outcomes, node.Resources}
	return authorityDigest("dagrail-node-contract-v1\x00", contract)
}

func authorityDigest(domainSeparator string, value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(domainSeparator), canonical...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
