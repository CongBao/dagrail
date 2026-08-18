package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/google/uuid"
	"github.com/gowebpki/jcs"
)

const (
	// LifecycleMigrationAPIVersion is the legacy single-command record contract.
	LifecycleMigrationAPIVersion = "dagrail.io/lifecycle-migration/v1alpha1"
	// LifecycleMigrationBundleAPIVersion adds ordered, independently closed
	// commands to one immutable source record without weakening v1alpha1.
	LifecycleMigrationBundleAPIVersion = "dagrail.io/lifecycle-migration/v1beta1"
	LifecycleProjectionAPIVersion      = "dagrail.io/lifecycle-projection/v1alpha1"
	maxMigrationRecords                = 10_000
	maxMigrationCommandsPerRecord      = 128
)

type LifecycleMigrationSource struct {
	System        string `json:"system"`
	Project       string `json:"project"`
	AuthorityHash string `json:"authorityHash"`
	HeadSequence  uint64 `json:"headSequence"`
	HeadEventID   string `json:"headEventId"`
	HeadEventHash string `json:"headEventHash"`
}

type LifecycleMigrationEvent struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// LifecycleMigrationCommand is one current-writer-equivalent command inside a
// source record. CommandIndex is contiguous and one-based. Its events have an
// independent proof ledger and must be closed before the next command begins.
type LifecycleMigrationCommand struct {
	CommandIndex uint64                    `json:"commandIndex"`
	Events       []LifecycleMigrationEvent `json:"events"`
}

type LifecycleMigrationRecord struct {
	SourceSequence     uint64                      `json:"sourceSequence"`
	SourceEventID      string                      `json:"sourceEventId"`
	SourceEventHash    string                      `json:"sourceEventHash"`
	PreviousSourceHash string                      `json:"previousSourceEventHash,omitempty"`
	OccurredAt         string                      `json:"occurredAt"`
	Events             []LifecycleMigrationEvent   `json:"events,omitempty"`
	Commands           []LifecycleMigrationCommand `json:"commands,omitempty"`
	eventsPresent      bool
	commandsPresent    bool
}

func (record *LifecycleMigrationRecord) UnmarshalJSON(data []byte) error {
	type wire LifecycleMigrationRecord
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("lifecycle migration record has trailing content")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*record = LifecycleMigrationRecord(decoded)
	_, record.eventsPresent = fields["events"]
	_, record.commandsPresent = fields["commands"]
	return nil
}

type LifecycleMigrationManifest struct {
	APIVersion          string                     `json:"apiVersion"`
	Kind                string                     `json:"kind"`
	ProjectID           string                     `json:"projectId"`
	GraphRevision       string                     `json:"graphRevision"`
	ExpectedJournalHead string                     `json:"expectedJournalHead"`
	Source              LifecycleMigrationSource   `json:"source"`
	RecordsDigest       string                     `json:"recordsDigest"`
	Records             []LifecycleMigrationRecord `json:"records"`
}

type LifecycleMigrationValidation struct {
	APIVersion       string `json:"apiVersion"`
	Kind             string `json:"kind"`
	Valid            bool   `json:"valid"`
	MigrationID      string `json:"migrationId"`
	RecordsDigest    string `json:"recordsDigest"`
	RecordCount      int    `json:"recordCount"`
	NativeEventCount int    `json:"nativeEventCount"`
	GraphRevision    string `json:"graphRevision"`
	ExpectedHead     string `json:"expectedJournalHead"`
}

type LifecycleNodeProjection struct {
	ID           string                `json:"id"`
	Status       string                `json:"status"`
	Outcome      string                `json:"outcome,omitempty"`
	OutcomeClass string                `json:"outcomeClass,omitempty"`
	Facts        domain.PredicateFacts `json:"facts,omitempty"`
}

type LifecycleEffectProjection struct {
	ID                string `json:"id"`
	NodeID            string `json:"nodeId"`
	AttemptID         string `json:"attemptId"`
	AdapterID         string `json:"adapterId"`
	AdapterVersion    string `json:"adapterVersion,omitempty"`
	AdapterSchemaHash string `json:"adapterSchemaHash,omitempty"`
	OwnerRole         string `json:"ownerRole,omitempty"`
	Status            string `json:"status"`
	ReceiptDigest     string `json:"receiptDigest,omitempty"`
	Sequence          uint64 `json:"sequence"`
}

type LifecycleResourceProjection struct {
	ID                   string `json:"id"`
	Kind                 string `json:"kind"`
	Quantity             int    `json:"quantity"`
	NodeID               string `json:"nodeId"`
	AttemptID            string `json:"attemptId"`
	RoleID               string `json:"roleId,omitempty"`
	Status               string `json:"status"`
	ClosureStatus        string `json:"closureStatus,omitempty"`
	ClosureReceiptDigest string `json:"closureReceiptDigest,omitempty"`
	LeasedAt             string `json:"leasedAt"`
	ReleasedAt           string `json:"releasedAt,omitempty"`
}

type LifecycleArtifactProjection struct {
	Digest           string `json:"digest"`
	Type             string `json:"type"`
	Size             int64  `json:"size"`
	Producer         string `json:"producer"`
	Revision         string `json:"revision,omitempty"`
	InvocationDigest string `json:"invocationDigest,omitempty"`
}

type LifecycleExecutionProjection struct {
	ID                 string                        `json:"id"`
	ProjectID          string                        `json:"projectId"`
	GraphRevision      string                        `json:"graphRevision"`
	NodeID             string                        `json:"nodeId"`
	AttemptID          string                        `json:"attemptId"`
	NodeContractDigest string                        `json:"nodeContractDigest"`
	Candidate          LifecycleArtifactProjection   `json:"candidate"`
	ProspectiveTree    LifecycleArtifactProjection   `json:"prospectiveTree"`
	CommandGraphDigest string                        `json:"commandGraphDigest"`
	ProtectedInputs    []domain.ProtectedInput       `json:"protectedInputs"`
	Observations       domain.ExecutionObservations  `json:"observations"`
	Artifacts          []LifecycleArtifactProjection `json:"artifacts"`
	CoreDigest         string                        `json:"coreDigest"`
	CreatedAt          string                        `json:"createdAt"`
	Sequence           uint64                        `json:"sequence"`
}

type LifecycleProjection struct {
	APIVersion       string                             `json:"apiVersion"`
	Kind             string                             `json:"kind"`
	ProjectID        string                             `json:"projectId"`
	GraphRevision    string                             `json:"graphRevision"`
	JournalHead      string                             `json:"journalHead"`
	HeadSequence     uint64                             `json:"headSequence"`
	Migrations       []domain.LifecycleMigrationReceipt `json:"migrations"`
	Nodes            []LifecycleNodeProjection          `json:"nodes"`
	Attempts         []domain.Attempt                   `json:"attempts"`
	Leases           []domain.RoleLease                 `json:"leases"`
	Checkpoints      []domain.Checkpoint                `json:"checkpoints"`
	Decisions        []domain.DecisionRecord            `json:"decisions"`
	EvidencePackages []LifecycleExecutionProjection     `json:"evidencePackages"`
	ReuseDecisions   []domain.ReuseDecision             `json:"reuseDecisions"`
	Actions          []domain.ActionRecord              `json:"actions"`
	Effects          []LifecycleEffectProjection        `json:"effects"`
	Resources        []LifecycleResourceProjection      `json:"resources"`
	Incidents        []domain.Incident                  `json:"incidents"`
	Frontier         domain.Frontier                    `json:"frontier"`
	ProjectionDigest string                             `json:"projectionDigest,omitempty"`
}

var migratableEventTypes = map[string]bool{
	"role.bound": true, "role.released": true,
	"attempt.started": true, "attempt.leased": true, "attempt.status-changed": true, "attempt.checkpointed": true, "attempt.finished": true,
	"evidence.package-published": true, "evidence.reuse-assessed": true, "decision.recorded": true,
	"resource.leased": true, "resource.closure-observed": true, "resource.released": true,
	"action.applied": true, "effect.prepared": true, "effect.dispatched": true, "effect.reconciling": true, "effect.observed": true,
	"incident.opened": true, "incident.updated": true, "incident.resolved": true,
}

func MigratableLifecycleEventTypes() []string {
	result := make([]string, 0, len(migratableEventTypes))
	for eventType := range migratableEventTypes {
		result = append(result, eventType)
	}
	sort.Strings(result)
	return result
}

func LifecycleRecordsDigest(records []LifecycleMigrationRecord) (string, error) {
	apiVersion := LifecycleMigrationAPIVersion
	if lifecycleRecordsUseBundles(records) {
		apiVersion = LifecycleMigrationBundleAPIVersion
	}
	if err := validateLifecycleHashRecords(apiVersion, records, true); err != nil {
		return "", err
	}
	raw, err := json.Marshal(records)
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", err
	}
	hashDomain := "dagrail-lifecycle-migration-records-v1\x00"
	if apiVersion == LifecycleMigrationBundleAPIVersion {
		hashDomain = "dagrail-lifecycle-migration-records-v2\x00"
	}
	sum := sha256.Sum256(append([]byte(hashDomain), canonical...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func lifecycleRecordsUseBundles(records []LifecycleMigrationRecord) bool {
	for _, record := range records {
		if record.commandsPresent || len(record.Commands) != 0 {
			return true
		}
	}
	return false
}

func validateLifecycleHashRecords(apiVersion string, records []LifecycleMigrationRecord, requireSourceEventHash bool) error {
	if len(records) == 0 || len(records) > maxMigrationRecords {
		return fmt.Errorf("lifecycle migration hash input must contain 1..%d records", maxMigrationRecords)
	}
	previousHash := ""
	previousTime := time.Time{}
	seenIDs := map[string]bool{}
	seenHashes := map[string]bool{}
	for recordIndex, record := range records {
		if record.SourceSequence == 0 || record.SourceSequence > maxMigrationRecords {
			return fmt.Errorf("lifecycle migration record %d sourceSequence is invalid", recordIndex+1)
		}
		if !portableExternalID(record.SourceEventID, 512) {
			return fmt.Errorf("lifecycle migration record %d sourceEventId is invalid", recordIndex+1)
		}
		if record.PreviousSourceHash != "" && !validDigest(record.PreviousSourceHash) {
			return fmt.Errorf("lifecycle migration record %d previousSourceEventHash is invalid", recordIndex+1)
		}
		occurredAt, err := time.Parse(time.RFC3339Nano, record.OccurredAt)
		if err != nil {
			return fmt.Errorf("lifecycle migration record %d occurredAt is invalid", recordIndex+1)
		}
		if requireSourceEventHash {
			if record.SourceSequence != uint64(recordIndex+1) || record.PreviousSourceHash != previousHash || (!previousTime.IsZero() && occurredAt.Before(previousTime)) {
				return fmt.Errorf("lifecycle migration record %d is not a contiguous source prefix", recordIndex+1)
			}
			if !validDigest(record.SourceEventHash) {
				return fmt.Errorf("lifecycle migration record %d sourceEventHash is invalid", recordIndex+1)
			}
			if seenIDs[record.SourceEventID] || seenHashes[record.SourceEventHash] {
				return fmt.Errorf("lifecycle migration record %d repeats source identity", recordIndex+1)
			}
		}
		commands, err := lifecycleRecordCommands(apiVersion, record)
		if err != nil {
			return fmt.Errorf("lifecycle migration record %d: %w", recordIndex+1, err)
		}
		for commandIndex, command := range commands {
			for eventIndex, event := range command.Events {
				if !migratableEventTypes[event.Type] {
					return fmt.Errorf("lifecycle migration record %d command %d event %d has unsupported native event %q", recordIndex+1, commandIndex+1, eventIndex+1, event.Type)
				}
				if err := domain.ValidateAuthorityJSON(event.Payload); err != nil {
					return fmt.Errorf("lifecycle migration record %d command %d event %d payload: %w", recordIndex+1, commandIndex+1, eventIndex+1, err)
				}
				if err := domain.RejectSensitiveFields(event.Payload); err != nil {
					return fmt.Errorf("lifecycle migration record %d command %d event %d payload: %w", recordIndex+1, commandIndex+1, eventIndex+1, err)
				}
				if err := validateMigratableEventPayload(event); err != nil {
					return fmt.Errorf("lifecycle migration record %d command %d event %d payload: %w", recordIndex+1, commandIndex+1, eventIndex+1, err)
				}
			}
		}
		if requireSourceEventHash {
			eventHash, err := lifecycleSourceEventHash(record, apiVersion)
			if err != nil || eventHash != record.SourceEventHash {
				return fmt.Errorf("lifecycle migration record %d sourceEventHash does not bind its preimage", recordIndex+1)
			}
			previousHash = record.SourceEventHash
			previousTime = occurredAt
			seenIDs[record.SourceEventID] = true
			seenHashes[record.SourceEventHash] = true
		}
	}
	return nil
}

func lifecycleRecordCommands(apiVersion string, record LifecycleMigrationRecord) ([]LifecycleMigrationCommand, error) {
	switch apiVersion {
	case LifecycleMigrationAPIVersion:
		if record.commandsPresent || len(record.Commands) != 0 || len(record.Events) == 0 || len(record.Events) > 128 {
			return nil, fmt.Errorf("v1alpha1 requires exactly one bounded events mapping and forbids commands")
		}
		return []LifecycleMigrationCommand{{CommandIndex: 1, Events: record.Events}}, nil
	case LifecycleMigrationBundleAPIVersion:
		if record.eventsPresent || len(record.Events) != 0 || len(record.Commands) == 0 || len(record.Commands) > maxMigrationCommandsPerRecord {
			return nil, fmt.Errorf("v1beta1 requires 1..%d commands and forbids record-level events", maxMigrationCommandsPerRecord)
		}
		for index, command := range record.Commands {
			if command.CommandIndex != uint64(index+1) || len(command.Events) == 0 || len(command.Events) > 128 {
				return nil, fmt.Errorf("commandIndex must be contiguous from one and each command must contain 1..128 events")
			}
		}
		return record.Commands, nil
	default:
		return nil, fmt.Errorf("unsupported lifecycle migration contract")
	}
}

// LifecycleSourceEventHash binds one normalized source record to its native
// DAGrail mapping and the previous normalized record. Source-specific
// converters may retain their original hashes as external evidence, but the
// portable migration chain always uses this deterministic algorithm.
func LifecycleSourceEventHash(record LifecycleMigrationRecord) (string, error) {
	apiVersion := LifecycleMigrationAPIVersion
	if record.commandsPresent || len(record.Commands) != 0 {
		apiVersion = LifecycleMigrationBundleAPIVersion
	}
	if err := validateLifecycleHashRecords(apiVersion, []LifecycleMigrationRecord{record}, false); err != nil {
		return "", err
	}
	return lifecycleSourceEventHash(record, apiVersion)
}

func lifecycleSourceEventHash(record LifecycleMigrationRecord, apiVersion string) (string, error) {
	statement := struct {
		SourceSequence     uint64                      `json:"sourceSequence"`
		SourceEventID      string                      `json:"sourceEventId"`
		PreviousSourceHash string                      `json:"previousSourceEventHash,omitempty"`
		OccurredAt         string                      `json:"occurredAt"`
		Events             []LifecycleMigrationEvent   `json:"events,omitempty"`
		Commands           []LifecycleMigrationCommand `json:"commands,omitempty"`
	}{record.SourceSequence, record.SourceEventID, record.PreviousSourceHash, record.OccurredAt, record.Events, record.Commands}
	raw, err := json.Marshal(statement)
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", err
	}
	hashDomain := "dagrail-lifecycle-source-event-v1\x00"
	if apiVersion == LifecycleMigrationBundleAPIVersion {
		hashDomain = "dagrail-lifecycle-source-event-v2\x00"
	}
	sum := sha256.Sum256(append([]byte(hashDomain), canonical...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// LifecycleSourceAuthorityHash binds the complete mapped source prefix and its
// exact target graph/head. The authorityHash field itself is cleared before
// canonicalization to avoid a circular digest. Operators must obtain this
// digest through a channel independent of the manifest supplied for import.
func LifecycleSourceAuthorityHash(manifest LifecycleMigrationManifest) (string, error) {
	if !oneOf(manifest.APIVersion, LifecycleMigrationAPIVersion, LifecycleMigrationBundleAPIVersion) {
		return "", fmt.Errorf("unsupported lifecycle migration contract")
	}
	if manifest.Kind != "LifecycleMigration" {
		return "", fmt.Errorf("lifecycle migration kind is invalid")
	}
	if parsed, err := uuid.Parse(manifest.ProjectID); err != nil || parsed.String() != manifest.ProjectID {
		return "", fmt.Errorf("lifecycle migration projectId is invalid")
	}
	if !validBareHash(manifest.GraphRevision) || !validBareHash(manifest.ExpectedJournalHead) {
		return "", fmt.Errorf("lifecycle migration target hashes are invalid")
	}
	if !portableExternalID(manifest.Source.System, 256) || !portableExternalID(manifest.Source.Project, 512) || manifest.Source.HeadSequence == 0 || manifest.Source.HeadSequence > maxMigrationRecords || !portableExternalID(manifest.Source.HeadEventID, 512) || !validDigest(manifest.Source.HeadEventHash) {
		return "", fmt.Errorf("lifecycle migration source authority is incomplete")
	}
	if manifest.Source.AuthorityHash != "" && !validDigest(manifest.Source.AuthorityHash) {
		return "", fmt.Errorf("lifecycle migration source authorityHash is invalid")
	}
	if err := validateLifecycleHashRecords(manifest.APIVersion, manifest.Records, true); err != nil {
		return "", err
	}
	if uint64(len(manifest.Records)) != manifest.Source.HeadSequence || manifest.Records[len(manifest.Records)-1].SourceEventID != manifest.Source.HeadEventID || manifest.Records[len(manifest.Records)-1].SourceEventHash != manifest.Source.HeadEventHash {
		return "", fmt.Errorf("lifecycle migration source head does not match its records")
	}
	recordsDigest, err := LifecycleRecordsDigest(manifest.Records)
	if err != nil || !validDigest(manifest.RecordsDigest) || manifest.RecordsDigest != recordsDigest {
		return "", fmt.Errorf("lifecycle migration recordsDigest does not match its records")
	}
	manifest.Source.AuthorityHash = ""
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", err
	}
	hashDomain := "dagrail-lifecycle-source-authority-v1\x00"
	if manifest.APIVersion == LifecycleMigrationBundleAPIVersion {
		hashDomain = "dagrail-lifecycle-source-authority-v2\x00"
	}
	sum := sha256.Sum256(append([]byte(hashDomain), canonical...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func DecodeLifecycleMigrationFile(path string) (LifecycleMigrationManifest, error) {
	var manifest LifecycleMigrationManifest
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxDefinitionBytes {
		return manifest, fmt.Errorf("lifecycle migration input must be a regular non-symlink file no larger than %d bytes", maxDefinitionBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return manifest, err
	}
	if err := decodeStrictAuthorityJSON(raw, &manifest); err != nil {
		return manifest, fmt.Errorf("decode lifecycle migration: %w", err)
	}
	if err := domain.RejectSensitiveFields(raw); err != nil {
		return manifest, fmt.Errorf("lifecycle migration contains prohibited material: %w", err)
	}
	return manifest, nil
}

func (s *Service) ValidateLifecycleMigration(manifest LifecycleMigrationManifest, trustedSourceAuthority string) (LifecycleMigrationValidation, error) {
	state, segments, err := s.load()
	if err != nil {
		return LifecycleMigrationValidation{}, err
	}
	return s.validateLifecycleMigration(state, segments, manifest, trustedSourceAuthority)
}

func (s *Service) validateLifecycleMigration(state domain.State, segments []journal.Segment, manifest LifecycleMigrationManifest, trustedSourceAuthority string) (LifecycleMigrationValidation, error) {
	result := LifecycleMigrationValidation{APIVersion: manifest.APIVersion, Kind: "LifecycleMigrationValidation", GraphRevision: manifest.GraphRevision, ExpectedHead: manifest.ExpectedJournalHead}
	if !oneOf(manifest.APIVersion, LifecycleMigrationAPIVersion, LifecycleMigrationBundleAPIVersion) || manifest.Kind != "LifecycleMigration" {
		return result, fmt.Errorf("unsupported lifecycle migration contract")
	}
	if state.Graph == nil || manifest.ProjectID != state.ProjectID || manifest.GraphRevision != state.GraphRevision || manifest.ExpectedJournalHead != state.HeadHash {
		return result, fmt.Errorf("lifecycle migration target authority is stale or mismatched")
	}
	if len(state.LifecycleMigrations) != 0 || !pristineLifecycleState(state, segments) {
		return result, fmt.Errorf("historical lifecycle import requires a pristine graph-only project")
	}
	if !portableExternalID(manifest.Source.System, 256) || !portableExternalID(manifest.Source.Project, 512) || !validDigest(manifest.Source.AuthorityHash) || !validDigest(manifest.Source.HeadEventHash) || manifest.Source.HeadSequence == 0 || !portableExternalID(manifest.Source.HeadEventID, 512) {
		return result, fmt.Errorf("lifecycle migration source authority is incomplete")
	}
	authorityHash, authorityErr := LifecycleSourceAuthorityHash(manifest)
	if authorityErr != nil || !validDigest(trustedSourceAuthority) || trustedSourceAuthority != manifest.Source.AuthorityHash || trustedSourceAuthority != authorityHash {
		return result, fmt.Errorf("lifecycle migration source authority does not match the trusted out-of-band digest")
	}
	if len(manifest.Records) == 0 || len(manifest.Records) > maxMigrationRecords || uint64(len(manifest.Records)) != manifest.Source.HeadSequence {
		return result, fmt.Errorf("lifecycle migration must contain the complete bounded source prefix")
	}
	digest, err := LifecycleRecordsDigest(manifest.Records)
	if err != nil || digest != manifest.RecordsDigest {
		return result, fmt.Errorf("lifecycle migration records digest mismatch")
	}
	events := make([]journal.Event, 0, len(manifest.Records)+1)
	normalizedRecords := make([]LifecycleMigrationRecord, 0, len(manifest.Records))
	seenIDs := map[string]bool{}
	seenHashes := map[string]bool{}
	seenIntroductions := map[string]bool{}
	previous := ""
	previousTime := time.Time{}
	nativeCount := 0
	for index, record := range manifest.Records {
		eventHash, eventHashErr := LifecycleSourceEventHash(record)
		if record.SourceSequence != uint64(index+1) || !portableExternalID(record.SourceEventID, 512) || seenIDs[record.SourceEventID] || !validDigest(record.SourceEventHash) || seenHashes[record.SourceEventHash] || record.PreviousSourceHash != previous || eventHashErr != nil || eventHash != record.SourceEventHash {
			return result, fmt.Errorf("source lifecycle chain is invalid at record %d", index+1)
		}
		occurredAt, err := time.Parse(time.RFC3339Nano, record.OccurredAt)
		if err != nil || (!previousTime.IsZero() && occurredAt.Before(previousTime)) {
			return result, fmt.Errorf("source event %s occurredAt is invalid", record.SourceEventID)
		}
		commands, commandErr := lifecycleRecordCommands(manifest.APIVersion, record)
		if commandErr != nil {
			return result, fmt.Errorf("source event %s: %w", record.SourceEventID, commandErr)
		}
		seenIDs[record.SourceEventID] = true
		seenHashes[record.SourceEventHash] = true
		previous = record.SourceEventHash
		previousTime = occurredAt
		for _, command := range commands {
			normalizedRecords = append(normalizedRecords, LifecycleMigrationRecord{SourceSequence: record.SourceSequence, SourceEventID: fmt.Sprintf("%s@%d", record.SourceEventID, command.CommandIndex), OccurredAt: record.OccurredAt, Events: command.Events})
			for _, event := range command.Events {
				if !migratableEventTypes[event.Type] {
					return result, fmt.Errorf("source event %s maps to unsupported native event %s", record.SourceEventID, event.Type)
				}
				if err := domain.ValidateAuthorityJSON(event.Payload); err != nil {
					return result, fmt.Errorf("source event %s native payload: %w", record.SourceEventID, err)
				}
				if err := domain.RejectSensitiveFields(event.Payload); err != nil {
					return result, fmt.Errorf("source event %s native payload: %w", record.SourceEventID, err)
				}
				if err := validateMigratableEventPayload(event); err != nil {
					return result, fmt.Errorf("source event %s native payload: %w", record.SourceEventID, err)
				}
				if identity := migrationIntroductionIdentity(event); identity != "" {
					if seenIntroductions[identity] {
						return result, fmt.Errorf("source event %s repeats native lifecycle identity %s", record.SourceEventID, identity)
					}
					seenIntroductions[identity] = true
				}
				events = append(events, journal.Event{Type: event.Type, SchemaVersion: journal.CurrentEventSchemaVersion, Payload: event.Payload})
				nativeCount++
			}
		}
	}
	if previous != manifest.Source.HeadEventHash || manifest.Records[len(manifest.Records)-1].SourceEventID != manifest.Source.HeadEventID {
		return result, fmt.Errorf("source lifecycle head does not match the imported prefix")
	}
	if nativeCount == 0 || nativeCount+1 > journal.MaxEventsPerSegment {
		return result, fmt.Errorf("native lifecycle event count exceeds the atomic segment limit")
	}
	simulatedState, err := simulateLifecycleEventSequence(state, normalizedRecords)
	if err != nil {
		return result, fmt.Errorf("lifecycle migration transition preflight: %w", err)
	}
	for _, effect := range simulatedState.Effects {
		// A confirmed Effect is terminal external evidence: it can never be
		// dispatched or reconciled again, so importing it does not depend on the
		// currently compiled adapter. Preserve its original metadata for audit
		// while allowing later runtimes to upgrade or remove that adapter.
		if effect.Status == "confirmed" {
			continue
		}
		if effect.AdapterVersion == "" && effect.AdapterSchemaHash == "" {
			continue
		}
		adapter, exists := s.Providers.Effect(effect.AdapterID)
		if !exists {
			return result, fmt.Errorf("lifecycle migration effect adapter %s is unavailable", effect.AdapterID)
		}
		if err := validateEffectAdapterBinding(effect, adapter.Metadata()); err != nil {
			return result, fmt.Errorf("lifecycle migration effect %s: %w", effect.ID, err)
		}
	}
	migrationID, err := lifecycleMigrationID(manifest)
	if err != nil {
		return result, err
	}
	receipt := lifecycleMigrationReceipt(manifest, migrationID, nativeCount, state.HeadSequence+1, s.Now())
	receiptRaw, _ := json.Marshal(receipt)
	events = append([]journal.Event{{Type: "lifecycle.history-imported", SchemaVersion: journal.CurrentEventSchemaVersion, Payload: receiptRaw}}, events...)
	synthetic := journal.Segment{SchemaVersion: journal.CurrentSegmentSchemaVersion, Sequence: state.HeadSequence + 1, ProjectID: state.ProjectID, PreviousHash: state.HeadHash, Events: events, CommittedAt: receipt.ImportedAt}
	migratedState, err := reduceSegments(state.ProjectID, append(append([]journal.Segment{}, segments...), synthetic))
	if err != nil {
		return result, fmt.Errorf("lifecycle migration reducer preflight: %w", err)
	}
	if err := validateMigratedState(migratedState); err != nil {
		return result, fmt.Errorf("lifecycle migration invariant preflight: %w", err)
	}
	result.Valid, result.MigrationID, result.RecordsDigest, result.RecordCount, result.NativeEventCount = true, migrationID, digest, len(manifest.Records), nativeCount
	return result, nil
}

func (s *Service) ImportLifecycleHistory(manifest LifecycleMigrationManifest, trustedSourceAuthority, actorRole, idempotencyKey string) (domain.LifecycleMigrationReceipt, error) {
	if strings.TrimSpace(actorRole) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return domain.LifecycleMigrationReceipt{}, fmt.Errorf("actor role and idempotency key are required")
	}
	if !validDigest(trustedSourceAuthority) || trustedSourceAuthority != manifest.Source.AuthorityHash {
		return domain.LifecycleMigrationReceipt{}, fmt.Errorf("lifecycle migration source authority does not match the trusted out-of-band digest")
	}
	state, segments, err := s.load()
	if err != nil {
		return domain.LifecycleMigrationReceipt{}, err
	}
	requestRaw, _ := json.Marshal(manifest)
	requestDigest, err := authorityRequestDigest("lifecycle.import-history", requestRaw)
	if err != nil {
		return domain.LifecycleMigrationReceipt{}, err
	}
	migrationID, idErr := lifecycleMigrationID(manifest)
	if idErr != nil {
		return domain.LifecycleMigrationReceipt{}, idErr
	}
	if existing, ok := state.Commands[idempotencyKey]; ok {
		objectRef := "lifecycle-migration:" + migrationID
		if existing.Kind != "lifecycle.import-history" || existing.ActorRole != actorRole || existing.ObjectRef != objectRef || existing.RequestDigest != requestDigest {
			return domain.LifecycleMigrationReceipt{}, fmt.Errorf("idempotency key is already bound to another command")
		}
		receipt, ok := state.LifecycleMigrations[migrationID]
		if !ok {
			return domain.LifecycleMigrationReceipt{}, fmt.Errorf("committed lifecycle migration receipt is unavailable")
		}
		if err := s.settleAutomatic(); err != nil {
			return domain.LifecycleMigrationReceipt{}, err
		}
		state, segments, err = s.load()
		if err != nil {
			return domain.LifecycleMigrationReceipt{}, err
		}
		if err := s.Projection.Sync(state, segments); err != nil {
			return domain.LifecycleMigrationReceipt{}, err
		}
		return receipt, nil
	}
	validation, err := s.validateLifecycleMigration(state, segments, manifest, trustedSourceAuthority)
	if err != nil {
		return domain.LifecycleMigrationReceipt{}, err
	}
	receipt := lifecycleMigrationReceipt(manifest, validation.MigrationID, validation.NativeEventCount, state.HeadSequence+1, s.Now())
	receiptRaw, _ := json.Marshal(receipt)
	events := []journal.Event{{Type: "lifecycle.history-imported", Payload: receiptRaw}}
	for _, record := range manifest.Records {
		commands, err := lifecycleRecordCommands(manifest.APIVersion, record)
		if err != nil {
			return domain.LifecycleMigrationReceipt{}, err
		}
		for _, command := range commands {
			for _, event := range command.Events {
				events = append(events, journal.Event{Type: event.Type, Payload: event.Payload})
			}
		}
	}
	expectedHead := manifest.ExpectedJournalHead
	segment, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "lifecycle.import-history", ActorRole: actorRole, IdempotencyKey: idempotencyKey, ObjectRef: "lifecycle-migration:" + receipt.ID, RequestDigest: requestDigest}, events, s.Now(), &expectedHead)
	if err != nil {
		return domain.LifecycleMigrationReceipt{}, err
	}
	if err := s.settleAutomatic(); err != nil {
		return domain.LifecycleMigrationReceipt{}, err
	}
	state, segments, err = s.load()
	if err != nil {
		return domain.LifecycleMigrationReceipt{}, err
	}
	receipt, ok := state.LifecycleMigrations[receipt.ID]
	if !ok || receipt.TargetSequence != segment.Sequence {
		return domain.LifecycleMigrationReceipt{}, fmt.Errorf("committed lifecycle migration receipt is unavailable")
	}
	if err := s.Projection.Sync(state, segments); err != nil {
		return domain.LifecycleMigrationReceipt{}, err
	}
	return receipt, nil
}

func (s *Service) LifecycleProjection() (LifecycleProjection, error) {
	state, _, err := s.load()
	if err != nil {
		return LifecycleProjection{}, err
	}
	report := LifecycleProjection{
		APIVersion: LifecycleProjectionAPIVersion, Kind: "LifecycleProjection", ProjectID: state.ProjectID,
		GraphRevision: state.GraphRevision, JournalHead: state.HeadHash, HeadSequence: state.HeadSequence,
		Migrations: []domain.LifecycleMigrationReceipt{}, Nodes: []LifecycleNodeProjection{}, Attempts: []domain.Attempt{},
		Leases: []domain.RoleLease{}, Checkpoints: []domain.Checkpoint{}, Decisions: []domain.DecisionRecord{},
		EvidencePackages: []LifecycleExecutionProjection{}, ReuseDecisions: []domain.ReuseDecision{},
		Actions: []domain.ActionRecord{}, Effects: []LifecycleEffectProjection{}, Resources: []LifecycleResourceProjection{},
		Incidents: []domain.Incident{}, Frontier: domain.ComputeFrontier(state),
	}
	for _, value := range state.LifecycleMigrations {
		report.Migrations = append(report.Migrations, value)
	}
	for id, value := range state.Nodes {
		report.Nodes = append(report.Nodes, LifecycleNodeProjection{ID: id, Status: value.Status, Outcome: value.Outcome, OutcomeClass: value.OutcomeClass, Facts: value.Facts})
	}
	for _, value := range state.Attempts {
		report.Attempts = append(report.Attempts, value)
	}
	for _, value := range state.Leases {
		report.Leases = append(report.Leases, value)
	}
	for _, value := range state.Checkpoints {
		value.EvidenceRefs = redactedEvidenceRefs(value.EvidenceRefs)
		report.Checkpoints = append(report.Checkpoints, value)
	}
	for _, value := range state.Decisions {
		value.EvidenceRefs = redactedEvidenceRefs(value.EvidenceRefs)
		report.Decisions = append(report.Decisions, value)
	}
	for _, value := range state.EvidencePackages {
		entry := LifecycleExecutionProjection{
			ID: value.ID, ProjectID: value.ProjectID, GraphRevision: value.GraphRevision, NodeID: value.NodeID,
			AttemptID: value.AttemptID, NodeContractDigest: value.NodeContractDigest,
			Candidate: redactedLifecycleArtifact(value.Candidate), ProspectiveTree: redactedLifecycleArtifact(value.ProspectiveTree),
			CommandGraphDigest: value.CommandGraphDigest, ProtectedInputs: append([]domain.ProtectedInput{}, value.ProtectedInputs...),
			Observations: value.Observations, Artifacts: []LifecycleArtifactProjection{}, CoreDigest: value.CoreDigest,
			CreatedAt: value.CreatedAt, Sequence: value.Sequence,
		}
		for _, artifact := range value.Artifacts {
			entry.Artifacts = append(entry.Artifacts, redactedLifecycleArtifact(artifact))
		}
		report.EvidencePackages = append(report.EvidencePackages, entry)
	}
	for _, value := range state.ReuseDecisions {
		report.ReuseDecisions = append(report.ReuseDecisions, value)
	}
	for _, value := range state.Actions {
		value.Input = nil
		report.Actions = append(report.Actions, value)
	}
	for _, value := range state.Effects {
		report.Effects = append(report.Effects, LifecycleEffectProjection{ID: value.ID, NodeID: value.NodeID, AttemptID: value.AttemptID, AdapterID: value.AdapterID, AdapterVersion: value.AdapterVersion, AdapterSchemaHash: value.AdapterSchemaHash, OwnerRole: value.OwnerRole, Status: value.Status, ReceiptDigest: digestRaw(value.Receipt), Sequence: value.Sequence})
	}
	for _, value := range state.Resources {
		report.Resources = append(report.Resources, LifecycleResourceProjection{ID: value.ID, Kind: value.Kind, Quantity: value.Quantity, NodeID: value.NodeID, AttemptID: value.AttemptID, RoleID: value.RoleID, Status: value.Status, ClosureStatus: value.ClosureStatus, ClosureReceiptDigest: digestRaw(value.ClosureReceipt), LeasedAt: value.LeasedAt, ReleasedAt: value.ReleasedAt})
	}
	for _, value := range state.Incidents {
		report.Incidents = append(report.Incidents, value)
	}
	sort.Slice(report.Migrations, func(i, j int) bool { return report.Migrations[i].ID < report.Migrations[j].ID })
	sort.Slice(report.Nodes, func(i, j int) bool { return report.Nodes[i].ID < report.Nodes[j].ID })
	sort.Slice(report.Attempts, func(i, j int) bool { return report.Attempts[i].ID < report.Attempts[j].ID })
	sort.Slice(report.Leases, func(i, j int) bool { return report.Leases[i].RoleID < report.Leases[j].RoleID })
	sort.Slice(report.Checkpoints, func(i, j int) bool { return report.Checkpoints[i].ID < report.Checkpoints[j].ID })
	sort.Slice(report.Decisions, func(i, j int) bool { return report.Decisions[i].ID < report.Decisions[j].ID })
	sort.Slice(report.EvidencePackages, func(i, j int) bool { return report.EvidencePackages[i].ID < report.EvidencePackages[j].ID })
	sort.Slice(report.ReuseDecisions, func(i, j int) bool { return report.ReuseDecisions[i].ID < report.ReuseDecisions[j].ID })
	sort.Slice(report.Actions, func(i, j int) bool { return report.Actions[i].ID < report.Actions[j].ID })
	sort.Slice(report.Effects, func(i, j int) bool { return report.Effects[i].ID < report.Effects[j].ID })
	sort.Slice(report.Resources, func(i, j int) bool { return report.Resources[i].ID < report.Resources[j].ID })
	sort.Slice(report.Incidents, func(i, j int) bool { return report.Incidents[i].ID < report.Incidents[j].ID })
	raw, _ := json.Marshal(report)
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return LifecycleProjection{}, err
	}
	sum := sha256.Sum256(append([]byte("dagrail-lifecycle-projection-v1\x00"), canonical...))
	report.ProjectionDigest = "sha256:" + hex.EncodeToString(sum[:])
	return report, nil
}

func redactedLifecycleArtifact(value domain.ArtifactRef) LifecycleArtifactProjection {
	return LifecycleArtifactProjection{Digest: value.Digest, Type: value.Type, Size: value.Size, Producer: value.Provenance.Producer, Revision: value.Provenance.Revision, InvocationDigest: value.Provenance.InvocationDigest}
}

func redactedEvidenceRefs(values []domain.EvidenceRef) []domain.EvidenceRef {
	result := make([]domain.EvidenceRef, 0, len(values))
	for _, value := range values {
		value.URI = ""
		result = append(result, value)
	}
	return result
}

func pristineLifecycleState(state domain.State, segments []journal.Segment) bool {
	if len(segments) == 0 || len(state.Commands) != len(segments) {
		return false
	}
	graphIndex := 0
	if segments[0].Command.Kind == "authority.establish" {
		if segments[0].SchemaVersion != journal.AuthorityFenceSchemaVersion || len(segments[0].Events) != 1 || segments[0].Events[0].Type != "authority.established" {
			return false
		}
		graphIndex = 1
	}
	if len(segments) <= graphIndex || segments[graphIndex].Command.Kind != "graph.import" {
		return false
	}
	for _, segment := range segments[graphIndex+1:] {
		if segment.Command.Kind != "node.auto-complete" && segment.Command.Kind != "node.auto-skip" {
			return false
		}
	}
	if len(state.Attempts)+len(state.Leases)+len(state.Checkpoints)+len(state.Decisions)+len(state.EvidencePackages)+len(state.ReuseDecisions)+len(state.Actions)+len(state.Effects)+len(state.Resources)+len(state.Incidents) != 0 {
		return false
	}
	for nodeID, runtime := range state.Nodes {
		if runtime.Status != "planned" {
			node, ok := state.NodeDefinition(nodeID)
			if !ok || (node.Kind != "join" && node.Kind != "milestone") || (runtime.Status != "terminal" && runtime.Status != "skipped") {
				return false
			}
		}
		if runtime.Status == "active" || runtime.Status == "superseded" {
			return false
		}
	}
	return true
}

func lifecycleMigrationID(manifest LifecycleMigrationManifest) (string, error) {
	copy := manifest
	copy.Records = nil
	raw, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("dagrail-lifecycle-migration-v1\x00"), canonical...))
	return "migration_" + hex.EncodeToString(sum[:]), nil
}

func lifecycleMigrationReceipt(manifest LifecycleMigrationManifest, id string, nativeEvents int, sequence uint64, now time.Time) domain.LifecycleMigrationReceipt {
	return domain.LifecycleMigrationReceipt{ID: id, SourceSystem: manifest.Source.System, SourceProject: manifest.Source.Project, SourceAuthorityHash: manifest.Source.AuthorityHash, SourceHeadSequence: manifest.Source.HeadSequence, SourceHeadEventID: manifest.Source.HeadEventID, SourceHeadEventHash: manifest.Source.HeadEventHash, RecordsDigest: manifest.RecordsDigest, RecordCount: len(manifest.Records), NativeEventCount: nativeEvents, GraphRevision: manifest.GraphRevision, TargetSequence: sequence, ImportedAt: now.UTC().Format(time.RFC3339Nano)}
}

func validDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func validBareHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validBoundedTrimmed(value string, maxBytes int) bool {
	return value != "" && value == strings.TrimSpace(value) && len([]byte(value)) <= maxBytes
}

func portableExternalID(value string, maxBytes int) bool {
	if !validBoundedTrimmed(value, maxBytes) || value == "." || value == ".." || strings.Contains(value, "..") {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '@' || character == '-' {
			continue
		}
		return false
	}
	first := value[0]
	return (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || (first >= '0' && first <= '9')
}

func digestRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateMigratableEventPayload(event LifecycleMigrationEvent) error {
	var target any
	switch event.Type {
	case "role.bound":
		target = &domain.RoleLease{}
	case "role.released":
		target = &struct {
			RoleID     string `json:"roleId"`
			ReleasedAt string `json:"releasedAt"`
		}{}
	case "attempt.started", "attempt.leased":
		target = &domain.Attempt{}
	case "attempt.status-changed":
		target = &struct {
			AttemptID string `json:"attemptId"`
			Status    string `json:"status"`
			UpdatedAt string `json:"updatedAt"`
		}{}
	case "attempt.checkpointed":
		target = &domain.Checkpoint{}
	case "attempt.finished":
		target = &struct {
			AttemptID    string                `json:"attemptId"`
			Outcome      string                `json:"outcome"`
			OutcomeClass string                `json:"outcomeClass"`
			Facts        domain.PredicateFacts `json:"facts"`
			UpdatedAt    string                `json:"updatedAt"`
		}{}
	case "evidence.package-published":
		target = &domain.ExecutionPackage{}
	case "evidence.reuse-assessed":
		target = &domain.ReuseDecision{}
	case "decision.recorded":
		target = &domain.DecisionRecord{}
	case "resource.leased":
		target = &domain.ResourceLease{}
	case "resource.closure-observed":
		target = &struct {
			ResourceID string          `json:"resourceId"`
			Status     string          `json:"status"`
			Receipt    json.RawMessage `json:"receipt"`
			UpdatedAt  string          `json:"updatedAt"`
		}{}
	case "resource.released":
		target = &struct {
			ResourceID string `json:"resourceId"`
			ReleasedAt string `json:"releasedAt"`
		}{}
	case "action.applied":
		target = &domain.ActionRecord{}
	case "effect.prepared":
		target = &domain.EffectAction{}
	case "effect.dispatched":
		target = &struct {
			ActionID     string `json:"actionId"`
			DispatchedAt string `json:"dispatchedAt"`
		}{}
	case "effect.reconciling":
		target = &struct {
			ActionID      string `json:"actionId"`
			ReconcilingAt string `json:"reconcilingAt"`
		}{}
	case "effect.observed":
		target = &struct {
			ActionID  string          `json:"actionId"`
			Status    string          `json:"status"`
			Receipt   json.RawMessage `json:"receipt"`
			UpdatedAt string          `json:"updatedAt"`
		}{}
	case "incident.opened", "incident.updated":
		target = &domain.Incident{}
	case "incident.resolved":
		target = &struct {
			IncidentID string `json:"incidentId"`
			ResolvedAt string `json:"resolvedAt"`
		}{}
	default:
		return fmt.Errorf("unsupported native event %s", event.Type)
	}
	if err := decodeStrictAuthorityJSON(event.Payload, target); err != nil {
		return fmt.Errorf("%s is not a closed event payload: %w", event.Type, err)
	}
	return nil
}

func migrationIntroductionIdentity(event LifecycleMigrationEvent) string {
	var id string
	kind := event.Type
	switch event.Type {
	case "attempt.started", "attempt.leased":
		kind = "attempt"
		var value domain.Attempt
		_ = json.Unmarshal(event.Payload, &value)
		id = value.ID
	case "attempt.checkpointed":
		var value domain.Checkpoint
		_ = json.Unmarshal(event.Payload, &value)
		id = value.ID
	case "evidence.package-published":
		var value domain.ExecutionPackage
		_ = json.Unmarshal(event.Payload, &value)
		id = value.ID
	case "evidence.reuse-assessed":
		var value domain.ReuseDecision
		_ = json.Unmarshal(event.Payload, &value)
		id = value.ID
	case "decision.recorded":
		var value domain.DecisionRecord
		_ = json.Unmarshal(event.Payload, &value)
		id = value.ID
	case "resource.leased":
		var value domain.ResourceLease
		_ = json.Unmarshal(event.Payload, &value)
		id = value.ID
	case "action.applied":
		var value domain.ActionRecord
		_ = json.Unmarshal(event.Payload, &value)
		id = value.ID
	case "effect.prepared":
		var value domain.EffectAction
		_ = json.Unmarshal(event.Payload, &value)
		id = value.ID
	}
	if id == "" {
		return ""
	}
	return kind + ":" + id
}

func validateMigratedState(state domain.State) error {
	if state.Graph == nil {
		return fmt.Errorf("graph is unavailable")
	}
	roles := map[string]bool{}
	for _, role := range state.Graph.Spec.Roles {
		roles[role.ID] = true
	}
	nodes := map[string]domain.NodeDefinition{}
	for _, node := range state.Graph.Spec.Nodes {
		nodes[node.ID] = node
	}
	listedAttempts := map[string]bool{}
	for nodeID, attemptIDs := range state.NodeAttempts {
		if _, ok := nodes[nodeID]; !ok {
			return fmt.Errorf("attempt index references unknown node %s", nodeID)
		}
		for _, attemptID := range attemptIDs {
			attempt, ok := state.Attempts[attemptID]
			if !ok || attempt.NodeID != nodeID || listedAttempts[attemptID] {
				return fmt.Errorf("attempt index is inconsistent for %s", attemptID)
			}
			listedAttempts[attemptID] = true
		}
	}
	for id, attempt := range state.Attempts {
		node, nodeOK := nodes[attempt.NodeID]
		if id == "" || attempt.ID != id || !nodeOK || !roles[attempt.RoleID] || node.Role != attempt.RoleID || !listedAttempts[id] || attempt.Number < 1 || !oneOf(attempt.Status, "leased", "running", "waiting", "submitted", "terminal") || !validTimestamp(attempt.StartedAt) || !timestampAtOrAfter(attempt.UpdatedAt, attempt.StartedAt) {
			return fmt.Errorf("attempt %q violates the imported lifecycle contract", id)
		}
		if attempt.Status == "terminal" && (attempt.Outcome == "" || outcomeClass(node, attempt.Outcome) == "") {
			return fmt.Errorf("terminal attempt %s has an undeclared outcome", id)
		}
	}
	for nodeID := range nodes {
		attemptIDs := state.NodeAttempts[nodeID]
		nonTerminal := 0
		for index, attemptID := range attemptIDs {
			attempt := state.Attempts[attemptID]
			if attempt.Number != index+1 {
				return fmt.Errorf("node %s attempt numbers are not contiguous", nodeID)
			}
			if index > 0 {
				previous := state.Attempts[attemptIDs[index-1]]
				if previous.Status != "terminal" || !timestampAtOrAfter(attempt.StartedAt, previous.UpdatedAt) {
					return fmt.Errorf("node %s attempts overlap or are out of order", nodeID)
				}
			}
			if attempt.Status != "terminal" {
				nonTerminal++
				if index != len(attemptIDs)-1 {
					return fmt.Errorf("node %s has a non-latest active attempt", nodeID)
				}
			}
		}
		runtime := state.Nodes[nodeID]
		switch runtime.Status {
		case "planned":
			if nonTerminal != 0 || (len(attemptIDs) > 0 && runtime.OutcomeClass != "retryable") {
				return fmt.Errorf("planned node %s is inconsistent with its attempts", nodeID)
			}
		case "active":
			latestStatus := ""
			if len(attemptIDs) > 0 {
				latestStatus = state.Attempts[attemptIDs[len(attemptIDs)-1]].Status
			}
			if nonTerminal != 1 || latestStatus == "leased" {
				return fmt.Errorf("active node %s must have exactly one active attempt", nodeID)
			}
		case "terminal":
			if nonTerminal != 0 {
				return fmt.Errorf("terminal node %s retains an active attempt", nodeID)
			}
		case "skipped":
			if len(attemptIDs) != 0 {
				return fmt.Errorf("skipped node %s cannot have attempts", nodeID)
			}
		}
	}
	for roleID, lease := range state.Leases {
		bound, boundErr := time.Parse(time.RFC3339Nano, lease.BoundAt)
		expires, expiresErr := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
		if roleID == "" || lease.RoleID != roleID || !roles[roleID] || lease.Harness == "" || lease.SessionID == "" || boundErr != nil || expiresErr != nil || !expires.After(bound) || expires.Sub(bound) > 24*time.Hour {
			return fmt.Errorf("role lease %q violates the imported lifecycle contract", roleID)
		}
	}
	for id, checkpoint := range state.Checkpoints {
		if id == "" || checkpoint.ID != id || checkpoint.Summary == "" || len([]byte(checkpoint.Summary)) > 2048 || !validTimestamp(checkpoint.CreatedAt) {
			return fmt.Errorf("checkpoint %q violates the imported lifecycle contract", id)
		}
		if _, ok := state.Attempts[checkpoint.AttemptID]; !ok {
			return fmt.Errorf("checkpoint %s references an unknown attempt", id)
		}
	}
	for id, attempt := range state.Attempts {
		if attempt.CheckpointID == "" {
			continue
		}
		checkpoint, ok := state.Checkpoints[attempt.CheckpointID]
		if !ok || checkpoint.AttemptID != id {
			return fmt.Errorf("attempt %s checkpoint index is inconsistent", id)
		}
	}
	for id, action := range state.Actions {
		if id == "" || action.ID != id {
			return fmt.Errorf("action %q violates the imported lifecycle contract", id)
		}
		if err := validateLifecycleActionFinal(action, state); err != nil {
			return fmt.Errorf("action %s violates the imported lifecycle contract: %w", id, err)
		}
	}
	for id, effect := range state.Effects {
		action, actionOK := state.Actions[id]
		attempt, attemptOK := state.Attempts[effect.AttemptID]
		node, nodeOK := state.NodeDefinition(effect.NodeID)
		if id == "" || effect.ID != id || !actionOK || action.Kind != "effect.prepare" || !lifecycleEffectActionStatusCompatible(effect.Status, action.Status) || action.NodeID != effect.NodeID || action.AttemptID != effect.AttemptID || !attemptOK || !nodeOK || node.Kind != "effect" || effect.NodeID != attempt.NodeID || effect.AdapterID == "" || !validEffectAdapterMetadataPair(effect) || effect.IdempotencyKey == "" || len(effect.Request) == 0 || len(effect.Prepared) == 0 || !oneOf(effect.Status, "prepared", "dispatched", "confirmed", "failed", "unknown", "reconciling") || !validTimestamp(effect.PreparedAt) || !timestampAtOrAfter(effect.UpdatedAt, effect.PreparedAt) {
			return fmt.Errorf("effect %q violates the imported lifecycle contract", id)
		}
		if err := validateLifecycleEffectDeclaration(effect, node); err != nil {
			return fmt.Errorf("effect %q violates its graph declaration: %w", id, err)
		}
	}
	for id, resource := range state.Resources {
		attempt, attemptOK := state.Attempts[resource.AttemptID]
		node, nodeOK := state.NodeDefinition(resource.NodeID)
		if id == "" || resource.ID != id || !attemptOK || !nodeOK || resource.NodeID != attempt.NodeID || resource.RoleID != attempt.RoleID || !nodeRequestsResource(node, resource.Kind, resource.Quantity) || !oneOf(resource.Status, "active", "released") || !oneOf(resource.ClosureStatus, "pending", "confirmed", "failed", "unknown", "reconciling") || !validTimestamp(resource.LeasedAt) || (resource.ClosureUpdatedAt != "" && !timestampAtOrAfter(resource.ClosureUpdatedAt, resource.LeasedAt)) || (resource.ReleasedAt != "" && !timestampAtOrAfter(resource.ReleasedAt, resource.ClosureUpdatedAt)) {
			return fmt.Errorf("resource lease %q violates the imported lifecycle contract", id)
		}
	}
	for _, attempt := range state.Attempts {
		node, _ := state.NodeDefinition(attempt.NodeID)
		for _, request := range node.Resources {
			if !resourceForAttempt(state, attempt.ID, request.Kind) {
				return fmt.Errorf("attempt %s is missing declared resource %s", attempt.ID, request.Kind)
			}
		}
	}
	for id, incident := range state.Incidents {
		if id == "" || incident.ID != id || !oneOf(incident.SourceType, "attempt", "effect", "resource") || incident.SourceID == "" || !oneOf(incident.Status, "open", "circuit-open", "resolved") || !domain.ValidIncidentClassification(incident.Classification) || incident.AttemptBudget < 1 || !validTimestamp(incident.OpenedAt) || !timestampAtOrAfter(incident.UpdatedAt, incident.OpenedAt) {
			return fmt.Errorf("incident %q violates the imported lifecycle contract", id)
		}
		switch incident.SourceType {
		case "attempt":
			if _, ok := state.Attempts[incident.SourceID]; !ok {
				return fmt.Errorf("incident %s references an unknown attempt", id)
			}
		case "effect":
			if _, ok := state.Effects[incident.SourceID]; !ok {
				return fmt.Errorf("incident %s references an unknown effect", id)
			}
		case "resource":
			if _, ok := state.Resources[incident.SourceID]; !ok {
				return fmt.Errorf("incident %s references an unknown resource", id)
			}
		}
	}
	return nil
}

func validTimestamp(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
