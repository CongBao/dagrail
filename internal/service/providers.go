package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	internalproviders "github.com/CongBao/dagrail/internal/providers"
	"github.com/gowebpki/jcs"
)

func (s *Service) ProviderInventory() []internalproviders.InventoryItem {
	return s.ProviderRuntime.Inventory()
}

func (s *Service) CheckProviders() internalproviders.ConformanceReport {
	return s.ProviderRuntime.Check()
}

func (s *Service) InvokeProvider(ctx context.Context, invocation internalproviders.Invocation) (internalproviders.InvocationResult, error) {
	return s.ProviderRuntime.Invoke(ctx, invocation)
}

func (s *Service) ImportGraphFromProvider(ctx context.Context, providerID string, input json.RawMessage, idempotencyKey, actorRole string) (domain.CommandResult, error) {
	if idempotencyKey == "" {
		return domain.CommandResult{}, fmt.Errorf("idempotency key is required")
	}
	state, _, err := s.load()
	if err != nil {
		return domain.CommandResult{}, err
	}
	if existing, ok := state.Commands[idempotencyKey]; ok {
		return existing, nil
	}
	if state.Graph != nil {
		return domain.CommandResult{}, fmt.Errorf("graph is already imported; use a graph change")
	}
	result, err := s.ProviderRuntime.Invoke(ctx, internalproviders.Invocation{Kind: internalproviders.KindImporter, ProviderID: providerID, Input: input})
	if err != nil {
		return domain.CommandResult{}, err
	}
	graph := domain.GraphDefinition{}
	if err := json.Unmarshal(result.Output, &graph); err != nil {
		return domain.CommandResult{}, err
	}
	digest, err := providerInputDigest(input)
	if err != nil {
		return domain.CommandResult{}, err
	}
	source := map[string]any{"kind": "provider", "provider": result.Provider, "inputDigest": digest}
	return s.importGraphDefinition(graph, idempotencyKey, actorRole, source)
}

func (s *Service) RenderProjection(ctx context.Context, providerID string) (internalproviders.InvocationResult, error) {
	segments, err := s.Journal.ReadAll()
	if err != nil {
		return internalproviders.InvocationResult{}, err
	}
	events := make([]json.RawMessage, 0)
	for _, segment := range segments {
		for _, stored := range segment.Events {
			event, err := journal.UpcastEvent(segment.SchemaVersion, stored)
			if err != nil {
				return internalproviders.InvocationResult{}, err
			}
			raw, err := json.Marshal(struct {
				Sequence uint64          `json:"sequence"`
				Type     string          `json:"type"`
				Payload  json.RawMessage `json:"payload"`
			}{segment.Sequence, event.Type, event.Payload})
			if err != nil {
				return internalproviders.InvocationResult{}, err
			}
			events = append(events, raw)
		}
	}
	input, err := json.Marshal(events)
	if err != nil {
		return internalproviders.InvocationResult{}, err
	}
	return s.ProviderRuntime.Invoke(ctx, internalproviders.Invocation{Kind: internalproviders.KindProjection, ProviderID: providerID, Input: input})
}

func providerInputDigest(input json.RawMessage) (string, error) {
	canonical, err := jcs.Transform(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("dagrail-provider-input-v1\x00"), canonical...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
