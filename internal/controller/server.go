package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CongBao/dagrail/internal/commandcatalog"
	"github.com/CongBao/dagrail/internal/service"
	"github.com/CongBao/dagrail/internal/version"
	"github.com/gofrs/flock"
)

type ExecuteFunc func(context.Context, []string, io.Reader, io.Writer, io.Writer) error
type UIStartFunc func(context.Context, UIRequest) (UIStatus, error)
type UIStatusFunc func() UIStatus
type UIStopFunc func(context.Context) error

type Server struct {
	execute   ExecuteFunc
	endpoint  string
	startedAt string
	requests  atomic.Uint64
	draining  atomic.Bool
	stopOnce  sync.Once
	stop      chan struct{}
	projects  sync.Map
	uiStart   UIStartFunc
	uiStatus  UIStatusFunc
	uiStop    UIStopFunc
	outbox    *service.DaemonOutbox
}

type projectActor struct{ mu sync.RWMutex }

func NewServer(execute ExecuteFunc, endpoint string) *Server {
	return &Server{execute: execute, endpoint: endpoint, startedAt: nowString(), stop: make(chan struct{}), outbox: service.NewDaemonOutbox()}
}

func (server *Server) SetUIManager(start UIStartFunc, status UIStatusFunc, stop UIStopFunc) {
	server.uiStart, server.uiStatus, server.uiStop = start, status, stop
}

func (server *Server) status() Status {
	projects := 0
	server.projects.Range(func(_, _ any) bool { projects++; return true })
	dataNamespace, _ := dataNamespaceIdentity()
	return Status{APIVersion: APIVersion, Kind: "ControllerStatus", Version: version.Version, Commit: version.Commit, PID: os.Getpid(), StartedAt: server.startedAt, Endpoint: server.endpoint, DataNamespace: dataNamespace, Draining: server.draining.Load(), Projects: projects, Requests: server.requests.Load()}
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeResponse(w, http.StatusOK, server.status())
	})
	mux.HandleFunc("/v1/execute", func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if server.draining.Load() {
			writeResponse(w, http.StatusServiceUnavailable, ExecuteResponse{APIVersion: APIVersion, Error: "controller is draining", ErrorCode: "controller_draining", Retryable: true})
			return
		}
		request.Body = http.MaxBytesReader(w, request.Body, MaxRequestBytes)
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var input ExecuteRequest
		if err := decoder.Decode(&input); err != nil {
			writeResponse(w, http.StatusBadRequest, ExecuteResponse{APIVersion: APIVersion, Error: err.Error(), ErrorCode: "invalid_request"})
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			writeResponse(w, http.StatusBadRequest, ExecuteResponse{APIVersion: APIVersion, Error: "request contains trailing content", ErrorCode: "invalid_request"})
			return
		}
		if err := validateExecuteRequest(input); err != nil {
			writeResponse(w, http.StatusBadRequest, ExecuteResponse{APIVersion: APIVersion, Error: err.Error(), ErrorCode: "invalid_request"})
			return
		}
		input.Args = resolveCallerRoot(input.Args, input.CWD)
		operationID := server.requests.Add(1)
		started := time.Now()
		root := commandRoot(input.Args)
		actorValue, _ := server.projects.LoadOrStore(root, &projectActor{})
		actor := actorValue.(*projectActor)
		readOnly := commandcatalog.ProjectReadOnly(input.Args)
		if readOnly {
			actor.mu.RLock()
			defer actor.mu.RUnlock()
		} else {
			actor.mu.Lock()
			defer actor.mu.Unlock()
		}
		var stdout, stderr bytes.Buffer
		executeCtx := request.Context()
		if supportsDaemonOutbox(input.Args) {
			// Admission remains cancellable. Only effect.prepare transfers work to
			// the daemon outbox, and only after the prepared/dispatched journal
			// prefix is durable; the service then gives the adapter an independent
			// context. Ordinary actions and reconcile retain caller cancellation.
			executeCtx = service.WithDaemonOutbox(executeCtx, server.outbox)
		}
		err := server.execute(executeCtx, input.Args, bytes.NewReader(input.Stdin), &stdout, &stderr)
		if stdout.Len()+stderr.Len() > MaxResultBytes {
			writeResponse(w, http.StatusRequestEntityTooLarge, ExecuteResponse{APIVersion: APIVersion, Error: "controller command output exceeds bounded response limit", ErrorCode: "response_too_large"})
			return
		}
		response := ExecuteResponse{APIVersion: APIVersion, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
		status := http.StatusOK
		if err != nil {
			response.Error = err.Error()
			response.ErrorCode, response.Hint = classifyOperationError(err)
			response.Retryable = errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
			status = http.StatusUnprocessableEntity
		}
		logControllerOperation(operationID, input.Args[0], time.Since(started), response.ErrorCode)
		writeResponse(w, status, response)
	})
	mux.HandleFunc("/v1/stop", func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		server.draining.Store(true)
		if server.uiStop != nil {
			stopCtx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
			_ = server.uiStop(stopCtx)
			cancel()
		}
		writeResponse(w, http.StatusOK, map[string]any{"stopping": true, "status": server.status()})
		server.stopOnce.Do(func() { close(server.stop) })
	})
	mux.HandleFunc("/v1/ui/status", func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || server.uiStatus == nil {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeResponse(w, http.StatusOK, server.uiStatus())
	})
	mux.HandleFunc("/v1/ui/start", func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || server.uiStart == nil {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		request.Body = http.MaxBytesReader(w, request.Body, 16*1024)
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var input UIRequest
		if err := decoder.Decode(&input); err != nil {
			writeResponse(w, http.StatusBadRequest, ExecuteResponse{APIVersion: APIVersion, Error: err.Error(), ErrorCode: "invalid_request"})
			return
		}
		status, err := server.uiStart(request.Context(), input)
		if err != nil {
			writeResponse(w, http.StatusUnprocessableEntity, ExecuteResponse{APIVersion: APIVersion, Error: err.Error(), ErrorCode: "ui_start_failed"})
			return
		}
		writeResponse(w, http.StatusOK, status)
	})
	mux.HandleFunc("/v1/ui/stop", func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || server.uiStop == nil {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := server.uiStop(request.Context()); err != nil {
			writeResponse(w, http.StatusUnprocessableEntity, ExecuteResponse{APIVersion: APIVersion, Error: err.Error(), ErrorCode: "ui_stop_failed"})
			return
		}
		writeResponse(w, http.StatusOK, server.uiStatus())
	})
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		mux.ServeHTTP(w, request)
	})
}

func classifyOperationError(err error) (string, string) {
	message := err.Error()
	switch {
	case strings.Contains(message, ".dagrail/project.yaml was not found"):
		return "project_not_found", "Pass an explicit root containing .dagrail/project.yaml, or run 'dagrail init' there."
	case strings.Contains(message, "action reference is stale"):
		return "stale_action", "Refresh DAG context and use a newly issued allowed-action ref."
	case strings.Contains(message, "role lease") || strings.Contains(message, "role is not bound"):
		return "role_lease_required", "Bind or renew the stable Role, then request fresh bounded context."
	case errors.Is(err, context.Canceled):
		return "interrupted", "Retry only with the same idempotency key when an action may already have started."
	default:
		return "operation_failed", "Inspect the bounded error and run 'dagrail doctor' for project diagnostics."
	}
}

func (server *Server) Serve(ctx context.Context) error {
	if server.execute == nil {
		return fmt.Errorf("controller execute handler is required")
	}
	directory, err := ensureRuntimeDir()
	if err != nil {
		return err
	}
	instanceLock := flock.New(filepath.Join(directory, "instance.lock"))
	locked, err := instanceLock.TryLock()
	if err != nil {
		return fmt.Errorf("acquire controller instance lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("another DAGrail controller already owns this runtime")
	}
	defer func() { _ = instanceLock.Unlock() }()
	listener, err := listenEndpoint(server.endpoint)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		removeEndpoint(server.endpoint)
	}()
	httpServer := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second}
	shutdown := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-server.stop:
		}
		server.draining.Store(true)
		if server.uiStop != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = server.uiStop(stopCtx)
			cancel()
		}
		// A drain has no internal timeout: a previously authorized external
		// Effect must reach a durable observation before this process exits.
		// Upgrade callers use their own bounded wait and receive a blocker while
		// an Effect is still in flight; they never force the daemon to retry it.
		_ = httpServer.Shutdown(context.Background())
		server.outbox.Drain()
		close(shutdown)
	}()
	err = httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdown
		return nil
	}
	return err
}

func logControllerOperation(id uint64, command string, elapsed time.Duration, code string) {
	status := "ok"
	if code != "" {
		status = code
	}
	entry, err := json.Marshal(map[string]any{
		"operationId":    fmt.Sprintf("op-%d", id),
		"command":        command,
		"durationMillis": elapsed.Milliseconds(),
		"status":         status,
	})
	if err == nil {
		_, _ = fmt.Fprintln(os.Stderr, string(entry))
	}
}

func commandRoot(args []string) string {
	root := "."
	for index := 0; index < len(args); index++ {
		if args[index] == "--root" && index+1 < len(args) {
			root = args[index+1]
			break
		}
		if strings.HasPrefix(args[index], "--root=") {
			root = strings.TrimPrefix(args[index], "--root=")
			break
		}
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return root
	}
	return filepath.Clean(absolute)
}

func resolveCallerRoot(args []string, cwd string) []string {
	resolved := append([]string(nil), args...)
	for index := 0; index < len(resolved); index++ {
		if resolved[index] == "--root" && index+1 < len(resolved) {
			if !filepath.IsAbs(resolved[index+1]) {
				resolved[index+1] = filepath.Join(cwd, resolved[index+1])
			}
			resolved[index+1] = filepath.Clean(resolved[index+1])
			return resolved
		}
		if strings.HasPrefix(resolved[index], "--root=") {
			root := strings.TrimPrefix(resolved[index], "--root=")
			if !filepath.IsAbs(root) {
				root = filepath.Join(cwd, root)
			}
			resolved[index] = "--root=" + filepath.Clean(root)
			return resolved
		}
	}
	if commandcatalog.UsesProjectRoot(resolved) {
		resolved = append(resolved, "--root", filepath.Clean(cwd))
	}
	return resolved
}

func supportsDaemonOutbox(args []string) bool {
	return len(args) > 1 && args[0] == "action" && args[1] == "apply"
}

func writeResponse(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
