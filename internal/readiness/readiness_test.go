package readiness_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/CongBao/dagrail/internal/readiness"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestSourceReadinessStopsAtExternalValidation(t *testing.T) {
	report, err := readiness.Evaluate(context.Background(), readiness.Options{SourceRoot: filepath.Join("..", "..")})
	if err != nil {
		t.Fatal(err)
	}
	if !report.StructuralCandidate || !report.ExternalValidationReady || report.Decision != "ready_for_external_validation" || report.OneDotZeroReady || report.ProductionValidated || len(report.AdoptionGaps) != 4 {
		t.Fatalf("readiness overstated or understated evidence: %#v", report)
	}
	for _, gap := range report.AdoptionGaps {
		if gap.Status != "outstanding" || gap.RequiredFor != "production_validation" {
			t.Fatalf("adoption gap was weakened: %#v", gap)
		}
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 32*1024 {
		t.Fatalf("readiness report is unbounded: %d", len(raw))
	}
	validateSchema(t, raw)
}

func TestReadinessHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readiness.Evaluate(ctx, readiness.Options{SourceRoot: filepath.Join("..", "..")}); err == nil {
		t.Fatal("cancelled readiness evaluation continued")
	}
}

func validateSchema(t *testing.T, raw []byte) {
	t.Helper()
	schemaRaw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "readiness-decision-v1alpha1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document, instance any
	if err := json.Unmarshal(schemaRaw, &document); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:readiness-decision", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:readiness-decision")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("readiness report does not match schema: %v", err)
	}
}
