package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveCallerRootPreservesClientWorkingDirectory(t *testing.T) {
	cwd := filepath.Join(string(filepath.Separator), "workspace", "client")
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"status"}, cwd},
		{[]string{"status", "--root", "nested"}, filepath.Join(cwd, "nested")},
		{[]string{"graph", "validate", "--file", "graph.json"}, ""},
	} {
		resolved := resolveCallerRoot(test.args, cwd)
		root := ""
		for index := range resolved {
			if resolved[index] == "--root" && index+1 < len(resolved) {
				root = resolved[index+1]
			}
		}
		if root != test.want {
			t.Fatalf("resolve %v root = %q, want %q", test.args, root, test.want)
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
