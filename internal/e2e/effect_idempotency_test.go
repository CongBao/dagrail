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
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect race"},"spec":{"roles":[{"id":"operator","capabilities":["node.run"]}],"nodes":[{"id":"merge","kind":"effect","role":"operator","title":"merge","inputs":{"adapter":"test.racing","request":{"target":"main"}},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
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
