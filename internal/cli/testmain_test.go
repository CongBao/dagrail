package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/install"
	"github.com/CongBao/dagrail/internal/project"
	"github.com/CongBao/dagrail/internal/version"
)

func TestMain(m *testing.M) {
	// InstallRuntime validates a candidate in a fresh process before publishing
	// it. The cli_test executable stands in for the linked runtime in the one
	// offline bundle test, so expose only the exact, production-shaped version
	// probe when that child is explicitly marked as a fresh-process probe.
	if os.Getenv("DAGRAIL_FRESH_PROCESS_PROBE") == "1" && len(os.Args) == 2 && os.Args[1] == "version" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"version": version.Version, "commit": version.Commit})
		os.Exit(0)
	}
	os.Exit(runTestsWithIsolatedAuthority(m))
}

func runTestsWithIsolatedAuthority(m *testing.M) int {
	_ = os.Setenv("DAGRAIL_DAEMON_DISABLE", "1")
	realRuntime, _ := install.DefaultRuntimePath()
	if realRuntime != "" {
		realRuntimeDir := filepath.Clean(filepath.Dir(realRuntime))
		filtered := make([]string, 0)
		for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
			if filepath.Clean(entry) != realRuntimeDir {
				filtered = append(filtered, entry)
			}
		}
		_ = os.Setenv("PATH", strings.Join(filtered, string(os.PathListSeparator)))
	}
	// InstallRuntime is intentionally exercised by the CLI suite, but a test
	// process must never publish its test binary over the user's real runtime.
	// Keep both the runtime and its receipt inside the same disposable root as
	// the isolated authority. This also prevents parallel package tests from
	// turning the PATH-resolved doctor probe into a recursive cli.test run.
	runtimeRoot, err := os.MkdirTemp("", "dagrail-cli-runtime-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer os.RemoveAll(runtimeRoot)
	runtimeName := "dagrail"
	if os.PathSeparator == '\\' {
		runtimeName += ".exe"
	}
	_ = os.Setenv("DAGRAIL_RUNTIME_PATH", filepath.Join(runtimeRoot, "bin", runtimeName))
	if inherited := os.Getenv("DAGRAIL_TEST_AUTHORITY_HOME"); inherited != "" && os.Getenv("DAGRAIL_TEST_AUTHORITY_PARENT_PID") == strconv.Itoa(os.Getppid()) {
		if err := project.SetAuthorityRootForTesting(inherited); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		_ = os.Setenv("DAGRAIL_TEST_AUTHORITY_PARENT_PID", strconv.Itoa(os.Getpid()))
		return m.Run()
	}
	root, err := os.MkdirTemp("", "dagrail-cli-authority-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer os.RemoveAll(root)
	if err := os.Setenv("DAGRAIL_TEST_AUTHORITY_HOME", root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	_ = os.Setenv("DAGRAIL_TEST_AUTHORITY_PARENT_PID", strconv.Itoa(os.Getpid()))
	if err := project.SetAuthorityRootForTesting(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return m.Run()
}
