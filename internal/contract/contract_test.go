package contract

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/compatibility"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestContractSourceDoesNotCopySchemaDigests(t *testing.T) {
	raw, err := os.ReadFile("contract.go")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`SchemaSHA256:\s*"sha256:[a-f0-9]{64}"`).Match(raw) {
		t.Fatal("contract source contains a manually copied schema digest")
	}
}

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
	if first.APIVersion != "dagrail.io/v1beta1" || first.Kind != "CompatibilityContract" || first.UI.APIVersion != "dagrail.io/ui/v1beta3" || first.GraphPatch.APIVersion != "dagrail.io/v1alpha1" || len(first.MCP) != 6 {
		t.Fatalf("unexpected beta contract: %#v", first)
	}
	for name, surface := range map[string]DocumentedSurface{"graph patch": first.GraphPatch, "command catalog": first.CommandCatalog, "CLI error": first.CLIError, "decision record": first.DecisionRecord, "installation diagnostic": first.Installation, "historical binary matrix": first.HistoricalMatrix, "readiness decision": first.Readiness, "ui": first.UI, "security": first.Security, "journal verification": first.JournalVerification, "plugin conformance": first.PluginConformance, "support": first.Support, "recovery": first.Recovery, "authority adoption": first.AuthorityAdoption, "authority rotation": first.AuthorityRotation, "authority relocation": first.AuthorityRelocation, "git artifact closure": first.GitArtifactClosure, "git integration scope": first.GitIntegrationScope, "release qualification": first.ReleaseQualification, "release manifest": first.ReleaseManifest, "release verification": first.ReleaseVerification, "lifecycle migration v1alpha1": first.LifecycleMigrationV1Alpha1, "lifecycle migration v1beta1": first.LifecycleMigration, "lifecycle projection": first.LifecycleProjection} {
		schemaRaw, err := os.ReadFile("../../" + surface.SchemaPath)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(schemaRaw)
		if got := "sha256:" + fmt.Sprintf("%x", digest); got != surface.SchemaSHA256 {
			t.Fatalf("%s schema digest drift: contract=%s file=%s", name, surface.SchemaSHA256, got)
		}
	}
	graphSchema, err := os.ReadFile("../../" + first.Graph.SchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	graphDigest := sha256.Sum256(graphSchema)
	if got := "sha256:" + fmt.Sprintf("%x", graphDigest); got != first.Graph.SchemaSHA256 {
		t.Fatalf("graph schema digest drift: contract=%s file=%s", first.Graph.SchemaSHA256, got)
	}
	wantCapabilities := []string{"declared-lanes", "dynamic-graph", "hierarchical-subgraphs", "historical-lifecycle-import", "lifecycle-projection", "positive-predicate-ast", "resource-capacities", "resource-requests", "role-leases"}
	if !reflect.DeepEqual(first.Graph.Capabilities, wantCapabilities) {
		t.Fatalf("graph capabilities are not closed and deterministic: %v", first.Graph.Capabilities)
	}
	want := []string{"dag_context", "dag_inspect", "dag_apply", "dag_graph_change", "dag_reconcile", "dag_pre_wait"}
	for index, tool := range first.MCP {
		if tool.Name != want[index] || tool.InputSchemaSHA256 == "" {
			t.Fatalf("tool %d = %#v", index, tool)
		}
	}
	wantContexts := []ContextBudget{{View: "orchestrator", Bytes: 12288}, {View: "reviewer", Bytes: 12288}, {View: "worker", Bytes: 8192}}
	if !reflect.DeepEqual(first.Contexts, wantContexts) {
		t.Fatalf("context budgets drifted from the public contract: got=%v want=%v", first.Contexts, wantContexts)
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

func TestPublishedGraphContractsAcceptHierarchicalGroupsAndClosedMoves(t *testing.T) {
	instances := []struct {
		path string
		raw  string
	}{
		{
			path: "../../schemas/graph-v1alpha1.schema.json",
			raw:  `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"grouped"},"spec":{"roles":[],"groups":[{"id":"phase","title":"Phase","kind":"custom"},{"id":"work","title":"Work","kind":"work-unit","parentGroupId":"phase","summaryNodeId":"done","collapsedByDefault":true}],"nodes":[{"id":"done","kind":"milestone","title":"Done","groupId":"work","outcomes":[{"id":"reached","class":"success"}]}],"edges":[]}}`,
		},
		{
			path: "../../schemas/graph-patch-v1alpha1.schema.json",
			raw:  `{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"moveNodeToGroup","nodeId":"done","groupId":"work"}]}`,
		},
	}
	for _, test := range instances {
		schemaRaw, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		var document, instance any
		if json.Unmarshal(schemaRaw, &document) != nil || json.Unmarshal([]byte(test.raw), &instance) != nil {
			t.Fatalf("invalid test document for %s", test.path)
		}
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		if err := compiler.AddResource(test.path, document); err != nil {
			t.Fatal(err)
		}
		schema, err := compiler.Compile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Fatalf("%s rejected a supported hierarchical document: %v", test.path, err)
		}
	}

	graphSchemaRaw, err := os.ReadFile("../../schemas/graph-v1alpha1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var graphDocument any
	if err := json.Unmarshal(graphSchemaRaw, &graphDocument); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:bounded-group-graph", graphDocument); err != nil {
		t.Fatal(err)
	}
	graphSchema, err := compiler.Compile("urn:dagrail:bounded-group-graph")
	if err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("n", 257)
	grouped := fmt.Sprintf(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"grouped"},"spec":{"roles":[],"groups":[{"id":"work","title":"Work","kind":"work-unit"}],"nodes":[{"id":%q,"kind":"milestone","title":"Done","groupId":"work","outcomes":[{"id":"done","class":"success"}]}]}}`, oversized)
	legacy := fmt.Sprintf(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"legacy"},"spec":{"roles":[],"nodes":[{"id":%q,"kind":"milestone","title":"Done","outcomes":[{"id":"done","class":"success"}]}]}}`, oversized)
	for name, test := range map[string]struct {
		raw  string
		want bool
	}{"grouped oversized": {grouped, false}, "legacy oversized": {legacy, true}} {
		var instance any
		if err := json.Unmarshal([]byte(test.raw), &instance); err != nil {
			t.Fatal(err)
		}
		err := graphSchema.Validate(instance)
		if (err == nil) != test.want {
			t.Fatalf("%s schema result mismatch: want valid=%v err=%v", name, test.want, err)
		}
	}
}

func TestBetaContractPromiseNamesTheClosedHistoricalWindow(t *testing.T) {
	window, _, err := compatibility.Current()
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("v%s through v%s", window.FromVersion, window.Entries[len(window.Entries)-1].Version)
	found := false
	for _, promise := range Current().Promises {
		found = found || strings.Contains(promise, want)
	}
	if !found {
		t.Fatalf("compatibility promises do not name the executable historical window %s: %v", want, Current().Promises)
	}
}
