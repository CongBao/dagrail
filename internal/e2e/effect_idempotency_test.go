package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/service"
	"github.com/CongBao/dagrail/sdk"
)

type racingEffect struct {
	prepared atomic.Int32
	dispatch atomic.Int32
	ready    chan struct{}
	once     sync.Once
}

type reconcileRacingEffect struct {
	reconciles atomic.Int32
}

type subprocessReconcileEffect struct {
	counterPath string
	crash       bool
}

func (r *subprocessReconcileEffect) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "test.subprocess-reconcile", Version: "1.0.0", SchemaHash: "test-v1"}
}
func (r *subprocessReconcileEffect) Prepare(context.Context, sdk.EffectRequest) (sdk.PreparedEffect, error) {
	return sdk.PreparedEffect{AdapterID: "test.subprocess-reconcile", Binding: json.RawMessage(`{}`)}, nil
}
func (r *subprocessReconcileEffect) Dispatch(context.Context, sdk.EffectRequest, sdk.PreparedEffect) (sdk.EffectReceipt, error) {
	return sdk.EffectReceipt{Status: "unknown", ExternalID: "pending"}, nil
}
func (r *subprocessReconcileEffect) Reconcile(context.Context, sdk.EffectRequest, sdk.PreparedEffect, json.RawMessage) (sdk.EffectReceipt, error) {
	file, err := os.OpenFile(r.counterPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return sdk.EffectReceipt{}, err
	}
	if _, err := file.WriteString("call\n"); err != nil {
		_ = file.Close()
		return sdk.EffectReceipt{}, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return sdk.EffectReceipt{}, err
	}
	if err := file.Close(); err != nil {
		return sdk.EffectReceipt{}, err
	}
	if r.crash {
		os.Exit(23)
	}
	time.Sleep(150 * time.Millisecond)
	return sdk.EffectReceipt{Status: "confirmed", ExternalID: "subprocess-confirmed"}, nil
}

func (r *reconcileRacingEffect) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "test.reconcile-racing", Version: "1.0.0", SchemaHash: "test-v1"}
}
func (r *reconcileRacingEffect) Prepare(context.Context, sdk.EffectRequest) (sdk.PreparedEffect, error) {
	return sdk.PreparedEffect{AdapterID: "test.reconcile-racing", Binding: json.RawMessage(`{}`)}, nil
}
func (r *reconcileRacingEffect) Dispatch(context.Context, sdk.EffectRequest, sdk.PreparedEffect) (sdk.EffectReceipt, error) {
	return sdk.EffectReceipt{Status: "unknown", ExternalID: "pending"}, nil
}
func (r *reconcileRacingEffect) Reconcile(context.Context, sdk.EffectRequest, sdk.PreparedEffect, json.RawMessage) (sdk.EffectReceipt, error) {
	r.reconciles.Add(1)
	time.Sleep(100 * time.Millisecond)
	return sdk.EffectReceipt{Status: "confirmed", ExternalID: "confirmed-once"}, nil
}

func (r *racingEffect) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "test.racing", Version: "1.0.0", SchemaHash: "test-v1"}
}

func (r *racingEffect) Prepare(context.Context, sdk.EffectRequest) (sdk.PreparedEffect, error) {
	if r.prepared.Add(1) == 2 {
		r.once.Do(func() { close(r.ready) })
	}
	<-r.ready
	return sdk.PreparedEffect{AdapterID: "test.racing", Binding: json.RawMessage(`{}`)}, nil
}

func (r *racingEffect) Dispatch(context.Context, sdk.EffectRequest, sdk.PreparedEffect) (sdk.EffectReceipt, error) {
	r.dispatch.Add(1)
	return sdk.EffectReceipt{Status: "confirmed", ExternalID: "only-once"}, nil
}

func (r *racingEffect) Reconcile(context.Context, sdk.EffectRequest, sdk.PreparedEffect, json.RawMessage) (sdk.EffectReceipt, error) {
	return sdk.EffectReceipt{Status: "confirmed", ExternalID: "only-once"}, nil
}

func TestConcurrentEffectApplyDispatchesOnlyOnce(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "effect race")
	if err != nil {
		t.Fatal(err)
	}
	provider := &racingEffect{ready: make(chan struct{})}
	if err := svc.Providers.RegisterEffect(provider); err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect race"},"spec":{"roles":[{"id":"operator","capabilities":["effect.apply","effect.reconcile"]}],"nodes":[{"id":"merge","kind":"effect","role":"operator","title":"merge","inputs":{"adapter":"test.racing","request":{"target":"main"}},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "import", "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("operator", "codex", "session-1", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	startRef := actionRef(t, svc, "operator", "merge", "node.start")
	if _, err := svc.ApplyAction(startRef, json.RawMessage(`{}`), "start"); err != nil {
		t.Fatal(err)
	}
	effectRef := actionRef(t, svc, "operator", "merge", "effect.prepare")
	results := make(chan service.ActionResult, 2)
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			result, applyErr := svc.ApplyAction(effectRef, json.RawMessage(`{}`), "same-effect")
			results <- result
			errors <- applyErr
		}()
	}
	first, second := <-results, <-results
	for range 2 {
		if applyErr := <-errors; applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	if first.ActionID == "" || first.ActionID != second.ActionID {
		t.Fatalf("duplicate calls returned different actions: %#v %#v", first, second)
	}
	if got := provider.dispatch.Load(); got != 1 {
		t.Fatalf("effect dispatched %d times, want 1", got)
	}
}

func TestConcurrentEffectReconcileCallsAdapterOnlyOnce(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "effect reconcile race")
	if err != nil {
		t.Fatal(err)
	}
	provider := &reconcileRacingEffect{}
	if err := svc.Providers.RegisterEffect(provider); err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph-reconcile.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect reconcile race"},"spec":{"roles":[{"id":"operator","capabilities":["effect.apply","effect.reconcile"]}],"nodes":[{"id":"effect","kind":"effect","role":"operator","title":"effect","inputs":{"adapter":"test.reconcile-racing","request":{"target":"main"}},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "import-reconcile", "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("operator", "codex", "session-reconcile", time.Hour, false, "bind-reconcile"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(actionRef(t, svc, "operator", "effect", "node.start"), json.RawMessage(`{}`), "start-reconcile"); err != nil {
		t.Fatal(err)
	}
	prepared, err := svc.ApplyAction(actionRef(t, svc, "operator", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare-reconcile")
	if err != nil || prepared.Status != "unknown" {
		t.Fatalf("effect did not enter unknown state: %+v %v", prepared, err)
	}
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			_, reconcileErr := svc.ReconcileEffectContext(context.Background(), prepared.ActionID, json.RawMessage(`{"observed":true}`), "same-reconcile")
			errors <- reconcileErr
		}()
	}
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if got := provider.reconciles.Load(); got != 1 {
		t.Fatalf("concurrent reconcile called adapter %d times, want 1", got)
	}
}

func TestEffectReconcileHelperProcess(t *testing.T) {
	if os.Getenv("DAGRAIL_RECONCILE_HELPER") != "1" {
		t.Skip("helper subprocess only")
	}
	svc, err := service.Open(os.Getenv("DAGRAIL_RECONCILE_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	provider := &subprocessReconcileEffect{counterPath: os.Getenv("DAGRAIL_RECONCILE_COUNTER"), crash: os.Getenv("DAGRAIL_RECONCILE_CRASH") == "1"}
	if err := svc.Providers.RegisterEffect(provider); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReconcileEffectContext(context.Background(), os.Getenv("DAGRAIL_RECONCILE_ACTION"), json.RawMessage(`{"observed":true}`), os.Getenv("DAGRAIL_RECONCILE_KEY")); err != nil {
		t.Fatal(err)
	}
}

func TestCrossProcessEffectReconcileCallsAdapterOnlyOnce(t *testing.T) {
	svc, root, actionID, counter := prepareSubprocessEffect(t)
	key := "cross-process-reconcile"
	first, firstOutput := reconcileHelperCommand(t, root, actionID, counter, key, false)
	second, secondOutput := reconcileHelperCommand(t, root, actionID, counter, key, false)
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err != nil {
		t.Fatalf("first reconcile helper: %v\n%s", err, firstOutput.String())
	}
	if err := second.Wait(); err != nil {
		t.Fatalf("second reconcile helper: %v\n%s", err, secondOutput.String())
	}
	assertReconcileEvidence(t, svc, counter, key, 1)
}

func TestEffectReconcileRecoversAfterLockHolderCrash(t *testing.T) {
	svc, root, actionID, counter := prepareSubprocessEffect(t)
	key := "crash-reconcile"
	crashed, crashOutput := reconcileHelperCommand(t, root, actionID, counter, key, true)
	err := crashed.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("crash helper exit=%v, want 23\n%s", err, crashOutput.String())
	}
	recovered, recoveryOutput := reconcileHelperCommand(t, root, actionID, counter, key, false)
	if err := recovered.Run(); err != nil {
		t.Fatalf("recovery helper: %v\n%s", err, recoveryOutput.String())
	}
	assertReconcileEvidence(t, svc, counter, key, 2)
}

func prepareSubprocessEffect(t *testing.T) (*service.Service, string, string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "subprocess reconcile")
	if err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(root, "reconcile-calls.txt")
	if err := svc.Providers.RegisterEffect(&subprocessReconcileEffect{counterPath: counter}); err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph-subprocess.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"subprocess reconcile"},"spec":{"roles":[{"id":"operator","capabilities":["effect.apply","effect.reconcile"]}],"nodes":[{"id":"effect","kind":"effect","role":"operator","title":"effect","inputs":{"adapter":"test.subprocess-reconcile","request":{"target":"main"}},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "import-subprocess", "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("operator", "codex", "session-subprocess", time.Hour, false, "bind-subprocess"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(actionRef(t, svc, "operator", "effect", "node.start"), json.RawMessage(`{}`), "start-subprocess"); err != nil {
		t.Fatal(err)
	}
	prepared, err := svc.ApplyAction(actionRef(t, svc, "operator", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare-subprocess")
	if err != nil || prepared.Status != "unknown" {
		t.Fatalf("effect did not enter unknown state: %+v %v", prepared, err)
	}
	return svc, root, prepared.ActionID, counter
}

func reconcileHelperCommand(t *testing.T, root, actionID, counter, key string, crash bool) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestEffectReconcileHelperProcess$", "-test.count=1")
	command.Env = append(os.Environ(),
		"DAGRAIL_RECONCILE_HELPER=1",
		"DAGRAIL_RECONCILE_ROOT="+root,
		"DAGRAIL_RECONCILE_ACTION="+actionID,
		"DAGRAIL_RECONCILE_COUNTER="+counter,
		"DAGRAIL_RECONCILE_KEY="+key,
	)
	if crash {
		command.Env = append(command.Env, "DAGRAIL_RECONCILE_CRASH=1")
	}
	output := &bytes.Buffer{}
	command.Stdout, command.Stderr = output, output
	return command, output
}

func assertReconcileEvidence(t *testing.T, svc *service.Service, counter, key string, wantCalls int) {
	t.Helper()
	raw, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if calls := strings.Count(string(raw), "call\n"); calls != wantCalls {
		t.Fatalf("adapter calls=%d, want %d: %q", calls, wantCalls, raw)
	}
	segments, err := svc.VerifyJournal()
	if err != nil {
		t.Fatal(err)
	}
	observations := 0
	for _, segment := range segments {
		if segment.Command.Kind == "effect.observe" && segment.Command.IdempotencyKey == key {
			observations++
		}
	}
	if observations != 1 {
		t.Fatalf("final observations=%d, want 1", observations)
	}
}

func actionRef(t *testing.T, svc *service.Service, roleID, nodeID, kind string) string {
	t.Helper()
	actions, err := svc.ListActions(roleID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range actions.Actions {
		if action.Kind == kind {
			return action.Ref
		}
	}
	t.Fatalf("action %s was not allowed", kind)
	return ""
}
