package install

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/CongBao/dagrail/internal/mcpserver"
	"github.com/CongBao/dagrail/internal/version"
)

func TestMain(main *testing.M) {
	probeMCP = func(context.Context, string, string) (mcpserver.ProbeReport, error) {
		return mcpserver.ProbeReport{ServerHandshakeReady: true, ToolCount: 6}, nil
	}
	pluginRuntimeStatus = func() (RuntimeResult, error) {
		path, _ := DefaultRuntimePath()
		if path == "" {
			path = filepath.Join(os.TempDir(), "dagrail-test-runtime")
		}
		return RuntimeResult{Status: "verified", Version: version.Version, RuntimePath: path}, nil
	}
	os.Exit(main.Run())
}
