package install_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/install"
	"github.com/CongBao/dagrail/internal/version"
)

func TestInstallPlanUsesOneAbsoluteRuntimeForAllThreeHarnesses(t *testing.T) {
	runtime := filepath.Join(t.TempDir(), "bin", "dagrail")
	plans, err := install.Plan(install.Options{Harnesses: []string{"codex", "claude-code", "copilot-cli"}, RuntimePath: runtime, MarketplaceSource: "CongBao/dagrail"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 3 {
		t.Fatalf("plans=%d", len(plans))
	}
	for _, plan := range plans {
		joined := strings.Join(plan.MCPAdd, " ")
		if plan.Harness == "codex" {
			if len(plan.MCPAdd) != 0 {
				t.Fatalf("Codex must use the receipt-bound plugin MCP projection, not a standalone registration: %s", joined)
			}
		} else if !strings.Contains(joined, runtime+" mcp --stdio") {
			t.Fatalf("%s MCP launcher is not absolute and shared: %s", plan.Harness, joined)
		}
		if len(plan.PluginInstall) == 0 || len(plan.MCPRemove) == 0 {
			t.Fatalf("incomplete install plan: %#v", plan)
		}
	}
}

func TestInstallPlanUsesBundledLocalMarketplaceWithoutRemoteRef(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "bin", "dagrail")
	marketplace := filepath.Join(root, "marketplace")
	plans, err := install.Plan(install.Options{Harnesses: []string{"codex", "claude-code", "copilot-cli"}, RuntimePath: runtime, MarketplaceSource: marketplace})
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range plans {
		if !strings.Contains(strings.Join(plan.PluginInstall, " "), "dagrail@dagrail-bundled") {
			t.Fatalf("%s did not select the bundled marketplace: %#v", plan.Harness, plan)
		}
		if strings.Contains(strings.Join(plan.MarketplaceAdd, " "), "--ref") {
			t.Fatalf("%s treated a local marketplace as a remote branch: %#v", plan.Harness, plan)
		}
		if !strings.Contains(strings.Join(plan.MarketplaceRemove, " "), install.BundledMarketplaceName) {
			t.Fatalf("%s did not plan bundled marketplace cleanup: %#v", plan.Harness, plan)
		}
		if len(plan.PluginRemove) == 0 {
			t.Fatalf("%s did not plan plugin removal: %#v", plan.Harness, plan)
		}
	}
}

func TestPublishedPluginMetadataMatchesRuntimeVersion(t *testing.T) {
	for _, path := range []string{
		"../../.codex-plugin/plugin.json",
		"../../.claude-plugin/plugin.json",
		"../../.plugin/plugin.json",
		"../../.github/plugin/marketplace.json",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		observed, _ := value["version"].(string)
		if metadata, ok := value["metadata"].(map[string]any); ok {
			observed, _ = metadata["version"].(string)
		}
		if observed != version.Version {
			t.Fatalf("%s version %q does not match runtime %q", path, observed, version.Version)
		}
	}
}
