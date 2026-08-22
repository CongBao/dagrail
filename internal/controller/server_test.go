package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/version"
)

func TestPublishStartupReadinessIsOwnerRuntimeBoundAndExclusive(t *testing.T) {
	originalRuntime, err := RuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	runtimeDir := t.TempDir()
	if err := SetRuntimeDirForTesting(runtimeDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetRuntimeDirForTesting(originalRuntime) })

	readyPath := filepath.Join(runtimeDir, ".controller-ready-test")
	t.Setenv("DAGRAIL_DAEMON_READY_FILE", readyPath)
	status := Status{APIVersion: APIVersion, Kind: "ControllerStatus", Version: "test", PID: 42, DataNamespace: "sha256:test"}
	if err := publishStartupReadiness(status); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatal(err)
	}
	var observed Status
	if err := json.Unmarshal(data, &observed); err != nil || observed.PID != status.PID || observed.DataNamespace != status.DataNamespace {
		t.Fatalf("unexpected readiness receipt: value=%+v err=%v", observed, err)
	}
	if err := publishStartupReadiness(status); err == nil {
		t.Fatal("readiness publication unexpectedly replaced an existing receipt")
	}

	t.Setenv("DAGRAIL_DAEMON_READY_FILE", filepath.Join(t.TempDir(), ".controller-ready-outside"))
	if err := publishStartupReadiness(status); err == nil {
		t.Fatal("readiness publication escaped the owner-private runtime directory")
	}
}

func TestControllerChildEnvironmentReplacesCallerStartupValues(t *testing.T) {
	environment := controllerChildEnvironment([]string{
		"PATH=/bin",
		"DAGRAIL_DAEMON_CHILD=stale",
		"DAGRAIL_DAEMON_READY_FILE=/tmp/stale",
	}, "/private/runtime/.controller-ready-current")
	joined := "\n" + strings.Join(environment, "\n") + "\n"
	for _, expected := range []string{
		"\nPATH=/bin\n",
		"\nDAGRAIL_DAEMON_CHILD=1\n",
		"\nDAGRAIL_DAEMON_READY_FILE=/private/runtime/.controller-ready-current\n",
	} {
		if strings.Count(joined, expected) != 1 {
			t.Fatalf("expected exactly one %q in %q", expected, joined)
		}
	}
}

func TestServerRejectsSuccessfulMutationWithoutReceipt(t *testing.T) {
	server := NewServer(func(context.Context, []string, io.Reader, io.Writer, io.Writer) error { return nil }, "test")
	requestBody, err := json.Marshal(ExecuteRequest{
		APIVersion: APIVersion,
		Args:       []string{"role", "bind", "--root", t.TempDir()},
		CWD:        t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(requestBody)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("missing mutation receipt returned HTTP %d: %s", response.Code, response.Body.String())
	}
	var failure ExecuteResponse
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || failure.ErrorCode != "ambiguous_result" || !failure.Retryable {
		t.Fatalf("unexpected missing-receipt response: value=%+v err=%v", failure, err)
	}
}

func TestControllerStopRejectsLegacyAndDowngradeRequests(t *testing.T) {
	server := NewServer(func(context.Context, []string, io.Reader, io.Writer, io.Writer) error { return nil }, "test")
	handler := server.Handler()

	legacy := httptest.NewRecorder()
	handler.ServeHTTP(legacy, httptest.NewRequest(http.MethodPost, "/v1/stop", strings.NewReader(`{}`)))
	if legacy.Code != http.StatusBadRequest || server.draining.Load() {
		t.Fatalf("legacy empty stop request was accepted: status=%d draining=%v body=%s", legacy.Code, server.draining.Load(), legacy.Body.String())
	}

	downgrade := httptest.NewRecorder()
	handler.ServeHTTP(downgrade, httptest.NewRequest(http.MethodPost, "/v1/stop", strings.NewReader(`{"reason":"replace","targetVersion":"0.1.0","targetDataNamespace":"replacement"}`)))
	if downgrade.Code != http.StatusConflict || server.draining.Load() {
		t.Fatalf("daemon downgrade was accepted: status=%d draining=%v body=%s", downgrade.Code, server.draining.Load(), downgrade.Body.String())
	}
	var failure ExecuteResponse
	if err := json.Unmarshal(downgrade.Body.Bytes(), &failure); err != nil || failure.ErrorCode != "controller_downgrade_rejected" {
		t.Fatalf("downgrade diagnostic was not structured: err=%v response=%+v", err, failure)
	}
}

func TestControllerStopAcceptsExplicitShutdownAndForwardReplacement(t *testing.T) {
	for name, body := range map[string]string{
		"shutdown":             `{"reason":"shutdown"}`,
		"forward replacement":  `{"reason":"replace","targetVersion":"99.0.0","targetDataNamespace":"replacement"}`,
		"same version restart": `{"reason":"restart","targetVersion":"` + version.Version + `","targetDataNamespace":"test"}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := NewServer(func(context.Context, []string, io.Reader, io.Writer, io.Writer) error { return nil }, "test")
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/stop", strings.NewReader(body)))
			if response.Code != http.StatusOK || !server.draining.Load() {
				t.Fatalf("authorized stop failed: status=%d draining=%v body=%s", response.Code, server.draining.Load(), response.Body.String())
			}
		})
	}
}

func TestControllerRestartRejectsOlderClient(t *testing.T) {
	server := NewServer(func(context.Context, []string, io.Reader, io.Writer, io.Writer) error { return nil }, "test")
	original := version.Version
	version.Version = "0.26.4"
	t.Cleanup(func() { version.Version = original })

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/stop", strings.NewReader(`{"reason":"restart","targetVersion":"0.26.3","targetDataNamespace":"test"}`)))
	if response.Code != http.StatusConflict || server.draining.Load() {
		t.Fatalf("older restart stopped a newer daemon: status=%d draining=%v body=%s", response.Code, server.draining.Load(), response.Body.String())
	}
	var failure ExecuteResponse
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || failure.ErrorCode != "controller_downgrade_rejected" {
		t.Fatalf("restart downgrade diagnostic was not structured: err=%v response=%+v", err, failure)
	}
}

func TestCompareSemanticVersions(t *testing.T) {
	for _, test := range []struct {
		left, right string
		want        int
		valid       bool
	}{
		{"0.26.2", "0.26.3", -1, true},
		{"1.0.0", "1.0.0", 0, true},
		{"2.0.0", "1.99.99", 1, true},
		{"1.0.0-alpha.2", "1.0.0-alpha.10", -1, true},
		{"1.0.0-rc.1", "1.0.0", -1, true},
		{"not-a-version", "1.0.0", 0, false},
	} {
		got, valid := compareSemanticVersions(test.left, test.right)
		if got != test.want || valid != test.valid {
			t.Fatalf("compare(%q, %q) = (%d, %v), want (%d, %v)", test.left, test.right, got, valid, test.want, test.valid)
		}
	}
}

func TestOlderClientCannotReplaceNewerController(t *testing.T) {
	original := version.Version
	version.Version = "0.26.3"
	t.Cleanup(func() { version.Version = original })
	err := rejectControllerDowngrade("0.27.0")
	var remote *RPCError
	if !errors.As(err, &remote) || remote.Code != "client_outdated" {
		t.Fatalf("newer controller did not produce client_outdated: %T %v", err, err)
	}
	if err := rejectControllerDowngrade("0.26.2"); err != nil {
		t.Fatalf("forward controller upgrade was rejected: %v", err)
	}
}

func TestResolveCallerRootPreservesClientWorkingDirectory(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "workspace", "client")
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"status"}, cwd},
		{[]string{"status", "--root", "nested"}, filepath.Join(cwd, "nested")},
		{[]string{"inspect", "node:task", "-root", "nested"}, filepath.Join(cwd, "nested")},
		{[]string{"inspect", "node:task", "-root=nested"}, filepath.Join(cwd, "nested")},
		{[]string{"inspect", "node:task", "--root", "first", "-root", "second"}, filepath.Join(cwd, "second")},
		{[]string{"inspect", "node:task", "-root=first", "--root=second"}, filepath.Join(cwd, "second")},
		{[]string{"graph", "validate", "--file", "graph.json"}, ""},
	} {
		resolved := resolveCallerRoot(test.args, cwd)
		root := ""
		for index := range resolved {
			if (resolved[index] == "--root" || resolved[index] == "-root") && index+1 < len(resolved) {
				root = resolved[index+1]
			}
			if strings.HasPrefix(resolved[index], "-root=") {
				root = strings.TrimPrefix(resolved[index], "-root=")
			}
			if strings.HasPrefix(resolved[index], "--root=") {
				root = strings.TrimPrefix(resolved[index], "--root=")
			}
		}
		if root != test.want {
			t.Fatalf("resolve %v root = %q, want %q", test.args, root, test.want)
		}
		if test.want != "" && commandRoot(resolved) != test.want {
			t.Fatalf("command root for %v = %q, want %q", resolved, commandRoot(resolved), test.want)
		}
	}
}

func TestProjectActorSharesReadsAndSerializesWriter(t *testing.T) {
	release := make(chan struct{})
	readEntered := make(chan struct{}, 2)
	writeEntered := make(chan struct{}, 1)
	server := NewServer(func(_ context.Context, args []string, _ io.Reader, output, _ io.Writer) error {
		if args[0] == "status" {
			readEntered <- struct{}{}
			<-release
		} else {
			writeEntered <- struct{}{}
		}
		_, _ = output.Write([]byte(`{"ok":true}`))
		return nil
	}, "test")
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	cwd := t.TempDir()
	post := func(args []string) {
		payload, _ := json.Marshal(ExecuteRequest{APIVersion: APIVersion, Args: args, CWD: cwd})
		response, err := http.Post(httpServer.URL+"/v1/execute", "application/json", bytes.NewReader(payload))
		if err == nil {
			_ = response.Body.Close()
		}
	}
	go post([]string{"status", "--root", "/tmp/project"})
	go post([]string{"status", "--root", "/tmp/project"})
	for range 2 {
		select {
		case <-readEntered:
		case <-time.After(time.Second):
			t.Fatal("project reads were serialized")
		}
	}
	go post([]string{"action", "apply", "--root", "/tmp/project"})
	select {
	case <-writeEntered:
		t.Fatal("writer crossed active project readers")
	case <-time.After(75 * time.Millisecond):
	}
	close(release)
	select {
	case <-writeEntered:
	case <-time.After(time.Second):
		t.Fatal("writer did not resume after readers")
	}
}

func TestActionAdmissionPropagatesCancellationBeforeOutboxTransfer(t *testing.T) {
	entered, completed := make(chan struct{}), make(chan struct{})
	var observedCancellation atomic.Bool
	server := NewServer(func(ctx context.Context, _ []string, _ io.Reader, _ io.Writer, _ io.Writer) error {
		close(entered)
		<-ctx.Done()
		observedCancellation.Store(true)
		close(completed)
		return ctx.Err()
	}, "test")
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	payload, _ := json.Marshal(ExecuteRequest{APIVersion: APIVersion, Args: []string{"action", "apply", "--root", "/tmp/project"}, CWD: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/v1/execute", bytes.NewReader(payload))
	done := make(chan struct{})
	go func() { _, _ = http.DefaultClient.Do(request); close(done) }()
	<-entered
	cancel()
	<-done
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("action admission did not observe client cancellation")
	}
	if !observedCancellation.Load() {
		t.Fatal("client cancellation was discarded before durable outbox transfer")
	}
}
