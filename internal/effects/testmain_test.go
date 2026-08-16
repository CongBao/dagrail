package effects_test

import (
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/CongBao/dagrail/internal/project"
)

func TestMain(m *testing.M) {
	os.Exit(runTestsWithIsolatedAuthority(m))
}

func runTestsWithIsolatedAuthority(m *testing.M) int {
	if inherited := os.Getenv("DAGRAIL_TEST_AUTHORITY_HOME"); inherited != "" && os.Getenv("DAGRAIL_TEST_AUTHORITY_PARENT_PID") == strconv.Itoa(os.Getppid()) {
		if err := project.SetAuthorityRootForTesting(inherited); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		_ = os.Setenv("DAGRAIL_TEST_AUTHORITY_PARENT_PID", strconv.Itoa(os.Getpid()))
		return m.Run()
	}
	root, err := os.MkdirTemp("", "dagrail-effects-authority-*")
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
