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
	Project                       project.Project
	Journal                       *journal.Store
	Projection                    *projection.Store
	Providers                     *internalproviders.Registry
	ProviderRuntime               *internalproviders.Runtime
	Now                           func() time.Time
	ConfirmLocator                func(string) error
	beforeLegacyAuthoritySnapshot func()
	recoveryInspection            bool
}

func Open(root string) (*Service, error) {
	return open(root, false, true)
}

// OpenForRecovery opens existing authority and projection state without automatic
// node settlement, projection migration, repair, or synchronization.
func OpenForRecovery(root string) (*Service, error) {
	return open(root, true, false)
}

// OpenForMigration opens writable local projections without running derived
// automatic Node settlement before a migration's expected-head validation.
func OpenForMigration(root string) (*Service, error) {
	return open(root, false, false)
}

func open(root string, recoveryInspection, settle bool) (*Service, error) {
	p, err := project.Open(root)
	if err != nil {
		return nil, err
	}
	var j *journal.Store
	if recoveryInspection {
		j, err = journal.OpenRecovery(p.DataDir, p.Config.ProjectID)
	} else {
		j, err = journal.Open(p.DataDir, p.Config.ProjectID)
	}
	if err != nil {
		return nil, err
	}
	var projectionStore *projection.Store
	if recoveryInspection {
		projectionStore, err = projection.Inspect(p.DataDir)
	} else {
		projectionStore, err = projection.Open(p.DataDir)
	}
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
	service := &Service{Project: p, Journal: j, Projection: projectionStore, Providers: registry, ProviderRuntime: internalproviders.NewRuntime(registry), Now: time.Now, ConfirmLocator: project.SyncProjectLocator, recoveryInspection: recoveryInspection}
	if recoveryInspection {
		return service, nil
	}
	if settle {
		if err := service.settleAutomatic(); err != nil {
			return nil, err
		}
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
	if _, err := project.InitWithInitializer(root, name, func(prepared project.Project, existing bool) error {
		store, err := journal.OpenForAuthorityEstablishment(prepared.DataDir, prepared.Config.ProjectID)
		if err != nil {
			return err
		}
		segments, err := store.ReadAll()
		if err != nil {
			return err
		}
		if len(segments) != 0 {
			return validateCurrentAuthority(prepared, segments)
		}
		if existing {
			return fmt.Errorf("existing claimed authority is missing its schema-4 establishment fence")
		}
		establishedAt := time.Now().UTC()
		provenance, err := authorityDigest("dagrail-initial-authority-v1\x00", prepared.Config)
		if err != nil {
			return err
		}
		establishment := authorityEstablishment{APIVersion: "dagrail.io/authority-establishment/v1alpha1", Kind: "AuthorityEstablishment", ProjectID: prepared.Config.ProjectID, Operation: "initialization", EstablishedAt: establishedAt.Format(time.RFC3339Nano), ProvenanceDigest: provenance}
		raw, err := json.Marshal(establishment)
		if err != nil {
			return err
		}
		raw, err = jcs.Transform(raw)
		if err != nil {
			return err
		}
		_, err = store.EstablishAuthority(raw, establishedAt)
		return err
	}); err != nil {
		return nil, err
	}
	return Open(root)
}

func validateCurrentAuthority(authority project.Project, segments []journal.Segment) error {
	projectID := authority.Config.ProjectID
	if len(segments) == 0 {
		return fmt.Errorf("existing project initialization fence is missing")
	}
	segment := segments[0]
	if segment.SchemaVersion != journal.AuthorityFenceSchemaVersion || segment.Sequence != 1 || segment.PreviousHash != "" || segment.Command.Kind != "authority.establish" || len(segment.Events) != 1 || segment.Events[0].Type != "authority.established" {
		return fmt.Errorf("existing project initialization fence is invalid")
	}
	var establishment authorityEstablishment
	if err := decodeStrictAuthorityJSON(segment.Events[0].Payload, &establishment); err != nil || validateAuthorityEstablishment(establishment, projectID) != nil || establishment.EstablishedAt != segment.CommittedAt {
		return fmt.Errorf("existing project initialization fence is not bound to this authority")
	}
	var expectedProvenance string
	var provenanceErr error
	switch establishment.Operation {
	case "initialization":
		expectedProvenance, provenanceErr = authorityDigest("dagrail-initial-authority-v1\x00", authority.Config)
	case "rotation", "legacy-adoption", "relocation":
		lineage, lineageErr := project.ReadAuthorityLineage(authority.DataDir)
		if lineageErr != nil || lineage.Operation != establishment.Operation || lineage.PreviousProjectID != establishment.PreviousProjectID {
			return fmt.Errorf("replacement authority establishment is not claim-bound to its lineage")
		}
		retirement := authorityRetirement{
			PreviousProjectID: lineage.PreviousProjectID, PreviousHead: lineage.PreviousHead,
			PreviousLocatorID: lineage.PreviousLocatorID, TargetRootDigest: lineage.TargetRootDigest, DestinationRootDigest: lineage.DestinationRootDigest,
			RecoveryHead: lineage.RecoveryHead, RecoveryBackupDigest: lineage.RecoveryBackupDigest,
			ReplacementProjectID: projectID, RotatedAt: lineage.RotatedAt,
			Reason: lineage.Reason, IdempotencyKey: lineage.IdempotencyKey,
		}
		if establishment.Operation == "rotation" {
			retirement.APIVersion, retirement.Kind = authorityRetirementAPIVersion, rotationRetirementKind
		} else if establishment.Operation == "legacy-adoption" {
			retirement.APIVersion, retirement.Kind = AuthorityAdoptionAPIVersion, legacyRetirementKind
		} else {
			retirement.APIVersion, retirement.Kind = AuthorityRelocationAPIVersion, relocationRetirementKind
		}
		raw, marshalErr := json.Marshal(retirement)
		if marshalErr != nil {
			return marshalErr
		}
		raw, marshalErr = jcs.Transform(raw)
		if marshalErr != nil {
			return marshalErr
		}
		sum := sha256.Sum256(raw)
		expectedProvenance = "sha256:" + hex.EncodeToString(sum[:])
	}
	if provenanceErr != nil || establishment.ProvenanceDigest != expectedProvenance {
		return fmt.Errorf("existing project initialization provenance is not bound to this authority")
	}
	state, err := reduceSegments(projectID, segments)
	if err != nil {
		return err
	}
	if state.HeadSequence != segments[len(segments)-1].Sequence {
		return fmt.Errorf("existing project initialization has unexpected runtime state")
	}
	return nil
}

func (s *Service) ImportGraph(path, idempotencyKey, actorRole string) (domain.CommandResult, error) {
	return s.ImportGraphWithProvenance(path, idempotencyKey, actorRole, nil)
}

func (s *Service) ImportGraphWithProvenance(path, idempotencyKey, actorRole string, source any) (domain.CommandResult, error) {
	if idempotencyKey == "" {
		return domain.CommandResult{}, fmt.Errorf("idempotency key is required")
	}
	graph, err := decodeGraph(path)
	if err != nil {
		return domain.CommandResult{}, err
	}
	sourceRaw := []byte("null")
	if source != nil {
		sourceRaw, err = json.Marshal(source)
		if err != nil {
			return domain.CommandResult{}, err
		}
		if err := domain.ValidateAuthorityJSON(sourceRaw); err != nil {
			return domain.CommandResult{}, fmt.Errorf("graph provenance: %w", err)
		}
		if err := domain.RejectSensitiveFields(sourceRaw); err != nil {
			return domain.CommandResult{}, fmt.Errorf("graph provenance: %w", err)
		}
	}
	requestDigest, err := authorityRequestDigest("graph.import", sourceRaw)
	if err != nil {
		return domain.CommandResult{}, err
	}
	return s.importGraphDefinition(graph, idempotencyKey, actorRole, source, requestDigest)
}

func (s *Service) importGraphDefinition(graph domain.GraphDefinition, idempotencyKey, actorRole string, source any, requestDigest string) (domain.CommandResult, error) {
	if idempotencyKey == "" {
		return domain.CommandResult{}, fmt.Errorf("idempotency key is required")
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
	state, _, err := s.load()
	if err != nil {
		return domain.CommandResult{}, err
	}
	if existing, ok := state.Commands[idempotencyKey]; ok {
		if existing.Kind != "graph.import" || existing.ActorRole != actorRole || existing.ObjectRef != "graph:"+revision || (existing.RequestDigest != "" && existing.RequestDigest != requestDigest) {
			return domain.CommandResult{}, fmt.Errorf("idempotency key is already bound to another command")
		}
		return existing, nil
	}
	if state.Graph != nil {
		return domain.CommandResult{}, fmt.Errorf("graph is already imported; use a graph change")
	}
	payload, _ := json.Marshal(struct {
		Graph    domain.GraphDefinition `json:"graph"`
		Revision string                 `json:"revision"`
		Source   any                    `json:"source,omitempty"`
	}{graph, revision, source})
	expectedHead := state.HeadHash
	segment, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "graph.import", ActorRole: actorRole, IdempotencyKey: idempotencyKey, ObjectRef: "graph:" + revision, RequestDigest: requestDigest}, []journal.Event{{Type: "graph.imported", Payload: payload}}, s.Now(), &expectedHead)
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
	if !s.recoveryInspection {
		if err := validateCurrentAuthority(s.Project, segments); err != nil {
			return domain.State{}, nil, err
		}
	}
	state, err := reduceSegments(s.Project.Config.ProjectID, segments)
	if err != nil {
		return domain.State{}, nil, err
	}
	return state, segments, nil
}

// reduceSegments is the one replay path for both committed authority and a
// lifecycle migration preflight. A migration is never appended until the exact
// candidate segment has reduced successfully through this function.
func reduceSegments(projectID string, segments []journal.Segment) (domain.State, error) {
	state := domain.NewState(projectID)
	retired := false
	established := false
	for _, segment := range segments {
		if retired {
			return domain.State{}, fmt.Errorf("journal contains a command after authority retirement at sequence %d", segment.Sequence)
		}
		for _, storedEvent := range segment.Events {
			event, err := journal.UpcastEvent(segment.SchemaVersion, storedEvent)
			if err != nil {
				return domain.State{}, fmt.Errorf("upcast journal event at sequence %d: %w", segment.Sequence, err)
			}
			switch event.Type {
			case "authority.established":
				if established || segment.Sequence != 1 || segment.PreviousHash != "" || len(segment.Events) != 1 || segment.Command.Kind != "authority.establish" || segment.Command.ActorRole != "dagrail.recovery" || segment.Command.ObjectRef != "project:"+projectID {
					return domain.State{}, fmt.Errorf("authority establishment is not a closed bootstrap command at sequence %d", segment.Sequence)
				}
				var establishment authorityEstablishment
				if err := decodeStrictAuthorityJSON(event.Payload, &establishment); err != nil || validateAuthorityEstablishment(establishment, projectID) != nil || establishment.EstablishedAt != segment.CommittedAt {
					return domain.State{}, fmt.Errorf("authority establishment event is invalid at sequence %d", segment.Sequence)
				}
				sum := sha256.Sum256(event.Payload)
				hexDigest := hex.EncodeToString(sum[:])
				if segment.Command.ID != "authority-establish-"+hex.EncodeToString(sum[:16]) || segment.Command.IdempotencyKey != "authority-establish/"+hexDigest || segment.Command.RequestDigest != "sha256:"+hexDigest {
					return domain.State{}, fmt.Errorf("authority establishment command binding is invalid at sequence %d", segment.Sequence)
				}
				established = true
			case "authority.retired":
				if len(segment.Events) != 1 || segment.Command.Kind != "authority.retire" || segment.Command.ActorRole != "dagrail.recovery" || segment.Command.ObjectRef != "project:"+projectID {
					return domain.State{}, fmt.Errorf("authority retirement fence is not a closed command at sequence %d", segment.Sequence)
				}
				var retirement authorityRetirement
				if err := decodeStrictAuthorityJSON(event.Payload, &retirement); err != nil {
					return domain.State{}, fmt.Errorf("authority retirement event is invalid at sequence %d: %w", segment.Sequence, err)
				}
				if err := validateAnyRetirement(retirement); err != nil || retirement.PreviousProjectID != projectID || retirement.PreviousHead != segment.PreviousHash || retirement.RotatedAt != segment.CommittedAt {
					return domain.State{}, fmt.Errorf("authority retirement event is invalid at sequence %d", segment.Sequence)
				}
				sum := sha256.Sum256(event.Payload)
				hexDigest := hex.EncodeToString(sum[:])
				if segment.Command.ID != "authority-retire-"+hex.EncodeToString(sum[:16]) || segment.Command.IdempotencyKey != "authority-retire/"+hexDigest || segment.Command.RequestDigest != "sha256:"+hexDigest {
					return domain.State{}, fmt.Errorf("authority retirement command binding is invalid at sequence %d", segment.Sequence)
				}
				retired = true
			case "lifecycle.history-imported":
				var receipt domain.LifecycleMigrationReceipt
				if err := json.Unmarshal(event.Payload, &receipt); err != nil {
					return domain.State{}, err
				}
				if receipt.ID == "" || receipt.GraphRevision != state.GraphRevision || receipt.TargetSequence != segment.Sequence || receipt.RecordCount <= 0 || receipt.NativeEventCount <= 0 {
					return domain.State{}, fmt.Errorf("lifecycle migration receipt is invalid at sequence %d", segment.Sequence)
				}
				if _, exists := state.LifecycleMigrations[receipt.ID]; exists {
					return domain.State{}, fmt.Errorf("lifecycle migration %s is duplicated", receipt.ID)
				}
				state.LifecycleMigrations[receipt.ID] = receipt
			case "graph.imported":
				var payload struct {
					Graph    domain.GraphDefinition `json:"graph"`
					Revision string                 `json:"revision"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, err
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
					return domain.State{}, err
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
					return domain.State{}, err
				}
				state.Leases[lease.RoleID] = lease
			case "role.released":
				var payload struct {
					RoleID string `json:"roleId"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, err
				}
				lease := state.Leases[payload.RoleID]
				lease.Active = false
				state.Leases[payload.RoleID] = lease
			case "attempt.started", "attempt.leased":
				var attempt domain.Attempt
				if err := json.Unmarshal(event.Payload, &attempt); err != nil {
					return domain.State{}, err
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
					return domain.State{}, err
				}
				runtime, ok := state.Nodes[payload.NodeID]
				if !ok || runtime.Status != "planned" {
					return domain.State{}, fmt.Errorf("automatic completion references non-planned node %s", payload.NodeID)
				}
				state.Nodes[payload.NodeID] = domain.NodeRuntime{Status: "terminal", Outcome: payload.Outcome, OutcomeClass: "success"}
			case "node.auto-skipped":
				var payload struct {
					NodeID    string `json:"nodeId"`
					Reason    string `json:"reason"`
					SkippedAt string `json:"skippedAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, err
				}
				runtime, ok := state.Nodes[payload.NodeID]
				if !ok || runtime.Status != "planned" {
					return domain.State{}, fmt.Errorf("automatic skip references non-planned node %s", payload.NodeID)
				}
				state.Nodes[payload.NodeID] = domain.NodeRuntime{Status: "skipped", Outcome: payload.Reason, OutcomeClass: "cancelled"}
			case "attempt.checkpointed":
				var checkpoint domain.Checkpoint
				if err := json.Unmarshal(event.Payload, &checkpoint); err != nil {
					return domain.State{}, err
				}
				state.Checkpoints[checkpoint.ID] = checkpoint
				attempt := state.Attempts[checkpoint.AttemptID]
				attempt.CheckpointID = checkpoint.ID
				attempt.UpdatedAt = checkpoint.CreatedAt
				state.Attempts[attempt.ID] = attempt
			case "evidence.package-published":
				var pack domain.ExecutionPackage
				if err := json.Unmarshal(event.Payload, &pack); err != nil {
					return domain.State{}, err
				}
				attempt, ok := state.Attempts[pack.AttemptID]
				node, nodeExists := state.NodeDefinition(pack.NodeID)
				if !ok || !nodeExists || attempt.NodeID != pack.NodeID || pack.ProjectID != state.ProjectID || pack.GraphRevision != state.GraphRevision {
					return domain.State{}, fmt.Errorf("execution package %s references an invalid attempt", pack.ID)
				}
				if err := validateExecutionPackageRecord(pack, node); err != nil {
					return domain.State{}, fmt.Errorf("execution package %s: %w", pack.ID, err)
				}
				if _, exists := state.EvidencePackages[pack.ID]; exists {
					return domain.State{}, fmt.Errorf("execution package %s is duplicated", pack.ID)
				}
				pack.Sequence = segment.Sequence
				state.EvidencePackages[pack.ID] = pack
				state.AttemptPackages[pack.AttemptID] = append(state.AttemptPackages[pack.AttemptID], pack.ID)
			case "evidence.reuse-assessed":
				var decision domain.ReuseDecision
				if err := json.Unmarshal(event.Payload, &decision); err != nil {
					return domain.State{}, err
				}
				if _, exists := state.EvidencePackages[decision.PackageID]; !exists {
					return domain.State{}, fmt.Errorf("reuse decision %s references unknown package %s", decision.ID, decision.PackageID)
				}
				if _, exists := state.Attempts[decision.AssessedByAttempt]; !exists {
					return domain.State{}, fmt.Errorf("reuse decision %s references unknown assessor attempt", decision.ID)
				}
				pack := state.EvidencePackages[decision.PackageID]
				node, exists := state.NodeDefinition(pack.NodeID)
				if !exists {
					return domain.State{}, fmt.Errorf("reuse decision %s references an unavailable package node", decision.ID)
				}
				if err := validateReuseDecisionRecord(decision, pack, node); err != nil {
					return domain.State{}, fmt.Errorf("reuse decision %s: %w", decision.ID, err)
				}
				if _, exists := state.ReuseDecisions[decision.ID]; exists {
					return domain.State{}, fmt.Errorf("reuse decision %s is duplicated", decision.ID)
				}
				decision.Sequence = segment.Sequence
				state.ReuseDecisions[decision.ID] = decision
				state.PackageDecisions[decision.PackageID] = append(state.PackageDecisions[decision.PackageID], decision.ID)
			case "decision.recorded":
				var decision domain.DecisionRecord
				if err := json.Unmarshal(event.Payload, &decision); err != nil {
					return domain.State{}, err
				}
				if err := validateDecisionRecord(decision, state); err != nil {
					return domain.State{}, fmt.Errorf("decision record %s: %w", decision.ID, err)
				}
				if _, exists := state.Decisions[decision.ID]; exists {
					return domain.State{}, fmt.Errorf("decision record %s is duplicated", decision.ID)
				}
				decision.Sequence = segment.Sequence
				state.Decisions[decision.ID] = decision
				state.AttemptDecisions[decision.AttemptID] = append(state.AttemptDecisions[decision.AttemptID], decision.ID)
			case "attempt.status-changed":
				var payload struct {
					AttemptID string `json:"attemptId"`
					Status    string `json:"status"`
					UpdatedAt string `json:"updatedAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, err
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
					return domain.State{}, err
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
					return domain.State{}, err
				}
				state.Resources[lease.ID] = lease
			case "resource.closure-observed":
				var payload struct {
					ResourceID string          `json:"resourceId"`
					Status     string          `json:"status"`
					Receipt    json.RawMessage `json:"receipt"`
					UpdatedAt  string          `json:"updatedAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, err
				}
				lease, ok := state.Resources[payload.ResourceID]
				if !ok || lease.Status != "active" || (payload.Status != "confirmed" && payload.Status != "failed" && payload.Status != "unknown") {
					return domain.State{}, fmt.Errorf("closure observation references invalid resource lease %s", payload.ResourceID)
				}
				lease.ClosureStatus, lease.ClosureReceipt, lease.ClosureUpdatedAt = payload.Status, payload.Receipt, payload.UpdatedAt
				state.Resources[lease.ID] = lease
			case "resource.released":
				var payload struct {
					ResourceID string `json:"resourceId"`
					ReleasedAt string `json:"releasedAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, err
				}
				lease, ok := state.Resources[payload.ResourceID]
				if !ok {
					return domain.State{}, fmt.Errorf("release references unknown resource lease %s", payload.ResourceID)
				}
				if lease.ClosureStatus != "" && lease.ClosureStatus != "confirmed" {
					return domain.State{}, fmt.Errorf("resource lease %s cannot release without a confirmed closure", payload.ResourceID)
				}
				lease.Status, lease.ReleasedAt = "released", payload.ReleasedAt
				state.Resources[lease.ID] = lease
			case "action.applied":
				var action domain.ActionRecord
				if err := json.Unmarshal(event.Payload, &action); err != nil {
					return domain.State{}, err
				}
				action.Sequence = segment.Sequence
				state.Actions[action.ID] = action
			case "effect.prepared":
				var effect domain.EffectAction
				if err := json.Unmarshal(event.Payload, &effect); err != nil {
					return domain.State{}, err
				}
				effect.Sequence = segment.Sequence
				state.Effects[effect.ID] = effect
			case "effect.dispatched":
				var payload struct {
					ActionID     string `json:"actionId"`
					DispatchedAt string `json:"dispatchedAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, err
				}
				effect, ok := state.Effects[payload.ActionID]
				if !ok {
					return domain.State{}, fmt.Errorf("dispatch references unknown effect %s", payload.ActionID)
				}
				effect.Status, effect.UpdatedAt, effect.Sequence = "dispatched", payload.DispatchedAt, segment.Sequence
				state.Effects[payload.ActionID] = effect
			case "effect.reconciling":
				var payload struct {
					ActionID      string `json:"actionId"`
					ReconcilingAt string `json:"reconcilingAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, err
				}
				effect, ok := state.Effects[payload.ActionID]
				if !ok {
					return domain.State{}, fmt.Errorf("reconcile references unknown effect %s", payload.ActionID)
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
					return domain.State{}, err
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
					return domain.State{}, err
				}
				state.Incidents[incident.ID] = incident
			case "incident.updated":
				var incident domain.Incident
				if err := json.Unmarshal(event.Payload, &incident); err != nil {
					return domain.State{}, err
				}
				if _, ok := state.Incidents[incident.ID]; !ok {
					return domain.State{}, fmt.Errorf("update references unknown incident %s", incident.ID)
				}
				state.Incidents[incident.ID] = incident
			case "incident.resolved":
				var payload struct {
					IncidentID string `json:"incidentId"`
					ResolvedAt string `json:"resolvedAt"`
				}
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					return domain.State{}, err
				}
				incident, ok := state.Incidents[payload.IncidentID]
				if !ok {
					return domain.State{}, fmt.Errorf("resolve references unknown incident %s", payload.IncidentID)
				}
				incident.Status, incident.UpdatedAt = "resolved", payload.ResolvedAt
				state.Incidents[incident.ID] = incident
			default:
				return domain.State{}, fmt.Errorf("unsupported journal event type %q at sequence %d", event.Type, segment.Sequence)
			}
		}
		if segment.Command.IdempotencyKey != "" {
			requestDigest := segment.Command.RequestDigest
			if requestDigest == "" {
				requestDigest = commandRequestDigest(segment)
			}
			objectRef := segment.Command.ObjectRef
			if objectRef == "" {
				objectRef = commandObjectRef(segment, state)
			}
			state.Commands[segment.Command.IdempotencyKey] = domain.CommandResult{Kind: segment.Command.Kind, ActorRole: segment.Command.ActorRole, ObjectRef: objectRef, RequestDigest: requestDigest, GraphRevision: state.GraphRevision, Sequence: segment.Sequence}
		}
		state.HeadSequence = segment.Sequence
		state.HeadHash = segment.SegmentHash
	}
	return state, nil
}

func commandObjectRef(segment journal.Segment, state domain.State) string {
	for _, event := range segment.Events {
		switch event.Type {
		case "action.applied":
			var value domain.ActionRecord
			if json.Unmarshal(event.Payload, &value) == nil && value.ID != "" {
				return "action:" + value.ID
			}
		case "effect.observed", "effect.dispatched", "effect.reconciling":
			var value struct {
				ActionID string `json:"actionId"`
			}
			if json.Unmarshal(event.Payload, &value) == nil && value.ActionID != "" {
				return "effect:" + value.ActionID
			}
		case "role.bound":
			var value domain.RoleLease
			if json.Unmarshal(event.Payload, &value) == nil && value.RoleID != "" {
				return "role:" + value.RoleID
			}
		case "role.released":
			var value struct {
				RoleID string `json:"roleId"`
			}
			if json.Unmarshal(event.Payload, &value) == nil && value.RoleID != "" {
				return "role:" + value.RoleID
			}
		case "incident.updated":
			var value domain.Incident
			if json.Unmarshal(event.Payload, &value) == nil && value.ID != "" {
				return "incident:" + value.ID
			}
		}
	}
	if segment.Command.Kind == "graph.import" || segment.Command.Kind == "graph.change" {
		return "graph:" + state.GraphRevision
	}
	return ""
}

func commandRequestDigest(segment journal.Segment) string {
	for _, event := range segment.Events {
		if event.Type != "graph.imported" {
			continue
		}
		var value struct {
			Source struct {
				InputDigest string `json:"inputDigest"`
			} `json:"source"`
		}
		if json.Unmarshal(event.Payload, &value) == nil {
			return value.Source.InputDigest
		}
	}
	return ""
}

func authorityRequestDigest(namespace string, raw []byte) (string, error) {
	if len(raw) == 0 {
		raw = []byte("null")
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("dagrail-request-v1\x00"+namespace+"\x00"), canonical...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
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
		if node.Decision != nil && node.Decision.Source == "provider" {
			provider, ok := s.Providers.Policy(node.Decision.ProviderID)
			if !ok {
				return fmt.Errorf("policy provider %s is not compiled into this DAGrail distribution", node.Decision.ProviderID)
			}
			metadata := provider.Metadata()
			ref, declaredOK := declared[node.Decision.ProviderID]
			if !declaredOK || ref.Version != metadata.Version || ref.SchemaHash != metadata.SchemaHash {
				return fmt.Errorf("gate %s must bind policy provider %s version and schema hash", node.ID, node.Decision.ProviderID)
			}
		}
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
