package install_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/install"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestInstallationDiagnosticIsBoundedPathFreeAndSchemaValid(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", root)
	t.Setenv("PATH", root)
	report, err := install.Diagnose(context.Background(), install.Options{Harnesses: []string{"claude-code"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if report.Healthy || len(raw) > 16*1024 || strings.Contains(string(raw), root) {
		t.Fatalf("diagnostic is unexpectedly healthy, unbounded, or path-leaking: %s", raw)
	}
	schemaRaw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "installation-diagnostic-v1alpha1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schemaDocument, instance any
	if err := json.Unmarshal(schemaRaw, &schemaDocument); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:installation-diagnostic", schemaDocument); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:installation-diagnostic")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("diagnostic does not match schema: %v", err)
	}
}

func TestInstallationDiagnosticHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := install.Diagnose(ctx, install.Options{Harnesses: []string{"claude-code"}})
	if err == nil {
		t.Fatal("cancelled diagnostic must not continue")
	}
}
