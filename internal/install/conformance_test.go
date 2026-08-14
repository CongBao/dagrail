package install

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/version"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestConformanceUsesClosedReasonsAndOmitsExecutablePaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime"))
	t.Setenv("PATH", filepath.Join(root, "empty-path"))
	if _, err := MaterializePluginBundle(); err != nil {
		t.Fatal(err)
	}
	report, err := Conformance(Options{Harnesses: []string{"codex", "claude-code", "copilot-cli"}, RuntimePath: filepath.Join(root, "bin", "dagrail")})
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || !report.Bundle.Verified || report.Runtime.Verified || len(report.Harnesses) != 3 {
		t.Fatalf("unexpected unavailable-host conformance: %+v", report)
	}
	for _, item := range report.Harnesses {
		joined := strings.Join(item.UnavailableCodes, ",")
		if item.Ready || !item.ManualFallback || !strings.Contains(joined, "runtime_unverified") || (!item.Detected && !strings.Contains(joined, "harness_not_detected")) {
			t.Fatalf("conformance did not retain safe fallback: %+v", item)
		}
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), root) || strings.Contains(string(raw), "executable") {
		t.Fatalf("conformance report disclosed executable paths: %s", raw)
	}
	validateConformanceSchema(t, report)
}

func validateConformanceSchema(t *testing.T, report ConformanceReport) {
	t.Helper()
	raw, err := os.ReadFile("../../schemas/plugin-conformance-v1alpha1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:plugin-conformance", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:plugin-conformance")
	if err != nil {
		t.Fatal(err)
	}
	instanceRaw, _ := json.Marshal(report)
	var instance any
	if err := json.Unmarshal(instanceRaw, &instance); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("conformance report does not match published schema: %v", err)
	}
}

func TestSafeProbeVersionRejectsCredentialsAndUnboundedOutput(t *testing.T) {
	if safeProbeVersion("dagrail-host 1.2.3") == "" {
		t.Fatal("ordinary version was redacted")
	}
	for _, value := range []string{"Bearer abcdefghijklmnopqrstuvwxyz", "github_pat_abcdefghijklmnopqrstuvwxyz", strings.Repeat("x", 257), "line one\nline two", "/private/host/path", `C:\\private\\host`} {
		if observed := safeProbeVersion(value); observed != "" {
			t.Fatalf("unsafe version output was retained: %q", observed)
		}
	}
}

func TestFreshHostFixturesConformAcrossAllThreeHarnesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fresh-host shell fixtures are covered by portable plan tests on Windows")
	}
	root := t.TempDir()
	dataRoot := filepath.Join(root, "runtime")
	binRoot := filepath.Join(root, "bin")
	t.Setenv("DAGRAIL_HOME", dataRoot)
	if err := os.MkdirAll(binRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeSource := filepath.Join(root, "dagrail-source")
	runtimeScript := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = version ]; then printf '%%s\\n' '{\"version\":\"%s\"}'; exit 0; fi\nexit 1\n", version.Version)
	if err := os.WriteFile(runtimeSource, []byte(runtimeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(binRoot, "dagrail")
	if _, err := installRuntimeFrom(context.Background(), runtimeSource, runtimePath, dataRoot, runtimeVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializePluginBundle(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "claude", "copilot"} {
		path := filepath.Join(binRoot, name)
		script := `#!/bin/sh
case "$1" in
  --version) echo "fixture 1.0.0" ;;
  --help) echo "--print --output-format --session-id --resume" ;;
  --acp)
    IFS= read -r line
    id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
    printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":false},"agentInfo":{"name":"fixture","version":"1.0.0"}}}\n' "$id"
    while IFS= read -r ignored; do :; done
    ;;
  *) echo "dagrail installed" ;;
esac
`
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binRoot+string(os.PathListSeparator)+os.Getenv("PATH"))
	report, err := Conformance(Options{Harnesses: []string{"codex", "claude-code", "copilot-cli"}, RuntimePath: runtimePath})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || !report.Runtime.Verified || !report.Bundle.Verified || len(report.Harnesses) != 3 {
		t.Fatalf("fresh host did not conform: %+v", report)
	}
	for _, item := range report.Harnesses {
		if !item.Ready || !item.Detected || item.PluginStatus != "installed" || !item.MCPConfigured || !item.ManualFallback || len(item.UnavailableCodes) != 0 {
			t.Fatalf("fixture host is not ready: %+v", item)
		}
	}
}
