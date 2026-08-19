package commandcatalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCatalogIsClosedSortedAndDeterministic(t *testing.T) {
	report := Current("0.17.0")
	if report.APIVersion != APIVersion || report.Kind != "CommandCatalog" || len(report.Commands) != 38 {
		t.Fatalf("unexpected catalog: %#v", report)
	}
	previous := ""
	for _, command := range report.Commands {
		if command.Name <= previous || command.Summary == "" || command.Effect == "" || command.Project == "" || command.Output == "" || command.Subcommands == nil {
			t.Fatalf("invalid or unsorted command after %q: %#v", previous, command)
		}
		previous = command.Name
		for index := 1; index < len(command.Subcommands); index++ {
			if command.Subcommands[index] <= command.Subcommands[index-1] {
				t.Fatalf("subcommands are not closed and sorted: %#v", command)
			}
		}
	}
	first, _ := json.Marshal(Current("0.17.0"))
	second, _ := json.Marshal(Current("0.17.0"))
	if string(first) != string(second) || len(first) > 32*1024 {
		t.Fatalf("catalog is nondeterministic or unbounded: %d bytes", len(first))
	}
	validateCatalogSchema(t, first)
}

func TestV026LifecycleAndHostSubcommandsAreDiscoverable(t *testing.T) {
	byName := map[string]Command{}
	for _, command := range Current("0.26.0").Commands {
		byName[command.Name] = command
	}
	if byName["mcp"].Project != "optional" || !contains(byName["mcp"].Subcommands, "probe") {
		t.Fatalf("MCP lazy bootstrap and fresh-process probe are not discoverable: %#v", byName["mcp"])
	}
	if !contains(byName["plugin"].Subcommands, "update") {
		t.Fatalf("plugin update is not discoverable: %#v", byName["plugin"])
	}
	for _, subcommand := range []string{"status", "stop"} {
		if !contains(byName["ui"].Subcommands, subcommand) {
			t.Fatalf("UI %s is not discoverable: %#v", subcommand, byName["ui"])
		}
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func validateCatalogSchema(t *testing.T, raw []byte) {
	t.Helper()
	schemaRaw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "command-catalog-v1alpha1.schema.json"))
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
	if err := compiler.AddResource("urn:dagrail:command-catalog", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:command-catalog")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("catalog does not match schema: %v", err)
	}
}

func TestAllCompletionTargetsComeFromCatalog(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		result, err := Completion(shell)
		if err != nil {
			t.Fatal(err)
		}
		if len(result) > 64*1024 || strings.Contains(result, "TODO") {
			t.Fatalf("%s completion is invalid or unbounded", shell)
		}
		for _, command := range commands {
			if !strings.Contains(result, command.Name) {
				t.Fatalf("%s completion omitted %s", shell, command.Name)
			}
		}
	}
	if _, err := Completion("unknown"); err == nil {
		t.Fatal("unsupported shell must fail closed")
	}
}
