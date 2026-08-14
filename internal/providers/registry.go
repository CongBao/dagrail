package providers

import (
	"fmt"
	"sort"
	"sync"

	"github.com/CongBao/dagrail/sdk"
)

const (
	KindNodeKind   = "node-kind"
	KindPredicate  = "predicate"
	KindPolicy     = "policy"
	KindEffect     = "effect"
	KindHarness    = "harness"
	KindImporter   = "graph-importer"
	KindProjection = "projection"
)

type InventoryItem struct {
	Kind           string       `json:"kind"`
	Metadata       sdk.Metadata `json:"metadata"`
	Callable       bool         `json:"callable"`
	SchemaRequired bool         `json:"schemaRequired"`
	HasSchema      bool         `json:"hasSchema"`
}

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
	if metadata.Stability != "" && metadata.Stability != sdk.StabilityExperimental && metadata.Stability != sdk.StabilityStable {
		return fmt.Errorf("provider metadata stability must be experimental or stable")
	}
	return nil
}

func normalizedMetadata(metadata sdk.Metadata) sdk.Metadata {
	if metadata.Stability == "" {
		metadata.Stability = sdk.StabilityExperimental
	}
	return metadata
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

func inventoryItem(kind string, provider any, callable, schemaRequired bool) InventoryItem {
	metadata := normalizedMetadata(provider.(interface{ Metadata() sdk.Metadata }).Metadata())
	_, hasSchema := provider.(sdk.InputSchemaProvider)
	return InventoryItem{Kind: kind, Metadata: metadata, Callable: callable, SchemaRequired: schemaRequired, HasSchema: hasSchema}
}

// Inventory returns a stable, sorted snapshot without exposing provider
// implementations to callers.
func (r *Registry) Inventory() []InventoryItem {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]InventoryItem, 0, len(r.nodeKinds)+len(r.predicates)+len(r.policies)+len(r.effects)+len(r.harnesses)+len(r.importers)+len(r.projections))
	for _, provider := range r.nodeKinds {
		items = append(items, inventoryItem(KindNodeKind, provider, false, true))
	}
	for _, provider := range r.predicates {
		items = append(items, inventoryItem(KindPredicate, provider, true, true))
	}
	for _, provider := range r.policies {
		items = append(items, inventoryItem(KindPolicy, provider, true, true))
	}
	for _, provider := range r.effects {
		items = append(items, inventoryItem(KindEffect, provider, false, false))
	}
	for _, provider := range r.harnesses {
		items = append(items, inventoryItem(KindHarness, provider, false, false))
	}
	for _, provider := range r.importers {
		items = append(items, inventoryItem(KindImporter, provider, true, true))
	}
	for _, provider := range r.projections {
		items = append(items, inventoryItem(KindProjection, provider, true, true))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind == items[j].Kind {
			return items[i].Metadata.ID < items[j].Metadata.ID
		}
		return items[i].Kind < items[j].Kind
	})
	return items
}
