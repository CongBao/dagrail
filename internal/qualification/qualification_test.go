package qualification

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/contract"
	"github.com/CongBao/dagrail/internal/service"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestSourceQualificationIsStructuralAndExplicitlyNotProductionValidation(t *testing.T) {
	report, err := Run("../..", "")
	if err != nil {
		t.Fatal(err)
	}
	if !report.StructuralCandidate || report.ProductionValidated || report.ProjectEvidence || len(report.Checks) != 13 || len(report.ExternalGates) != 10 || len(report.AdoptionGaps) != 4 {
		t.Fatalf("unexpected release qualification: %+v", report)
	}
	for _, gap := range report.AdoptionGaps {
		if gap.Status != "outstanding" {
			t.Fatalf("adoption gap was overstated: %+v", gap)
		}
	}
	validateQualificationSchema(t, report)
}

func TestQualificationCanIncludeInspectionOnlyProjectEvidence(t *testing.T) {
	projectRoot := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(t.TempDir(), "runtime"))
	if _, err := service.Init(projectRoot, "qualification-project"); err != nil {
		t.Fatal(err)
	}
	report, err := Run("../..", projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.StructuralCandidate || !report.ProjectEvidence || checkStatus(report, "project-security") != "pass" || checkStatus(report, "project-recovery") != "pass" {
		t.Fatalf("project evidence did not qualify: %+v", report)
	}
	validateQualificationSchema(t, report)
}

func TestWorkflowPinCheckRejectsMutableActionTags(t *testing.T) {
	if actionsPinned("- uses: actions/checkout@v4\n") {
		t.Fatal("mutable action tag passed pin validation")
	}
	if !actionsPinned("- uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7\n") {
		t.Fatal("commit-pinned action was rejected")
	}
}

func TestPublishedSchemaDigestGateIncludesAuthorityRelocation(t *testing.T) {
	report := contract.Current()
	reader := func(relative string) ([]byte, error) {
		raw, err := os.ReadFile(filepath.Join("..", "..", relative))
		if relative == report.AuthorityRelocation.SchemaPath {
			raw = append(raw, ' ')
		}
		return raw, err
	}
	if publishedSchemaDigestsMatchReader(report, reader) {
		t.Fatal("relocation schema mutation bypassed published digest qualification")
	}
}

func TestPublishedSchemaDigestGateIncludesGitEvidenceSurfaces(t *testing.T) {
	for name, selected := range map[string]func(contract.Report) contract.DocumentedSurface{
		"artifact closure":  func(report contract.Report) contract.DocumentedSurface { return report.GitArtifactClosure },
		"integration scope": func(report contract.Report) contract.DocumentedSurface { return report.GitIntegrationScope },
	} {
		t.Run(name, func(t *testing.T) {
			report := contract.Current()
			target := selected(report)
			reader := func(relative string) ([]byte, error) {
				raw, err := os.ReadFile(filepath.Join("..", "..", relative))
				if relative == target.SchemaPath {
					raw = append(raw, ' ')
				}
				return raw, err
			}
			if publishedSchemaDigestsMatchReader(report, reader) {
				t.Fatalf("%s schema mutation bypassed published digest qualification", name)
			}
		})
	}
}

func TestFailedBundleEvidenceRemainsSchemaValid(t *testing.T) {
	report, err := Run("../..", "")
	if err != nil {
		t.Fatal(err)
	}
	report.StructuralCandidate = false
	report.Bundle = BundleEvidence{Valid: false}
	validateQualificationSchema(t, report)
}

func TestWorkflowYAMLRequiresExactlyOneDocument(t *testing.T) {
	if !validYAMLDocument([]byte("name: ci\nsteps: []\n")) {
		t.Fatal("valid YAML was rejected")
	}
	if validYAMLDocument([]byte("name: [\n")) || validYAMLDocument([]byte("name: one\n---\nname: two\n")) {
		t.Fatal("invalid or multi-document YAML was accepted")
	}
}

func TestReleaseWorkflowRequiresExactSHAWindowsAdmission(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !releaseWorkflowRequiresWindowsTest(raw) {
		t.Fatal("release publication is not gated by a Windows full-test job")
	}
	withoutAdmission := []byte(strings.Replace(string(raw), ", windows-test]", "]", 1))
	if releaseWorkflowRequiresWindowsTest(withoutAdmission) {
		t.Fatal("release publication accepted a detached Windows test")
	}
	wrongRunner := []byte(strings.Replace(string(raw), "runs-on: windows-latest", "runs-on: ubuntu-latest", 1))
	if releaseWorkflowRequiresWindowsTest(wrongRunner) {
		t.Fatal("release publication accepted a non-Windows substitute")
	}
}

func TestValidationSubjectBoundaryRejectsRemoteSchemaDefinitions(t *testing.T) {
	if onlyLocalSchemaRefs(map[string]any{"$ref": "https://example.invalid/project-schema.json"}) {
		t.Fatal("validation-subject boundary accepted a remote project schema")
	}
	if !onlyLocalSchemaRefs(map[string]any{"properties": map[string]any{"event": map[string]any{"$ref": "#/$defs/nativeEvent"}}}) {
		t.Fatal("validation-subject boundary rejected a local closed definition")
	}
}

func TestReadSourceFileRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "public.md"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readSourceFile(root, "linked/public.md"); err == nil {
		t.Fatal("qualification followed a parent symlink outside the source root")
	}
	if err := os.Symlink(filepath.Join(outside, "public.md"), filepath.Join(root, "direct.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := readSourceFile(root, "direct.md"); err == nil {
		t.Fatal("qualification accepted a symlink source file")
	}
}

func TestInvalidSourceRootErrorDoesNotDisclosePath(t *testing.T) {
	privateRoot := filepath.Join(t.TempDir(), "private-source-name")
	_, err := Run(privateRoot, "")
	if err == nil {
		t.Fatal("missing source root was accepted")
	}
	if strings.Contains(err.Error(), privateRoot) || strings.Contains(err.Error(), "private-source-name") {
		t.Fatalf("qualification error disclosed source path: %v", err)
	}
}

func checkStatus(report Report, id string) string {
	for _, check := range report.Checks {
		if check.ID == id {
			return check.Status
		}
	}
	return ""
}

func validateQualificationSchema(t *testing.T, report Report) {
	t.Helper()
	raw, err := os.ReadFile("../../schemas/release-qualification-v1alpha1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:release-qualification", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:release-qualification")
	if err != nil {
		t.Fatal(err)
	}
	instanceRaw, _ := json.Marshal(report)
	var instance any
	if err := json.Unmarshal(instanceRaw, &instance); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("qualification report does not match published schema: %v", err)
	}
}
