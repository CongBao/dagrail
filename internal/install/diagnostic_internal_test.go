package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/CongBao/dagrail/internal/version"
)

func TestInstallationDiagnosticUsesVerifiedReceiptRuntimeForCodexMCP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fresh-host shell fixture")
	}
	root := t.TempDir()
	dataRoot := filepath.Join(root, "runtime")
	binRoot := filepath.Join(root, "bin")
	t.Setenv("DAGRAIL_HOME", dataRoot)
	if err := os.MkdirAll(binRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "dagrail-source")
	runtimeScript := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = version ]; then echo '{\"version\":\"%s\"}'; exit 0; fi\nexit 1\n", version.Version)
	if err := os.WriteFile(source, []byte(runtimeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(binRoot, "dagrail")
	if _, err := installRuntimeFrom(context.Background(), source, runtimePath, dataRoot, runtimeVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializePluginBundle(); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(binRoot, "codex")
	hostScript := fmt.Sprintf(`#!/bin/sh
if [ "$1" = plugin ] && [ "$2" = list ]; then
  echo '{"plugins":[{"name":"dagrail"}]}'
  exit 0
fi
if [ "$1" = mcp ] && [ "$2" = list ] && [ "$3" = --json ]; then
  echo '{"servers":[{"name":"dagrail","command":"%s","args":["mcp","--stdio"]}]}'
  exit 0
fi
exit 1
`, runtimePath)
	if err := os.WriteFile(codex, []byte(hostScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binRoot)

	report, err := Diagnose(context.Background(), Options{Harnesses: []string{"codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Healthy || len(report.Harnesses) != 1 || !report.Harnesses[0].MCPConfigured || !report.Harnesses[0].Ready {
		t.Fatalf("doctor did not bind MCP diagnosis to the verified receipt runtime: %+v", report)
	}
}
