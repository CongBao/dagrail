package contract

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestBetaContractIsDeterministicAndNamesExactlySixMCPTools(t *testing.T) {
	first, second := Current(), Current()
	firstRaw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstRaw, secondRaw) {
		t.Fatal("compatibility contract is not deterministic")
	}
	if first.APIVersion != "dagrail.io/v1beta1" || first.Kind != "CompatibilityContract" || first.UI.APIVersion != "dagrail.io/ui/v1beta1" || len(first.MCP) != 6 {
		t.Fatalf("unexpected beta contract: %#v", first)
	}
	for name, surface := range map[string]DocumentedSurface{"ui": first.UI, "security": first.Security, "journal verification": first.JournalVerification} {
		schemaRaw, err := os.ReadFile("../../" + surface.SchemaPath)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(schemaRaw)
		if got := "sha256:" + fmt.Sprintf("%x", digest); got != surface.SchemaSHA256 {
			t.Fatalf("%s schema digest drift: contract=%s file=%s", name, surface.SchemaSHA256, got)
		}
	}
	want := []string{"dag_context", "dag_inspect", "dag_apply", "dag_graph_change", "dag_reconcile", "dag_pre_wait"}
	for index, tool := range first.MCP {
		if tool.Name != want[index] || tool.InputSchemaSHA256 == "" {
			t.Fatalf("tool %d = %#v", index, tool)
		}
	}
}

func TestBetaContractMatchesItsPublishedSchema(t *testing.T) {
	schemaRaw, err := os.ReadFile("../../schemas/compatibility-contract-v1beta1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(schemaRaw, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:compatibility-contract", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:compatibility-contract")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(Current())
	if err != nil {
		t.Fatal(err)
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("contract does not match published schema: %v", err)
	}
}
