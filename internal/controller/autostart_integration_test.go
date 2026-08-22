package controller_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTwentyConcurrentCLIStartsConvergeOnOneDaemon(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "dagrail")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	// This integration test executes real project mutations. The explicit test
	// tag is mandatory: production binaries intentionally ignore environment-
	// selected authority and controller roots.
	build := exec.Command("go", "build", "-tags=dagrail_testauthority", "-o", binary, "../../cmd/dagrail")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build integration runtime: %v: %s", err, output)
	}
	environment := withEnvironment(os.Environ(), "DAGRAIL_HOME", filepath.Join(root, "runtime"))
	environment = withEnvironment(environment, "DAGRAIL_TEST_AUTHORITY_HOME", filepath.Join(root, "authority"))
	environment = withEnvironment(environment, "DAGRAIL_TEST_CONTROLLER_DIR", filepath.Join(root, "controller"))
	cacheRoot := filepath.Join(root, "cache")
	switch runtime.GOOS {
	case "windows":
		environment = withEnvironment(environment, "LOCALAPPDATA", cacheRoot)
	case "darwin":
		environment = withEnvironment(environment, "HOME", filepath.Join(root, "home"))
	default:
		environment = withEnvironment(environment, "XDG_CACHE_HOME", cacheRoot)
	}
	t.Cleanup(func() {
		command := exec.Command(binary, "daemon", "stop")
		command.Env = environment
		_, _ = command.CombinedOutput()
	})
	type result struct {
		PID           int    `json:"pid"`
		DataNamespace string `json:"dataNamespace"`
	}
	results := make(chan result, 20)
	errors := make(chan []byte, 20)
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			command := exec.Command(binary, "daemon", "start")
			command.Env = environment
			output, err := command.CombinedOutput()
			if err != nil {
				errors <- append([]byte(err.Error()+": "), output...)
				return
			}
			var status result
			if err := json.Unmarshal(output, &status); err != nil || status.PID <= 0 {
				errors <- output
				return
			}
			results <- status
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	for failure := range errors {
		t.Fatalf("concurrent daemon start failed: %s", failure)
	}
	pids := map[int]bool{}
	var first result
	for status := range results {
		if status.DataNamespace == "" {
			t.Fatal("controller omitted its authority-data namespace digest")
		}
		if first.PID == 0 {
			first = status
		} else if status.DataNamespace != first.DataNamespace {
			t.Fatalf("concurrent clients observed different namespaces: %q != %q", status.DataNamespace, first.DataNamespace)
		}
		pids[status.PID] = true
	}
	if len(pids) != 1 {
		t.Fatalf("concurrent automatic starts produced %d daemons: %v", len(pids), pids)
	}
	// A singleton daemon may manage many Projects, but it must not reinterpret
	// one locator through a different DAGRAIL_HOME. The client therefore drains
	// and replaces the process when its authority-data namespace changes.
	environmentB := withEnvironment(environment, "DAGRAIL_HOME", filepath.Join(root, "runtime-b"))
	restart := exec.Command(binary, "daemon", "start")
	restart.Env = environmentB
	restartOutput, err := restart.CombinedOutput()
	if err != nil {
		t.Fatalf("restart daemon for data namespace: %v: %s", err, restartOutput)
	}
	var restarted result
	if err := json.Unmarshal(restartOutput, &restarted); err != nil || restarted.PID <= 0 || restarted.DataNamespace == "" {
		t.Fatalf("decode namespace restart: %v %s", err, restartOutput)
	}
	if restarted.PID == first.PID {
		t.Fatalf("data namespace change reused daemon pid %d", restarted.PID)
	}
	environment = environmentB
	projectRoot := filepath.Join(root, "project")
	graphPath, err := filepath.Abs(filepath.Join("..", "..", "examples", "development-dag.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	runMutation := func(label string, args ...string) []byte {
		command := exec.Command(binary, args...)
		command.Env = environment
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", label, err, output)
		}
		if len(strings.TrimSpace(string(output))) == 0 || !json.Valid(output) {
			t.Fatalf("%s returned no JSON receipt: %q", label, output)
		}
		return output
	}
	runMutation("initialize project", "init", "--root", projectRoot, "--name", "receipt-test")
	runMutation("import graph", "graph", "import", "--root", projectRoot, "--file", graphPath, "--idempotency-key", "graph-import")
	runMutation("bind role", "role", "bind", "--root", projectRoot, "--role", "developer", "--harness", "codex", "--session", "receipt-session", "--ttl", "15m", "--idempotency-key", "role-bind")
	runMutation("start node", "action", "apply", "--root", projectRoot, "--kind", "node.start", "--role", "developer", "--node", "implement", "--input", "{}", "--idempotency-key", "node-start")
	inspect := exec.Command(binary, "inspect", "node:implement", "-root", projectRoot)
	inspect.Env = environment
	inspectOutput, err := inspect.CombinedOutput()
	if err != nil || len(strings.TrimSpace(string(inspectOutput))) == 0 || !json.Valid(inspectOutput) || !strings.Contains(string(inspectOutput), `"id":"implement"`) {
		t.Fatalf("positional-first inspect through daemon failed: err=%v output=%s", err, inspectOutput)
	}
	duplicateRootInspect := exec.Command(binary, "inspect", "project", "--root", filepath.Join(root, "wrong-project"), "-root", projectRoot)
	duplicateRootInspect.Env = environment
	duplicateRootOutput, err := duplicateRootInspect.CombinedOutput()
	if err != nil || !json.Valid(duplicateRootOutput) || !strings.Contains(string(duplicateRootOutput), `"name":"receipt-test"`) {
		t.Fatalf("daemon did not route duplicate roots through the final project actor: err=%v output=%s", err, duplicateRootOutput)
	}

	stop := exec.Command(binary, "daemon", "stop")
	stop.Env = environment
	if output, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("stop daemon: %v: %s", err, output)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := exec.Command(binary, "daemon", "status")
		status.Env = environment
		output, err := status.CombinedOutput()
		if err == nil {
			var value map[string]any
			if json.Unmarshal(output, &value) == nil && value["running"] == false {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("daemon did not finish its drain after stop")
}

func TestOlderCLIRestartCannotDowngradeNewerDaemon(t *testing.T) {
	root := t.TempDir()
	buildVersion := func(name, release string) string {
		binary := filepath.Join(root, name)
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		build := exec.Command("go", "build", "-ldflags", "-X github.com/CongBao/dagrail/internal/version.Version="+release, "-o", binary, "../../cmd/dagrail")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s integration runtime: %v: %s", release, err, output)
		}
		return binary
	}
	older := buildVersion("dagrail-older", "0.26.3")
	newer := buildVersion("dagrail-newer", "0.26.4")
	environment := withEnvironment(os.Environ(), "DAGRAIL_HOME", filepath.Join(root, "runtime"))
	switch runtime.GOOS {
	case "windows":
		environment = withEnvironment(environment, "LOCALAPPDATA", filepath.Join(root, "cache"))
	case "darwin":
		environment = withEnvironment(environment, "HOME", filepath.Join(root, "home"))
	default:
		environment = withEnvironment(environment, "XDG_CACHE_HOME", filepath.Join(root, "cache"))
	}
	t.Cleanup(func() {
		command := exec.Command(newer, "daemon", "stop")
		command.Env = environment
		_, _ = command.CombinedOutput()
	})

	start := exec.Command(newer, "daemon", "start")
	start.Env = environment
	startOutput, err := start.CombinedOutput()
	if err != nil {
		t.Fatalf("start newer daemon: %v: %s", err, startOutput)
	}
	var before struct {
		Version string `json:"version"`
		PID     int    `json:"pid"`
	}
	if err := json.Unmarshal(startOutput, &before); err != nil || before.Version != "0.26.4" || before.PID <= 0 {
		t.Fatalf("decode newer daemon status: err=%v output=%s", err, startOutput)
	}

	restart := exec.Command(older, "daemon", "restart")
	restart.Env = environment
	restartOutput, restartErr := restart.CombinedOutput()
	if restartErr == nil || !strings.Contains(string(restartOutput), "refuses replacement by older version") {
		t.Fatalf("older restart was not rejected: err=%v output=%s", restartErr, restartOutput)
	}

	status := exec.Command(newer, "daemon", "status")
	status.Env = environment
	statusOutput, err := status.CombinedOutput()
	if err != nil {
		t.Fatalf("query newer daemon after rejected restart: %v: %s", err, statusOutput)
	}
	var after struct {
		Version string `json:"version"`
		PID     int    `json:"pid"`
	}
	if err := json.Unmarshal(statusOutput, &after); err != nil || after.Version != before.Version || after.PID != before.PID {
		t.Fatalf("rejected restart changed the newer daemon: before=%+v after=%+v err=%v output=%s", before, after, err, statusOutput)
	}
}

func withEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, prefix+value)
}
