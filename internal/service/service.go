package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/effects"
	"github.com/CongBao/dagrail/internal/harness"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/project"
	"github.com/CongBao/dagrail/internal/projection"
	internalproviders "github.com/CongBao/dagrail/internal/providers"
	"github.com/CongBao/dagrail/internal/version"
	compileproviders "github.com/CongBao/dagrail/providers"
	"github.com/CongBao/dagrail/sdk"
	"github.com/google/uuid"
	"github.com/gowebpki/jcs"
	"gopkg.in/yaml.v3"
)

type Service struct {
	Project         project.Project
	Journal         *journal.Store
	Projection      *projection.Store
	Providers       *internalproviders.Registry
	ProviderRuntime *internalproviders.Runtime
	Now             func() time.Time
}

func Open(root string) (*Service, error) {
	p, err := project.Open(root)
	if err != nil {
		return nil, err
	}
	j, err := journal.Open(p.DataDir, p.Config.ProjectID)
	if err != nil {
		return nil, err
	}
	projectionStore, err := projection.Open(p.DataDir)
	if err != nil {
		return nil, err
	}
	registry := internalproviders.New()
	if err := registry.RegisterEffect(effects.Manual{}); err != nil {
		return nil, err
	}
	if err := registry.RegisterEffect(effects.GitMerge{}); err != nil {
		return nil, err
	}
	for _, harnessID := range []string{"codex", "claude-code", "copilot-cli"} {
		adapter, adapterErr := harness.New(harnessID)
		if adapterErr != nil {
			return nil, adapterErr
		}
		if err := registry.RegisterHarness(adapter); err != nil {
			return nil, err
		}
		if err := registry.RegisterEffect(effects.HarnessDispatch{Harness: adapter}); err != nil {
			return nil, err
		}
	}
	for _, registration := range compileproviders.Snapshot() {
		var registerErr error
		switch registration.Kind {
		case compileproviders.NodeKind:
			registerErr = registry.RegisterNodeKind(registration.Provider.(sdk.NodeKindProvider))
		case compileproviders.Predicate:
			registerErr = registry.RegisterPredicate(registration.Provider.(sdk.PredicateProvider))
		case compileproviders.Policy:
			registerErr = registry.RegisterPolicy(registration.Provider.(sdk.PolicyProvider))
		case compileproviders.Effect:
			registerErr = registry.RegisterEffect(registration.Provider.(sdk.EffectAdapter))
		case compileproviders.Harness:
			registerErr = registry.RegisterHarness(registration.Provider.(sdk.HarnessAdapter))
		case compileproviders.Importer:
			registerErr = registry.RegisterImporter(registration.Provider.(sdk.GraphImporterProvider))
		case compileproviders.Projection:
			registerErr = registry.RegisterProjection(registration.Provider.(sdk.ProjectionProvider))
		}
		if registerErr != nil {
			return nil, registerErr
		}
	}
	service := &Service{Project: p, Journal: j, Projection: projectionStore, Providers: registry, ProviderRuntime: internalproviders.NewRuntime(registry), Now: time.Now}
	if err := service.settleAutomatic(); err != nil {
		return nil, err
	}
	state, segments, err := service.load()
	if err != nil {
		return nil, err
	}
	if err := service.Projection.Sync(state, segments); err != nil {
		return nil, err
	}
	return service, nil
}

func Init(root, name string) (*Service, error) {
	if _, err := project.Init(root, name); err != nil {
		return nil, err
	}
	return Open(root)
}

func (s *Service) ImportGraph(path, idempotencyKey, actorRole string) (domain.CommandResult, error) {
	return s.ImportGraphWithProvenance(path, idempotencyKey, actorRole, nil)
}

func (s *Service) ImportGraphWithProvenance(path, idempotencyKey, actorRole string, source any) (domain.CommandResult, error) {
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
	graph, err := decodeGraph(path)
	if err != nil {
		return domain.CommandResult{}, err
	}
	if source != nil {
		raw, err := json.Marshal(source)
		if err != nil {
			return domain.CommandResult{}, err
		}
		if err := domain.ValidateAuthorityJSON(raw); err != nil {
			return domain.CommandResult{}, fmt.Errorf("graph provenance: %w", err)
		}
		if err := domain.RejectSensitiveFields(raw); err != nil {
			return domain.CommandResult{}, fmt.Errorf("graph provenance: %w", err)
		}
	}
	return s.importGraphDefinition(graph, idempotencyKey, actorRole, source)
}

func (s *Service) importGraphDefinition(graph domain.GraphDefinition, idempotencyKey, actorRole string, source any) (domain.CommandResult, error) {
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
	if err := domain.ValidateGraph(graph); err != nil {
		return domain.CommandResult{}, err
	}
	if err := s.validateGraphProviders(graph); err != nil {
		return domain.CommandResult{}, err
	}
	revision, err := graphRevision(graph)
	if err != nil {
		return domain.CommandResult{}, err
	}
	payload, _ := json.Marshal(struct {
		Graph    domain.GraphDefinition `json:"graph"`
		Revision string                 `json:"revision"`
		Source   any                    `json:"source,omitempty"`
	}{graph, revision, source})
	expectedHead := state.HeadHash
	segment, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "graph.import", ActorRole: actorRole, IdempotencyKey: idempotencyKey}, []journal.Event{{Type: "graph.imported", Payload: payload}}, s.Now(), &expectedHead)
	if err != nil {
		return domain.CommandResult{}, err
	}
	if err := s.settleAutomatic(); err != nil {
		return domain.CommandResult{}, err
	}
	state, segments, err := s.load()
	if err != nil {
		return domain.CommandResult{}, err
	}
	result := state.Commands[idempotencyKey]
	result.Sequence = segment.Sequence
	if err := s.Projection.Sync(state, segments); err != nil {
		return domain.CommandResult{}, err
	}
	return result, nil
}

func (s *Service) Frontier() (domain.Frontier, error) {
	state, _, err := s.load()
	if err != nil {
		return domain.Frontier{}, err
	}
	return domain.ComputeFrontier(state), nil
}

func (s *Service) State() (domain.State, error) { state, _, err := s.load(); return state, err }

func (s *Service) VerifyJournal() ([]journal.Segment, error) { return s.Journal.ReadAll() }

func (s *Service) RebuildProjection() error {
	state, segments, err := s.load()
	if err != nil {
		return err
	}
	return s.Projection.Rebuild(state, segments)
}

func (s *Service) load() (domain.State, []journal.Segment, error) {
	segments, err := s.Journal.ReadAll()
	if err != nil {
		return domain.State{}, nil, err
	}
	state := domain.NewState(s.Project.Config.ProjectID)
	for _, segment := range segments {
		for _, storedEvent := range segment.Events {
			event, err := journal.UpcastEvent(segment.SchemaVersion, storedEvent)
			if err != nil {
				return domain.State{}, nil, fmt.Errorf("upcast journal event at sequence %d: %w", segment.Sequence, err)
			}
			switch event.Type {
			case "graph.imported":
				var payload struct {
					Graph    domain.GraphDefinition `json:"graph"`
					Revision string                 `json:"revision"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, nil, err
				}
				state.Graph, state.GraphRevision = &payload.Graph, payload.Revision
				state.Nodes = map[string]domain.NodeRuntime{}
				for _, node := range payload.Graph.Spec.Nodes {
					state.Nodes[node.ID] = domain.NodeRuntime{Status: "planned"}
				}
			case "graph.revised":
				var payload struct {
					Graph      domain.GraphDefinition `json:"graph"`
					Revision   string                 `json:"revision"`
					Superseded []string               `json:"superseded"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, nil, err
				}
				previousNodes := state.Nodes
				state.Graph, state.GraphRevision = &payload.Graph, payload.Revision
				state.Nodes = map[string]domain.NodeRuntime{}
				for _, node := range payload.Graph.Spec.Nodes {
					if runtime, ok := previousNodes[node.ID]; ok {
						state.Nodes[node.ID] = runtime
					} else {
						state.Nodes[node.ID] = domain.NodeRuntime{Status: "planned"}
					}
				}
				for _, nodeID := range payload.Superseded {
					if runtime, ok := state.Nodes[nodeID]; ok {
						runtime.Status = "superseded"
						state.Nodes[nodeID] = runtime
					}
					if attempt, ok := state.LatestAttempt(nodeID); ok && attempt.Status != "terminal" {
						attempt.Status, attempt.Outcome, attempt.UpdatedAt = "terminal", "superseded", segment.CommittedAt
						state.Attempts[attempt.ID] = attempt
						for resourceID, lease := range state.Resources {
							if lease.AttemptID == attempt.ID && lease.Status == "active" {
								lease.Status, lease.ReleasedAt = "released", segment.CommittedAt
								state.Resources[resourceID] = lease
							}
						}
					}
				}
			case "role.bound":
				var lease domain.RoleLease
				if err := json.Unmarshal(event.Payload, &lease); err != nil {
					return domain.State{}, nil, err
				}
				state.Leases[lease.RoleID] = lease
			case "role.released":
				var payload struct {
					RoleID string `json:"roleId"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, nil, err
				}
				lease := state.Leases[payload.RoleID]
				lease.Active = false
				state.Leases[payload.RoleID] = lease
			case "attempt.started", "attempt.leased":
				var attempt domain.Attempt
				if err := json.Unmarshal(event.Payload, &attempt); err != nil {
					return domain.State{}, nil, err
				}
				state.Attempts[attempt.ID] = attempt
				state.NodeAttempts[attempt.NodeID] = append(state.NodeAttempts[attempt.NodeID], attempt.ID)
				state.Nodes[attempt.NodeID] = domain.NodeRuntime{Status: "active"}
			case "node.auto-completed":
				var payload struct {
					NodeID      string `json:"nodeId"`
					Outcome     string `json:"outcome"`
					CompletedAt string `json:"completedAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, nil, err
				}
				runtime, ok := state.Nodes[payload.NodeID]
				if !ok || runtime.Status != "planned" {
					return domain.State{}, nil, fmt.Errorf("automatic completion references non-planned node %s", payload.NodeID)
				}
				state.Nodes[payload.NodeID] = domain.NodeRuntime{Status: "terminal", Outcome: payload.Outcome, OutcomeClass: "success"}
			case "attempt.checkpointed":
				var checkpoint domain.Checkpoint
				if err := json.Unmarshal(event.Payload, &checkpoint); err != nil {
					return domain.State{}, nil, err
				}
				state.Checkpoints[checkpoint.ID] = checkpoint
				attempt := state.Attempts[checkpoint.AttemptID]
				attempt.CheckpointID = checkpoint.ID
				attempt.UpdatedAt = checkpoint.CreatedAt
				state.Attempts[attempt.ID] = attempt
			case "evidence.package-published":
				var pack domain.ExecutionPackage
				if err := json.Unmarshal(event.Payload, &pack); err != nil {
					return domain.State{}, nil, err
				}
				attempt, ok := state.Attempts[pack.AttemptID]
				node, nodeExists := state.NodeDefinition(pack.NodeID)
				if !ok || !nodeExists || attempt.NodeID != pack.NodeID || pack.ProjectID != state.ProjectID || pack.GraphRevision != state.GraphRevision {
					return domain.State{}, nil, fmt.Errorf("execution package %s references an invalid attempt", pack.ID)
				}
				if err := validateExecutionPackageRecord(pack, node); err != nil {
					return domain.State{}, nil, fmt.Errorf("execution package %s: %w", pack.ID, err)
				}
				if _, exists := state.EvidencePackages[pack.ID]; exists {
					return domain.State{}, nil, fmt.Errorf("execution package %s is duplicated", pack.ID)
				}
				pack.Sequence = segment.Sequence
				state.EvidencePackages[pack.ID] = pack
				state.AttemptPackages[pack.AttemptID] = append(state.AttemptPackages[pack.AttemptID], pack.ID)
			case "evidence.reuse-assessed":
				var decision domain.ReuseDecision
				if err := json.Unmarshal(event.Payload, &decision); err != nil {
					return domain.State{}, nil, err
				}
				if _, exists := state.EvidencePackages[decision.PackageID]; !exists {
					return domain.State{}, nil, fmt.Errorf("reuse decision %s references unknown package %s", decision.ID, decision.PackageID)
				}
				if _, exists := state.Attempts[decision.AssessedByAttempt]; !exists {
					return domain.State{}, nil, fmt.Errorf("reuse decision %s references unknown assessor attempt", decision.ID)
				}
				pack := state.EvidencePackages[decision.PackageID]
				node, exists := state.NodeDefinition(pack.NodeID)
				if !exists {
					return domain.State{}, nil, fmt.Errorf("reuse decision %s references an unavailable package node", decision.ID)
				}
				if err := validateReuseDecisionRecord(decision, pack, node); err != nil {
					return domain.State{}, nil, fmt.Errorf("reuse decision %s: %w", decision.ID, err)
				}
				if _, exists := state.ReuseDecisions[decision.ID]; exists {
					return domain.State{}, nil, fmt.Errorf("reuse decision %s is duplicated", decision.ID)
				}
				decision.Sequence = segment.Sequence
				state.ReuseDecisions[decision.ID] = decision
				state.PackageDecisions[decision.PackageID] = append(state.PackageDecisions[decision.PackageID], decision.ID)
			case "attempt.status-changed":
				var payload struct {
					AttemptID string `json:"attemptId"`
					Status    string `json:"status"`
					UpdatedAt string `json:"updatedAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, nil, err
				}
				attempt := state.Attempts[payload.AttemptID]
				attempt.Status = payload.Status
				attempt.UpdatedAt = payload.UpdatedAt
				state.Attempts[attempt.ID] = attempt
			case "attempt.finished":
				var payload struct {
					AttemptID    string                `json:"attemptId"`
					Outcome      string                `json:"outcome"`
					OutcomeClass string                `json:"outcomeClass"`
					Facts        domain.PredicateFacts `json:"facts"`
					UpdatedAt    string                `json:"updatedAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, nil, err
				}
				attempt := state.Attempts[payload.AttemptID]
				attempt.Status = "terminal"
				attempt.Outcome = payload.Outcome
				attempt.UpdatedAt = payload.UpdatedAt
				state.Attempts[attempt.ID] = attempt
				if payload.OutcomeClass == "retryable" {
					state.Nodes[attempt.NodeID] = domain.NodeRuntime{Status: "planned", Outcome: payload.Outcome, OutcomeClass: payload.OutcomeClass, Facts: payload.Facts}
				} else {
					state.Nodes[attempt.NodeID] = domain.NodeRuntime{Status: "terminal", Outcome: payload.Outcome, OutcomeClass: payload.OutcomeClass, Facts: payload.Facts}
				}
			case "resource.leased":
				var lease domain.ResourceLease
				if err := json.Unmarshal(event.Payload, &lease); err != nil {
					return domain.State{}, nil, err
				}
				state.Resources[lease.ID] = lease
			case "resource.released":
				var payload struct {
					ResourceID string `json:"resourceId"`
					ReleasedAt string `json:"releasedAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, nil, err
				}
				lease, ok := state.Resources[payload.ResourceID]
				if !ok {
					return domain.State{}, nil, fmt.Errorf("release references unknown resource lease %s", payload.ResourceID)
				}
				lease.Status, lease.ReleasedAt = "released", payload.ReleasedAt
				state.Resources[lease.ID] = lease
			case "action.applied":
				var action domain.ActionRecord
				if err := json.Unmarshal(event.Payload, &action); err != nil {
					return domain.State{}, nil, err
				}
				action.Sequence = segment.Sequence
				state.Actions[action.ID] = action
			case "effect.prepared":
				var effect domain.EffectAction
				if err := json.Unmarshal(event.Payload, &effect); err != nil {
					return domain.State{}, nil, err
				}
				effect.Sequence = segment.Sequence
				state.Effects[effect.ID] = effect
			case "effect.dispatched":
				var payload struct {
					ActionID     string `json:"actionId"`
					DispatchedAt string `json:"dispatchedAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, nil, err
				}
				effect, ok := state.Effects[payload.ActionID]
				if !ok {
					return domain.State{}, nil, fmt.Errorf("dispatch references unknown effect %s", payload.ActionID)
				}
				effect.Status, effect.UpdatedAt, effect.Sequence = "dispatched", payload.DispatchedAt, segment.Sequence
				state.Effects[payload.ActionID] = effect
			case "effect.reconciling":
				var payload struct {
					ActionID      string `json:"actionId"`
					ReconcilingAt string `json:"reconcilingAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, nil, err
				}
				effect, ok := state.Effects[payload.ActionID]
				if !ok {
					return domain.State{}, nil, fmt.Errorf("reconcile references unknown effect %s", payload.ActionID)
				}
				effect.Status, effect.UpdatedAt, effect.Sequence = "reconciling", payload.ReconcilingAt, segment.Sequence
				state.Effects[payload.ActionID] = effect
				if action, ok := state.Actions[payload.ActionID]; ok {
					action.Status = "reconciling"
					state.Actions[payload.ActionID] = action
				}
			case "effect.observed":
				var payload struct {
					ActionID  string          `json:"actionId"`
					Status    string          `json:"status"`
					Receipt   json.RawMessage `json:"receipt"`
					UpdatedAt string          `json:"updatedAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, nil, err
				}
				effect := state.Effects[payload.ActionID]
				effect.Status, effect.Receipt, effect.UpdatedAt, effect.Sequence = payload.Status, payload.Receipt, payload.UpdatedAt, segment.Sequence
				state.Effects[payload.ActionID] = effect
				if action, ok := state.Actions[payload.ActionID]; ok {
					action.Status = payload.Status
					state.Actions[payload.ActionID] = action
				}
			case "incident.opened":
				var incident domain.Incident
				if err := json.Unmarshal(event.Payload, &incident); err != nil {
					return domain.State{}, nil, err
				}
				state.Incidents[incident.ID] = incident
			case "incident.updated":
				var incident domain.Incident
				if err := json.Unmarshal(event.Payload, &incident); err != nil {
					return domain.State{}, nil, err
				}
				if _, ok := state.Incidents[incident.ID]; !ok {
					return domain.State{}, nil, fmt.Errorf("update references unknown incident %s", incident.ID)
				}
				state.Incidents[incident.ID] = incident
			case "incident.resolved":
				var payload struct {
					IncidentID string `json:"incidentId"`
					ResolvedAt string `json:"resolvedAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, nil, err
				}
				incident, ok := state.Incidents[payload.IncidentID]
				if !ok {
					return domain.State{}, nil, fmt.Errorf("resolve references unknown incident %s", payload.IncidentID)
				}
				incident.Status, incident.UpdatedAt = "resolved", payload.ResolvedAt
				state.Incidents[incident.ID] = incident
			default:
				return domain.State{}, nil, fmt.Errorf("unsupported journal event type %q at sequence %d", event.Type, segment.Sequence)
			}
		}
		if segment.Command.IdempotencyKey != "" {
			state.Commands[segment.Command.IdempotencyKey] = domain.CommandResult{Kind: segment.Command.Kind, GraphRevision: state.GraphRevision, Sequence: segment.Sequence}
		}
		state.HeadSequence = segment.Sequence
		state.HeadHash = segment.SegmentHash
	}
	return state, segments, nil
}

func decodeGraph(path string) (domain.GraphDefinition, error) {
	data, err := readDefinitionFile(path)
	if err != nil {
		return domain.GraphDefinition{}, err
	}
	return decodeGraphBytes(data)
}

func decodeGraphBytes(data []byte) (domain.GraphDefinition, error) {
	var graph domain.GraphDefinition
	if json.Valid(data) {
		if err := decodeStrictAuthorityJSON(data, &graph); err != nil {
			return graph, err
		}
		return graph, nil
	}
	var intermediate any
	if err := yaml.Unmarshal(data, &intermediate); err != nil {
		return graph, err
	}
	normalized, err := json.Marshal(intermediate)
	if err != nil {
		return graph, err
	}
	if len(normalized) > maxDefinitionBytes {
		return graph, fmt.Errorf("normalized definition input exceeds %d bytes", maxDefinitionBytes)
	}
	if err := decodeStrictAuthorityJSON(normalized, &graph); err != nil {
		return graph, err
	}
	return graph, nil
}

func graphRevision(graph domain.GraphDefinition) (string, error) {
	raw, err := json.Marshal(graph)
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("dagrail-graph-v1\x00"), canonical...))
	return hex.EncodeToString(sum[:]), nil
}

func GraphRevision(graph domain.GraphDefinition) (string, error) { return graphRevision(graph) }

func providerFingerprint(graph *domain.GraphDefinition) string {
	providers := []domain.ProviderRef{}
	if graph != nil {
		providers = append(providers, graph.Spec.Providers...)
	}
	raw, _ := json.Marshal(struct {
		Core      string               `json:"core"`
		Providers []domain.ProviderRef `json:"providers"`
	}{version.Version, providers})
	canonical, _ := jcs.Transform(raw)
	sum := sha256.Sum256(append([]byte("dagrail-provider-set-v1\x00"), canonical...))
	return hex.EncodeToString(sum[:])
}

func (s *Service) validateGraphProviders(graph domain.GraphDefinition) error {
	declared := map[string]domain.ProviderRef{}
	for _, provider := range graph.Spec.Providers {
		declared[provider.ID] = provider
		if !s.Providers.Matches(sdk.Metadata{ID: provider.ID, Version: provider.Version, SchemaHash: provider.SchemaHash}) {
			return fmt.Errorf("graph requires unavailable provider %s@%s with schema %s", provider.ID, provider.Version, provider.SchemaHash)
		}
	}
	for _, node := range graph.Spec.Nodes {
		if domain.IsBuiltinNodeKind(node.Kind) {
			continue
		}
		provider, ok := s.Providers.NodeKind(node.Kind)
		if !ok {
			return fmt.Errorf("custom node kind provider %s is not compiled into this DAGrail distribution", node.Kind)
		}
		metadata := provider.Metadata()
		ref, ok := declared[node.Kind]
		if !ok || ref.Version != metadata.Version || ref.SchemaHash != metadata.SchemaHash {
			return fmt.Errorf("custom node kind %s must bind its compiled provider version and schema hash", node.Kind)
		}
		outcomes := make([]sdk.OutcomeDefinition, 0, len(node.Outcomes))
		for _, outcome := range node.Outcomes {
			outcomes = append(outcomes, sdk.OutcomeDefinition{ID: outcome.ID, Class: outcome.Class})
		}
		if err := s.ProviderRuntime.ValidateNodeKind(node.Kind, node.Inputs, outcomes); err != nil {
			return fmt.Errorf("custom node %s: %w", node.ID, err)
		}
	}
	return nil
}
