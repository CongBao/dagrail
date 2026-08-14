// Package sdk defines the compile-time extension boundary for DAGrail providers.
package sdk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/gowebpki/jcs"
)

type Stability string

const (
	StabilityExperimental Stability = "experimental"
	StabilityStable       Stability = "stable"
)

type Metadata struct {
	ID         string    `json:"id"`
	Version    string    `json:"version"`
	SchemaHash string    `json:"schemaHash"`
	Stability  Stability `json:"stability,omitempty"`
}

// InputSchemaProvider is implemented by callable providers. DAGrail validates
// each request against this JSON Schema before provider code is entered.
type InputSchemaProvider interface {
	InputSchema() json.RawMessage
}

// InputSchemaHash returns the domain-separated hash used by stable provider
// metadata. The schema must be valid JSON and is canonicalized with RFC 8785.
func InputSchemaHash(schema json.RawMessage) (string, error) {
	canonical, err := jcs.Transform(schema)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("dagrail-provider-input-schema-v1\x00"), canonical...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

type OutcomeDefinition struct {
	ID    string `json:"id"`
	Class string `json:"class"`
}

type NodeKindProvider interface {
	Metadata() Metadata
	InputSchema() json.RawMessage
	Outcomes() []OutcomeDefinition
}

type PredicateRequest struct {
	Predicate json.RawMessage `json:"predicate"`
	Source    json.RawMessage `json:"source"`
}
type PredicateProvider interface {
	Metadata() Metadata
	Evaluate(context.Context, PredicateRequest) (bool, error)
}

type PolicyRequest struct {
	PolicyID string            `json:"policyId"`
	Input    json.RawMessage   `json:"input"`
	Evidence []json.RawMessage `json:"evidence,omitempty"`
}
type PolicyDecision struct {
	Outcome string          `json:"outcome"`
	Facts   json.RawMessage `json:"facts,omitempty"`
}
type PolicyProvider interface {
	Metadata() Metadata
	Decide(context.Context, PolicyRequest) (PolicyDecision, error)
}

type EffectRequest struct {
	ActionID       string          `json:"actionId"`
	ProjectRoot    string          `json:"projectRoot"`
	NodeID         string          `json:"nodeId"`
	AttemptID      string          `json:"attemptId"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Request        json.RawMessage `json:"request"`
	PriorReceipt   json.RawMessage `json:"priorReceipt,omitempty"`
}

type PreparedEffect struct {
	AdapterID string          `json:"adapterId"`
	Binding   json.RawMessage `json:"binding"`
}

type EffectReceipt struct {
	Status            string          `json:"status"`
	ExternalID        string          `json:"externalId,omitempty"`
	TransportAccepted bool            `json:"transportAccepted,omitempty"`
	RecipientVisible  bool            `json:"recipientVisible,omitempty"`
	TransportStatus   string          `json:"transportStatus,omitempty"`
	SessionStatus     string          `json:"sessionStatus,omitempty"`
	DeliveryStatus    string          `json:"deliveryStatus,omitempty"`
	AcceptanceStatus  string          `json:"acceptanceStatus,omitempty"`
	CompletionStatus  string          `json:"completionStatus,omitempty"`
	Detail            json.RawMessage `json:"detail,omitempty"`
}

type EffectAdapter interface {
	Metadata() Metadata
	Prepare(context.Context, EffectRequest) (PreparedEffect, error)
	Dispatch(context.Context, EffectRequest, PreparedEffect) (EffectReceipt, error)
	Reconcile(context.Context, EffectRequest, PreparedEffect, json.RawMessage) (EffectReceipt, error)
}

type HarnessCapabilities struct {
	ContextHook bool `json:"contextHook"`
	Dispatch    bool `json:"dispatch"`
	Resume      bool `json:"resume"`
	Inspect     bool `json:"inspect"`
	Cancel      bool `json:"cancel"`
}

type HarnessAdapter interface {
	Metadata() Metadata
	Probe(context.Context) (HarnessCapabilities, error)
	Dispatch(context.Context, EffectRequest) (EffectReceipt, error)
	Resume(context.Context, string, EffectRequest) (EffectReceipt, error)
}

// HarnessObserver is an optional extension for adapters that can inspect a
// previously dispatched native session without relying on caller-supplied
// evidence. Observation must not send a new user message or start a new turn.
type HarnessObserver interface {
	Observe(context.Context, string, EffectRequest) (EffectReceipt, error)
}

type GraphImporterProvider interface {
	Metadata() Metadata
	Import(context.Context, json.RawMessage) (json.RawMessage, error)
}
type ProjectionProvider interface {
	Metadata() Metadata
	Project(context.Context, []json.RawMessage) (json.RawMessage, error)
}
