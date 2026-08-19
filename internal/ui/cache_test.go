package ui

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/service"
)

func cacheTestService(t *testing.T) *service.Service {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "ui-cache")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"cache"},"spec":{"roles":[{"id":"controller","capabilities":[]}],"nodes":[{"id":"work","kind":"task","title":"Work","role":"controller","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "cache/graph", "controller"); err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestExplorerCacheCollapsesConcurrentReplayAndAdvancesOnlyWhenForced(t *testing.T) {
	svc := cacheTestService(t)
	cache := newExplorerCache(svc)
	original := cache.load
	var loads atomic.Int32
	cache.load = func(ctx context.Context) (service.InspectionSnapshot, error) {
		loads.Add(1)
		time.Sleep(25 * time.Millisecond)
		return original(ctx)
	}
	const callers = 16
	var wait sync.WaitGroup
	wait.Add(callers)
	errors := make(chan error, callers)
	for range callers {
		go func() {
			defer wait.Done()
			state, _, err := cache.state(context.Background(), false)
			if err == nil && state.GraphRevision == "" {
				err = context.Canceled
			}
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("concurrent views replayed journal %d times", got)
	}

	if _, err := svc.BindRole("controller", "test", "session", time.Minute, false, "cache/bind"); err != nil {
		t.Fatal(err)
	}
	stale, _, err := cache.state(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 1 {
		t.Fatalf("ordinary view unexpectedly advanced snapshot: head=%d loads=%d", stale.HeadSequence, loads.Load())
	}
	head, snapshot, err := cache.head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || head.Sequence <= snapshot.Head.Sequence {
		t.Fatalf("head observation did not report stale snapshot: journal=%d snapshot=%v", head.Sequence, snapshot)
	}
	state, _, err := cache.state(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if state.HeadSequence != head.Sequence {
		t.Fatalf("forced replay did not advance to observed head: got=%d want=%d", state.HeadSequence, head.Sequence)
	}
	if got := loads.Load(); got != 2 {
		t.Fatalf("forced replay count = %d", got)
	}
}

func TestExplorerCacheWaiterCanCancel(t *testing.T) {
	svc := cacheTestService(t)
	cache := newExplorerCache(svc)
	original := cache.load
	started := make(chan struct{})
	release := make(chan struct{})
	cache.load = func(ctx context.Context) (service.InspectionSnapshot, error) {
		close(started)
		<-release
		return original(ctx)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := cache.state(context.Background(), false)
		done <- err
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	startedAt := time.Now()
	if _, _, err := cache.state(ctx, false); err != context.Canceled {
		t.Fatalf("cancelled waiter returned %v", err)
	}
	if time.Since(startedAt) > 100*time.Millisecond {
		t.Fatal("cancelled waiter did not return promptly")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestExplorerCacheLeaderCanCancelWhileReplayWarmsCache(t *testing.T) {
	svc := cacheTestService(t)
	cache := newExplorerCache(svc)
	original := cache.load
	started := make(chan struct{})
	release := make(chan struct{})
	var loads atomic.Int32
	cache.load = func(ctx context.Context) (service.InspectionSnapshot, error) {
		loads.Add(1)
		close(started)
		<-release
		return original(ctx)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := cache.state(ctx, false)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("cancelled leader returned %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("cancelled leader waited for full replay")
	}
	close(release)
	state, _, err := cache.state(context.Background(), false)
	if err != nil || state.GraphRevision == "" || loads.Load() != 1 {
		t.Fatalf("background replay was not reused: revision=%q loads=%d err=%v", state.GraphRevision, loads.Load(), err)
	}
}

func TestExplorerCacheReturnsOneVerifiedPrefixWhenWriterAdvancesDuringReplay(t *testing.T) {
	svc := cacheTestService(t)
	cache := newExplorerCache(svc)
	original := cache.load
	loaded := make(chan struct{})
	release := make(chan struct{})
	cache.load = func(ctx context.Context) (service.InspectionSnapshot, error) {
		snapshot, err := original(ctx)
		close(loaded)
		<-release
		return snapshot, err
	}
	result := make(chan service.InspectionSnapshot, 1)
	errors := make(chan error, 1)
	go func() {
		snapshot, err := cache.snapshotFor(context.Background(), false)
		if err != nil {
			errors <- err
			return
		}
		result <- service.InspectionSnapshot{State: snapshot.State, History: snapshot.History}
	}()
	<-loaded
	if _, err := svc.BindRole("controller", "test", "moving-writer", time.Minute, false, "cache/moving-bind"); err != nil {
		t.Fatal(err)
	}
	close(release)
	var first service.InspectionSnapshot
	select {
	case err := <-errors:
		t.Fatal(err)
	case first = <-result:
	case <-time.After(time.Second):
		t.Fatal("view chased a moving writer instead of returning its verified prefix")
	}
	cache.load = original
	head, snapshot, err := cache.head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || snapshot.Head.Sequence != first.State.HeadSequence || head.Sequence <= snapshot.Head.Sequence {
		t.Fatalf("head did not expose moving-writer staleness: first=%d snapshot=%v current=%d", first.State.HeadSequence, snapshot, head.Sequence)
	}
	current, _, err := cache.state(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if current.HeadSequence != head.Sequence {
		t.Fatalf("forced request did not advance stale snapshot: want=%d current=%d", head.Sequence, current.HeadSequence)
	}
}
