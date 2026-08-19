package ui

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/service"
)

type explorerSnapshot struct {
	State    domain.State
	History  []service.HistoryEntry
	Head     journal.Head
	LoadedAt time.Time
}

type explorerCache struct {
	service *service.Service
	load    func(context.Context) (service.InspectionSnapshot, error)

	mu       sync.Mutex
	snapshot *explorerSnapshot
	loading  *explorerLoad
}

type explorerLoad struct {
	done     chan struct{}
	snapshot *explorerSnapshot
	err      error
}

func newExplorerCache(svc *service.Service) *explorerCache {
	return &explorerCache{service: svc, load: svc.InspectionSnapshotContext}
}

// state returns one verified/reduced snapshot shared by every UI view. Normal
// reads remain on that stable prefix; the head endpoint performs the cheap tail
// observation and an explicit refresh advances the snapshot. Concurrent cold
// or forced loads collapse into one replay, so overview/topology/detail cannot
// each replay the same journal prefix independently.
func (cache *explorerCache) snapshotFor(ctx context.Context, force bool) (*explorerSnapshot, error) {
	if cache == nil || cache.service == nil {
		return nil, fmt.Errorf("explorer cache is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cache.mu.Lock()
	if !force && cache.loading == nil && cache.snapshot != nil {
		snapshot := cache.snapshot
		cache.mu.Unlock()
		copy := *snapshot
		return &copy, nil
	}
	loading := cache.loading
	if loading == nil {
		loading = &explorerLoad{done: make(chan struct{})}
		cache.loading = loading
		go cache.populate(loading)
	}
	cache.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-loading.done:
		if loading.err != nil {
			return nil, loading.err
		}
		// Return the exact verified prefix this request joined even if a
		// writer appended while replay was running. The lightweight head
		// poll will mark it stale and a forced refresh can advance it; one
		// request never chases a moving writer forever.
		copy := *loading.snapshot
		return &copy, nil
	}
}

// populate deliberately outlives one HTTP request. A browser abort returns to
// that caller immediately while the single verified replay warms the shared
// cache for the next request instead of discarding expensive completed work.
func (cache *explorerCache) populate(loading *explorerLoad) {
	loaded, err := cache.load(context.Background())
	loadedAt := time.Now().UTC()
	cache.mu.Lock()
	if err == nil {
		cache.snapshot = &explorerSnapshot{State: loaded.State, History: loaded.History, Head: journal.Head{Sequence: loaded.State.HeadSequence, Hash: loaded.State.HeadHash}, LoadedAt: loadedAt}
		loading.snapshot = cache.snapshot
	}
	loading.err = err
	if cache.loading == loading {
		cache.loading = nil
	}
	close(loading.done)
	cache.mu.Unlock()
}

func (cache *explorerCache) state(ctx context.Context, force bool) (domain.State, time.Time, error) {
	snapshot, err := cache.snapshotFor(ctx, force)
	if err != nil {
		return domain.State{}, time.Time{}, err
	}
	return snapshot.State, snapshot.LoadedAt, nil
}

func (cache *explorerCache) head(ctx context.Context) (journal.Head, *explorerSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return journal.Head{}, nil, err
	}
	head, err := cache.service.Journal.InspectHead()
	if err != nil {
		return journal.Head{}, nil, err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.snapshot == nil {
		return head, nil, nil
	}
	copy := *cache.snapshot
	return head, &copy, nil
}
