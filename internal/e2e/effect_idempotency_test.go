package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
