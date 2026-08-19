package compatibility

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/version"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestEmbeddedBetaWindowIsClosedAndSchemaValid(t *testing.T) {
	window, evidence, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if window.CurrentVersion != version.Version || evidence.Historical != 24 || !strings.HasPrefix(evidence.Digest, "sha256:") {
		t.Fatalf("unexpected compatibility evidence: %#v %#v", window, evidence)
	}
	raw, err := json.Marshal(window)
	if err != nil {
		t.Fatal(err)
	}
	schemaRaw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "historical-binary-matrix-v1alpha1.schema.json"))
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
	if err := compiler.AddResource("urn:dagrail:historical-binary-matrix", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:historical-binary-matrix")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("matrix does not match schema: %v", err)
	}
}

func TestBetaWindowRejectsMutationAndOmission(t *testing.T) {
	raw, err := windowFiles.ReadFile("beta-window.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string]string{
		"wrong candidate": strings.Replace(string(raw), `"currentVersion": "0.26.2"`, `"currentVersion": "1.0.0"`, 1),
		"duplicate key":   strings.Replace(string(raw), `"kind": "HistoricalBinaryMatrix"`, `"kind": "HistoricalBinaryMatrix", "kind": "HistoricalBinaryMatrix"`, 1),
		"missing release": strings.Replace(string(raw), `    {"version": "0.22.2", "commit": "b73186b5cf7402f590e99a12b5886eee3d47fd0e"}`, "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(mutation), version.Version); err == nil {
				t.Fatal("mutated compatibility window was accepted")
			}
		})
	}
}

func TestHistoricalWindowDocumentationMatchesClosedManifest(t *testing.T) {
	const exactWindow = "v0.10.0–v0.26.1"
	for _, path := range []string{"COMPATIBILITY.md", "SECURITY.md", filepath.Join("docs", "qualification.md"), filepath.Join("docs", "readiness.md")} {
		raw, err := os.ReadFile(filepath.Join("..", "..", path))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), exactWindow) {
			t.Fatalf("%s does not name the exact closed historical window %s", path, exactWindow)
		}
	}
}
