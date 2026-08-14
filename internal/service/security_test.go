package service_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/service"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestSecurityAuditIsSafeAndDetectsPermissionDrift(t *testing.T) {
	root := t.TempDir()
	dataHome := filepath.Join(root, ".data")
	t.Setenv("DAGRAIL_HOME", dataHome)
	svc, err := service.Init(root, "security")
	if err != nil {
		t.Fatal(err)
	}
	report := svc.SecurityAudit()
	if !report.Secure || report.Boundary.MultiTenantIsolation || report.Boundary.MaliciousPeerProcess {
		t.Fatalf("fresh security boundary is incorrect: %+v", report)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), root) || strings.Contains(string(raw), dataHome) {
		t.Fatalf("security diagnostics disclosed absolute paths: %s", raw)
	}
	verification, err := svc.VerifyJournalReport()
	if err != nil || !verification.Valid || !strings.HasPrefix(verification.ExportSHA256, "sha256:") {
		t.Fatalf("journal verification report is incomplete: %+v %v", verification, err)
	}
	validatePublishedSchema(t, "../../schemas/security-audit-v1alpha1.schema.json", report)
	validatePublishedSchema(t, "../../schemas/journal-verification-v1alpha1.schema.json", verification)
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(svc.Project.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	report = svc.SecurityAudit()
	if report.Secure {
		t.Fatalf("world-readable project data directory was not detected: %+v", report)
	}
}

func TestSecurityAuditRejectsInsecureOrphanJournalTemporaryFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("portable mode-bit policy is not asserted on Windows")
	}
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "temporary-file-audit")
	if err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(svc.Project.DataDir, "journal", ".segment-orphan")
	if err := os.WriteFile(temporary, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	if report := svc.SecurityAudit(); report.Secure {
		t.Fatalf("insecure orphan journal file was not detected: %+v", report)
	}
}

func validatePublishedSchema(t *testing.T, path string, value any) {
	t.Helper()
	schemaRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(schemaRaw, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:test-schema", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:test-schema")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var instance any
	if err := decoder.Decode(&instance); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("value does not match %s: %v", path, err)
	}
}
