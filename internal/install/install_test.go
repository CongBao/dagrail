package install_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/install"
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
		if !strings.Contains(joined, runtime+" mcp --stdio") {
			t.Fatalf("%s MCP launcher is not absolute and shared: %s", plan.Harness, joined)
		}
		if len(plan.PluginInstall) == 0 || len(plan.MCPRemove) == 0 {
			t.Fatalf("incomplete install plan: %#v", plan)
		}
	}
}
