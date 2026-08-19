package qualification

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CongBao/dagrail/internal/compatibility"
	"github.com/CongBao/dagrail/internal/contract"
	"github.com/CongBao/dagrail/internal/install"
	"github.com/CongBao/dagrail/internal/service"
	"github.com/CongBao/dagrail/internal/version"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const APIVersion = "dagrail.io/release-qualification/v1alpha1"

const maxQualificationFileBytes = 4 * 1024 * 1024

type Check struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Code   string `json:"code"`
}

type Requirement struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	AutomatedBy string `json:"automatedBy"`
}

type BundleEvidence struct {
	Valid   bool   `json:"valid"`
	Version string `json:"version,omitempty"`
	Digest  string `json:"digest,omitempty"`
	Files   int    `json:"files,omitempty"`
	Bytes   int    `json:"bytes,omitempty"`
}

type Report struct {
	APIVersion          string         `json:"apiVersion"`
	Kind                string         `json:"kind"`
	Version             string         `json:"version"`
	StructuralCandidate bool           `json:"structuralCandidate"`
	ProductionValidated bool           `json:"productionValidated"`
	ProjectEvidence     bool           `json:"projectEvidence"`
	Bundle              BundleEvidence `json:"bundle"`
	Checks              []Check        `json:"checks"`
	ExternalGates       []Requirement  `json:"externalGates"`
	AdoptionGaps        []Requirement  `json:"adoptionGaps"`
}

func Run(sourceRoot, projectRoot string) (Report, error) {
	report := Report{
		APIVersion: APIVersion, Kind: "ReleaseQualification", Version: version.Version,
		StructuralCandidate: true, ProductionValidated: false, Checks: []Check{},
		ExternalGates: releaseRequirements(), AdoptionGaps: adoptionRequirements(),
	}
	add := func(id string, passed bool, code string) {
		status := "pass"
		if !passed {
			status = "fail"
			report.StructuralCandidate = false
		}
		report.Checks = append(report.Checks, Check{ID: id, Status: status, Code: code})
	}
	addOptional := func(id, code string) {
		report.Checks = append(report.Checks, Check{ID: id, Status: "not_run", Code: code})
	}

	root, err := filepath.Abs(sourceRoot)
	if err != nil {
		return report, fmt.Errorf("qualification root is invalid")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return report, fmt.Errorf("qualification root is invalid")
	}
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		return report, fmt.Errorf("qualification root must be a directory")
	}
	required := []string{
		"LICENSE", "README.md", "SECURITY.md", "SUPPORT.md", "GOVERNANCE.md",
		"CODE_OF_CONDUCT.md", "CONTRIBUTING.md", "COMPATIBILITY.md", "CONTEXT.md",
		"CHANGELOG.md", "docs/api.md", "docs/tutorial.md", "docs/release.md", "docs/readiness.md", "docs/migration.md",
		"docs/qualification.md", "docs/recovery.md", "schemas/compatibility-contract-v1beta1.schema.json",
		"schemas/graph-v1alpha1.schema.json", "schemas/graph-patch-v1alpha1.schema.json", "schemas/ui-api-v1beta2.schema.json", "schemas/ui-api-v1beta3.schema.json",
		"schemas/lifecycle-migration-v1alpha1.schema.json", "schemas/lifecycle-migration-v1beta1.schema.json", "schemas/authority-adoption-v1alpha1.schema.json", "schemas/authority-rotation-v1alpha1.schema.json", "schemas/authority-relocation-v1alpha1.schema.json", "schemas/lifecycle-projection-v1alpha1.schema.json",
		"schemas/git-artifact-closure-v1alpha1.schema.json", "schemas/git-integration-scope-v1alpha1.schema.json",
		"internal/compatibility/beta-window.json",
	}
	layoutOK := true
	for _, path := range required {
		if _, readErr := readSourceFile(root, path); readErr != nil {
			layoutOK = false
		}
	}
	add("source-layout", layoutOK, chooseCode(layoutOK, "required_public_files_present", "required_public_file_missing"))

	contractReport := contract.Current()
	contractOK := validateContractSchema(root, contractReport) == nil
	add("compatibility-contract", contractOK, chooseCode(contractOK, "contract_schema_valid", "contract_schema_invalid"))

	digestsOK := publishedSchemaDigestsMatch(root, contractReport)
	add("published-schema-digests", digestsOK, chooseCode(digestsOK, "all_schema_digests_match", "schema_digest_mismatch"))
	neutral := validationSubjectBoundary(root)
	add("validation-subject-boundary", neutral, chooseCode(neutral, "generic_boundary_contracts_closed", "generic_boundary_contract_invalid"))

	metadataOK := validateMetadataVersions(root, version.Version)
	add("plugin-metadata-versions", metadataOK, chooseCode(metadataOK, "all_versions_match", "plugin_version_mismatch"))

	bundle, bundleErr := install.LinkedPluginBundleStatus()
	bundleOK := bundleErr == nil && bundle.Version == version.Version && bundle.Digest != "" && bundle.Files > 0
	report.Bundle.Valid = bundleOK
	if bundleErr == nil {
		report.Bundle.Version = bundle.Version
		report.Bundle.Digest = bundle.Digest
		report.Bundle.Files = bundle.Files
		report.Bundle.Bytes = bundle.Bytes
	}
	add("linked-plugin-bundle", bundleOK, chooseCode(bundleOK, "closed_bundle_valid", "linked_bundle_invalid"))

	ci, ciErr := readSourceFile(root, ".github/workflows/ci.yml")
	release, releaseErr := readSourceFile(root, ".github/workflows/release.yml")
	ciOK := ciErr == nil && validYAMLDocument(ci) && containsAll(string(ci), []string{"go test ./...", "go test -race ./...", "go vet ./...", "-fuzz", "FuzzReleaseMetadataInputs", "release-artifact-rehearsal", "sbom-action@", "file: stage/dagrail", "upload-artifact: 'false'", "upload-release-assets: 'false'", "release manifest", "release verify", "govulncheck@", "go-licenses/v2@", "{goos: windows, goarch: arm64", "{goos: darwin, goarch: arm64", "{goos: linux, goarch: arm64"})
	releaseOK := releaseErr == nil && validYAMLDocument(release) && containsAll(string(release), []string{"Build twice and compare", "sbom-action@", "file: stage-a/dagrail", "upload-artifact: 'false'", "upload-release-assets: 'false'", "attest-build-provenance@", "checksums.txt", "release manifest", "release verify", "release-manifest.json", "FuzzReleaseMetadataInputs", "go test -race ./...", "qualify release"})
	add("ci-workflow", ciOK, chooseCode(ciOK, "continuous_gates_declared", "continuous_gate_missing"))
	add("release-workflow", releaseOK, chooseCode(releaseOK, "tag_gates_declared", "release_gate_missing"))
	pinsOK := ciErr == nil && releaseErr == nil && actionsPinned(string(ci)) && actionsPinned(string(release))
	add("workflow-action-pins", pinsOK, chooseCode(pinsOK, "all_actions_commit_pinned", "unpinned_action"))
	matrix, matrixErr := readSourceFile(root, "internal/compatibility/beta-window.json")
	_, decodeMatrixErr := compatibility.Decode(matrix, version.Version)
	historicalOK := matrixErr == nil && decodeMatrixErr == nil && containsAll(string(ci), []string{"historical-binary-compatibility", "fetch-depth: 0", "-tags=historical", "TestHistoricalBinaryCompatibilityWindow"}) && containsAll(string(release), []string{"historical-binary-compatibility", "fetch-depth: 0", "-tags=historical", "TestHistoricalBinaryCompatibilityWindow"})
	add("historical-compatibility", historicalOK, chooseCode(historicalOK, "historical_binary_window_declared", "historical_binary_window_missing"))
	uiServer, uiServerErr := readSourceFile(root, "internal/ui/server.go")
	uiTests, uiTestsErr := readSourceFile(root, "internal/ui/server_test.go")
	originBoundaryOK := uiServerErr == nil && uiTestsErr == nil && containsAll(string(uiServer), []string{"validUIHost", "validUIOrigin", "Cross-Origin-Resource-Policy", "Cross-Origin-Opener-Policy"}) && containsAll(string(uiTests), []string{"TestExplorerRejectsDNSRebindingAndCrossPortOrigins", "rebound.example.invalid", "Origin"})
	add("localhost-origin-boundary", originBoundaryOK, chooseCode(originBoundaryOK, "localhost_origin_boundary_declared", "localhost_origin_boundary_missing"))

	if strings.TrimSpace(projectRoot) == "" {
		addOptional("project-security", "optional_project_not_supplied")
		addOptional("project-recovery", "optional_project_not_supplied")
	} else {
		report.ProjectEvidence = true
		projectService, openErr := service.OpenForInspection(projectRoot)
		if openErr != nil {
			add("project-security", false, "project_open_failed")
			add("project-recovery", false, "project_open_failed")
		} else {
			security := projectService.SecurityAudit()
			add("project-security", security.Secure, chooseCode(security.Secure, "security_audit_passed", "security_audit_failed"))
			recovery, recoveryErr := projectService.RehearseRecovery()
			recoveryOK := recoveryErr == nil && recovery.Ready
			add("project-recovery", recoveryOK, chooseCode(recoveryOK, "recovery_rehearsal_passed", "recovery_rehearsal_failed"))
		}
	}
	return report, nil
}

func publishedSchemaSurfaces(report contract.Report) []contract.DocumentedSurface {
	return []contract.DocumentedSurface{
		report.GraphPatch, report.CommandCatalog, report.CLIError, report.DecisionRecord, report.Installation, report.HistoricalMatrix, report.Readiness,
		report.UI, report.Security, report.JournalVerification,
		report.PluginConformance, report.Support, report.Recovery, report.AuthorityAdoption, report.AuthorityRotation, report.AuthorityRelocation,
		report.GitArtifactClosure, report.GitIntegrationScope,
		report.ReleaseQualification, report.ReleaseManifest, report.ReleaseVerification,
		report.LifecycleMigrationV1Alpha1, report.LifecycleMigration, report.LifecycleProjection,
	}
}

func publishedSchemaDigestsMatch(root string, report contract.Report) bool {
	return publishedSchemaDigestsMatchReader(report, func(relative string) ([]byte, error) {
		return readSourceFile(root, relative)
	})
}

func publishedSchemaDigestsMatchReader(report contract.Report, read func(string) ([]byte, error)) bool {
	valid := true
	for _, surface := range publishedSchemaSurfaces(report) {
		raw, err := read(surface.SchemaPath)
		if err != nil || !json.Valid(raw) {
			valid = false
			continue
		}
		digest := sha256.Sum256(raw)
		if "sha256:"+hex.EncodeToString(digest[:]) != surface.SchemaSHA256 {
			valid = false
		}
	}
	graphRaw, graphErr := read(report.Graph.SchemaPath)
	graphDigest := sha256.Sum256(graphRaw)
	return valid && graphErr == nil && json.Valid(graphRaw) && "sha256:"+hex.EncodeToString(graphDigest[:]) == report.Graph.SchemaSHA256
}

func validationSubjectBoundary(root string) bool {
	adr, adrErr := readSourceFile(root, "docs/adr/0017-validation-subjects-do-not-define-kernel-contracts.md")
	migration, migrationErr := readSourceFile(root, "docs/migration.md")
	if adrErr != nil || migrationErr != nil || !containsAll(string(adr), []string{"repository-neutral", "external converter", "outside DAGrail"}) || !containsAll(string(migration), []string{"Project-neutral boundary", "outside the DAGrail repository and binary"}) {
		return false
	}
	for _, path := range []string{"schemas/lifecycle-migration-v1alpha1.schema.json", "schemas/lifecycle-migration-v1beta1.schema.json"} {
		schemaRaw, schemaErr := readSourceFile(root, path)
		if schemaErr != nil || !closedLifecycleEventSchema(schemaRaw) {
			return false
		}
	}
	return true
}

func closedLifecycleEventSchema(schemaRaw []byte) bool {
	var schema map[string]any
	if json.Unmarshal(schemaRaw, &schema) != nil {
		return false
	}
	if !onlyLocalSchemaRefs(schema) {
		return false
	}
	definitions, ok := schema["$defs"].(map[string]any)
	if !ok {
		return false
	}
	native, ok := definitions["nativeEvent"].(map[string]any)
	if !ok {
		return false
	}
	branches, ok := native["oneOf"].([]any)
	if !ok {
		return false
	}
	declared := map[string]bool{}
	for _, branchValue := range branches {
		branch, ok := branchValue.(map[string]any)
		if !ok {
			return false
		}
		properties, ok := branch["properties"].(map[string]any)
		if !ok {
			return false
		}
		typeSchema, ok := properties["type"].(map[string]any)
		if !ok {
			return false
		}
		if value, ok := typeSchema["const"].(string); ok {
			declared[value] = true
		}
		if values, ok := typeSchema["enum"].([]any); ok {
			for _, value := range values {
				text, ok := value.(string)
				if !ok {
					return false
				}
				declared[text] = true
			}
		}
	}
	want := service.MigratableLifecycleEventTypes()
	if len(declared) != len(want) {
		return false
	}
	for _, eventType := range want {
		if !declared[eventType] {
			return false
		}
	}
	return true
}

func onlyLocalSchemaRefs(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" {
				ref, ok := child.(string)
				if !ok || !strings.HasPrefix(ref, "#/") {
					return false
				}
			}
			if !onlyLocalSchemaRefs(child) {
				return false
			}
		}
	case []any:
		for _, child := range typed {
			if !onlyLocalSchemaRefs(child) {
				return false
			}
		}
	}
	return true
}

func validateContractSchema(root string, report contract.Report) error {
	raw, err := readSourceFile(root, "schemas/compatibility-contract-v1beta1.schema.json")
	if err != nil {
		return err
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:qualification-contract", document); err != nil {
		return err
	}
	schema, err := compiler.Compile("urn:dagrail:qualification-contract")
	if err != nil {
		return err
	}
	instanceRaw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	var instance any
	if err := json.Unmarshal(instanceRaw, &instance); err != nil {
		return err
	}
	return schema.Validate(instance)
}

func validateMetadataVersions(root, expected string) bool {
	files := []struct {
		path     string
		metadata bool
	}{
		{".codex-plugin/plugin.json", false}, {".claude-plugin/plugin.json", false},
		{".plugin/plugin.json", false}, {".github/plugin/marketplace.json", true},
	}
	for _, file := range files {
		raw, err := readSourceFile(root, file.path)
		if err != nil {
			return false
		}
		var value map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(&value); err != nil {
			return false
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return false
		}
		observed, _ := value["version"].(string)
		if file.metadata {
			metadata, _ := value["metadata"].(map[string]any)
			observed, _ = metadata["version"].(string)
		}
		if observed != expected {
			return false
		}
	}
	return true
}

func readSourceFile(root, relative string) ([]byte, error) {
	clean := filepath.Clean(relative)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("source path escapes qualification root")
	}
	path := filepath.Join(root, clean)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxQualificationFileBytes {
		return nil, fmt.Errorf("qualification input is missing or invalid")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("qualification input is missing or invalid")
	}
	relativeToRoot, err := filepath.Rel(root, resolved)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("qualification input escapes its root")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, fmt.Errorf("qualification input is missing or invalid")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxQualificationFileBytes+1))
	if err != nil || len(raw) > maxQualificationFileBytes {
		return nil, fmt.Errorf("qualification input exceeds its limit")
	}
	return raw, nil
}

func actionsPinned(workflow string) bool {
	uses := regexp.MustCompile(`(?m)^\s*-\s*uses:\s*([^\s#]+)`).FindAllStringSubmatch(workflow, -1)
	if len(uses) == 0 {
		return false
	}
	pin := regexp.MustCompile(`^[^@]+@[a-f0-9]{40}$`)
	for _, match := range uses {
		if strings.HasPrefix(match[1], "./") {
			continue
		}
		if !pin.MatchString(match[1]) {
			return false
		}
	}
	return true
}

func validYAMLDocument(raw []byte) bool {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document any
	if err := decoder.Decode(&document); err != nil || document == nil {
		return false
	}
	var trailing any
	return decoder.Decode(&trailing) == io.EOF
}

func containsAll(value string, required []string) bool {
	for _, item := range required {
		if !strings.Contains(value, item) {
			return false
		}
	}
	return true
}

func chooseCode(condition bool, success, failure string) string {
	if condition {
		return success
	}
	return failure
}

func releaseRequirements() []Requirement {
	return []Requirement{
		{ID: "unit-integration-tests", Status: "automated", AutomatedBy: "ci+release"},
		{ID: "race-detection", Status: "automated", AutomatedBy: "ci+release"},
		{ID: "fuzz-smoke", Status: "automated", AutomatedBy: "ci+release"},
		{ID: "static-analysis", Status: "automated", AutomatedBy: "ci+release"},
		{ID: "six-target-static-build", Status: "automated", AutomatedBy: "ci+release"},
		{ID: "vulnerability-and-license", Status: "automated", AutomatedBy: "ci+release"},
		{ID: "reproducible-build", Status: "automated", AutomatedBy: "ci+release"},
		{ID: "sbom-checksum-provenance", Status: "automated", AutomatedBy: "release"},
		{ID: "closed-artifact-manifest", Status: "automated", AutomatedBy: "ci+release"},
		{ID: "historical-binary-window", Status: "automated", AutomatedBy: "ci+release"},
	}
}

func adoptionRequirements() []Requirement {
	return []Requirement{
		{ID: "independent-external-adopter", Status: "outstanding", AutomatedBy: "manual"},
		{ID: "long-running-live-dag", Status: "outstanding", AutomatedBy: "manual"},
		{ID: "real-host-dispatch-receipts", Status: "outstanding", AutomatedBy: "manual"},
		{ID: "operator-backup-restore-drill", Status: "outstanding", AutomatedBy: "manual"},
	}
}
