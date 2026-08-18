package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
)

func TestExecutionPackageIdentityNormalizesUnorderedInputs(t *testing.T) {
	svc := &Service{}
	node := domain.NodeDefinition{ID: "node-A", Kind: "task", Objective: "build", Inputs: json.RawMessage(`{"target":"x"}`), Outcomes: []domain.Outcome{{ID: "success", Class: "success"}}}
	attempt := domain.Attempt{ID: "attempt-A", NodeID: node.ID}
	state := domain.NewState("project-A")
	state.GraphRevision = strings.Repeat("a", 64)
	first := packageInputJSON(t, []domain.ProtectedInput{{Name: "toolchain", Digest: testDigest("4")}, {Name: "fixture", Digest: testDigest("3")}}, []domain.ArtifactRef{testArtifact("6", "report"), testArtifact("5", "log")})
	second := packageInputJSON(t, []domain.ProtectedInput{{Name: "fixture", Digest: testDigest("3")}, {Name: "toolchain", Digest: testDigest("4")}}, []domain.ArtifactRef{testArtifact("5", "log"), testArtifact("6", "report")})
	one, err := svc.buildExecutionPackage(state, node, attempt, first, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	two, err := svc.buildExecutionPackage(state, node, attempt, second, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if one.ID != two.ID || one.CoreDigest != two.CoreDigest {
		t.Fatalf("unordered inputs changed identity: %s/%s vs %s/%s", one.ID, one.CoreDigest, two.ID, two.CoreDigest)
	}
	if one.Observations.Clean {
		t.Fatal("a recorded false observation must remain valid evidence rather than being treated as incomplete")
	}
}

func TestReuseDecisionExcludesPolicyVersionButDetectsCoreChange(t *testing.T) {
	svc := &Service{}
	node := domain.NodeDefinition{ID: "node-A", Kind: "task", Objective: "build", Outcomes: []domain.Outcome{{ID: "success", Class: "success"}}}
	attempt := domain.Attempt{ID: "attempt-A", NodeID: node.ID}
	state := domain.NewState("project-A")
	state.GraphRevision = strings.Repeat("a", 64)
	state.Graph = &domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "test"}, Spec: domain.GraphSpec{Nodes: []domain.NodeDefinition{node}}}
	pack, err := svc.buildExecutionPackage(state, node, attempt, packageInputJSON(t, []domain.ProtectedInput{{Name: "fixture", Digest: testDigest("3")}}, nil), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	state.EvidencePackages[pack.ID] = pack
	base := reuseInputJSON(t, pack, domain.PolicyBinding{ID: "validator", Version: "2.0.0", SchemaHash: testDigest("7")}, pack.Candidate.Digest)
	reusable, err := svc.buildReuseDecision(state, attempt, base, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if reusable.Result != "reuse_execution" || len(reusable.Reasons) != 1 || reusable.Reasons[0] != "protected_core_unchanged" {
		t.Fatalf("policy-only change should reuse execution: %#v", reusable)
	}
	changed := reuseInputJSON(t, pack, domain.PolicyBinding{ID: "validator", Version: "3.0.0", SchemaHash: testDigest("8")}, testDigest("9"))
	rerun, err := svc.buildReuseDecision(state, attempt, changed, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rerun.Result != "rerun_required" || !contains(rerun.Reasons, "candidate_changed") {
		t.Fatalf("candidate change should require rerun: %#v", rerun)
	}
	rerun.Reasons = []string{"protected_core_unchanged"}
	if err := validateReuseDecisionRecord(rerun, pack, node); err == nil {
		t.Fatal("reducer validation accepted tampered reuse reasons")
	}
}

func packageInputJSON(t *testing.T, protected []domain.ProtectedInput, artifacts []domain.ArtifactRef) json.RawMessage {
	t.Helper()
	if artifacts == nil {
		artifacts = []domain.ArtifactRef{}
	}
	value := map[string]any{
		"candidate":          testArtifact("1", "candidate"),
		"prospectiveTree":    testArtifact("2", "git-tree"),
		"commandGraphDigest": testDigest("c"),
		"protectedInputs":    protected,
		"observations": map[string]bool{
			"exact": true, "clean": false, "depthComplete": true, "sourceUnmodified": true, "resourcesClosed": true,
		},
		"artifacts": artifacts,
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func reuseInputJSON(t *testing.T, pack domain.ExecutionPackage, policy domain.PolicyBinding, candidateDigest string) json.RawMessage {
	t.Helper()
	value := map[string]any{
		"packageId": pack.ID, "policy": policy, "candidateDigest": candidateDigest,
		"prospectiveTreeDigest": pack.ProspectiveTree.Digest, "commandGraphDigest": pack.CommandGraphDigest,
		"protectedInputs": pack.ProtectedInputs,
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testArtifact(character, artifactType string) domain.ArtifactRef {
	return domain.ArtifactRef{Digest: testDigest(character), Type: artifactType, Size: 1, Provenance: domain.ArtifactProvenance{Producer: "test"}}
}

func testDigest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
