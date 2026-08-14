package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/sdk"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	DefaultCallTimeout    = 5 * time.Second
	DefaultMaxInputBytes  = 8 * 1024 * 1024
	DefaultMaxOutputBytes = 64 * 1024
)

type Runtime struct {
	Registry       *Registry
	CallTimeout    time.Duration
	MaxOutputBytes int
}

type Invocation struct {
	Kind       string          `json:"kind"`
	ProviderID string          `json:"providerId"`
	Input      json.RawMessage `json:"input"`
}

type InvocationResult struct {
	Kind     string          `json:"kind"`
	Provider sdk.Metadata    `json:"provider"`
	Output   json.RawMessage `json:"output"`
}

type ConformanceCheck struct {
	Kind       string       `json:"kind"`
	Provider   sdk.Metadata `json:"provider"`
	Status     string       `json:"status"`
	SchemaHash string       `json:"computedSchemaHash,omitempty"`
	Detail     string       `json:"detail,omitempty"`
}

type ConformanceReport struct {
	Healthy bool               `json:"healthy"`
	Checks  []ConformanceCheck `json:"checks"`
}

func NewRuntime(registry *Registry) *Runtime {
	return &Runtime{Registry: registry, CallTimeout: DefaultCallTimeout, MaxOutputBytes: DefaultMaxOutputBytes}
}

func (r *Runtime) Inventory() []InventoryItem { return r.Registry.Inventory() }

func (r *Runtime) Check() ConformanceReport {
	report := ConformanceReport{Healthy: true, Checks: []ConformanceCheck{}}
	for _, item := range r.Registry.Inventory() {
		check := ConformanceCheck{Kind: item.Kind, Provider: item.Metadata, Status: "pass"}
		if err := validateMetadata(item.Metadata); err != nil {
			check.Status, check.Detail, report.Healthy = "fail", err.Error(), false
			report.Checks = append(report.Checks, check)
			continue
		}
		if !item.SchemaRequired {
			check.Detail = "registered; invocation is governed by its dedicated control path"
			report.Checks = append(report.Checks, check)
			continue
		}
		provider, ok := r.provider(item.Kind, item.Metadata.ID)
		if !ok {
			check.Status, check.Detail, report.Healthy = "fail", "registry lookup failed", false
			report.Checks = append(report.Checks, check)
			continue
		}
		schemaProvider, ok := provider.(sdk.InputSchemaProvider)
		if !ok {
			check.Status, check.Detail, report.Healthy = "fail", "callable provider does not expose InputSchema", false
			report.Checks = append(report.Checks, check)
			continue
		}
		computed, err := validateProviderSchema(schemaProvider.InputSchema())
		check.SchemaHash = computed
		if err != nil {
			check.Status, check.Detail, report.Healthy = "fail", err.Error(), false
		} else if item.Metadata.Stability == sdk.StabilityStable && item.Metadata.SchemaHash != computed {
			check.Status, check.Detail, report.Healthy = "fail", "stable provider schema hash does not match InputSchema", false
		}
		report.Checks = append(report.Checks, check)
	}
	return report
}

func (r *Runtime) ValidateNodeKind(id string, input json.RawMessage, outcomes []sdk.OutcomeDefinition) error {
	provider, ok := r.Registry.NodeKind(id)
	if !ok {
		return fmt.Errorf("node kind provider %s is not registered", id)
	}
	metadata := normalizedMetadata(provider.Metadata())
	if err := validateMetadata(metadata); err != nil {
		return fmt.Errorf("provider metadata: %w", err)
	}
	computed, err := validateProviderSchema(provider.InputSchema())
	if err != nil {
		return fmt.Errorf("provider schema: %w", err)
	}
	if metadata.Stability == sdk.StabilityStable && metadata.SchemaHash != computed {
		return fmt.Errorf("stable provider schema hash mismatch: metadata=%s computed=%s", metadata.SchemaHash, computed)
	}
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	if err := validateInput(provider.InputSchema(), input); err != nil {
		return fmt.Errorf("node input: %w", err)
	}
	declared := make(map[string]string, len(outcomes))
	for _, outcome := range outcomes {
		declared[outcome.ID] = outcome.Class
	}
	expected := provider.Outcomes()
	if len(declared) != len(expected) {
		return fmt.Errorf("node outcomes do not match provider contract")
	}
	for _, outcome := range expected {
		if declared[outcome.ID] != outcome.Class {
			return fmt.Errorf("node outcome %s does not match provider contract", outcome.ID)
		}
	}
	return nil
}

func (r *Runtime) Invoke(ctx context.Context, invocation Invocation) (InvocationResult, error) {
	provider, ok := r.provider(invocation.Kind, invocation.ProviderID)
	if !ok {
		return InvocationResult{}, fmt.Errorf("%s provider %s is not registered", invocation.Kind, invocation.ProviderID)
	}
	metadata := normalizedMetadata(provider.(interface{ Metadata() sdk.Metadata }).Metadata())
	if err := validateMetadata(metadata); err != nil {
		return InvocationResult{}, fmt.Errorf("provider metadata: %w", err)
	}
	schemaProvider, ok := provider.(sdk.InputSchemaProvider)
	if !ok {
		return InvocationResult{}, fmt.Errorf("%s provider %s is not callable without InputSchema", invocation.Kind, invocation.ProviderID)
	}
	computed, err := validateProviderSchema(schemaProvider.InputSchema())
	if err != nil {
		return InvocationResult{}, fmt.Errorf("provider schema: %w", err)
	}
	if metadata.Stability == sdk.StabilityStable && metadata.SchemaHash != computed {
		return InvocationResult{}, fmt.Errorf("stable provider schema hash mismatch: metadata=%s computed=%s", metadata.SchemaHash, computed)
	}
	if err := validateInput(schemaProvider.InputSchema(), invocation.Input); err != nil {
		return InvocationResult{}, fmt.Errorf("provider input: %w", err)
	}

	timeout := r.CallTimeout
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := invokeIsolated(callCtx, func() (json.RawMessage, error) {
		switch invocation.Kind {
		case KindPredicate:
			request := sdk.PredicateRequest{}
			if err := decodeJSON(invocation.Input, &request); err != nil {
				return nil, err
			}
			matched, err := provider.(sdk.PredicateProvider).Evaluate(callCtx, request)
			if err != nil {
				return nil, err
			}
			return json.Marshal(map[string]bool{"matched": matched})
		case KindPolicy:
			request := sdk.PolicyRequest{}
			if err := decodeJSON(invocation.Input, &request); err != nil {
				return nil, err
			}
			decision, err := provider.(sdk.PolicyProvider).Decide(callCtx, request)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(decision.Outcome) == "" {
				return nil, fmt.Errorf("policy outcome is required")
			}
			return json.Marshal(decision)
		case KindImporter:
			result, err := provider.(sdk.GraphImporterProvider).Import(callCtx, invocation.Input)
			return result, err
		case KindProjection:
			var events []json.RawMessage
			if err := decodeJSON(invocation.Input, &events); err != nil {
				return nil, err
			}
			return provider.(sdk.ProjectionProvider).Project(callCtx, events)
		default:
			return nil, fmt.Errorf("provider kind %s cannot be invoked through the generic runtime", invocation.Kind)
		}
	})
	if err != nil {
		return InvocationResult{}, err
	}
	if err := r.validateOutput(output); err != nil {
		return InvocationResult{}, fmt.Errorf("provider output: %w", err)
	}
	if invocation.Kind == KindImporter {
		graph := domain.GraphDefinition{}
		if err := decodeJSON(output, &graph); err != nil {
			return InvocationResult{}, fmt.Errorf("importer output is not a graph: %w", err)
		}
		if err := domain.ValidateGraph(graph); err != nil {
			return InvocationResult{}, fmt.Errorf("importer output graph: %w", err)
		}
	}
	return InvocationResult{Kind: invocation.Kind, Provider: metadata, Output: output}, nil
}

func (r *Runtime) provider(kind, id string) (any, bool) {
	switch kind {
	case KindNodeKind:
		value, ok := r.Registry.NodeKind(id)
		return value, ok
	case KindPredicate:
		value, ok := r.Registry.Predicate(id)
		return value, ok
	case KindPolicy:
		value, ok := r.Registry.Policy(id)
		return value, ok
	case KindImporter:
		value, ok := r.Registry.Importer(id)
		return value, ok
	case KindProjection:
		value, ok := r.Registry.Projection(id)
		return value, ok
	default:
		return nil, false
	}
}

func (r *Runtime) validateOutput(output json.RawMessage) error {
	limit := r.MaxOutputBytes
	if limit <= 0 {
		limit = DefaultMaxOutputBytes
	}
	if len(output) > limit {
		return fmt.Errorf("exceeds %d byte limit", limit)
	}
	if err := domain.ValidateAuthorityJSON(output); err != nil {
		return err
	}
	if err := domain.RejectSensitiveFields(output); err != nil {
		return err
	}
	return nil
}

func invokeIsolated(ctx context.Context, call func() (json.RawMessage, error)) (json.RawMessage, error) {
	type result struct {
		output json.RawMessage
		err    error
	}
	done := make(chan result, 1)
	go func() {
		var value result
		defer func() {
			if recovered := recover(); recovered != nil {
				value.err = fmt.Errorf("provider panicked: %v", recovered)
			}
			done <- value
		}()
		value.output, value.err = call()
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("provider call timed out or was cancelled: %w", ctx.Err())
	case value := <-done:
		return value.output, value.err
	}
}

var providerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]+$`)
var semverPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

func validateMetadata(metadata sdk.Metadata) error {
	if !providerIDPattern.MatchString(metadata.ID) {
		return fmt.Errorf("invalid provider ID %q", metadata.ID)
	}
	if !semverPattern.MatchString(metadata.Version) {
		return fmt.Errorf("invalid provider version %q", metadata.Version)
	}
	if strings.TrimSpace(metadata.SchemaHash) == "" {
		return fmt.Errorf("schema hash is required")
	}
	if metadata.Stability != sdk.StabilityExperimental && metadata.Stability != sdk.StabilityStable {
		return fmt.Errorf("stability must be experimental or stable")
	}
	return nil
}

func validateProviderSchema(raw json.RawMessage) (string, error) {
	if err := domain.ValidateAuthorityJSON(raw); err != nil {
		return "", err
	}
	if err := domain.RejectSensitiveFields(raw); err != nil {
		return "", err
	}
	if _, err := compileSchema(raw); err != nil {
		return "", err
	}
	return sdk.InputSchemaHash(raw)
}

func validateInput(schemaRaw, input json.RawMessage) error {
	if len(input) > DefaultMaxInputBytes {
		return fmt.Errorf("input exceeds %d bytes", DefaultMaxInputBytes)
	}
	if err := domain.ValidateAuthorityJSON(input); err != nil {
		return err
	}
	if err := domain.RejectSensitiveFields(input); err != nil {
		return err
	}
	schema, err := compileSchema(schemaRaw)
	if err != nil {
		return err
	}
	var value any
	if err := decodeJSON(input, &value); err != nil {
		return err
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("does not match schema: %w", err)
	}
	return nil
}

type denyRemoteLoader struct{}

func (denyRemoteLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("remote schema reference %s is not allowed", url)
}

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	var document any
	if err := decodeJSON(raw, &document); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(denyRemoteLoader{})
	const location = "urn:dagrail:provider-input-schema"
	if err := compiler.AddResource(location, document); err != nil {
		return nil, err
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		return nil, fmt.Errorf("compile JSON Schema: %w", err)
	}
	return schema, nil
}

func decodeJSON(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("invalid trailing JSON content")
	}
	return nil
}
