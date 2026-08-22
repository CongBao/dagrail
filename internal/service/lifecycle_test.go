package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/sdk"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type leaseAdvancingEffectAdapter struct {
	advance    func()
	dispatched *bool
}

type leaseAdvancingPolicy struct {
	metadata sdk.Metadata
	advance  func()
}

type serializedObservationAdapter struct {
	dispatchEntered  chan struct{}
	releaseDispatch  chan struct{}
	reconcileEntered chan struct{}
}

type incidentSerializedEffectAdapter struct {
	reconcileEntered chan struct{}
	releaseReconcile chan struct{}
	reconcileStatus  string
}

func (serializedObservationAdapter) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "serialized-observation", Version: "1.0.0", SchemaHash: digest("8")}
}
func (serializedObservationAdapter) Prepare(_ context.Context, _ sdk.EffectRequest) (sdk.PreparedEffect, error) {
	return sdk.PreparedEffect{AdapterID: "serialized-observation", Binding: json.RawMessage(`true`)}, nil
}
func (adapter serializedObservationAdapter) Dispatch(_ context.Context, _ sdk.EffectRequest, _ sdk.PreparedEffect) (sdk.EffectReceipt, error) {
	close(adapter.dispatchEntered)
	<-adapter.releaseDispatch
	return sdk.EffectReceipt{Status: "confirmed"}, nil
}
func (adapter serializedObservationAdapter) Reconcile(_ context.Context, _ sdk.EffectRequest, _ sdk.PreparedEffect, _ json.RawMessage) (sdk.EffectReceipt, error) {
	close(adapter.reconcileEntered)
	return sdk.EffectReceipt{Status: "unknown"}, nil
}

func (incidentSerializedEffectAdapter) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "incident-serialized-effect", Version: "1.0.0", SchemaHash: digest("7")}
}
func (incidentSerializedEffectAdapter) Prepare(_ context.Context, _ sdk.EffectRequest) (sdk.PreparedEffect, error) {
	return sdk.PreparedEffect{AdapterID: "incident-serialized-effect", Binding: json.RawMessage(`true`)}, nil
}
func (incidentSerializedEffectAdapter) Dispatch(_ context.Context, _ sdk.EffectRequest, _ sdk.PreparedEffect) (sdk.EffectReceipt, error) {
	return sdk.EffectReceipt{Status: "unknown"}, nil
}
func (adapter incidentSerializedEffectAdapter) Reconcile(_ context.Context, _ sdk.EffectRequest, _ sdk.PreparedEffect, _ json.RawMessage) (sdk.EffectReceipt, error) {
	close(adapter.reconcileEntered)
	<-adapter.releaseReconcile
	return sdk.EffectReceipt{Status: adapter.reconcileStatus}, nil
}

func (provider leaseAdvancingPolicy) Metadata() sdk.Metadata { return provider.metadata }
func (leaseAdvancingPolicy) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["policyId","input"],"properties":{"policyId":{"type":"string"},"input":{"type":"object"},"evidence":{"type":"array"}}}`)
}
func (provider leaseAdvancingPolicy) Decide(_ context.Context, _ sdk.PolicyRequest) (sdk.PolicyDecision, error) {
	provider.advance()
	return sdk.PolicyDecision{Outcome: "pass"}, nil
}

func (leaseAdvancingEffectAdapter) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "lease-advancing", Version: "1.0.0", SchemaHash: digest("9")}
}

func (adapter leaseAdvancingEffectAdapter) Prepare(_ context.Context, _ sdk.EffectRequest) (sdk.PreparedEffect, error) {
	adapter.advance()
	return sdk.PreparedEffect{AdapterID: "lease-advancing", Binding: json.RawMessage(`true`)}, nil
}

func (adapter leaseAdvancingEffectAdapter) Dispatch(_ context.Context, _ sdk.EffectRequest, _ sdk.PreparedEffect) (sdk.EffectReceipt, error) {
	*adapter.dispatched = true
	return sdk.EffectReceipt{Status: "confirmed"}, nil
}

func (leaseAdvancingEffectAdapter) Reconcile(_ context.Context, _ sdk.EffectRequest, _ sdk.PreparedEffect, _ json.RawMessage) (sdk.EffectReceipt, error) {
	return sdk.EffectReceipt{Status: "unknown"}, nil
}

func TestLifecycleHistoryImportIsAtomicIdempotentAndProjectable(t *testing.T) {
	svc := lifecycleService(t)
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	manifest := lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	validateLifecycleMigrationSchema(t, manifest)
	validation, err := svc.ValidateLifecycleMigration(manifest, manifest.Source.AuthorityHash)
	if err != nil || !validation.Valid || validation.RecordCount != 4 || validation.NativeEventCount != 8 {
		t.Fatalf("migration validation = %+v, %v", validation, err)
	}
	receipt, err := svc.ImportLifecycleHistory(manifest, manifest.Source.AuthorityHash, "migration-admin", "history/import-1")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ID != validation.MigrationID || receipt.TargetSequence != 3 || receipt.SourceHeadSequence != 4 {
		t.Fatalf("unexpected migration receipt: %+v", receipt)
	}
	retried, err := svc.ImportLifecycleHistory(manifest, manifest.Source.AuthorityHash, "migration-admin", "history/import-1")
	if err != nil || retried.ID != receipt.ID || retried.TargetSequence != receipt.TargetSequence {
		t.Fatalf("idempotent retry changed the result: %+v %v", retried, err)
	}
	projection, err := svc.LifecycleProjection()
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Migrations) != 1 || len(projection.Attempts) != 1 || len(projection.Nodes) != 2 || projection.HeadSequence != 4 || projection.Nodes[0].Status != "terminal" || projection.Nodes[1].Status != "terminal" || projection.ProjectionDigest == "" {
		t.Fatalf("unexpected lifecycle projection: %+v", projection)
	}
	validateLifecycleProjectionSchema(t, projection)
	before := projection.ProjectionDigest
	if err := svc.RebuildProjection(); err != nil {
		t.Fatal(err)
	}
	after, err := svc.LifecycleProjection()
	if err != nil || after.ProjectionDigest != before {
		t.Fatalf("projection changed after rebuild: %s %s %v", before, after.ProjectionDigest, err)
	}
}

func TestLifecycleV1Beta1ImportsMultipleClosedCommandsFromOneSourceRecord(t *testing.T) {
	svc := lifecycleService(t)
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	manifest := lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	manifest.APIVersion = LifecycleMigrationBundleAPIVersion
	manifest.Records = []LifecycleMigrationRecord{
		{SourceEventID: manifest.Records[0].SourceEventID, OccurredAt: manifest.Records[0].OccurredAt, Commands: []LifecycleMigrationCommand{{CommandIndex: 1, Events: manifest.Records[0].Events}}},
		{SourceEventID: manifest.Records[1].SourceEventID, OccurredAt: manifest.Records[1].OccurredAt, Commands: []LifecycleMigrationCommand{{CommandIndex: 1, Events: manifest.Records[1].Events}, {CommandIndex: 2, Events: manifest.Records[2].Events}}},
		{SourceEventID: manifest.Records[3].SourceEventID, OccurredAt: manifest.Records[3].OccurredAt, Commands: []LifecycleMigrationCommand{{CommandIndex: 1, Events: manifest.Records[3].Events}}},
	}
	sealLifecycleManifest(t, &manifest)
	validateLifecycleMigrationSchema(t, manifest)
	validation, err := svc.ValidateLifecycleMigration(manifest, manifest.Source.AuthorityHash)
	if err != nil || !validation.Valid || validation.RecordCount != 3 || validation.NativeEventCount != 8 {
		t.Fatalf("bundle validation = %+v, %v", validation, err)
	}
	if _, err := svc.ImportLifecycleHistory(manifest, manifest.Source.AuthorityHash, "migration-admin", "history/bundle-1"); err != nil {
		t.Fatal(err)
	}
	projection, err := svc.LifecycleProjection()
	if err != nil || len(projection.Actions) != 3 || projection.Nodes[0].Status != "terminal" {
		t.Fatalf("bundle projection = %+v, %v", projection, err)
	}
}

func TestLifecycleHistoryImportBootstrapsAfterLegacyIdentityReplacement(t *testing.T) {
	for _, apiVersion := range []string{LifecycleMigrationAPIVersion, LifecycleMigrationBundleAPIVersion} {
		t.Run(apiVersion, func(t *testing.T) {
			svc := adoptedLifecycleService(t)
			state, err := svc.State()
			if err != nil {
				t.Fatal(err)
			}
			manifest := lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
			if apiVersion == LifecycleMigrationBundleAPIVersion {
				manifest.APIVersion = apiVersion
				for index := range manifest.Records {
					manifest.Records[index].Commands = []LifecycleMigrationCommand{{CommandIndex: 1, Events: manifest.Records[index].Events}}
					manifest.Records[index].Events = nil
				}
				sealLifecycleManifest(t, &manifest)
			}
			validation, err := svc.ValidateLifecycleMigration(manifest, manifest.Source.AuthorityHash)
			if err != nil || !validation.Valid {
				t.Fatalf("replacement authority migration validation = %+v, %v", validation, err)
			}
			if _, err := svc.ImportLifecycleHistory(manifest, manifest.Source.AuthorityHash, "migration-admin", "history/adopted-"+apiVersion); err != nil {
				t.Fatalf("replacement authority migration import: %v", err)
			}
		})
	}
}

func TestLifecycleV1Beta1KeepsCommandProofLedgersIndependent(t *testing.T) {
	svc := lifecycleService(t)
	state, _ := svc.State()
	manifest := lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	manifest.APIVersion = LifecycleMigrationBundleAPIVersion
	combined := append(append([]LifecycleMigrationEvent{}, manifest.Records[1].Events...), manifest.Records[2].Events...)
	manifest.Records = []LifecycleMigrationRecord{
		{SourceEventID: "source-1", OccurredAt: manifest.Records[0].OccurredAt, Commands: []LifecycleMigrationCommand{{CommandIndex: 1, Events: manifest.Records[0].Events}}},
		{SourceEventID: "source-2", OccurredAt: manifest.Records[1].OccurredAt, Commands: []LifecycleMigrationCommand{{CommandIndex: 1, Events: combined}}},
		{SourceEventID: "source-4", OccurredAt: manifest.Records[3].OccurredAt, Commands: []LifecycleMigrationCommand{{CommandIndex: 1, Events: manifest.Records[3].Events}}},
	}
	sealLifecycleManifest(t, &manifest)
	if _, err := svc.ValidateLifecycleMigration(manifest, manifest.Source.AuthorityHash); err == nil || !strings.Contains(err.Error(), "one source command") {
		t.Fatalf("two actions in one bundle command were accepted: %v", err)
	}

	manifest = lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	manifest.APIVersion = LifecycleMigrationBundleAPIVersion
	manifest.Records[0].Commands = []LifecycleMigrationCommand{{CommandIndex: 2, Events: manifest.Records[0].Events}}
	manifest.Records[0].Events = nil
	for index := 1; index < len(manifest.Records); index++ {
		manifest.Records[index].Commands = []LifecycleMigrationCommand{{CommandIndex: 1, Events: manifest.Records[index].Events}}
		manifest.Records[index].Events = nil
	}
	if _, err := LifecycleSourceEventHash(manifest.Records[0]); err == nil || !strings.Contains(err.Error(), "commandIndex") {
		t.Fatalf("non-contiguous command indexes reached a public hash helper: %v", err)
	}

	manifest = lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	manifest.APIVersion = LifecycleMigrationBundleAPIVersion
	for index := range manifest.Records {
		manifest.Records[index].Commands = []LifecycleMigrationCommand{{CommandIndex: 1, Events: manifest.Records[index].Events}}
		manifest.Records[index].Events = nil
	}
	startEvents := manifest.Records[1].Commands[0].Events
	manifest.Records[1].Commands = []LifecycleMigrationCommand{
		{CommandIndex: 1, Events: append([]LifecycleMigrationEvent{}, startEvents[:2]...)},
		{CommandIndex: 2, Events: append([]LifecycleMigrationEvent{}, startEvents[2:]...)},
	}
	sealLifecycleManifest(t, &manifest)
	if _, err := svc.ValidateLifecycleMigration(manifest, manifest.Source.AuthorityHash); err == nil {
		t.Fatal("support events in one command authorized action.applied in another command")
	}

	manifest = lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	manifest.APIVersion = LifecycleMigrationBundleAPIVersion
	for index := range manifest.Records {
		manifest.Records[index].Commands = []LifecycleMigrationCommand{{CommandIndex: 1, Events: manifest.Records[index].Events}}
		manifest.Records[index].Events = nil
	}
	sealLifecycleManifest(t, &manifest)
	boundAuthority := manifest.Source.AuthorityHash
	manifest.Records[1].Commands[0].Events, manifest.Records[2].Commands[0].Events = manifest.Records[2].Commands[0].Events, manifest.Records[1].Commands[0].Events
	if _, err := svc.ValidateLifecycleMigration(manifest, boundAuthority); err == nil {
		t.Fatal("command event order changed without updating authority hashes was accepted")
	}

	manifest = lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	manifest.APIVersion = LifecycleMigrationBundleAPIVersion
	manifest.Records[0].Commands = make([]LifecycleMigrationCommand, 129)
	for index := range manifest.Records[0].Commands {
		manifest.Records[0].Commands[index] = LifecycleMigrationCommand{CommandIndex: uint64(index + 1), Events: append([]LifecycleMigrationEvent{}, manifest.Records[0].Events...)}
	}
	manifest.Records[0].Events = nil
	for index := 1; index < len(manifest.Records); index++ {
		manifest.Records[index].Commands = []LifecycleMigrationCommand{{CommandIndex: 1, Events: manifest.Records[index].Events}}
		manifest.Records[index].Events = nil
	}
	if _, err := LifecycleSourceEventHash(manifest.Records[0]); err == nil || !strings.Contains(err.Error(), "128") {
		t.Fatalf("129 commands in one source record reached authority hashing: %v", err)
	}
}

func TestLifecycleRecordRejectsForbiddenCrossVersionFieldsEvenWhenEmpty(t *testing.T) {
	var alpha LifecycleMigrationRecord
	if err := json.Unmarshal([]byte(`{"sourceSequence":1,"sourceEventId":"a","sourceEventHash":"`+strings.Repeat("a", 64)+`","occurredAt":"2026-08-16T00:00:00Z","events":[{"type":"role.bound","payload":{}}],"commands":[]}`), &alpha); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycleRecordCommands(LifecycleMigrationAPIVersion, alpha); err == nil || !strings.Contains(err.Error(), "forbids commands") {
		t.Fatalf("v1alpha1 accepted an explicitly present empty commands field: %v", err)
	}
	assertLifecycleHashHelpersReject(t, LifecycleMigrationAPIVersion, alpha)
	var beta LifecycleMigrationRecord
	if err := json.Unmarshal([]byte(`{"sourceSequence":1,"sourceEventId":"b","sourceEventHash":"`+strings.Repeat("b", 64)+`","occurredAt":"2026-08-16T00:00:00Z","events":[],"commands":[{"commandIndex":1,"events":[{"type":"role.bound","payload":{}}]}]}`), &beta); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycleRecordCommands(LifecycleMigrationBundleAPIVersion, beta); err == nil || !strings.Contains(err.Error(), "forbids record-level events") {
		t.Fatalf("v1beta1 accepted an explicitly present empty events field: %v", err)
	}
	assertLifecycleHashHelpersReject(t, LifecycleMigrationBundleAPIVersion, beta)
}

func TestLifecycleHashHelpersRejectInvalidCommandAndNestedPayload(t *testing.T) {
	invalidOrdinal := LifecycleMigrationRecord{
		SourceSequence: 1,
		SourceEventID:  "source-1",
		OccurredAt:     "2026-08-16T00:00:00Z",
		Commands: []LifecycleMigrationCommand{{
			CommandIndex: 2,
			Events:       []LifecycleMigrationEvent{{Type: "role.bound", Payload: json.RawMessage(`{}`)}},
		}},
	}
	assertLifecycleHashHelpersReject(t, LifecycleMigrationBundleAPIVersion, invalidOrdinal)

	unknownPayload := LifecycleMigrationRecord{
		SourceSequence: 1,
		SourceEventID:  "source-1",
		OccurredAt:     "2026-08-16T00:00:00Z",
		Events: []LifecycleMigrationEvent{{
			Type:    "role.bound",
			Payload: json.RawMessage(`{"roleId":"worker","harness":"manual","sessionId":"session","boundAt":"2026-08-16T00:00:00Z","expiresAt":"2026-08-16T01:00:00Z","active":true,"unknown":true}`),
		}},
	}
	assertLifecycleHashHelpersReject(t, LifecycleMigrationAPIVersion, unknownPayload)
}

func TestLifecyclePublicHashHelpersCloseEveryScalarPreimage(t *testing.T) {
	svc := lifecycleService(t)
	state, _ := svc.State()
	alpha := lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	beta := cloneLifecycleManifest(t, alpha)
	beta.APIVersion = LifecycleMigrationBundleAPIVersion
	for index := range beta.Records {
		beta.Records[index].Commands = []LifecycleMigrationCommand{{CommandIndex: 1, Events: beta.Records[index].Events}}
		beta.Records[index].Events = nil
		beta.Records[index].eventsPresent = false
		beta.Records[index].commandsPresent = true
	}
	sealLifecycleManifest(t, &beta)

	for _, base := range []LifecycleMigrationManifest{alpha, beta} {
		t.Run(base.APIVersion, func(t *testing.T) {
			for name, mutate := range map[string]func(*LifecycleMigrationRecord){
				"zero-sequence":     func(record *LifecycleMigrationRecord) { record.SourceSequence = 0 },
				"large-sequence":    func(record *LifecycleMigrationRecord) { record.SourceSequence = maxMigrationRecords + 1 },
				"empty-id":          func(record *LifecycleMigrationRecord) { record.SourceEventID = "" },
				"non-portable-id":   func(record *LifecycleMigrationRecord) { record.SourceEventID = "bad id" },
				"bad-previous-hash": func(record *LifecycleMigrationRecord) { record.PreviousSourceHash = "bad" },
				"bad-time":          func(record *LifecycleMigrationRecord) { record.OccurredAt = "not-a-time" },
			} {
				t.Run(name, func(t *testing.T) {
					candidate := cloneLifecycleManifest(t, base)
					mutate(&candidate.Records[0])
					assertLifecycleHashHelpersReject(t, candidate.APIVersion, candidate.Records[0])
				})
			}

			badOutput := cloneLifecycleManifest(t, base)
			badOutput.Records[0].SourceEventHash = "bad"
			if _, err := LifecycleSourceEventHash(badOutput.Records[0]); err != nil {
				t.Fatalf("source-event helper incorrectly validated its own output field: %v", err)
			}
			if _, err := LifecycleRecordsDigest(badOutput.Records); err == nil {
				t.Fatal("records digest accepted an invalid sourceEventHash")
			}
			if _, err := LifecycleSourceAuthorityHash(badOutput); err == nil {
				t.Fatal("source authority hash accepted an invalid sourceEventHash")
			}

			for name, mutate := range map[string]func(*LifecycleMigrationManifest){
				"wrong-kind":       func(manifest *LifecycleMigrationManifest) { manifest.Kind = "WrongKind" },
				"bad-project":      func(manifest *LifecycleMigrationManifest) { manifest.ProjectID = "not-a-uuid" },
				"bad-graph":        func(manifest *LifecycleMigrationManifest) { manifest.GraphRevision = "bad" },
				"bad-journal-head": func(manifest *LifecycleMigrationManifest) { manifest.ExpectedJournalHead = "bad" },
				"bad-source":       func(manifest *LifecycleMigrationManifest) { manifest.Source.System = "bad source" },
				"zero-head":        func(manifest *LifecycleMigrationManifest) { manifest.Source.HeadSequence = 0 },
				"bad-head-id":      func(manifest *LifecycleMigrationManifest) { manifest.Source.HeadEventID = "bad id" },
				"bad-head-hash":    func(manifest *LifecycleMigrationManifest) { manifest.Source.HeadEventHash = "bad" },
				"bad-records-hash": func(manifest *LifecycleMigrationManifest) { manifest.RecordsDigest = "bad" },
			} {
				t.Run(name, func(t *testing.T) {
					candidate := cloneLifecycleManifest(t, base)
					mutate(&candidate)
					if _, err := LifecycleSourceAuthorityHash(candidate); err == nil {
						t.Fatal("source authority hash accepted an invalid manifest preimage")
					}
				})
			}
		})
	}
}

func cloneLifecycleManifest(t *testing.T, manifest LifecycleMigrationManifest) LifecycleMigrationManifest {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var clone LifecycleMigrationManifest
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func assertLifecycleHashHelpersReject(t *testing.T, apiVersion string, record LifecycleMigrationRecord) {
	t.Helper()
	if _, err := LifecycleRecordsDigest([]LifecycleMigrationRecord{record}); err == nil {
		t.Fatal("LifecycleRecordsDigest accepted an invalid lifecycle wrapper")
	}
	if _, err := LifecycleSourceEventHash(record); err == nil {
		t.Fatal("LifecycleSourceEventHash accepted an invalid lifecycle wrapper")
	}
	manifest := LifecycleMigrationManifest{APIVersion: apiVersion, Records: []LifecycleMigrationRecord{record}}
	if _, err := LifecycleSourceAuthorityHash(manifest); err == nil {
		t.Fatal("LifecycleSourceAuthorityHash accepted an invalid lifecycle wrapper")
	}
}

func TestLifecycleMigrationDecodeRejectsUnknownNestedFields(t *testing.T) {
	for _, apiVersion := range []string{LifecycleMigrationAPIVersion, LifecycleMigrationBundleAPIVersion} {
		for _, layer := range []string{"record", "event", "command"} {
			if apiVersion == LifecycleMigrationAPIVersion && layer == "command" {
				continue
			}
			t.Run(apiVersion+"/"+layer, func(t *testing.T) {
				event := map[string]any{"type": "role.bound", "payload": map[string]any{}}
				record := map[string]any{
					"sourceSequence":  1,
					"sourceEventId":   "source-1",
					"sourceEventHash": strings.Repeat("a", 64),
					"occurredAt":      "2026-08-16T00:00:00Z",
				}
				command := map[string]any{"commandIndex": 1, "events": []any{event}}
				if apiVersion == LifecycleMigrationAPIVersion {
					record["events"] = []any{event}
				} else {
					record["commands"] = []any{command}
				}
				switch layer {
				case "record":
					record["unexpectedRecordField"] = true
				case "event":
					event["unexpectedEventField"] = true
				case "command":
					command["unexpectedCommandField"] = true
				}
				manifest := map[string]any{
					"apiVersion":          apiVersion,
					"kind":                "LifecycleMigration",
					"projectId":           "project",
					"graphRevision":       "revision",
					"expectedJournalHead": "",
					"source": map[string]any{
						"system": "source", "project": "project",
						"authorityHash": "sha256:" + strings.Repeat("b", 64),
						"headSequence":  1, "headEventId": "source-1", "headEventHash": strings.Repeat("a", 64),
					},
					"recordsDigest": "sha256:" + strings.Repeat("c", 64),
					"records":       []any{record},
				}
				raw, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "migration.json")
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeLifecycleMigrationFile(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
					t.Fatalf("%s nested unknown field was accepted: %v", layer, err)
				}
			})
		}
	}
}

func validateLifecycleMigrationSchema(t *testing.T, manifest LifecycleMigrationManifest) {
	t.Helper()
	name := "lifecycle-migration-v1alpha1.schema.json"
	if manifest.APIVersion == LifecycleMigrationBundleAPIVersion {
		name = "lifecycle-migration-v1beta1.schema.json"
	}
	validateLifecycleSchema(t, filepath.Join("..", "..", "schemas", name), "urn:dagrail:lifecycle-migration", manifest)
}

func validateLifecycleProjectionSchema(t *testing.T, projection LifecycleProjection) {
	t.Helper()
	validateLifecycleSchema(t, filepath.Join("..", "..", "schemas", "lifecycle-projection-v1alpha1.schema.json"), "urn:dagrail:lifecycle-projection", projection)
}

func validateLifecycleSchema(t *testing.T, path, resource string, value any) {
	t.Helper()
	schemaRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document, instance any
	if err := json.Unmarshal(schemaRaw, &document); err != nil {
		t.Fatal(err)
	}
	instanceRaw, _ := json.Marshal(value)
	if err := json.Unmarshal(instanceRaw, &instance); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(resource, document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("lifecycle contract does not match its published schema: %v", err)
	}
}

func TestLifecycleHistoryImportRejectsTamperStaleAndNonPristineState(t *testing.T) {
	svc := lifecycleService(t)
	state, _ := svc.State()
	manifest := lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	if _, err := svc.ValidateLifecycleMigration(manifest, digest("b")); err == nil || !strings.Contains(err.Error(), "trusted out-of-band") {
		t.Fatalf("self-asserted source authority was accepted without its trust anchor: %v", err)
	}
	tampered := manifest
	tampered.Records = append([]LifecycleMigrationRecord{}, manifest.Records...)
	tampered.Records[1].SourceEventID = "tampered"
	if _, err := svc.ValidateLifecycleMigration(tampered, manifest.Source.AuthorityHash); err == nil || !strings.Contains(err.Error(), "trusted out-of-band") {
		t.Fatalf("tampered migration was not rejected: %v", err)
	}
	stale := manifest
	stale.ExpectedJournalHead = digest("f")
	if _, err := svc.ValidateLifecycleMigration(stale, manifest.Source.AuthorityHash); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale migration was not rejected: %v", err)
	}
	if _, err := svc.ImportLifecycleHistory(manifest, manifest.Source.AuthorityHash, "migration-admin", "history/import-1"); err != nil {
		t.Fatal(err)
	}
	changed := manifest
	changed.Source.Project = "different"
	sealLifecycleManifest(t, &changed)
	if _, err := svc.ImportLifecycleHistory(changed, changed.Source.AuthorityHash, "migration-admin", "history/import-1"); err == nil || !strings.Contains(err.Error(), "another command") {
		t.Fatalf("changed retry was not rejected: %v", err)
	}
}

func TestLifecycleHistoryImportRejectsUnknownNativeEventBeforeAppend(t *testing.T) {
	svc := lifecycleService(t)
	state, _ := svc.State()
	manifest := lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	manifest.Records[0].Events[0].Type = "project-specific.accepted"
	if _, err := LifecycleSourceEventHash(manifest.Records[0]); err == nil || !strings.Contains(err.Error(), "unsupported native event") {
		t.Fatalf("unknown native event reached authority hashing: %v", err)
	}
	after, _ := svc.State()
	if after.HeadSequence != state.HeadSequence || after.HeadHash != state.HeadHash {
		t.Fatalf("rejected migration changed journal authority: before=%+v after=%+v", state, after)
	}
}

func TestLifecycleHistoryImportRejectsUnknownNativePayloadField(t *testing.T) {
	svc := lifecycleService(t)
	state, _ := svc.State()
	manifest := lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	manifest.Records[0].Events[0].Payload = json.RawMessage(`{"roleId":"worker","harness":"manual","sessionId":"source-session","boundAt":"2026-08-15T01:02:03Z","expiresAt":"2026-08-16T01:02:03Z","active":true,"projectSpecificStage":"S1"}`)
	if _, err := LifecycleSourceEventHash(manifest.Records[0]); err == nil || !strings.Contains(err.Error(), "closed event payload") {
		t.Fatalf("project-specific native event field reached authority hashing: %v", err)
	}
}

func TestLifecycleHistoryTrustAnchorBindsNativeMapping(t *testing.T) {
	svc := lifecycleService(t)
	state, _ := svc.State()
	manifest := lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	trusted := manifest.Source.AuthorityHash
	manifest.Records[0].Events[0].Payload = json.RawMessage(`{"roleId":"worker","harness":"manual","sessionId":"substituted-session","boundAt":"2026-08-15T01:02:03Z","expiresAt":"2026-08-16T01:02:03Z","active":true}`)
	sealLifecycleManifest(t, &manifest)
	if manifest.Source.AuthorityHash == trusted {
		t.Fatal("changed native mapping did not change the canonical authority statement")
	}
	if _, err := svc.ValidateLifecycleMigration(manifest, trusted); err == nil || !strings.Contains(err.Error(), "trusted out-of-band") {
		t.Fatalf("changed native mapping remained valid under the original trust anchor: %v", err)
	}
}

func TestLifecycleHistoryRecomputesEveryNormalizedSourceEventHash(t *testing.T) {
	svc := lifecycleService(t)
	state, _ := svc.State()
	manifest := lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	manifest.Records[0].Events[0].Payload = json.RawMessage(`{"roleId":"worker","harness":"manual","sessionId":"changed-without-rehash","boundAt":"2026-08-15T01:02:03Z","expiresAt":"2026-08-16T01:02:03Z","active":true}`)
	if _, err := LifecycleRecordsDigest(manifest.Records); err == nil || !strings.Contains(err.Error(), "does not bind") {
		t.Fatalf("records digest accepted a stale normalized source hash: %v", err)
	}
	if _, err := LifecycleSourceAuthorityHash(manifest); err == nil || !strings.Contains(err.Error(), "does not bind") {
		t.Fatalf("source authority hash accepted a stale normalized source hash: %v", err)
	}
}

func TestLifecycleHistoryRejectsIllegalIntermediateAttemptStates(t *testing.T) {
	newService := func(t *testing.T) (*Service, LifecycleMigrationManifest) {
		t.Helper()
		svc := lifecycleService(t)
		state, _ := svc.State()
		return svc, lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	}
	payload := func(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }

	t.Run("overlapping attempts", func(t *testing.T) {
		svc, manifest := newService(t)
		now := manifest.Records[0].OccurredAt
		second := LifecycleMigrationRecord{SourceEventID: "source-overlap", OccurredAt: now, Events: []LifecycleMigrationEvent{{Type: "attempt.leased", Payload: payload(map[string]any{"id": "attempt-2", "nodeId": "task", "roleId": "worker", "number": 2, "status": "leased", "startedAt": now, "updatedAt": now})}}}
		manifest.Records = append(manifest.Records[:3], append([]LifecycleMigrationRecord{second}, manifest.Records[3:]...)...)
		sealLifecycleManifest(t, &manifest)
		if _, err := svc.ValidateLifecycleMigration(manifest, manifest.Source.AuthorityHash); err == nil || !strings.Contains(err.Error(), "transition preflight") {
			t.Fatalf("overlapping attempts were accepted: %v", err)
		}
	})

	t.Run("invalid overwritten status", func(t *testing.T) {
		svc, manifest := newService(t)
		manifest.Records[2].Events[0].Payload = payload(map[string]any{"attemptId": "attempt-1", "status": "not-a-native-status", "updatedAt": manifest.Records[2].OccurredAt})
		sealLifecycleManifest(t, &manifest)
		if _, err := svc.ValidateLifecycleMigration(manifest, manifest.Source.AuthorityHash); err == nil || !strings.Contains(err.Error(), "transition preflight") {
			t.Fatalf("invalid intermediate status was accepted: %v", err)
		}
	})

	t.Run("non-contiguous attempt number", func(t *testing.T) {
		svc, manifest := newService(t)
		var attempt map[string]any
		_ = json.Unmarshal(manifest.Records[1].Events[0].Payload, &attempt)
		attempt["number"] = 42
		manifest.Records[1].Events[0].Payload = payload(attempt)
		sealLifecycleManifest(t, &manifest)
		if _, err := svc.ValidateLifecycleMigration(manifest, manifest.Source.AuthorityHash); err == nil || !strings.Contains(err.Error(), "transition preflight") {
			t.Fatalf("non-contiguous attempt number was accepted: %v", err)
		}
	})

	t.Run("completion before start", func(t *testing.T) {
		svc, manifest := newService(t)
		var finish map[string]any
		_ = json.Unmarshal(manifest.Records[len(manifest.Records)-1].Events[0].Payload, &finish)
		finish["updatedAt"] = "2026-08-14T00:00:00Z"
		manifest.Records[len(manifest.Records)-1].Events[0].Payload = payload(finish)
		sealLifecycleManifest(t, &manifest)
		if _, err := svc.ValidateLifecycleMigration(manifest, manifest.Source.AuthorityHash); err == nil || !strings.Contains(err.Error(), "transition preflight") {
			t.Fatalf("completion before attempt start was accepted: %v", err)
		}
	})
}

func TestLifecycleTransitionPreflightRejectsInvalidResourceAndEffectHistories(t *testing.T) {
	payload := func(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
	now := "2026-08-15T01:02:03Z"
	lease := LifecycleMigrationEvent{Type: "role.bound", Payload: payload(map[string]any{"roleId": "worker", "harness": "manual", "sessionId": "session", "boundAt": now, "expiresAt": "2026-08-16T01:02:03Z", "active": true})}
	attempt := func(node string) LifecycleMigrationEvent {
		return LifecycleMigrationEvent{Type: "attempt.leased", Payload: payload(map[string]any{"id": "attempt-1", "nodeId": node, "roleId": "worker", "number": 1, "status": "leased", "startedAt": now, "updatedAt": now})}
	}
	running := LifecycleMigrationEvent{Type: "attempt.status-changed", Payload: payload(map[string]any{"attemptId": "attempt-1", "status": "running", "updatedAt": now})}
	commandRecords := func(node string, commands ...[]LifecycleMigrationEvent) []LifecycleMigrationRecord {
		records := []LifecycleMigrationRecord{
			{SourceEventID: "bind", OccurredAt: now, Events: []LifecycleMigrationEvent{lease}},
			{SourceEventID: "start", OccurredAt: now, Events: []LifecycleMigrationEvent{attempt(node), running, {Type: "action.applied", Payload: payload(map[string]any{"id": "action-start", "kind": "node.start", "nodeId": node, "attemptId": "attempt-1", "status": "confirmed", "input": map[string]any{}})}}},
		}
		for index, events := range commands {
			records = append(records, LifecycleMigrationRecord{SourceEventID: fmt.Sprintf("command-%d", index+1), OccurredAt: now, Events: events})
		}
		return records
	}

	t.Run("resource release before closure", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "resource"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run"}}}, ResourceCapacities: []domain.ResourceCapacity{{Kind: "browser", Capacity: 1}}, Nodes: []domain.NodeDefinition{{ID: "task", Kind: "task", Role: "worker", Title: "task", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}, Resources: []domain.ResourceRequest{{Kind: "browser", Quantity: 1}}}}}}
		state := domain.NewState("00000000-0000-4000-8000-000000000001")
		state.Graph, state.GraphRevision = &graph, strings.Repeat("a", 64)
		state.Nodes["task"] = domain.NodeRuntime{Status: "planned"}
		events := []LifecycleMigrationEvent{lease, attempt("task"), running,
			{Type: "resource.leased", Payload: payload(map[string]any{"id": "resource-1", "kind": "browser", "quantity": 1, "nodeId": "task", "attemptId": "attempt-1", "roleId": "worker", "status": "active", "closureStatus": "pending", "leasedAt": now})},
			{Type: "resource.released", Payload: payload(map[string]any{"resourceId": "resource-1", "releasedAt": now})},
		}
		if err := validateLifecycleEventSequence(state, []LifecycleMigrationRecord{{SourceEventID: "resource-history", OccurredAt: now, Events: events}}); err == nil || !strings.Contains(err.Error(), "confirmed closure") {
			t.Fatalf("resource released without closure confirmation: %v", err)
		}
	})

	t.Run("effect observation before dispatch", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "effect"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"effect.apply"}}}, Nodes: []domain.NodeDefinition{{ID: "effect", Kind: "effect", Role: "worker", Title: "effect", Inputs: json.RawMessage(`{"adapter":"manual","request":{"operation":"dispatch"}}`), Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}}}}}
		state := domain.NewState("00000000-0000-4000-8000-000000000002")
		state.Graph, state.GraphRevision = &graph, strings.Repeat("b", 64)
		state.Nodes["effect"] = domain.NodeRuntime{Status: "planned"}
		events := []LifecycleMigrationEvent{lease, attempt("effect"), running,
			{Type: "effect.prepared", Payload: payload(map[string]any{"id": "action-1", "nodeId": "effect", "attemptId": "attempt-1", "adapterId": "manual", "adapterVersion": "0.1.0", "adapterSchemaHash": "sha256:manual-effect-v1", "ownerRole": "worker", "status": "prepared", "request": map[string]any{"operation": "dispatch"}, "prepared": map[string]any{"adapterId": "manual", "binding": map[string]any{"operation": "dispatch"}}, "idempotencyKey": "effect/1", "preparedAt": now, "updatedAt": now})},
			{Type: "action.applied", Payload: payload(map[string]any{"id": "action-1", "kind": "effect.prepare", "nodeId": "effect", "attemptId": "attempt-1", "status": "prepared", "input": map[string]any{}})},
			{Type: "effect.observed", Payload: payload(map[string]any{"actionId": "action-1", "status": "confirmed", "receipt": map[string]any{"status": "confirmed"}, "updatedAt": now})},
		}
		if err := validateLifecycleEventSequence(state, []LifecycleMigrationRecord{{SourceEventID: "effect-history", OccurredAt: now, Events: events}}); err == nil || !strings.Contains(err.Error(), "observation is invalid") {
			t.Fatalf("effect observation before dispatch was accepted: %v", err)
		}
	})

	t.Run("valid effect saga remains importable", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "effect"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"effect.apply"}}}, Nodes: []domain.NodeDefinition{{ID: "effect", Kind: "effect", Role: "worker", Title: "effect", Inputs: json.RawMessage(`{"adapter":"manual","request":{"operation":"dispatch"}}`), Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}}}}}
		state := domain.NewState("00000000-0000-4000-8000-000000000003")
		state.Graph, state.GraphRevision = &graph, strings.Repeat("c", 64)
		state.Nodes["effect"] = domain.NodeRuntime{Status: "planned"}
		prepare := []LifecycleMigrationEvent{
			{Type: "effect.prepared", Payload: payload(map[string]any{"id": "action-1", "nodeId": "effect", "attemptId": "attempt-1", "adapterId": "manual", "adapterVersion": "0.1.0", "adapterSchemaHash": "sha256:manual-effect-v1", "ownerRole": "worker", "status": "prepared", "request": map[string]any{"operation": "dispatch"}, "prepared": map[string]any{"adapterId": "manual", "binding": map[string]any{"operation": "dispatch"}}, "idempotencyKey": "effect/1", "preparedAt": now, "updatedAt": now})},
			{Type: "action.applied", Payload: payload(map[string]any{"id": "action-1", "kind": "effect.prepare", "nodeId": "effect", "attemptId": "attempt-1", "status": "prepared", "input": map[string]any{}})},
		}
		records := commandRecords("effect", prepare, []LifecycleMigrationEvent{{Type: "effect.dispatched", Payload: payload(map[string]any{"actionId": "action-1", "dispatchedAt": now})}}, []LifecycleMigrationEvent{{Type: "effect.observed", Payload: payload(map[string]any{"actionId": "action-1", "status": "confirmed", "receipt": map[string]any{"status": "confirmed"}, "updatedAt": now})}})
		if err := validateLifecycleEventSequence(state, records); err != nil {
			t.Fatalf("valid effect saga was rejected: %v", err)
		}
	})

	t.Run("dispatched crash prefix remains importable", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "effect"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"effect.apply"}}}, Nodes: []domain.NodeDefinition{{ID: "effect", Kind: "effect", Role: "worker", Title: "effect", Inputs: json.RawMessage(`{"adapter":"manual","request":{"operation":"dispatch"}}`), Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}}}}}
		state := domain.NewState("00000000-0000-4000-8000-000000000004")
		state.Graph, state.GraphRevision = &graph, strings.Repeat("d", 64)
		state.Nodes["effect"] = domain.NodeRuntime{Status: "planned"}
		prepare := []LifecycleMigrationEvent{
			{Type: "effect.prepared", Payload: payload(map[string]any{"id": "action-1", "nodeId": "effect", "attemptId": "attempt-1", "adapterId": "manual", "adapterVersion": "0.1.0", "adapterSchemaHash": "sha256:manual-effect-v1", "ownerRole": "worker", "status": "prepared", "request": map[string]any{"operation": "dispatch"}, "prepared": map[string]any{"adapterId": "manual", "binding": map[string]any{"operation": "dispatch"}}, "idempotencyKey": "effect/1", "preparedAt": now, "updatedAt": now})},
			{Type: "action.applied", Payload: payload(map[string]any{"id": "action-1", "kind": "effect.prepare", "nodeId": "effect", "attemptId": "attempt-1", "status": "prepared", "input": map[string]any{}})},
		}
		records := commandRecords("effect", prepare, []LifecycleMigrationEvent{{Type: "effect.dispatched", Payload: payload(map[string]any{"actionId": "action-1", "dispatchedAt": now})}})
		if err := validateLifecycleEventSequence(state, records); err != nil {
			t.Fatalf("writer dispatch crash prefix was rejected: %v", err)
		}
	})

	t.Run("reconciling crash prefix remains reducer-equivalent", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "effect"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"effect.apply", "effect.reconcile"}}}, Nodes: []domain.NodeDefinition{{ID: "effect", Kind: "effect", Role: "worker", Title: "effect", Inputs: json.RawMessage(`{"adapter":"manual","request":{"operation":"dispatch"}}`), Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}}}}}
		state := domain.NewState("00000000-0000-4000-8000-000000000005")
		state.Graph, state.GraphRevision = &graph, strings.Repeat("e", 64)
		state.Nodes["effect"] = domain.NodeRuntime{Status: "planned"}
		prepare := []LifecycleMigrationEvent{
			{Type: "effect.prepared", Payload: payload(map[string]any{"id": "action-1", "nodeId": "effect", "attemptId": "attempt-1", "adapterId": "manual", "adapterVersion": "0.1.0", "adapterSchemaHash": "sha256:manual-effect-v1", "ownerRole": "worker", "status": "prepared", "request": map[string]any{"operation": "dispatch"}, "prepared": map[string]any{"adapterId": "manual", "binding": map[string]any{"operation": "dispatch"}}, "idempotencyKey": "effect/1", "preparedAt": now, "updatedAt": now})},
			{Type: "action.applied", Payload: payload(map[string]any{"id": "action-1", "kind": "effect.prepare", "nodeId": "effect", "attemptId": "attempt-1", "status": "prepared", "input": map[string]any{}})},
		}
		records := commandRecords("effect", prepare, []LifecycleMigrationEvent{{Type: "effect.reconciling", Payload: payload(map[string]any{"actionId": "action-1", "reconcilingAt": now})}})
		if err := validateLifecycleEventSequence(state, records); err != nil {
			t.Fatalf("writer reconcile crash prefix was rejected: %v", err)
		}
	})

	t.Run("reconciling receipt remains importable", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "effect"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"effect.apply"}}}, Nodes: []domain.NodeDefinition{{ID: "effect", Kind: "effect", Role: "worker", Title: "effect", Inputs: json.RawMessage(`{"adapter":"manual","request":{"operation":"dispatch"}}`), Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}}}}}
		state := domain.NewState("00000000-0000-4000-8000-000000000006")
		state.Graph, state.GraphRevision = &graph, strings.Repeat("f", 64)
		state.Nodes["effect"] = domain.NodeRuntime{Status: "planned"}
		prepare := []LifecycleMigrationEvent{
			{Type: "effect.prepared", Payload: payload(map[string]any{"id": "action-1", "nodeId": "effect", "attemptId": "attempt-1", "adapterId": "manual", "adapterVersion": "0.1.0", "adapterSchemaHash": "sha256:manual-effect-v1", "ownerRole": "worker", "status": "prepared", "request": map[string]any{"operation": "dispatch"}, "prepared": map[string]any{"adapterId": "manual", "binding": map[string]any{"operation": "dispatch"}}, "idempotencyKey": "effect/1", "preparedAt": now, "updatedAt": now})},
			{Type: "action.applied", Payload: payload(map[string]any{"id": "action-1", "kind": "effect.prepare", "nodeId": "effect", "attemptId": "attempt-1", "status": "prepared", "input": map[string]any{}})},
		}
		records := commandRecords("effect", prepare, []LifecycleMigrationEvent{{Type: "effect.dispatched", Payload: payload(map[string]any{"actionId": "action-1", "dispatchedAt": now})}}, []LifecycleMigrationEvent{{Type: "effect.observed", Payload: payload(map[string]any{"actionId": "action-1", "status": "reconciling", "receipt": map[string]any{"status": "reconciling"}, "updatedAt": now})}})
		if err := validateLifecycleEventSequence(state, records); err != nil {
			t.Fatalf("writer reconciling receipt was rejected: %v", err)
		}
	})
}

func TestLifecycleTransitionPreflightClosesCausalityVocabularyAndTime(t *testing.T) {
	payload := func(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
	now := "2026-08-15T01:02:03Z"
	expires := "2026-08-16T01:02:03Z"
	lease := LifecycleMigrationEvent{Type: "role.bound", Payload: payload(map[string]any{"roleId": "worker", "harness": "manual", "sessionId": "session", "boundAt": now, "expiresAt": expires, "active": true})}
	attempt := func(node string) LifecycleMigrationEvent {
		return LifecycleMigrationEvent{Type: "attempt.leased", Payload: payload(map[string]any{"id": "attempt-1", "nodeId": node, "roleId": "worker", "number": 1, "status": "leased", "startedAt": now, "updatedAt": now})}
	}
	running := LifecycleMigrationEvent{Type: "attempt.status-changed", Payload: payload(map[string]any{"attemptId": "attempt-1", "status": "running", "updatedAt": now})}

	t.Run("blocked downstream node", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "causal"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run"}}}, Nodes: []domain.NodeDefinition{{ID: "A", Kind: "task", Role: "worker", Title: "A", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}}, {ID: "B", Kind: "task", Role: "worker", Title: "B", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}}}, Edges: []domain.EdgeDefinition{{ID: "A-B", From: "A", To: "B", When: domain.Predicate{Outcome: "done"}}}}}
		state := lifecycleValidationState(&graph, "causal-project", "a")
		err := validateLifecycleEventSequence(state, []LifecycleMigrationRecord{{SourceEventID: "blocked", OccurredAt: now, Events: []LifecycleMigrationEvent{lease, attempt("B")}}})
		if err == nil || !strings.Contains(err.Error(), "node state") {
			t.Fatalf("blocked downstream attempt was accepted: %v", err)
		}
	})

	t.Run("role without node capability", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "capability"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.review"}}}, Nodes: []domain.NodeDefinition{{ID: "task", Kind: "task", Role: "worker", Title: "task", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}}}}}
		state := lifecycleValidationState(&graph, "capability-project", "b")
		err := validateLifecycleEventSequence(state, []LifecycleMigrationRecord{{SourceEventID: "capability", OccurredAt: now, Events: []LifecycleMigrationEvent{lease, attempt("task")}}})
		if err == nil || !strings.Contains(err.Error(), "capability") {
			t.Fatalf("attempt without role capability was accepted: %v", err)
		}
	})

	t.Run("open action vocabulary", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "action"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run"}}}, Nodes: []domain.NodeDefinition{{ID: "task", Kind: "task", Role: "worker", Title: "task", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}}}}}
		state := lifecycleValidationState(&graph, "action-project", "c")
		events := []LifecycleMigrationEvent{lease, attempt("task"), running, {Type: "action.applied", Payload: payload(map[string]any{"id": "action-1", "kind": "project.accept", "nodeId": "task", "attemptId": "attempt-1", "status": "confirmed", "input": map[string]any{}})}}
		err := validateLifecycleEventSequence(state, []LifecycleMigrationRecord{{SourceEventID: "action", OccurredAt: now, Events: events}})
		if err == nil || !strings.Contains(err.Error(), "closed lifecycle vocabulary") {
			t.Fatalf("project-specific action kind was accepted: %v", err)
		}
	})

	t.Run("open incident source vocabulary", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "incident"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run"}}}, Nodes: []domain.NodeDefinition{{ID: "task", Kind: "task", Role: "worker", Title: "task", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}}}}}
		state := lifecycleValidationState(&graph, "incident-project", "d")
		incident := map[string]any{"id": "incident-1", "sourceType": "project-stage", "sourceId": "stage-1", "status": "open", "classification": "unknown", "attemptBudget": 1, "attempts": 0, "openedAt": now, "updatedAt": now}
		events := []LifecycleMigrationEvent{lease, attempt("task"), running, {Type: "incident.opened", Payload: payload(incident)}}
		err := validateLifecycleEventSequence(state, []LifecycleMigrationRecord{{SourceEventID: "incident", OccurredAt: now, Events: events}})
		if err == nil || !strings.Contains(err.Error(), "incident fields") {
			t.Fatalf("project-specific incident source was accepted: %v", err)
		}
	})

	t.Run("lease longer than writer maximum", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "lease"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run"}}}, Nodes: []domain.NodeDefinition{{ID: "task", Kind: "task", Role: "worker", Title: "task", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}}}}}
		state := lifecycleValidationState(&graph, "lease-project", "e")
		longLease := LifecycleMigrationEvent{Type: "role.bound", Payload: payload(map[string]any{"roleId": "worker", "harness": "manual", "sessionId": "session", "boundAt": now, "expiresAt": "2026-08-16T01:02:04Z", "active": true})}
		err := validateLifecycleEventSequence(state, []LifecycleMigrationRecord{{SourceEventID: "lease", OccurredAt: now, Events: []LifecycleMigrationEvent{longLease}}})
		if err == nil || !strings.Contains(err.Error(), "timestamps") {
			t.Fatalf("overlong role lease was accepted: %v", err)
		}
	})

	t.Run("payload time after source record", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "time"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run"}}}, Nodes: []domain.NodeDefinition{{ID: "task", Kind: "task", Role: "worker", Title: "task", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}}}}}
		state := lifecycleValidationState(&graph, "time-project", "f")
		err := validateLifecycleEventSequence(state, []LifecycleMigrationRecord{{SourceEventID: "time", OccurredAt: "2026-08-15T01:02:02Z", Events: []LifecycleMigrationEvent{lease}}})
		if err == nil || !strings.Contains(err.Error(), "timestamps") {
			t.Fatalf("payload time after its source record was accepted: %v", err)
		}
	})

	t.Run("attempt transition after lease expiry", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "expired"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run"}}}, Nodes: []domain.NodeDefinition{{ID: "task", Kind: "task", Role: "worker", Title: "task", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}}}}}
		state := lifecycleValidationState(&graph, "expired-project", "3")
		late := "2026-08-16T01:02:03Z"
		events := []LifecycleMigrationEvent{lease, attempt("task"), {Type: "attempt.status-changed", Payload: payload(map[string]any{"attemptId": "attempt-1", "status": "running", "updatedAt": late})}}
		err := validateLifecycleEventSequence(state, []LifecycleMigrationRecord{{SourceEventID: "expired", OccurredAt: late, Events: events}})
		if err == nil || !strings.Contains(err.Error(), "status transition") {
			t.Fatalf("attempt transition at lease expiry was accepted: %v", err)
		}
	})

	t.Run("resource lease before attempt", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "resource-time"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run"}}}, ResourceCapacities: []domain.ResourceCapacity{{Kind: "browser", Capacity: 1}}, Nodes: []domain.NodeDefinition{{ID: "task", Kind: "task", Role: "worker", Title: "task", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}, Resources: []domain.ResourceRequest{{Kind: "browser", Quantity: 1}}}}}}
		state := lifecycleValidationState(&graph, "resource-time-project", "4")
		resource := LifecycleMigrationEvent{Type: "resource.leased", Payload: payload(map[string]any{"id": "resource-1", "kind": "browser", "quantity": 1, "nodeId": "task", "attemptId": "attempt-1", "roleId": "worker", "status": "active", "closureStatus": "pending", "leasedAt": "2026-08-15T01:02:02Z"})}
		events := []LifecycleMigrationEvent{lease, attempt("task"), running, resource}
		err := validateLifecycleEventSequence(state, []LifecycleMigrationRecord{{SourceEventID: "resource-time", OccurredAt: now, Events: events}})
		if err == nil || !strings.Contains(err.Error(), "resource lease") {
			t.Fatalf("resource lease before its attempt was accepted: %v", err)
		}
	})
}

func TestLifecycleDecisionMustMatchCompletion(t *testing.T) {
	now := "2026-08-15T01:02:03Z"
	payload := func(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
	graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "decision"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.review"}}}, Nodes: []domain.NodeDefinition{{ID: "review", Kind: "review", Role: "worker", Title: "review", RetryBudget: 1, Outcomes: []domain.Outcome{{ID: "approve", Class: "success"}, {ID: "return", Class: "retryable"}}}}}}
	state := lifecycleValidationState(&graph, "decision-project", "1")
	decision := domain.DecisionRecord{ProjectID: state.ProjectID, GraphRevision: state.GraphRevision, NodeID: "review", AttemptID: "attempt-1", RoleID: "worker", Key: "verdict", Source: "llm", Outcome: "approve", Facts: domain.PredicateFacts{Decision: map[string]string{"verdict": "approve"}}, InputDigest: digest("a"), CreatedAt: now}
	if err := assignDecisionID(&decision); err != nil {
		t.Fatal(err)
	}
	events := []LifecycleMigrationEvent{
		{Type: "role.bound", Payload: payload(map[string]any{"roleId": "worker", "harness": "manual", "sessionId": "session", "boundAt": now, "expiresAt": "2026-08-16T01:02:03Z", "active": true})},
		{Type: "attempt.leased", Payload: payload(map[string]any{"id": "attempt-1", "nodeId": "review", "roleId": "worker", "number": 1, "status": "leased", "startedAt": now, "updatedAt": now})},
		{Type: "attempt.status-changed", Payload: payload(map[string]any{"attemptId": "attempt-1", "status": "running", "updatedAt": now})},
		{Type: "decision.recorded", Payload: payload(decision)},
		{Type: "attempt.finished", Payload: payload(map[string]any{"attemptId": "attempt-1", "outcome": "return", "outcomeClass": "retryable", "facts": map[string]any{"decision": map[string]string{"verdict": "return"}}, "updatedAt": now})},
	}
	err := validateLifecycleEventSequence(state, []LifecycleMigrationRecord{{SourceEventID: "decision", OccurredAt: now, Events: events}})
	if err == nil || !strings.Contains(err.Error(), "contradicts") {
		t.Fatalf("decision/completion contradiction was accepted: %v", err)
	}
}

func TestLifecycleActionMustMatchNativeEventsFromTheSameSourceRecord(t *testing.T) {
	now := "2026-08-15T01:02:03Z"
	payload := func(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
	graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "action-binding"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run"}}}, Nodes: []domain.NodeDefinition{{ID: "task", Kind: "task", Role: "worker", Title: "task", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}, {ID: "broken", Class: "failure"}}}}}}
	initial := lifecycleValidationState(&graph, "action-binding-project", "7")
	base := []LifecycleMigrationEvent{
		{Type: "role.bound", Payload: payload(map[string]any{"roleId": "worker", "harness": "manual", "sessionId": "session", "boundAt": now, "expiresAt": "2026-08-16T01:02:03Z", "active": true})},
		{Type: "attempt.leased", Payload: payload(map[string]any{"id": "attempt-1", "nodeId": "task", "roleId": "worker", "number": 1, "status": "leased", "startedAt": now, "updatedAt": now})},
		{Type: "attempt.status-changed", Payload: payload(map[string]any{"attemptId": "attempt-1", "status": "running", "updatedAt": now})},
	}

	t.Run("completion input", func(t *testing.T) {
		events := append([]LifecycleMigrationEvent{}, base...)
		events = append(events,
			LifecycleMigrationEvent{Type: "attempt.status-changed", Payload: payload(map[string]any{"attemptId": "attempt-1", "status": "submitted", "updatedAt": now})},
			LifecycleMigrationEvent{Type: "attempt.finished", Payload: payload(map[string]any{"attemptId": "attempt-1", "outcome": "done", "outcomeClass": "success", "facts": map[string]any{}, "updatedAt": now})},
			LifecycleMigrationEvent{Type: "action.applied", Payload: payload(map[string]any{"id": "action-1", "kind": "task.complete", "nodeId": "task", "attemptId": "attempt-1", "status": "confirmed", "input": map[string]any{"outcome": "broken"}})},
		)
		err := validateLifecycleEventSequence(initial, []LifecycleMigrationRecord{{SourceEventID: "completion", OccurredAt: now, Events: events}})
		if err == nil || !strings.Contains(err.Error(), "contradicts") {
			t.Fatalf("action input contradicted its terminal event without rejection: %v", err)
		}
	})

	t.Run("checkpoint input", func(t *testing.T) {
		events := append([]LifecycleMigrationEvent{}, base...)
		events = append(events,
			LifecycleMigrationEvent{Type: "attempt.checkpointed", Payload: payload(map[string]any{"id": "checkpoint-1", "attemptId": "attempt-1", "summary": "durable", "createdAt": now})},
			LifecycleMigrationEvent{Type: "action.applied", Payload: payload(map[string]any{"id": "action-1", "kind": "attempt.checkpoint", "nodeId": "task", "attemptId": "attempt-1", "status": "confirmed", "input": map[string]any{"summary": "substituted"}})},
		)
		err := validateLifecycleEventSequence(initial, []LifecycleMigrationRecord{{SourceEventID: "checkpoint", OccurredAt: now, Events: events}})
		if err == nil || !strings.Contains(err.Error(), "checkpoint event") {
			t.Fatalf("action input contradicted its checkpoint event without rejection: %v", err)
		}
	})
}

func TestLifecycleCurrentWriterReleaseAndIncidentResolveRemainImportable(t *testing.T) {
	now := "2026-08-15T01:02:03Z"
	payload := func(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }

	t.Run("role release payload", func(t *testing.T) {
		svc := lifecycleService(t)
		state, _ := svc.State()
		manifest := lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
		manifest.Records = append(manifest.Records, LifecycleMigrationRecord{SourceEventID: "source-release", OccurredAt: now, Events: []LifecycleMigrationEvent{{Type: "role.released", Payload: payload(map[string]string{"roleId": "worker", "releasedAt": now})}}})
		sealLifecycleManifest(t, &manifest)
		validateLifecycleMigrationSchema(t, manifest)
		if _, err := svc.ValidateLifecycleMigration(manifest, manifest.Source.AuthorityHash); err != nil {
			t.Fatalf("current writer role release was rejected: %v", err)
		}
	})

	t.Run("incident updated to resolved", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "incident-resolve"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run", "incident.manage"}}}, Nodes: []domain.NodeDefinition{{ID: "task", Kind: "task", Role: "worker", Title: "task", Outcomes: []domain.Outcome{{ID: "broken", Class: "failure"}}}}}}
		state := lifecycleValidationState(&graph, "incident-resolve-project", "2")
		opened := domain.Incident{ID: "attempt:attempt-1", SourceType: "attempt", SourceID: "attempt-1", NodeID: "task", OwnerRole: "worker", Status: "open", Classification: "work-product", Deadline: "2026-08-15T02:02:03Z", AttemptBudget: 2, Attempts: 0, ProgressMetric: "new candidate or changed failure classification", OpenedAt: now, UpdatedAt: now}
		resolved := opened
		resolved.Status, resolved.Resolution = "resolved", "fixed"
		records := []LifecycleMigrationRecord{
			{SourceEventID: "bind", OccurredAt: now, Events: []LifecycleMigrationEvent{{Type: "role.bound", Payload: payload(map[string]any{"roleId": "worker", "harness": "manual", "sessionId": "session", "boundAt": now, "expiresAt": "2026-08-16T01:02:03Z", "active": true})}}},
			{SourceEventID: "start", OccurredAt: now, Events: []LifecycleMigrationEvent{
				{Type: "attempt.leased", Payload: payload(map[string]any{"id": "attempt-1", "nodeId": "task", "roleId": "worker", "number": 1, "status": "leased", "startedAt": now, "updatedAt": now})},
				{Type: "attempt.status-changed", Payload: payload(map[string]any{"attemptId": "attempt-1", "status": "running", "updatedAt": now})},
				{Type: "action.applied", Payload: payload(map[string]any{"id": "action-start", "kind": "node.start", "nodeId": "task", "attemptId": "attempt-1", "status": "confirmed", "input": map[string]any{}})},
			}},
			{SourceEventID: "finish", OccurredAt: now, Events: []LifecycleMigrationEvent{
				{Type: "attempt.finished", Payload: payload(map[string]any{"attemptId": "attempt-1", "outcome": "broken", "outcomeClass": "failure", "facts": map[string]any{}, "updatedAt": now})},
				{Type: "incident.opened", Payload: payload(opened)},
				{Type: "action.applied", Payload: payload(map[string]any{"id": "action-finish", "kind": "task.complete", "nodeId": "task", "attemptId": "attempt-1", "status": "confirmed", "input": map[string]any{"outcome": "broken"}})},
			}},
			{SourceEventID: "resolve", OccurredAt: now, Events: []LifecycleMigrationEvent{{Type: "incident.updated", Payload: payload(resolved)}}},
		}
		if err := validateLifecycleEventSequence(state, records); err != nil {
			t.Fatalf("current writer incident resolution was rejected: %v", err)
		}
	})

	t.Run("incident update after owner lease expiry", func(t *testing.T) {
		graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "incident-expiry"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run", "incident.manage"}}}, Nodes: []domain.NodeDefinition{{ID: "task", Kind: "task", Role: "worker", Title: "task", Outcomes: []domain.Outcome{{ID: "broken", Class: "failure"}}}}}}
		state := lifecycleValidationState(&graph, "incident-expiry-project", "8")
		opened := domain.Incident{ID: "attempt:attempt-1", SourceType: "attempt", SourceID: "attempt-1", NodeID: "task", OwnerRole: "worker", Status: "open", Classification: "work-product", Deadline: "2026-08-15T02:02:03Z", AttemptBudget: 2, ProgressMetric: "new candidate or changed failure classification", OpenedAt: now, UpdatedAt: now}
		updated := opened
		updated.LastProgress, updated.LastProgressAt, updated.UpdatedAt = "late", "2026-08-16T01:02:03Z", "2026-08-16T01:02:03Z"
		records := []LifecycleMigrationRecord{
			{SourceEventID: "bind", OccurredAt: now, Events: []LifecycleMigrationEvent{{Type: "role.bound", Payload: payload(map[string]any{"roleId": "worker", "harness": "manual", "sessionId": "session", "boundAt": now, "expiresAt": "2026-08-16T01:02:03Z", "active": true})}}},
			{SourceEventID: "start", OccurredAt: now, Events: []LifecycleMigrationEvent{
				{Type: "attempt.leased", Payload: payload(map[string]any{"id": "attempt-1", "nodeId": "task", "roleId": "worker", "number": 1, "status": "leased", "startedAt": now, "updatedAt": now})},
				{Type: "attempt.status-changed", Payload: payload(map[string]any{"attemptId": "attempt-1", "status": "running", "updatedAt": now})},
				{Type: "action.applied", Payload: payload(map[string]any{"id": "action-start", "kind": "node.start", "nodeId": "task", "attemptId": "attempt-1", "status": "confirmed", "input": map[string]any{}})},
			}},
			{SourceEventID: "finish", OccurredAt: now, Events: []LifecycleMigrationEvent{
				{Type: "attempt.finished", Payload: payload(map[string]any{"attemptId": "attempt-1", "outcome": "broken", "outcomeClass": "failure", "facts": map[string]any{}, "updatedAt": now})},
				{Type: "incident.opened", Payload: payload(opened)},
				{Type: "action.applied", Payload: payload(map[string]any{"id": "action-finish", "kind": "task.complete", "nodeId": "task", "attemptId": "attempt-1", "status": "confirmed", "input": map[string]any{"outcome": "broken"}})},
			}},
			{SourceEventID: "late-update", OccurredAt: updated.UpdatedAt, Events: []LifecycleMigrationEvent{{Type: "incident.updated", Payload: payload(updated)}}},
		}
		if err := validateLifecycleEventSequence(state, records); err == nil || !strings.Contains(err.Error(), "owner role binding") {
			t.Fatalf("incident update after owner lease expiry was accepted: %v", err)
		}
	})
}

func TestLifecycleSimulatorMatchesEveryCurrentWriterPrefix(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "writer-prefix")
	if err != nil {
		t.Fatal(err)
	}
	current := time.Date(2026, 8, 15, 4, 5, 6, 0, time.UTC)
	svc.Now = func() time.Time {
		result := current
		current = current.Add(time.Millisecond)
		return result
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"writer-prefix"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph/import", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	initial, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "session-1", time.Hour, false, "role/bind-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "session-1", 2*time.Hour, false, "role/renew-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "node.start"), json.RawMessage(`{}`), "node/start"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "attempt.checkpoint"), json.RawMessage(`{"summary":"durable prefix"}`), "attempt/checkpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "evidence.publish"), packageInputJSON(t, []domain.ProtectedInput{{Name: "fixture", Digest: testDigest("3")}}, nil), "evidence/publish"); err != nil {
		t.Fatal(err)
	}
	evidenceState, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	var pack domain.ExecutionPackage
	for _, candidate := range evidenceState.EvidencePackages {
		pack = candidate
	}
	if pack.ID == "" {
		t.Fatal("current writer did not publish an execution package")
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "evidence.assess-reuse"), reuseInputJSON(t, pack, domain.PolicyBinding{ID: "validator", Version: "1.0.0", SchemaHash: testDigest("4")}, pack.Candidate.Digest), "evidence/reuse"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "attempt.wait"), json.RawMessage(`{}`), "attempt/wait"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReleaseRole("worker", "session-1", "role/release-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "session-2", time.Hour, false, "role/bind-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "attempt.resume"), json.RawMessage(`{}`), "attempt/resume"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "attempt.submit"), json.RawMessage(`{}`), "attempt/submit"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "task.complete"), json.RawMessage(`{"outcome":"done"}`), "task/complete"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReleaseRole("worker", "session-2", "role/release-2"); err != nil {
		t.Fatal(err)
	}

	assertLifecycleWriterPrefixes(t, svc, initial)
}

func TestLifecycleSimulatorMatchesSemanticResourceIncidentAndEffectWriters(t *testing.T) {
	t.Run("review decision", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "review-writer", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"review-writer"},"spec":{"roles":[{"id":"reviewer","capabilities":["node.review"]}],"nodes":[{"id":"review","kind":"review","role":"reviewer","title":"review","outcomes":[{"id":"approve","class":"success"},{"id":"return","class":"retryable"}]}],"edges":[]}}`)
		if _, err := svc.BindRole("reviewer", "codex", "review-session", time.Hour, false, "review/bind"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ApplyAction(findActionRef(t, svc, "reviewer", "review", "node.start"), json.RawMessage(`{}`), "review/start"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ApplyAction(findActionRef(t, svc, "reviewer", "review", "review.resolve"), json.RawMessage(`{"outcome":"approve"}`), "review/resolve"); err != nil {
			t.Fatal(err)
		}
		assertLifecycleWriterPrefixes(t, svc, initial)
	})

	t.Run("incident management", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "incident-writer", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"incident-writer"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","incident.manage"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"broken","class":"failure"}]}],"edges":[]}}`)
		if _, err := svc.BindRole("worker", "codex", "incident-session", time.Hour, false, "incident/bind"); err != nil {
			t.Fatal(err)
		}
		started, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "node.start"), json.RawMessage(`{}`), "incident/start")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "task.complete"), json.RawMessage(`{"outcome":"broken"}`), "incident/fail"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ProgressIncident("attempt:"+started.AttemptID, "worker", "bounded retry", false, "incident/progress"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ResolveIncident("attempt:"+started.AttemptID, "worker", "quarantined", "incident/resolve"); err != nil {
			t.Fatal(err)
		}
		assertLifecycleWriterPrefixes(t, svc, initial)
	})

	t.Run("resource closure", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "resource-writer", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"resource-writer"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","resource.close"]}],"resourceCapacities":[{"kind":"browser","capacity":1}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","resources":[{"kind":"browser","quantity":1}],"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
		if _, err := svc.BindRole("worker", "codex", "resource-session", time.Hour, false, "resource/bind"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "node.start"), json.RawMessage(`{}`), "resource/start"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "resource.close"), json.RawMessage(`{"status":"unknown","receipt":true}`), "resource/close"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "resource.reconcile"), json.RawMessage(`{"status":"confirmed","receipt":["closed"]}`), "resource/reconcile"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "attempt.submit"), json.RawMessage(`{}`), "resource/submit"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "task.complete"), json.RawMessage(`{"outcome":"done"}`), "resource/complete"); err != nil {
			t.Fatal(err)
		}
		assertLifecycleWriterPrefixes(t, svc, initial)
	})

	t.Run("effect saga", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "effect-writer", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect-writer"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply","effect.reconcile"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"manual","request":true},"outcomes":[{"id":"done","class":"success"},{"id":"failed","class":"failure"}]}],"edges":[]}}`)
		if _, err := svc.BindRole("worker", "codex", "effect-session", time.Hour, false, "effect/bind"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "effect/start"); err != nil {
			t.Fatal(err)
		}
		prepared, err := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "effect.prepare"), json.RawMessage(`{}`), "effect/prepare")
		if err != nil || prepared.Status != "unknown" {
			t.Fatalf("manual effect did not enter unknown: %+v %v", prepared, err)
		}
		if _, err := svc.ReconcileEffect(prepared.ActionID, json.RawMessage(`{"externalId":"delivery-1","recipientVisible":true,"deliveryStatus":"visible"}`), "effect/reconcile"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "effect.complete"), json.RawMessage(`{"outcome":"done"}`), "effect/complete"); err != nil {
			t.Fatal(err)
		}
		assertLifecycleWriterPrefixes(t, svc, initial)
	})
}

func TestLifecycleRecordProofsAreSingleUseAndClosed(t *testing.T) {
	t.Run("checkpoint proof cannot be reused", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "checkpoint-ledger", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"checkpoint-ledger"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "node.start"), json.RawMessage(`{}`), "start")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "attempt.checkpoint"), json.RawMessage(`{"summary":"durable"}`), "checkpoint")
		records := lifecycleRecordsFromWriter(t, svc, initial.HeadSequence)
		last := &records[len(records)-1]
		var duplicate map[string]any
		_ = json.Unmarshal(last.Events[len(last.Events)-1].Payload, &duplicate)
		duplicate["id"] = "duplicated-action"
		last.Events = append(last.Events, LifecycleMigrationEvent{Type: "action.applied", Payload: payloadJSON(duplicate)})
		if _, err := simulateLifecycleEventSequence(initial, records); err == nil || !strings.Contains(err.Error(), "checkpoint event") {
			t.Fatalf("one checkpoint proof was consumed by two actions: %v", err)
		}
	})

	t.Run("confirmed resource observation requires release and action", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "resource-ledger", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"resource-ledger"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","resource.close"]}],"resourceCapacities":[{"kind":"browser","capacity":1}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","resources":[{"kind":"browser","quantity":1}],"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "node.start"), json.RawMessage(`{}`), "start")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "resource.close"), json.RawMessage(`{"status":"confirmed","receipt":true}`), "close")
		records := lifecycleRecordsFromWriter(t, svc, initial.HeadSequence)
		last := &records[len(records)-1]
		last.Events = []LifecycleMigrationEvent{last.Events[0]}
		if _, err := simulateLifecycleEventSequence(initial, records); err == nil || !strings.Contains(err.Error(), "closed current-writer command") {
			t.Fatalf("orphan confirmed resource observation was accepted: %v", err)
		}
	})

	t.Run("confirmed effect observation requires incident resolution", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "effect-ledger", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect-ledger"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply","effect.reconcile"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"manual","request":true},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
		prepared, _ := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare")
		_, _ = svc.ReconcileEffect(prepared.ActionID, json.RawMessage(`{"externalId":"delivery","recipientVisible":true,"deliveryStatus":"visible"}`), "reconcile")
		records := lifecycleRecordsFromWriter(t, svc, initial.HeadSequence)
		for index := range records {
			filtered := records[index].Events[:0]
			for _, event := range records[index].Events {
				if event.Type != "incident.resolved" {
					filtered = append(filtered, event)
				}
			}
			records[index].Events = filtered
		}
		if _, err := simulateLifecycleEventSequence(initial, records); err == nil || !strings.Contains(err.Error(), "must resolve") {
			t.Fatalf("confirmed effect left its incident open: %v", err)
		}
	})
}

func TestLifecycleIncidentStateMachineAndObservationTimeAreClosed(t *testing.T) {
	t.Run("circuit cannot reopen", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "incident-circuit", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"incident-circuit"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","incident.manage"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"broken","class":"failure"}]}],"edges":[]}}`)
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		started, _ := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "node.start"), json.RawMessage(`{}`), "start")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "task.complete"), json.RawMessage(`{"outcome":"broken"}`), "fail")
		incidentID := "attempt:" + started.AttemptID
		_, _ = svc.ProgressIncident(incidentID, "worker", "no progress", false, "progress-1")
		_, _ = svc.ProgressIncident(incidentID, "worker", "still no progress", false, "progress-2")
		state, _ := svc.State()
		forged := state.Incidents[incidentID]
		forged.Status, forged.CircuitReason = "open", ""
		records := lifecycleRecordsFromWriter(t, svc, initial.HeadSequence)
		records = append(records, LifecycleMigrationRecord{SourceEventID: "forged-reopen", OccurredAt: forged.UpdatedAt, Events: []LifecycleMigrationEvent{{Type: "incident.updated", Payload: payloadJSON(forged)}}})
		if _, err := simulateLifecycleEventSequence(initial, records); err == nil || !strings.Contains(err.Error(), "current writer transition") {
			t.Fatalf("circuit-open incident returned to open: %v", err)
		}
	})

	t.Run("resolution cannot precede confirmed observation", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "resource-time-order", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"resource-time-order"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","resource.close"]}],"resourceCapacities":[{"kind":"browser","capacity":1}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","resources":[{"kind":"browser","quantity":1}],"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
		current := time.Date(2026, 8, 15, 4, 5, 6, 0, time.UTC)
		svc.Now = func() time.Time { return current }
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "node.start"), json.RawMessage(`{}`), "start")
		current = current.Add(time.Minute)
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "resource.close"), json.RawMessage(`{"status":"unknown","receipt":true}`), "close")
		beforeConfirmation := current.Format(time.RFC3339Nano)
		current = current.Add(time.Minute)
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "resource.reconcile"), json.RawMessage(`{"status":"confirmed","receipt":[]}`), "reconcile")
		records := lifecycleRecordsFromWriter(t, svc, initial.HeadSequence)
		for recordIndex := range records {
			for eventIndex := range records[recordIndex].Events {
				if records[recordIndex].Events[eventIndex].Type == "incident.resolved" {
					var value map[string]any
					_ = json.Unmarshal(records[recordIndex].Events[eventIndex].Payload, &value)
					value["resolvedAt"] = beforeConfirmation
					records[recordIndex].Events[eventIndex].Payload = payloadJSON(value)
				}
			}
		}
		if _, err := simulateLifecycleEventSequence(initial, records); err == nil || !strings.Contains(err.Error(), "observation time") {
			t.Fatalf("incident resolved before confirmed observation: %v", err)
		}
	})
}

func TestLifecycleRawJSONWriterShapesMatchPublishedSchema(t *testing.T) {
	for name, receipt := range map[string]string{"object": `{"closed":true}`, "array": `["closed"]`, "scalar": `true`} {
		t.Run("resource-"+name, func(t *testing.T) {
			svc, initial := lifecycleWriterService(t, "resource-"+name, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"resource-shape"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","resource.close"]}],"resourceCapacities":[{"kind":"browser","capacity":1}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","resources":[{"kind":"browser","quantity":1}],"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
			_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
			_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "node.start"), json.RawMessage(`{}`), "start")
			assertResourceActionSchema(t, svc, "worker", "task", "resource.close", json.RawMessage(`{"status":"confirmed","receipt":`+receipt+`}`), true)
			input := json.RawMessage(`{"status":"confirmed","receipt":` + receipt + `}`)
			if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "resource.close"), input, "close"); err != nil {
				t.Fatal(err)
			}
			assertLifecycleWriterPrefixes(t, svc, initial)
		})
	}
	t.Run("resource-null", func(t *testing.T) {
		svc, _ := lifecycleWriterService(t, "resource-null", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"resource-null"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","resource.close"]}],"resourceCapacities":[{"kind":"browser","capacity":1}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","resources":[{"kind":"browser","quantity":1}],"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "node.start"), json.RawMessage(`{}`), "start")
		assertResourceActionSchema(t, svc, "worker", "task", "resource.close", json.RawMessage(`{"status":"confirmed","receipt":null}`), false)
	})
	for name, request := range map[string]string{"object": `{"operation":"deliver"}`, "array": `["deliver"]`, "scalar": `true`} {
		t.Run("effect-"+name, func(t *testing.T) {
			graph := fmt.Sprintf(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect-shape"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply","effect.reconcile"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"manual","request":%s},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`, request)
			svc, initial := lifecycleWriterService(t, "effect-"+name, graph)
			_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
			_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
			if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare"); err != nil {
				t.Fatal(err)
			}
			assertLifecycleWriterPrefixes(t, svc, initial)
		})
	}
}

func TestAutomaticIncidentsRequireConfirmedObservationForResolution(t *testing.T) {
	t.Run("resource", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "resource-resolution", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"resource-resolution"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","resource.close","incident.manage"]}],"resourceCapacities":[{"kind":"browser","capacity":1}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","resources":[{"kind":"browser","quantity":1}],"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "node.start"), json.RawMessage(`{}`), "start")
		closed, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "resource.close"), json.RawMessage(`{"status":"unknown","receipt":true}`), "close")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ResolveIncident(closed.ObjectRef, "worker", "operator guessed closure", "manual-resolve"); err == nil || !strings.Contains(err.Error(), "confirmed observation") {
			t.Fatalf("resource incident was manually resolved: %v", err)
		}
		if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "resource.reconcile"), json.RawMessage(`{"status":"confirmed","receipt":[]}`), "reconcile"); err != nil {
			t.Fatal(err)
		}
		assertLifecycleWriterPrefixes(t, svc, initial)
	})

	t.Run("effect", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "effect-resolution", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect-resolution"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply","effect.reconcile","incident.manage"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"manual","request":true},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
		prepared, err := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ResolveIncident("effect:"+prepared.ActionID, "worker", "operator guessed delivery", "manual-resolve"); err == nil || !strings.Contains(err.Error(), "confirmed observation") {
			t.Fatalf("effect incident was manually resolved: %v", err)
		}
		if _, err := svc.ReconcileEffect(prepared.ActionID, json.RawMessage(`{"externalId":"delivery","recipientVisible":true,"deliveryStatus":"visible"}`), "reconcile"); err != nil {
			t.Fatal(err)
		}
		assertLifecycleWriterPrefixes(t, svc, initial)
	})

	t.Run("resource circuit retry", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "resource-circuit-retry", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"resource-circuit-retry"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","resource.close","incident.manage"]}],"resourceCapacities":[{"kind":"browser","capacity":1}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","resources":[{"kind":"browser","quantity":1}],"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
		current := time.Date(2026, 8, 15, 4, 5, 6, 0, time.UTC)
		svc.Now = func() time.Time { return current }
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "node.start"), json.RawMessage(`{}`), "start")
		closed, _ := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "resource.close"), json.RawMessage(`{"status":"unknown","receipt":true}`), "close")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "resource.reconcile"), json.RawMessage(`{"status":"unknown","receipt":[]}`), "unknown-again")
		state, _ := svc.State()
		if state.Incidents[closed.ObjectRef].Status != "circuit-open" {
			t.Fatalf("resource circuit did not open: %+v", state.Incidents[closed.ObjectRef])
		}
		retried, err := svc.SetIncidentDisposition(closed.ObjectRef, "worker", "retry", "external system recovered", "retry")
		if err != nil {
			t.Fatal(err)
		}
		current = current.Add(30 * time.Minute)
		if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "resource.reconcile"), json.RawMessage(`{"status":"unknown","receipt":{"lookup":"still pending"}}`), "unknown-after-retry"); err != nil {
			t.Fatal(err)
		}
		state, _ = svc.State()
		preserved := state.Incidents[closed.ObjectRef]
		if preserved.Disposition != "retry" || preserved.DispositionBy != "worker" || preserved.DispositionAt != retried.DispositionAt || preserved.Deadline != retried.Deadline {
			t.Fatalf("resource retry audit drifted after unknown observation: before=%+v after=%+v", retried, preserved)
		}
		if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "resource.reconcile"), json.RawMessage(`{"status":"confirmed","receipt":{"closed":true}}`), "confirmed"); err != nil {
			t.Fatal(err)
		}
		assertLifecycleWriterPrefixes(t, svc, initial)
	})

	t.Run("effect circuit retry", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "effect-circuit-retry", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect-circuit-retry"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply","effect.reconcile","incident.manage"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"manual","request":true},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
		current := time.Date(2026, 8, 15, 4, 5, 6, 0, time.UTC)
		svc.Now = func() time.Time { return current }
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
		prepared, _ := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare")
		_, _ = svc.ReconcileEffect(prepared.ActionID, json.RawMessage(`{}`), "unknown-1")
		_, _ = svc.ReconcileEffect(prepared.ActionID, json.RawMessage(`{}`), "unknown-2")
		incidentID := "effect:" + prepared.ActionID
		state, _ := svc.State()
		if state.Incidents[incidentID].Status != "circuit-open" {
			t.Fatalf("effect circuit did not open: %+v", state.Incidents[incidentID])
		}
		retried, err := svc.SetIncidentDisposition(incidentID, "worker", "retry", "delivery lookup restored", "retry")
		if err != nil {
			t.Fatal(err)
		}
		current = current.Add(30 * time.Minute)
		if _, err := svc.ReconcileEffect(prepared.ActionID, json.RawMessage(`{}`), "unknown-after-retry"); err != nil {
			t.Fatal(err)
		}
		state, _ = svc.State()
		preserved := state.Incidents[incidentID]
		if preserved.Disposition != "retry" || preserved.DispositionBy != "worker" || preserved.DispositionAt != retried.DispositionAt || preserved.Deadline != retried.Deadline {
			t.Fatalf("effect retry audit drifted after unknown observation: before=%+v after=%+v", retried, preserved)
		}
		if _, err := svc.ReconcileEffect(prepared.ActionID, json.RawMessage(`{"externalId":"delivery","recipientVisible":true,"deliveryStatus":"visible"}`), "confirmed"); err != nil {
			t.Fatal(err)
		}
		assertLifecycleWriterPrefixes(t, svc, initial)
	})
}

func TestEffectDispatchAndReconcileShareObservationSerialization(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "effect-observation-serialization")
	if err != nil {
		t.Fatal(err)
	}
	adapter := serializedObservationAdapter{dispatchEntered: make(chan struct{}), releaseDispatch: make(chan struct{}), reconcileEntered: make(chan struct{})}
	if err := svc.Providers.RegisterEffect(adapter); err != nil {
		t.Fatal(err)
	}
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect-observation-serialization"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply","effect.reconcile"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"serialized-observation","request":true},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph/import", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	initial, _ := svc.State()
	_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
	_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
	actions, _ := svc.ListActions("worker", "effect")
	var prepare AllowedAction
	for _, action := range actions.Actions {
		if action.Kind == "effect.prepare" {
			prepare = action
		}
	}
	if prepare.ID == "" {
		t.Fatal("effect.prepare action is unavailable")
	}
	applyResult := make(chan error, 1)
	go func() {
		_, applyErr := svc.ApplyAction(prepare.Ref, json.RawMessage(`{}`), "prepare")
		applyResult <- applyErr
	}()
	<-adapter.dispatchEntered
	reconcileResult := make(chan error, 1)
	go func() {
		_, reconcileErr := svc.ReconcileEffect(prepare.ID, json.RawMessage(`{}`), "reconcile")
		reconcileResult <- reconcileErr
	}()
	select {
	case <-adapter.reconcileEntered:
		t.Fatal("reconcile adapter overtook in-flight dispatch")
	case <-time.After(100 * time.Millisecond):
	}
	close(adapter.releaseDispatch)
	if err := <-applyResult; err != nil {
		t.Fatal(err)
	}
	if err := <-reconcileResult; err != nil {
		t.Fatal(err)
	}
	select {
	case <-adapter.reconcileEntered:
		t.Fatal("confirmed dispatch still invoked a stale reconcile observation")
	default:
	}
	state, _ := svc.State()
	if state.Effects[prepare.ID].Status != "confirmed" {
		t.Fatalf("confirmed Effect regressed after concurrent reconcile: %+v", state.Effects[prepare.ID])
	}
	if _, exists := state.Incidents["effect:"+prepare.ID]; exists {
		t.Fatalf("concurrent stale observation opened an Incident: %+v", state.Incidents["effect:"+prepare.ID])
	}
	assertLifecycleWriterPrefixes(t, svc, initial)
}

func TestDaemonOutboxReturnsPendingContinuationAndDrainsAuthorizedEffect(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "daemon-effect-outbox")
	if err != nil {
		t.Fatal(err)
	}
	adapter := serializedObservationAdapter{dispatchEntered: make(chan struct{}), releaseDispatch: make(chan struct{}), reconcileEntered: make(chan struct{})}
	if err := svc.Providers.RegisterEffect(adapter); err != nil {
		t.Fatal(err)
	}
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"daemon-effect-outbox"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply","effect.reconcile"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"serialized-observation","request":true},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph/import", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	initial, _ := svc.State()
	_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
	_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
	prepare := findActionRef(t, svc, "worker", "effect", "effect.prepare")
	outbox := NewDaemonOutbox()
	type applyOutcome struct {
		result ActionResult
		err    error
	}
	applied := make(chan applyOutcome, 1)
	go func() {
		result, applyErr := svc.ApplyActionContext(WithDaemonOutbox(context.Background(), outbox), prepare, json.RawMessage(`{}`), "prepare")
		applied <- applyOutcome{result: result, err: applyErr}
	}()
	var outcome applyOutcome
	select {
	case outcome = <-applied:
	case <-time.After(15 * time.Second):
		close(adapter.releaseDispatch)
		outcome = <-applied
		outbox.Drain()
		t.Fatal("daemon outbox did not return while the authorized Effect was blocked")
	}
	result := outcome.result
	if outcome.err != nil {
		close(adapter.releaseDispatch)
		outbox.Drain()
		t.Fatal(outcome.err)
	}
	if result.Status != "dispatched" || result.Continuation.Owner != "daemon" || !result.Continuation.SafeToWait {
		close(adapter.releaseDispatch)
		outbox.Drain()
		t.Fatalf("long Effect did not return a daemon-owned pending continuation: result=%+v", result)
	}
	select {
	case <-adapter.dispatchEntered:
	case <-time.After(10 * time.Second):
		close(adapter.releaseDispatch)
		outbox.Drain()
		t.Fatal("daemon outbox did not start the authorized dispatch")
	}
	drained := make(chan struct{})
	drainStarted := make(chan struct{})
	go func() {
		close(drainStarted)
		outbox.Drain()
		close(drained)
	}()
	<-drainStarted
	drainReturnedEarly := false
	select {
	case <-drained:
		drainReturnedEarly = true
	case <-time.After(100 * time.Millisecond):
	}
	close(adapter.releaseDispatch)
	select {
	case <-drained:
	case <-time.After(15 * time.Second):
		t.Fatal("daemon outbox did not drain after the Effect observation")
	}
	lockContext, cancelLock := context.WithTimeout(context.Background(), 15*time.Second)
	releaseObservation, lockErr := svc.acquireEffectReconcileLock(lockContext, result.ActionID)
	cancelLock()
	if lockErr != nil {
		t.Fatalf("daemon Effect lock was not released after dispatch cleanup: %v", lockErr)
	}
	releaseObservation()
	if drainReturnedEarly {
		t.Fatal("daemon drain abandoned an in-flight Effect")
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Effects[result.ActionID].Status != "confirmed" {
		t.Fatalf("daemon outbox failed to persist the receipt: %+v", state.Effects[result.ActionID])
	}
	assertLifecycleWriterPrefixes(t, svc, initial)
}

func TestDaemonOwnedEffectDoesNotHideUnrelatedReadyWork(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "daemon-effect-with-ready-work")
	if err != nil {
		t.Fatal(err)
	}
	adapter := serializedObservationAdapter{dispatchEntered: make(chan struct{}), releaseDispatch: make(chan struct{}), reconcileEntered: make(chan struct{})}
	if err := svc.Providers.RegisterEffect(adapter); err != nil {
		t.Fatal(err)
	}
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"daemon-effect-with-ready-work"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply","effect.reconcile","node.run"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"serialized-observation","request":true},"outcomes":[{"id":"done","class":"success"}]},{"id":"unrelated","kind":"task","role":"worker","title":"unrelated","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph/import", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
	_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
	outbox := NewDaemonOutbox()
	result, err := svc.ApplyActionContext(WithDaemonOutbox(context.Background(), outbox), findActionRef(t, svc, "worker", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare")
	if err != nil {
		t.Fatal(err)
	}
	if result.Continuation.Owner != "daemon" || result.Continuation.SafeToWait || !contains(result.Continuation.ReasonCodes, "ready_frontier_nonempty") {
		t.Fatalf("daemon-owned Effect hid unrelated ready work: %+v", result.Continuation)
	}
	select {
	case <-adapter.dispatchEntered:
	case <-time.After(time.Second):
		t.Fatal("daemon outbox did not begin dispatch")
	}
	close(adapter.releaseDispatch)
	outbox.Drain()
}

func TestEffectIncidentMutationWaitsForObservation(t *testing.T) {
	for _, receiptStatus := range []string{"confirmed", "unknown"} {
		t.Run(receiptStatus, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
			svc, err := Init(root, "effect-incident-serialization-"+receiptStatus)
			if err != nil {
				t.Fatal(err)
			}
			adapter := incidentSerializedEffectAdapter{reconcileEntered: make(chan struct{}), releaseReconcile: make(chan struct{}), reconcileStatus: receiptStatus}
			if err := svc.Providers.RegisterEffect(adapter); err != nil {
				t.Fatal(err)
			}
			graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect-incident-serialization"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply","effect.reconcile","incident.manage"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"incident-serialized-effect","request":true},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
			graphPath := filepath.Join(root, "graph.json")
			if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ImportGraph(graphPath, "graph/import", "bootstrap"); err != nil {
				t.Fatal(err)
			}
			initial, _ := svc.State()
			_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
			_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
			prepared, err := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare")
			if err != nil || prepared.Status != "unknown" {
				t.Fatalf("initial unknown dispatch failed: %+v %v", prepared, err)
			}
			incidentID := "effect:" + prepared.ActionID
			reconcileResult := make(chan error, 1)
			go func() {
				_, reconcileErr := svc.ReconcileEffect(prepared.ActionID, json.RawMessage(`{}`), "reconcile")
				reconcileResult <- reconcileErr
			}()
			<-adapter.reconcileEntered
			tripResult := make(chan error, 1)
			go func() {
				_, tripErr := svc.TripIncident(incidentID, "worker", "operator circuit", "trip")
				tripResult <- tripErr
			}()
			select {
			case err := <-tripResult:
				t.Fatalf("incident mutation crossed an in-flight Effect observation: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			close(adapter.releaseReconcile)
			if err := <-reconcileResult; err != nil {
				t.Fatal(err)
			}
			tripErr := <-tripResult
			state, _ := svc.State()
			incident := state.Incidents[incidentID]
			if receiptStatus == "confirmed" {
				if tripErr == nil || incident.Status != "resolved" || state.Effects[prepared.ActionID].Status != "confirmed" {
					t.Fatalf("confirmed observation did not resolve before the serialized trip: incident=%+v effect=%+v err=%v", incident, state.Effects[prepared.ActionID], tripErr)
				}
			} else if tripErr != nil || incident.Status != "circuit-open" || incident.CircuitReason != "operator circuit" || state.Effects[prepared.ActionID].Status != "unknown" {
				t.Fatalf("unknown observation was reopened or lost the serialized circuit: incident=%+v effect=%+v err=%v", incident, state.Effects[prepared.ActionID], tripErr)
			}
			assertLifecycleWriterPrefixes(t, svc, initial)
		})
	}
}

func TestLifecycleEffectMigrationRequiresReconcileAdmission(t *testing.T) {
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect-migration-admission"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply","effect.reconcile","incident.manage"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"manual","request":true},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`

	t.Run("direct observation after deleting reconcile admission", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "effect-migration-direct-observation", graph)
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
		prepared, err := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ReconcileEffect(prepared.ActionID, json.RawMessage(`{"recipientVisible":true,"deliveryStatus":"visible"}`), "reconcile"); err != nil {
			t.Fatal(err)
		}
		records := lifecycleRecordsFromWriter(t, svc, initial.HeadSequence)
		filtered := make([]LifecycleMigrationRecord, 0, len(records)-1)
		removed := false
		for _, record := range records {
			containsReconcile := false
			for _, event := range record.Events {
				containsReconcile = containsReconcile || event.Type == "effect.reconciling"
			}
			if containsReconcile {
				removed = true
				continue
			}
			filtered = append(filtered, record)
		}
		if !removed {
			t.Fatal("writer history did not contain effect.reconciling")
		}
		late := time.Date(2026, 8, 15, 6, 5, 6, 0, time.UTC).Format(time.RFC3339Nano)
		for recordIndex := len(filtered) - 1; recordIndex >= 0; recordIndex-- {
			changed := false
			for eventIndex := range filtered[recordIndex].Events {
				event := &filtered[recordIndex].Events[eventIndex]
				if event.Type != "effect.observed" {
					continue
				}
				var value map[string]any
				_ = json.Unmarshal(event.Payload, &value)
				if value["status"] != "confirmed" {
					continue
				}
				value["updatedAt"] = late
				event.Payload = payloadJSON(value)
				filtered[recordIndex].OccurredAt = late
				changed = true
				break
			}
			if changed {
				break
			}
		}
		if err := validateLifecycleRecordsManifest(t, svc, initial, filtered); err == nil || !strings.Contains(err.Error(), "observation is invalid") {
			t.Fatalf("direct post-lease observation bypassed reconcile admission: %v", err)
		}
	})

	t.Run("circuit blocks reconcile admission", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "effect-migration-circuit", graph)
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
		prepared, err := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = svc.ReconcileEffect(prepared.ActionID, json.RawMessage(`{}`), "unknown-1")
		_, _ = svc.ReconcileEffect(prepared.ActionID, json.RawMessage(`{}`), "unknown-2")
		state, _ := svc.State()
		if state.Incidents["effect:"+prepared.ActionID].Status != "circuit-open" {
			t.Fatal("writer history did not reach the Effect circuit")
		}
		records := lifecycleRecordsFromWriter(t, svc, initial.HeadSequence)
		now := time.Date(2026, 8, 15, 4, 5, 6, 0, time.UTC).Format(time.RFC3339Nano)
		records = append(records, LifecycleMigrationRecord{SourceEventID: "forged-reconcile", OccurredAt: now, Events: []LifecycleMigrationEvent{{Type: "effect.reconciling", Payload: payloadJSON(map[string]any{"actionId": prepared.ActionID, "reconcilingAt": now})}}})
		if err := validateLifecycleRecordsManifest(t, svc, initial, records); err == nil || !strings.Contains(err.Error(), "effect transition is invalid") {
			t.Fatalf("circuit-open Effect accepted reconcile admission without retry: %v", err)
		}
	})

	t.Run("reconciling receipt does not authorize another observation", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "effect-migration-consumed-admission", graph)
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
		prepared, err := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ReconcileEffect(prepared.ActionID, json.RawMessage(`{"externalId":"delivery","recipientVisible":true,"deliveryStatus":"visible"}`), "reconcile"); err != nil {
			t.Fatal(err)
		}
		records := lifecycleRecordsFromWriter(t, svc, initial.HeadSequence)
		changed := false
		for recordIndex := range records {
			filteredEvents := make([]LifecycleMigrationEvent, 0, len(records[recordIndex].Events))
			for _, event := range records[recordIndex].Events {
				if event.Type == "incident.resolved" {
					continue
				}
				if event.Type == "effect.observed" {
					var value map[string]any
					_ = json.Unmarshal(event.Payload, &value)
					if value["status"] == "confirmed" {
						value["status"] = "reconciling"
						value["receipt"] = map[string]any{"status": "reconciling"}
						event.Payload = payloadJSON(value)
						changed = true
					}
				}
				filteredEvents = append(filteredEvents, event)
			}
			records[recordIndex].Events = filteredEvents
		}
		if !changed {
			t.Fatal("writer history did not contain the final confirmed observation")
		}
		now := time.Date(2026, 8, 15, 4, 5, 6, 0, time.UTC).Format(time.RFC3339Nano)
		records = append(records, LifecycleMigrationRecord{SourceEventID: "forged-second-observation", OccurredAt: now, Events: []LifecycleMigrationEvent{
			{Type: "effect.observed", Payload: payloadJSON(map[string]any{"actionId": prepared.ActionID, "status": "confirmed", "receipt": map[string]any{"status": "confirmed"}, "updatedAt": now})},
			{Type: "incident.resolved", Payload: payloadJSON(map[string]any{"incidentId": "effect:" + prepared.ActionID, "resolvedAt": now})},
		}})
		if err := validateLifecycleRecordsManifest(t, svc, initial, records); err == nil || !strings.Contains(err.Error(), "observation is invalid") {
			t.Fatalf("a consumed reconcile admission authorized a second observation: %v", err)
		}
	})
}

func TestLifecycleEffectAuthorityBindsDeclarationReceiptAndFailureClassification(t *testing.T) {
	t.Run("effect declaration and receipt", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "effect-authority", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect-authority"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply","effect.reconcile"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"manual","request":{"operation":"deliver"}},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare")
		base := lifecycleRecordsFromWriter(t, svc, initial.HeadSequence)

		for name, mutate := range map[string]func(map[string]any){
			"adapter": func(value map[string]any) { value["adapterId"] = "forged" },
			"request": func(value map[string]any) { value["request"] = true },
			"prepared adapter": func(value map[string]any) {
				value["prepared"].(map[string]any)["adapterId"] = "forged"
			},
		} {
			t.Run(name, func(t *testing.T) {
				records := cloneLifecycleRecords(t, base)
				mutateLifecycleEventPayload(t, records, "effect.prepared", mutate)
				if _, err := simulateLifecycleEventSequence(initial, records); err == nil || (!strings.Contains(err.Error(), "graph declaration") && !strings.Contains(err.Error(), "binding")) {
					t.Fatalf("forged effect preparation was accepted: %v", err)
				}
			})
		}

		t.Run("receipt status", func(t *testing.T) {
			records := cloneLifecycleRecords(t, base)
			mutateLifecycleEventPayload(t, records, "effect.observed", func(value map[string]any) { value["status"] = "confirmed" })
			if _, err := simulateLifecycleEventSequence(initial, records); err == nil || !strings.Contains(err.Error(), "receipt contradicts") {
				t.Fatalf("contradictory effect receipt was accepted: %v", err)
			}
		})
	})

	t.Run("failure classification", func(t *testing.T) {
		svc, initial := lifecycleWriterService(t, "classification-authority", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"classification-authority"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"broken","class":"failure"}]}],"edges":[]}}`)
		_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "node.start"), json.RawMessage(`{}`), "start")
		_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "task", "task.complete"), json.RawMessage(`{"outcome":"broken","classification":"policy"}`), "complete")
		records := lifecycleRecordsFromWriter(t, svc, initial.HeadSequence)
		mutateLifecycleEventPayload(t, records, "incident.opened", func(value map[string]any) { value["classification"] = "infrastructure" })
		if _, err := simulateLifecycleEventSequence(initial, records); err == nil || !strings.Contains(err.Error(), "deterministic writer companion") {
			t.Fatalf("failure classification substitution was accepted: %v", err)
		}
	})
}

func TestLifecycleResourceReceiptSchemaRejectsNull(t *testing.T) {
	schemaRaw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "lifecycle-migration-v1alpha1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(schemaRaw, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:lifecycle-migration", document); err != nil {
		t.Fatal(err)
	}
	resourceSchema, err := compiler.Compile("urn:dagrail:lifecycle-migration#/$defs/resourceObserved")
	if err != nil {
		t.Fatal(err)
	}
	value := map[string]any{"resourceId": "resource-1", "status": "confirmed", "receipt": nil, "updatedAt": "2026-08-15T01:02:03Z"}
	if err := resourceSchema.Validate(value); err == nil {
		t.Fatal("published lifecycle schema accepted a null Resource receipt")
	}
	effectSchema, err := compiler.Compile("urn:dagrail:lifecycle-migration#/$defs/effect")
	if err != nil {
		t.Fatal(err)
	}
	effect := map[string]any{"id": "effect-1", "nodeId": "effect", "attemptId": "attempt-1", "adapterId": "manual", "adapterVersion": "0.1.0", "adapterSchemaHash": "sha256:manual-effect-v1", "ownerRole": "worker", "status": "prepared", "request": nil, "prepared": map[string]any{"adapterId": "manual", "binding": true}, "idempotencyKey": "prepare", "preparedAt": "2026-08-15T01:02:03Z", "updatedAt": "2026-08-15T01:02:03Z"}
	if err := effectSchema.Validate(effect); err == nil {
		t.Fatal("published lifecycle schema accepted a null Effect request")
	}

	svc, _ := lifecycleWriterService(t, "effect-null", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect-null"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"manual","request":null},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
	_, _ = svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare"); err == nil || !strings.Contains(err.Error(), "cannot be null") {
		t.Fatalf("current writer accepted a null Effect request: %v", err)
	}
}

func TestEffectPrepareCannotCrossRoleLeaseExpiry(t *testing.T) {
	svc, initial := lifecycleWriterService(t, "effect-lease", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect-lease"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"lease-advancing","request":true},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	current := time.Date(2026, 8, 15, 4, 5, 6, 0, time.UTC)
	svc.Now = func() time.Time { return current }
	dispatched := false
	if err := svc.Providers.RegisterEffect(leaseAdvancingEffectAdapter{advance: func() { current = current.Add(3 * time.Minute) }, dispatched: &dispatched}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "session", 2*time.Minute, false, "bind"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "effect.prepare"), json.RawMessage(`{}`), "prepare"); err == nil || (!strings.Contains(err.Error(), "outside its role lease") && !strings.Contains(err.Error(), "expired during effect preparation")) {
		t.Fatalf("slow effect preparation crossed lease expiry: %v", err)
	}
	if dispatched {
		t.Fatal("expired effect preparation reached dispatch")
	}
	state, _ := svc.State()
	if len(state.Effects) != 0 {
		t.Fatal("expired effect preparation committed a prepared effect")
	}
	assertLifecycleWriterPrefixes(t, svc, initial)
}

func TestLeaseBudgetOffersClosedRoleRenewalBeforeSlowEffect(t *testing.T) {
	svc, initial := lifecycleWriterService(t, "effect-renew", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect-renew"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"manual","request":{}},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	if _, err := svc.BindRole("worker", "codex", "session", 90*time.Second, false, "bind"); err != nil {
		t.Fatal(err)
	}
	started, err := svc.ApplyAction(findActionRef(t, svc, "worker", "effect", "node.start"), json.RawMessage(`{}`), "start")
	if err != nil {
		t.Fatal(err)
	}
	if started.Continuation.SafeToWait || !contains(started.Continuation.ReasonCodes, "role_lease_expiring") || len(started.Continuation.NextActions) == 0 || started.Continuation.NextActions[0].Kind != "role.renew" {
		t.Fatalf("lease-expiring continuation did not keep the agent active: %+v", started.Continuation)
	}
	actions, err := svc.ListActions("worker", "effect")
	if err != nil {
		t.Fatal(err)
	}
	var renew AllowedAction
	for _, action := range actions.Actions {
		if action.Kind == "effect.prepare" {
			t.Fatal("slow Effect was executable with insufficient lease budget")
		}
		if action.Kind == "role.renew" {
			renew = action
		}
	}
	if renew.Ref == "" || renew.AttemptID == "" || renew.LeaseRemainingSeconds > 90 || renew.RequiredLeaseSeconds != 0 {
		t.Fatalf("bounded renewal remediation is incomplete: %+v", renew)
	}
	result, err := svc.ApplyAction(renew.Ref, json.RawMessage(`{"ttlSeconds":600}`), "renew")
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "role.renew" || result.Continuation.Owner == "" {
		t.Fatalf("unexpected role renewal result: %+v", result)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	lease := state.Leases["worker"]
	if lease.SessionID != "session" || lease.Harness != "codex" {
		t.Fatalf("renewal changed the executor binding: %+v", lease)
	}
	boundAt, _ := time.Parse(time.RFC3339Nano, lease.BoundAt)
	expiresAt, _ := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
	if expiresAt.Sub(boundAt) != 10*time.Minute {
		t.Fatalf("renewal did not apply the requested closed TTL: %+v", lease)
	}
	actions, err = svc.ListActions("worker", "effect")
	if err != nil {
		t.Fatal(err)
	}
	var prepared AllowedAction
	for _, action := range actions.Actions {
		if action.Kind == "effect.prepare" {
			prepared = action
		}
	}
	if prepared.RequiredLeaseSeconds != 120 || prepared.LeaseRemainingSeconds < prepared.RequiredLeaseSeconds {
		t.Fatalf("renewed slow Effect budget is not executable: %+v", prepared)
	}
	assertLifecycleWriterPrefixes(t, svc, initial)
}

func TestPlannedNodeRoleRenewalIsPortableWithoutAttempt(t *testing.T) {
	svc, initial := lifecycleWriterService(t, "planned-renew", `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"planned-renew"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	if _, err := svc.BindRole("worker", "codex", "session", 3*time.Second, false, "bind"); err != nil {
		t.Fatal(err)
	}
	actions, err := svc.ListActions("worker", "task")
	if err != nil {
		t.Fatal(err)
	}
	var renew AllowedAction
	for _, action := range actions.Actions {
		if action.Kind == "role.renew" {
			renew = action
		}
	}
	if renew.Ref == "" || renew.AttemptID != "" {
		t.Fatalf("planned-node renewal was not Role-scoped: %+v", renew)
	}
	result, err := svc.ApplyAction(renew.Ref, json.RawMessage(`{"ttlSeconds":600}`), "renew")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	recorded := state.Actions[result.ActionID]
	if recorded.Kind != "role.renew" || recorded.AttemptID != "" || state.Leases["worker"].ExpiresAt == "" {
		t.Fatalf("planned-node renewal authority is inconsistent: action=%+v lease=%+v", recorded, state.Leases["worker"])
	}
	assertLifecycleWriterPrefixes(t, svc, initial)
}

func TestGateDecisionRevalidatesAuthorizationAtPersistence(t *testing.T) {
	for _, test := range []struct {
		name        string
		leaseTTL    time.Duration
		wantSuccess bool
	}{
		{name: "provider finishes inside lease", leaseTTL: 3 * time.Minute, wantSuccess: true},
		{name: "provider crosses lease expiry", leaseTTL: time.Minute, wantSuccess: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
			svc, err := Init(root, "gate-authorization")
			if err != nil {
				t.Fatal(err)
			}
			current := time.Date(2026, 8, 15, 4, 5, 6, 0, time.UTC)
			svc.Now = func() time.Time { return current }
			provider := leaseAdvancingPolicy{advance: func() { current = current.Add(2 * time.Minute) }}
			provider.metadata = sdk.Metadata{ID: "test.advancing-gate", Version: "1.0.0", Stability: sdk.StabilityStable}
			provider.metadata.SchemaHash, err = sdk.InputSchemaHash(provider.InputSchema())
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Providers.RegisterPolicy(provider); err != nil {
				t.Fatal(err)
			}
			graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"gate-authorization"},"spec":{"providers":[{"id":"test.advancing-gate","version":"1.0.0","schemaHash":"` + provider.metadata.SchemaHash + `"}],"roles":[{"id":"controller","capabilities":["node.gate"]}],"nodes":[{"id":"quality","kind":"gate","role":"controller","title":"quality","decision":{"key":"quality","source":"provider","providerId":"test.advancing-gate","policyId":"release"},"outcomes":[{"id":"pass","class":"success"}]}],"edges":[]}}`
			graphPath := filepath.Join(root, "graph.json")
			if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ImportGraph(graphPath, "graph/import", "bootstrap"); err != nil {
				t.Fatal(err)
			}
			initial, _ := svc.State()
			if _, err := svc.BindRole("controller", "codex", "session", test.leaseTTL, false, "bind"); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ApplyAction(findActionRef(t, svc, "controller", "quality", "node.start"), json.RawMessage(`{}`), "start"); err != nil {
				t.Fatal(err)
			}
			before, _ := svc.State()
			_, applyErr := svc.ApplyAction(findActionRef(t, svc, "controller", "quality", "gate.evaluate"), json.RawMessage(`{"input":{}}`), "evaluate")
			if test.wantSuccess {
				if applyErr != nil {
					t.Fatal(applyErr)
				}
				state, _ := svc.State()
				if len(state.Decisions) != 1 || state.Nodes["quality"].Status != "terminal" {
					t.Fatalf("gate decision did not commit after revalidation: %+v", state)
				}
			} else {
				if applyErr == nil || !strings.Contains(applyErr.Error(), "outside its action authorization") {
					t.Fatalf("slow gate crossed lease expiry: %v", applyErr)
				}
				after, _ := svc.State()
				if after.HeadHash != before.HeadHash || len(after.Decisions) != 0 || after.Nodes["quality"].Status != "active" {
					t.Fatalf("expired gate decision committed authority: before=%s after=%s", before.HeadHash, after.HeadHash)
				}
			}
			assertLifecycleWriterPrefixes(t, svc, initial)
		})
	}
}

func lifecycleWriterService(t *testing.T, name, graph string) (*Service, domain.State) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, name)
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 8, 15, 4, 5, 6, 0, time.UTC)
	svc.Now = func() time.Time { return fixed }
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph/import", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	initial, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	return svc, initial
}

func assertLifecycleWriterPrefixes(t *testing.T, svc *Service, initial domain.State) {
	t.Helper()
	segments, err := svc.Journal.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	records := make([]LifecycleMigrationRecord, 0, len(segments)-int(initial.HeadSequence))
	for _, segment := range segments[initial.HeadSequence:] {
		events := make([]LifecycleMigrationEvent, 0, len(segment.Events))
		for _, event := range segment.Events {
			candidate := LifecycleMigrationEvent{Type: event.Type, Payload: event.Payload}
			if err := validateMigratableEventPayload(candidate); err != nil {
				t.Fatalf("current writer event %s is outside the migration contract: %v", event.Type, err)
			}
			events = append(events, candidate)
		}
		records = append(records, LifecycleMigrationRecord{SourceEventID: fmt.Sprintf("writer-%d", segment.Sequence), OccurredAt: segment.CommittedAt, Events: events})
	}
	manifest := LifecycleMigrationManifest{APIVersion: LifecycleMigrationAPIVersion, Kind: "LifecycleMigration", ProjectID: initial.ProjectID, GraphRevision: initial.GraphRevision, ExpectedJournalHead: initial.HeadHash, Source: LifecycleMigrationSource{System: "dagrail-writer", Project: "writer-prefix"}, Records: append([]LifecycleMigrationRecord{}, records...)}
	sealLifecycleManifest(t, &manifest)
	validateLifecycleMigrationSchema(t, manifest)
	validation, err := svc.validateLifecycleMigration(initial, segments[:int(initial.HeadSequence)], manifest, manifest.Source.AuthorityHash)
	if err != nil || !validation.Valid {
		t.Fatalf("complete current-writer manifest was rejected: %+v %v", validation, err)
	}
	betaRecords := make([]LifecycleMigrationRecord, 0, len(records))
	for _, record := range records {
		betaRecords = append(betaRecords, LifecycleMigrationRecord{
			SourceSequence:     record.SourceSequence,
			SourceEventID:      record.SourceEventID,
			SourceEventHash:    record.SourceEventHash,
			PreviousSourceHash: record.PreviousSourceHash,
			OccurredAt:         record.OccurredAt,
			Commands:           []LifecycleMigrationCommand{{CommandIndex: 1, Events: record.Events}},
		})
	}
	betaManifest := manifest
	betaManifest.APIVersion = LifecycleMigrationBundleAPIVersion
	betaManifest.Records = betaRecords
	sealLifecycleManifest(t, &betaManifest)
	validateLifecycleMigrationSchema(t, betaManifest)
	betaValidation, betaErr := svc.validateLifecycleMigration(initial, segments[:int(initial.HeadSequence)], betaManifest, betaManifest.Source.AuthorityHash)
	if betaErr != nil || !betaValidation.Valid {
		t.Fatalf("complete current-writer v1beta1 manifest was rejected: %+v %v", betaValidation, betaErr)
	}
	projection, err := svc.LifecycleProjection()
	if err != nil {
		t.Fatal(err)
	}
	validateLifecycleProjectionSchema(t, projection)
	for index := range records {
		simulated, err := simulateLifecycleEventSequence(initial, records[:index+1])
		if err != nil {
			t.Fatalf("current writer prefix %d was rejected: %v", index+1, err)
		}
		actual, err := reduceSegments(initial.ProjectID, segments[:int(initial.HeadSequence)+index+1])
		if err != nil {
			t.Fatal(err)
		}
		normalizeLifecycleSequences(&actual)
		normalizeLifecycleSequences(&simulated)
		if !reflect.DeepEqual(actual.Nodes, simulated.Nodes) || !reflect.DeepEqual(actual.Attempts, simulated.Attempts) || !reflect.DeepEqual(actual.NodeAttempts, simulated.NodeAttempts) || !reflect.DeepEqual(actual.Leases, simulated.Leases) || !reflect.DeepEqual(actual.Checkpoints, simulated.Checkpoints) || !reflect.DeepEqual(actual.Actions, simulated.Actions) || !reflect.DeepEqual(actual.Decisions, simulated.Decisions) || !reflect.DeepEqual(actual.AttemptDecisions, simulated.AttemptDecisions) || !reflect.DeepEqual(actual.EvidencePackages, simulated.EvidencePackages) || !reflect.DeepEqual(actual.AttemptPackages, simulated.AttemptPackages) || !reflect.DeepEqual(actual.ReuseDecisions, simulated.ReuseDecisions) || !reflect.DeepEqual(actual.PackageDecisions, simulated.PackageDecisions) || !reflect.DeepEqual(actual.Effects, simulated.Effects) || !reflect.DeepEqual(actual.Resources, simulated.Resources) || !reflect.DeepEqual(actual.Incidents, simulated.Incidents) || !reflect.DeepEqual(domain.ComputeFrontier(actual), domain.ComputeFrontier(simulated)) {
			t.Fatalf("migration simulator drifted from reducer at writer prefix %d", index+1)
		}
	}
}

func validateLifecycleRecordsManifest(t *testing.T, svc *Service, initial domain.State, records []LifecycleMigrationRecord) error {
	return validateLifecycleRecordsManifestVersion(t, svc, initial, records, LifecycleMigrationAPIVersion)
}

func validateLifecycleRecordsManifestVersion(t *testing.T, svc *Service, initial domain.State, records []LifecycleMigrationRecord, apiVersion string) error {
	t.Helper()
	manifest := lifecycleRecordsManifestVersion(t, initial, records, apiVersion)
	validateLifecycleMigrationSchema(t, manifest)
	segments, err := svc.Journal.ReadAll()
	if err != nil {
		return err
	}
	_, err = svc.validateLifecycleMigration(initial, segments[:int(initial.HeadSequence)], manifest, manifest.Source.AuthorityHash)
	return err
}

func validateLifecycleRecordsManifestAuthorityVersion(t *testing.T, svc *Service, initial domain.State, records []LifecycleMigrationRecord, apiVersion string) error {
	t.Helper()
	manifest := lifecycleRecordsManifestVersion(t, initial, records, apiVersion)
	segments, err := svc.Journal.ReadAll()
	if err != nil {
		return err
	}
	_, err = svc.validateLifecycleMigration(initial, segments[:int(initial.HeadSequence)], manifest, manifest.Source.AuthorityHash)
	return err
}

func lifecycleRecordsManifestVersion(t *testing.T, initial domain.State, records []LifecycleMigrationRecord, apiVersion string) LifecycleMigrationManifest {
	t.Helper()
	manifestRecords := cloneLifecycleRecords(t, records)
	if apiVersion == LifecycleMigrationBundleAPIVersion {
		betaRecords := make([]LifecycleMigrationRecord, 0, len(manifestRecords))
		for _, record := range manifestRecords {
			betaRecords = append(betaRecords, LifecycleMigrationRecord{
				SourceSequence:     record.SourceSequence,
				SourceEventID:      record.SourceEventID,
				SourceEventHash:    record.SourceEventHash,
				PreviousSourceHash: record.PreviousSourceHash,
				OccurredAt:         record.OccurredAt,
				Commands:           []LifecycleMigrationCommand{{CommandIndex: 1, Events: record.Events}},
			})
		}
		manifestRecords = betaRecords
	}
	manifest := LifecycleMigrationManifest{APIVersion: apiVersion, Kind: "LifecycleMigration", ProjectID: initial.ProjectID, GraphRevision: initial.GraphRevision, ExpectedJournalHead: initial.HeadHash, Source: LifecycleMigrationSource{System: "adversarial-source", Project: "effect-admission"}, Records: manifestRecords}
	sealLifecycleManifest(t, &manifest)
	return manifest
}

func lifecycleRecordsFromWriter(t *testing.T, svc *Service, initialSequence uint64) []LifecycleMigrationRecord {
	t.Helper()
	segments, err := svc.Journal.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	records := make([]LifecycleMigrationRecord, 0, len(segments)-int(initialSequence))
	for _, segment := range segments[initialSequence:] {
		events := make([]LifecycleMigrationEvent, 0, len(segment.Events))
		for _, event := range segment.Events {
			events = append(events, LifecycleMigrationEvent{Type: event.Type, Payload: append(json.RawMessage(nil), event.Payload...)})
		}
		records = append(records, LifecycleMigrationRecord{SourceEventID: fmt.Sprintf("writer-%d", segment.Sequence), OccurredAt: segment.CommittedAt, Events: events})
	}
	return records
}

func payloadJSON(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func assertResourceActionSchema(t *testing.T, svc *Service, roleID, nodeID, kind string, input json.RawMessage, wantValid bool) {
	t.Helper()
	actions, err := svc.ListActions(roleID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range actions.Actions {
		if action.Kind != kind {
			continue
		}
		schemaRaw, _ := json.Marshal(action.InputSchema)
		var schemaDocument, instance any
		_ = json.Unmarshal(schemaRaw, &schemaDocument)
		_ = json.Unmarshal(input, &instance)
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		if err := compiler.AddResource("urn:dagrail:allowed-action", schemaDocument); err != nil {
			t.Fatal(err)
		}
		schema, err := compiler.Compile("urn:dagrail:allowed-action")
		if err != nil {
			t.Fatal(err)
		}
		validationErr := schema.Validate(instance)
		if wantValid && validationErr != nil {
			t.Fatalf("allowed action schema rejected runtime-valid input: %v", validationErr)
		}
		if !wantValid && validationErr == nil {
			t.Fatal("allowed action schema accepted runtime-invalid null receipt")
		}
		return
	}
	t.Fatalf("action %s unavailable", kind)
}

func cloneLifecycleRecords(t *testing.T, records []LifecycleMigrationRecord) []LifecycleMigrationRecord {
	t.Helper()
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	var clone []LifecycleMigrationRecord
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func mutateLifecycleEventPayload(t *testing.T, records []LifecycleMigrationRecord, eventType string, mutate func(map[string]any)) {
	t.Helper()
	for recordIndex := range records {
		for eventIndex := range records[recordIndex].Events {
			event := &records[recordIndex].Events[eventIndex]
			if event.Type != eventType {
				continue
			}
			var value map[string]any
			if err := json.Unmarshal(event.Payload, &value); err != nil {
				t.Fatal(err)
			}
			mutate(value)
			event.Payload = payloadJSON(value)
			return
		}
	}
	t.Fatalf("event %s is unavailable", eventType)
}

func normalizeLifecycleSequences(state *domain.State) {
	for id, action := range state.Actions {
		action.Sequence = 0
		state.Actions[id] = action
	}
	for id, decision := range state.Decisions {
		decision.Sequence = 0
		state.Decisions[id] = decision
	}
	for id, pack := range state.EvidencePackages {
		pack.Sequence = 0
		state.EvidencePackages[id] = pack
	}
	for id, decision := range state.ReuseDecisions {
		decision.Sequence = 0
		state.ReuseDecisions[id] = decision
	}
	for id, effect := range state.Effects {
		effect.Sequence = 0
		state.Effects[id] = effect
	}
}

func lifecycleValidationState(graph *domain.GraphDefinition, projectID, revisionCharacter string) domain.State {
	state := domain.NewState(projectID)
	state.Graph, state.GraphRevision = graph, strings.Repeat(revisionCharacter, 64)
	for _, node := range graph.Spec.Nodes {
		state.Nodes[node.ID] = domain.NodeRuntime{Status: "planned"}
	}
	return state
}

func TestLifecycleHistoryImportRejectsPrivateSourceLocator(t *testing.T) {
	for _, locator := range []string{"/private/workspace/source-project", "../private/workspace", `..\private\workspace`, "https:private.example/path", "org/project", "project..shadow"} {
		t.Run(locator, func(t *testing.T) {
			svc := lifecycleService(t)
			state, _ := svc.State()
			manifest := lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
			manifest.Source.Project = locator
			if _, err := svc.ValidateLifecycleMigration(manifest, manifest.Source.AuthorityHash); err == nil || !strings.Contains(err.Error(), "source authority is incomplete") {
				t.Fatalf("private source locator entered portable lifecycle authority: %v", err)
			}
		})
	}
}

func TestLifecycleProjectionHelpersRemoveExternalLocators(t *testing.T) {
	artifact := redactedLifecycleArtifact(domain.ArtifactRef{Digest: digest("c"), Type: "candidate", Size: 12, URI: "file:///private/source", Provenance: domain.ArtifactProvenance{Producer: "git", Revision: "abc"}})
	refs := redactedEvidenceRefs([]domain.EvidenceRef{{Digest: digest("d"), Type: "report", Size: 8, URI: "https://private.example/report"}})
	raw, _ := json.Marshal(map[string]any{"artifact": artifact, "refs": refs})
	if strings.Contains(string(raw), "private") || len(refs) != 1 || refs[0].URI != "" {
		t.Fatalf("lifecycle projection retained an external locator: %s", raw)
	}
}

func TestLifecycleProjectionSchemaRejectsEvidenceURI(t *testing.T) {
	schemaRaw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "lifecycle-projection-v1alpha1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(schemaRaw, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:lifecycle-projection", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:lifecycle-projection#/$defs/evidenceRef")
	if err != nil {
		t.Fatal(err)
	}
	value := map[string]any{"digest": digest("a"), "type": "report", "size": float64(1), "uri": "https://private.example/report"}
	if err := schema.Validate(value); err == nil {
		t.Fatal("published redacted projection schema accepted an evidence URI")
	}
}

func TestLifecycleProjectionSchemaClosesActionAndIncidentVocabulary(t *testing.T) {
	schemaRaw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "lifecycle-projection-v1alpha1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(schemaRaw, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:lifecycle-projection", document); err != nil {
		t.Fatal(err)
	}
	t.Run("action kind and status", func(t *testing.T) {
		schema, err := compiler.Compile("urn:dagrail:lifecycle-projection#/$defs/action")
		if err != nil {
			t.Fatal(err)
		}
		value := map[string]any{"id": "action-1", "kind": "project.accept", "nodeId": "task", "attemptId": "attempt-1", "status": "project-done", "sequence": float64(1)}
		if err := schema.Validate(value); err == nil {
			t.Fatal("published projection schema accepted project-specific action vocabulary")
		}
	})
	t.Run("incident source type", func(t *testing.T) {
		schema, err := compiler.Compile("urn:dagrail:lifecycle-projection#/$defs/incident")
		if err != nil {
			t.Fatal(err)
		}
		value := map[string]any{"id": "incident-1", "sourceType": "project-stage", "sourceId": "stage-1", "status": "open", "classification": "unknown", "attemptBudget": float64(1), "attempts": float64(0), "openedAt": "2026-08-15T01:02:03Z", "updatedAt": "2026-08-15T01:02:03Z"}
		if err := schema.Validate(value); err == nil {
			t.Fatal("published projection schema accepted a project-specific incident source type")
		}
	})
}

func lifecycleService(t *testing.T) *Service {
	t.Helper()
	t.Setenv("DAGRAIL_HOME", t.TempDir())
	root := t.TempDir()
	svc, err := Init(root, "lifecycle-migration")
	if err != nil {
		t.Fatal(err)
	}
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"migration"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"done","class":"success"}]},{"id":"complete","kind":"milestone","title":"complete","outcomes":[{"id":"complete","class":"success"}]}],"edges":[{"id":"task-complete","from":"task","to":"complete","when":{"outcome":"done"}}]}}`
	path := filepath.Join(root, "graph.json")
	if err := os.WriteFile(path, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(path, "graph/import", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	return svc
}

func adoptedLifecycleService(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "data"))
	svc, err := Init(root, "adopted-lifecycle-migration")
	if err != nil {
		t.Fatal(err)
	}
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"migration"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"done","class":"success"}]},{"id":"complete","kind":"milestone","title":"complete","outcomes":[{"id":"complete","class":"success"}]}],"edges":[{"id":"task-complete","from":"task","to":"complete","when":{"outcome":"done"}}]}}`
	path := filepath.Join(root, "graph.json")
	if err := os.WriteFile(path, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(path, "graph/import", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	previousProjectID := svc.Project.Config.ProjectID
	previousHead := simulatePreV022Authority(t, svc, testAuthorityHome(t))
	receipt, err := svc.AdoptLegacyAuthority(previousProjectID, previousHead, "adopt lifecycle fixture", "adopt/lifecycle-fixture")
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := Open(root)
	if err != nil || replacement.Project.Config.ProjectID != receipt.ReplacementProjectID {
		t.Fatalf("open replacement lifecycle authority: %+v %v", replacement, err)
	}
	if _, err := replacement.ImportGraph(path, "graph/import/replacement", "bootstrap"); err != nil {
		t.Fatalf("bootstrap replacement graph: %v", err)
	}
	return replacement
}

func lifecycleManifest(t *testing.T, projectID, graphRevision, head string) LifecycleMigrationManifest {
	t.Helper()
	now := time.Date(2026, 8, 15, 1, 2, 3, 0, time.UTC).Format(time.RFC3339Nano)
	attempt := map[string]any{"id": "attempt-1", "nodeId": "task", "roleId": "worker", "number": 1, "status": "leased", "startedAt": now, "updatedAt": now}
	finish := map[string]any{"attemptId": "attempt-1", "outcome": "done", "outcomeClass": "success", "facts": map[string]any{}, "updatedAt": now}
	payload := func(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
	records := []LifecycleMigrationRecord{
		{SourceSequence: 1, SourceEventID: "source-1", OccurredAt: now, Events: []LifecycleMigrationEvent{{Type: "role.bound", Payload: payload(map[string]any{"roleId": "worker", "harness": "manual", "sessionId": "source-session", "boundAt": now, "expiresAt": "2026-08-16T01:02:03Z", "active": true})}}},
		{SourceSequence: 2, SourceEventID: "source-2", OccurredAt: now, Events: []LifecycleMigrationEvent{
			{Type: "attempt.leased", Payload: payload(attempt)},
			{Type: "attempt.status-changed", Payload: payload(map[string]any{"attemptId": "attempt-1", "status": "running", "updatedAt": now})},
			{Type: "action.applied", Payload: payload(map[string]any{"id": "action-start", "kind": "node.start", "nodeId": "task", "attemptId": "attempt-1", "status": "confirmed", "input": map[string]any{}})},
		}},
		{SourceSequence: 3, SourceEventID: "source-3", OccurredAt: now, Events: []LifecycleMigrationEvent{
			{Type: "attempt.status-changed", Payload: payload(map[string]any{"attemptId": "attempt-1", "status": "submitted", "updatedAt": now})},
			{Type: "action.applied", Payload: payload(map[string]any{"id": "action-submit", "kind": "attempt.submit", "nodeId": "task", "attemptId": "attempt-1", "status": "confirmed", "input": map[string]any{}})},
		}},
		{SourceSequence: 4, SourceEventID: "source-4", OccurredAt: now, Events: []LifecycleMigrationEvent{
			{Type: "attempt.finished", Payload: payload(finish)},
			{Type: "action.applied", Payload: payload(map[string]any{"id": "action-complete", "kind": "task.complete", "nodeId": "task", "attemptId": "attempt-1", "status": "confirmed", "input": map[string]any{"outcome": "done"}})},
		}},
	}
	manifest := LifecycleMigrationManifest{APIVersion: LifecycleMigrationAPIVersion, Kind: "LifecycleMigration", ProjectID: projectID, GraphRevision: graphRevision, ExpectedJournalHead: head, Source: LifecycleMigrationSource{System: "external-registry", Project: "source-project"}, Records: records}
	sealLifecycleManifest(t, &manifest)
	return manifest
}

func sealLifecycleManifest(t *testing.T, manifest *LifecycleMigrationManifest) {
	t.Helper()
	previous := ""
	for index := range manifest.Records {
		manifest.Records[index].SourceSequence = uint64(index + 1)
		manifest.Records[index].PreviousSourceHash = previous
		hash, err := LifecycleSourceEventHash(manifest.Records[index])
		if err != nil {
			t.Fatal(err)
		}
		manifest.Records[index].SourceEventHash = hash
		previous = hash
	}
	manifest.Source.HeadSequence = uint64(len(manifest.Records))
	manifest.Source.HeadEventID = manifest.Records[len(manifest.Records)-1].SourceEventID
	manifest.Source.HeadEventHash = previous
	recordsDigest, err := LifecycleRecordsDigest(manifest.Records)
	if err != nil {
		t.Fatal(err)
	}
	manifest.RecordsDigest = recordsDigest
	manifest.Source.AuthorityHash = ""
	authorityHash, err := LifecycleSourceAuthorityHash(*manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Source.AuthorityHash = authorityHash
}

func TestLifecyclePublicHashHelpersRejectDuplicateSourceIdentity(t *testing.T) {
	svc := lifecycleService(t)
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	manifest := lifecycleManifest(t, state.ProjectID, state.GraphRevision, state.HeadHash)
	manifest.Records[1].SourceEventID = manifest.Records[0].SourceEventID
	previous := ""
	for index := range manifest.Records {
		manifest.Records[index].PreviousSourceHash = previous
		hash, err := LifecycleSourceEventHash(manifest.Records[index])
		if err != nil {
			t.Fatal(err)
		}
		manifest.Records[index].SourceEventHash = hash
		previous = hash
	}
	manifest.Source.HeadEventID = manifest.Records[len(manifest.Records)-1].SourceEventID
	manifest.Source.HeadEventHash = previous
	if _, err := LifecycleRecordsDigest(manifest.Records); err == nil || !strings.Contains(err.Error(), "repeats source identity") {
		t.Fatalf("records digest accepted duplicate sourceEventId: %v", err)
	}
	if _, err := LifecycleSourceAuthorityHash(manifest); err == nil || !strings.Contains(err.Error(), "repeats source identity") {
		t.Fatalf("authority hash accepted duplicate sourceEventId: %v", err)
	}
}

func digest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
