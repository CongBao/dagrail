package domain

const (
	ExecutionPackageAPIVersion = "dagrail.io/v1alpha1"
	ExecutionPackageKind       = "ExecutionPackage"
	ReuseDecisionKind          = "ReuseDecision"
)

type ArtifactProvenance struct {
	Producer         string `json:"producer"`
	Revision         string `json:"revision,omitempty"`
	InvocationDigest string `json:"invocationDigest,omitempty"`
}

type ArtifactRef struct {
	Digest     string             `json:"digest"`
	Type       string             `json:"type"`
	Size       int64              `json:"size"`
	URI        string             `json:"uri,omitempty"`
	Provenance ArtifactProvenance `json:"provenance"`
}

type ProtectedInput struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type ExecutionObservations struct {
	Exact            bool `json:"exact"`
	Clean            bool `json:"clean"`
	DepthComplete    bool `json:"depthComplete"`
	SourceUnmodified bool `json:"sourceUnmodified"`
	ResourcesClosed  bool `json:"resourcesClosed"`
}

type ExecutionPackage struct {
	APIVersion         string                `json:"apiVersion"`
	Kind               string                `json:"kind"`
	ID                 string                `json:"id"`
	ProjectID          string                `json:"projectId"`
	GraphRevision      string                `json:"graphRevision"`
	NodeID             string                `json:"nodeId"`
	AttemptID          string                `json:"attemptId"`
	NodeContractDigest string                `json:"nodeContractDigest"`
	Candidate          ArtifactRef           `json:"candidate"`
	ProspectiveTree    ArtifactRef           `json:"prospectiveTree"`
	CommandGraphDigest string                `json:"commandGraphDigest"`
	ProtectedInputs    []ProtectedInput      `json:"protectedInputs"`
	Observations       ExecutionObservations `json:"observations"`
	Artifacts          []ArtifactRef         `json:"artifacts"`
	CoreDigest         string                `json:"coreDigest"`
	CreatedAt          string                `json:"createdAt"`
	Sequence           uint64                `json:"sequence,omitempty"`
}

type PolicyBinding struct {
	ID         string `json:"id"`
	Version    string `json:"version"`
	SchemaHash string `json:"schemaHash"`
}

type ExecutionCore struct {
	CandidateDigest       string           `json:"candidateDigest"`
	ProspectiveTreeDigest string           `json:"prospectiveTreeDigest"`
	CommandGraphDigest    string           `json:"commandGraphDigest"`
	NodeContractDigest    string           `json:"nodeContractDigest"`
	ProtectedInputs       []ProtectedInput `json:"protectedInputs"`
}

type ReuseDecision struct {
	APIVersion         string        `json:"apiVersion"`
	Kind               string        `json:"kind"`
	ID                 string        `json:"id"`
	PackageID          string        `json:"packageId"`
	AssessedByAttempt  string        `json:"assessedByAttemptId"`
	Policy             PolicyBinding `json:"policy"`
	OriginalCoreDigest string        `json:"originalCoreDigest"`
	CurrentCore        ExecutionCore `json:"currentCore"`
	CurrentCoreDigest  string        `json:"currentCoreDigest"`
	Result             string        `json:"result"`
	Reasons            []string      `json:"reasons"`
	CreatedAt          string        `json:"createdAt"`
	Sequence           uint64        `json:"sequence,omitempty"`
}
