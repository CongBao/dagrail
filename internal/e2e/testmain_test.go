package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/cli"
	"github.com/CongBao/dagrail/internal/controller"
	"github.com/CongBao/dagrail/internal/project"
)

func TestMain(m *testing.M) {
	if os.Getenv("DAGRAIL_DAEMON_CHILD") == "1" {
		os.Exit(runIsolatedDaemonChild())
	}
	os.Exit(runTestsWithIsolatedAuthority(m))
}

func runIsolatedDaemonChild() int {
	if os.Getenv("DAGRAIL_TEST_AUTHORITY_PARENT_PID") != strconv.Itoa(os.Getppid()) {
		fmt.Fprintln(os.Stderr, "test daemon parent identity does not match")
		return 2
	}
	if err := project.SetAuthorityRootForTesting(os.Getenv("DAGRAIL_TEST_AUTHORITY_HOME")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := controller.SetRuntimeDirForTesting(os.Getenv("DAGRAIL_TEST_DAEMON_HOME")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var stderr bytes.Buffer
	if err := cli.Run(os.Args[1:], strings.NewReader(""), os.Stdout, &stderr); err != nil {
		fmt.Fprintln(os.Stderr, stderr.String()+err.Error())
		return 1
	}
	return 0
}

func runTestsWithIsolatedAuthority(m *testing.M) int {
	if inherited := os.Getenv("DAGRAIL_TEST_AUTHORITY_HOME"); inherited != "" && os.Getenv("DAGRAIL_TEST_AUTHORITY_PARENT_PID") == strconv.Itoa(os.Getppid()) {
		if err := project.SetAuthorityRootForTesting(inherited); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		if err := controller.SetRuntimeDirForTesting(os.Getenv("DAGRAIL_TEST_DAEMON_HOME")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		_ = os.Setenv("DAGRAIL_TEST_AUTHORITY_PARENT_PID", strconv.Itoa(os.Getpid()))
		return m.Run()
	}
	root, err := os.MkdirTemp("", "dagrail-e2e-authority-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer os.RemoveAll(root)
	daemonRoot := filepath.Join(root, "controller")
	if err := os.Setenv("DAGRAIL_TEST_AUTHORITY_HOME", root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	_ = os.Setenv("DAGRAIL_TEST_DAEMON_HOME", daemonRoot)
	_ = os.Setenv("DAGRAIL_TEST_AUTHORITY_PARENT_PID", strconv.Itoa(os.Getpid()))
	if err := project.SetAuthorityRootForTesting(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := controller.SetRuntimeDirForTesting(daemonRoot); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	code := m.Run()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if client, err := controller.NewClient(); err == nil {
		_ = client.Stop(ctx)
	}
	return code
}
