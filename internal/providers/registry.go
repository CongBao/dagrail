package providers

import (
	"fmt"
	"sync"

	"github.com/CongBao/dagrail/sdk"
)

type Registry struct {
	mu          sync.RWMutex
	nodeKinds   map[string]sdk.NodeKindProvider
	predicates  map[string]sdk.PredicateProvider
	policies    map[string]sdk.PolicyProvider
	effects     map[string]sdk.EffectAdapter
	harnesses   map[string]sdk.HarnessAdapter
	importers   map[string]sdk.GraphImporterProvider
	projections map[string]sdk.ProjectionProvider
}

func New() *Registry {
	return &Registry{
		nodeKinds: map[string]sdk.NodeKindProvider{}, predicates: map[string]sdk.PredicateProvider{}, policies: map[string]sdk.PolicyProvider{},
		effects: map[string]sdk.EffectAdapter{}, harnesses: map[string]sdk.HarnessAdapter{}, importers: map[string]sdk.GraphImporterProvider{}, projections: map[string]sdk.ProjectionProvider{},
	}
}

func metadataOK(metadata sdk.Metadata) error {
	if metadata.ID == "" || metadata.Version == "" || metadata.SchemaHash == "" {
		return fmt.Errorf("provider metadata requires ID, version and schema hash")
	}
	return nil
}

func register[T any](items map[string]T, metadata sdk.Metadata, provider T, kind string) error {
	if err := metadataOK(metadata); err != nil {
		return fmt.Errorf("%s %w", kind, err)
	}
	if _, exists := items[metadata.ID]; exists {
		return fmt.Errorf("%s provider %s is already registered", kind, metadata.ID)
	}
	items[metadata.ID] = provider
	return nil
}

func (r *Registry) RegisterNodeKind(provider sdk.NodeKindProvider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return register(r.nodeKinds, provider.Metadata(), provider, "node kind")
}

func (r *Registry) NodeKind(id string) (sdk.NodeKindProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.nodeKinds[id]
	return value, ok
}

func (r *Registry) RegisterPredicate(provider sdk.PredicateProvider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return register(r.predicates, provider.Metadata(), provider, "predicate")
}

func (r *Registry) Predicate(id string) (sdk.PredicateProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.predicates[id]
	return value, ok
}

func (r *Registry) RegisterPolicy(provider sdk.PolicyProvider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return register(r.policies, provider.Metadata(), provider, "policy")
}

func (r *Registry) Policy(id string) (sdk.PolicyProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.policies[id]
	return value, ok
}

func (r *Registry) RegisterEffect(provider sdk.EffectAdapter) error {
	metadata := provider.Metadata()
	r.mu.Lock()
	defer r.mu.Unlock()
	return register(r.effects, metadata, provider, "effect")
}

func (r *Registry) Effect(id string) (sdk.EffectAdapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.effects[id]
	return value, ok
}

func (r *Registry) RegisterHarness(provider sdk.HarnessAdapter) error {
	metadata := provider.Metadata()
	r.mu.Lock()
	defer r.mu.Unlock()
	return register(r.harnesses, metadata, provider, "harness")
}

func (r *Registry) RegisterImporter(provider sdk.GraphImporterProvider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return register(r.importers, provider.Metadata(), provider, "graph importer")
}

func (r *Registry) Importer(id string) (sdk.GraphImporterProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.importers[id]
	return value, ok
}

func (r *Registry) RegisterProjection(provider sdk.ProjectionProvider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return register(r.projections, provider.Metadata(), provider, "projection")
}

func (r *Registry) Projection(id string) (sdk.ProjectionProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.projections[id]
	return value, ok
}

func (r *Registry) Matches(metadata sdk.Metadata) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	values := []sdk.Metadata{}
	if value, ok := r.nodeKinds[metadata.ID]; ok {
		values = append(values, value.Metadata())
	}
	if value, ok := r.predicates[metadata.ID]; ok {
		values = append(values, value.Metadata())
	}
	if value, ok := r.policies[metadata.ID]; ok {
		values = append(values, value.Metadata())
	}
	if value, ok := r.effects[metadata.ID]; ok {
		values = append(values, value.Metadata())
	}
	if value, ok := r.harnesses[metadata.ID]; ok {
		values = append(values, value.Metadata())
	}
	if value, ok := r.importers[metadata.ID]; ok {
		values = append(values, value.Metadata())
	}
	if value, ok := r.projections[metadata.ID]; ok {
		values = append(values, value.Metadata())
	}
	for _, value := range values {
		if value.Version == metadata.Version && value.SchemaHash == metadata.SchemaHash {
			return true
		}
	}
	return false
}

func (r *Registry) Harness(id string) (sdk.HarnessAdapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.harnesses[id]
	return value, ok
}
