// Package providers is the compile-in extension point for custom DAGrail
// distributions. Providers are registered during process initialization and
// are copied into each subsequently opened Service.
package providers

import (
	"fmt"
	"sort"
	"sync"

	"github.com/CongBao/dagrail/sdk"
)

const (
	NodeKind   = "node-kind"
	Predicate  = "predicate"
	Policy     = "policy"
	Effect     = "effect"
	Harness    = "harness"
	Importer   = "graph-importer"
	Projection = "projection"
)

type Registration struct {
	Kind     string
	Provider any
}

var registry struct {
	sync.RWMutex
	items map[string]Registration
}

func init() { registry.items = map[string]Registration{} }

// Register accepts any of the public SDK provider interfaces. One concrete
// value may implement more than one interface; each capability is registered
// independently under its stable metadata ID.
func Register(provider any) error {
	metadataProvider, ok := provider.(interface{ Metadata() sdk.Metadata })
	if !ok {
		return fmt.Errorf("provider does not expose DAGrail metadata")
	}
	metadata := metadataProvider.Metadata()
	if metadata.ID == "" || metadata.Version == "" || metadata.SchemaHash == "" {
		return fmt.Errorf("provider metadata requires ID, version and schema hash")
	}
	kinds := make([]string, 0, 7)
	if _, ok := provider.(sdk.NodeKindProvider); ok {
		kinds = append(kinds, NodeKind)
	}
	if _, ok := provider.(sdk.PredicateProvider); ok {
		kinds = append(kinds, Predicate)
	}
	if _, ok := provider.(sdk.PolicyProvider); ok {
		kinds = append(kinds, Policy)
	}
	if _, ok := provider.(sdk.EffectAdapter); ok {
		kinds = append(kinds, Effect)
	}
	if _, ok := provider.(sdk.HarnessAdapter); ok {
		kinds = append(kinds, Harness)
	}
	if _, ok := provider.(sdk.GraphImporterProvider); ok {
		kinds = append(kinds, Importer)
	}
	if _, ok := provider.(sdk.ProjectionProvider); ok {
		kinds = append(kinds, Projection)
	}
	if len(kinds) == 0 {
		return fmt.Errorf("provider %s implements no DAGrail provider interface", metadata.ID)
	}
	registry.Lock()
	defer registry.Unlock()
	for _, kind := range kinds {
		key := kind + "\x00" + metadata.ID
		if _, exists := registry.items[key]; exists {
			return fmt.Errorf("%s provider %s is already registered", kind, metadata.ID)
		}
	}
	for _, kind := range kinds {
		registry.items[kind+"\x00"+metadata.ID] = Registration{Kind: kind, Provider: provider}
	}
	return nil
}

func Snapshot() []Registration {
	registry.RLock()
	defer registry.RUnlock()
	keys := make([]string, 0, len(registry.items))
	for key := range registry.items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]Registration, 0, len(keys))
	for _, key := range keys {
		result = append(result, registry.items[key])
	}
	return result
}
