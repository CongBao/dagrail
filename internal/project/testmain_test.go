package project

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestMain(m *testing.M) {
	os.Exit(runTestsWithIsolatedAuthority(m))
}

func TestTopLevelTestProcessDoesNotReuseCallerAuthorityHome(t *testing.T) {
	external := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestAuthorityIsolationHelper$", "-test.count=1")
	command.Env = []string{
		"DAGRAIL_TESTMAIN_ISOLATION_HELPER=1",
		"DAGRAIL_TEST_AUTHORITY_HOME=" + external,
		"DAGRAIL_TEST_AUTHORITY_PARENT_PID=untrusted",
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + os.TempDir(),
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("isolated test subprocess failed: %v\n%s", err, output)
	}
	entries, err := os.ReadDir(external)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("top-level test process polluted caller authority home: %v", entries)
	}
}

func TestAuthorityIsolationHelper(t *testing.T) {
	if os.Getenv("DAGRAIL_TESTMAIN_ISOLATION_HELPER") != "1" {
		return
	}
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(t.TempDir(), "data"))
	if _, err := Init(root, "testmain-isolation-helper"); err != nil {
		t.Fatal(err)
	}
}

func runTestsWithIsolatedAuthority(m *testing.M) int {
	if inherited := os.Getenv("DAGRAIL_TEST_AUTHORITY_HOME"); inherited != "" && os.Getenv("DAGRAIL_TEST_AUTHORITY_PARENT_PID") == strconv.Itoa(os.Getppid()) {
		if err := SetAuthorityRootForTesting(inherited); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		_ = os.Setenv("DAGRAIL_TEST_AUTHORITY_PARENT_PID", strconv.Itoa(os.Getpid()))
		return m.Run()
	}
	root, err := os.MkdirTemp("", "dagrail-project-authority-*")
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
	if err := SetAuthorityRootForTesting(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return m.Run()
}
