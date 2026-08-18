package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/project"
	"github.com/CongBao/dagrail/internal/projection"
	"github.com/google/uuid"
	"github.com/gowebpki/jcs"
)

const BackupAPIVersion = "dagrail.io/v1alpha1"
const maxLocalAuthorityStores = 10_000

type BackupEnvelope struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Project    project.Config    `json:"project"`
	CreatedAt  string            `json:"createdAt"`
	Segments   []journal.Segment `json:"segments"`
	Digest     string            `json:"digest"`
}

type BackupReport struct {
	Valid        bool   `json:"valid"`
	ProjectID    string `json:"projectId"`
	Segments     int    `json:"segments"`
	HeadSequence uint64 `json:"headSequence"`
	HeadHash     string `json:"headHash,omitempty"`
	Digest       string `json:"digest"`
}

const AuthorityRotationAPIVersion = "dagrail.io/authority-rotation/v1alpha1"
const AuthorityAdoptionAPIVersion = "dagrail.io/authority-adoption/v1alpha1"
const AuthorityRelocationAPIVersion = "dagrail.io/authority-relocation/v1alpha1"

const (
	authorityRetirementAPIVersion = "dagrail.io/authority-retirement/v1alpha1"
	legacyRetirementKind          = "LegacyAuthorityRetirement"
	rotationRetirementKind        = "AuthorityRetirement"
	relocationRetirementKind      = "AuthorityRelocationRetirement"
)

type AuthorityRotationReceipt struct {
	APIVersion           string `json:"apiVersion"`
	Kind                 string `json:"kind"`
	PreviousProjectID    string `json:"previousProjectId"`
	PreviousHead         string `json:"previousHead"`
	RecoveryHead         string `json:"recoveryHead"`
	RecoveryBackupDigest string `json:"recoveryBackupDigest"`
	ReplacementProjectID string `json:"replacementProjectId"`
	RotatedAt            string `json:"rotatedAt"`
	Reason               string `json:"reason"`
	IdempotencyKey       string `json:"idempotencyKey"`
	ReceiptDigest        string `json:"receiptDigest"`
}

type AuthorityAdoptionReceipt struct {
	APIVersion           string `json:"apiVersion"`
	Kind                 string `json:"kind"`
	PreviousProjectID    string `json:"previousProjectId"`
	PreviousHead         string `json:"previousHead"`
	SourceBackupDigest   string `json:"sourceBackupDigest"`
	ReplacementProjectID string `json:"replacementProjectId"`
	AdoptedAt            string `json:"adoptedAt"`
	Reason               string `json:"reason"`
	IdempotencyKey       string `json:"idempotencyKey"`
	ReceiptDigest        string `json:"receiptDigest"`
}

type AuthorityRelocationReceipt struct {
	APIVersion               string `json:"apiVersion"`
	Kind                     string `json:"kind"`
	SourceProjectID          string `json:"sourceProjectId"`
	PreviousLocatorProjectID string `json:"previousLocatorProjectId"`
	SourceHead               string `json:"sourceHead"`
	RecoveryHead             string `json:"recoveryHead"`
	RecoveryBackupDigest     string `json:"recoveryBackupDigest"`
	TargetRootDigest         string `json:"targetRootDigest"`
	DestinationRootDigest    string `json:"destinationRuntimeDigest"`
	ReplacementProjectID     string `json:"replacementProjectId"`
	RelocatedAt              string `json:"relocatedAt"`
	Reason                   string `json:"reason"`
	IdempotencyKey           string `json:"idempotencyKey"`
	ReceiptDigest            string `json:"receiptDigest"`
}

type authorityRetirement struct {
	APIVersion            string `json:"apiVersion"`
	Kind                  string `json:"kind"`
	PreviousProjectID     string `json:"previousProjectId"`
	PreviousLocatorID     string `json:"previousLocatorProjectId,omitempty"`
	TargetRootDigest      string `json:"targetRootDigest,omitempty"`
	DestinationRootDigest string `json:"destinationRuntimeDigest,omitempty"`
	PreviousHead          string `json:"previousHead"`
	RecoveryHead          string `json:"recoveryHead"`
	RecoveryBackupDigest  string `json:"recoveryBackupDigest"`
	ReplacementProjectID  string `json:"replacementProjectId"`
	RotatedAt             string `json:"rotatedAt"`
	Reason                string `json:"reason"`
	IdempotencyKey        string `json:"idempotencyKey"`
}

type authorityEstablishment struct {
	APIVersion        string `json:"apiVersion"`
	Kind              string `json:"kind"`
	ProjectID         string `json:"projectId"`
	PreviousProjectID string `json:"previousProjectId,omitempty"`
	Operation         string `json:"operation"`
	EstablishedAt     string `json:"establishedAt"`
	ProvenanceDigest  string `json:"provenanceDigest"`
}

type HistoryEntry struct {
	Sequence    uint64   `json:"sequence"`
	CommandKind string   `json:"commandKind"`
	ActorRole   string   `json:"actorRole,omitempty"`
	EventTypes  []string `json:"eventTypes"`
	CommittedAt string   `json:"committedAt"`
	SegmentHash string   `json:"segmentHash"`
}

type HistoryPage struct {
	After      uint64         `json:"after"`
	NextCursor uint64         `json:"nextCursor"`
	Truncated  bool           `json:"truncated"`
	Entries    []HistoryEntry `json:"entries"`
}

type OperationalStatus struct {
	ProjectID         string          `json:"projectId"`
	GraphRevision     string          `json:"graphRevision,omitempty"`
	HeadSequence      uint64          `json:"headSequence"`
	HeadHash          string          `json:"headHash,omitempty"`
	Nodes             map[string]int  `json:"nodes"`
	Attempts          map[string]int  `json:"attempts"`
	Effects           map[string]int  `json:"effects"`
	Incidents         map[string]int  `json:"incidents"`
	OverdueIncidents  []string        `json:"overdueIncidents,omitempty"`
	ExpiredRoleLeases []string        `json:"expiredRoleLeases,omitempty"`
	Frontier          domain.Frontier `json:"frontier"`
}

func (s *Service) CreateBackup() ([]byte, BackupReport, error) {
	segments, err := s.Journal.ReadAll()
	if err != nil {
		return nil, BackupReport{}, err
	}
	envelope := BackupEnvelope{APIVersion: BackupAPIVersion, Kind: "JournalBackup", Project: s.Project.Config, CreatedAt: s.Now().UTC().Format(time.RFC3339Nano), Segments: segments}
	digest, err := backupDigest(envelope)
	if err != nil {
		return nil, BackupReport{}, err
	}
	envelope.Digest = digest
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, BackupReport{}, err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, BackupReport{}, err
	}
	if len(canonical)+1 > MaxPortableJournalBytes {
		return nil, BackupReport{}, fmt.Errorf("backup exceeds %d bytes", MaxPortableJournalBytes)
	}
	report := backupReport(envelope)
	return append(canonical, '\n'), report, nil
}

func (s *Service) VerifyBackup(data []byte) (BackupReport, error) {
	envelope, err := decodeBackup(data)
	if err != nil {
		return BackupReport{}, err
	}
	if envelope.Project.ProjectID != s.Project.Config.ProjectID {
		return BackupReport{}, fmt.Errorf("backup project %s does not match current project %s", envelope.Project.ProjectID, s.Project.Config.ProjectID)
	}
	return backupReport(envelope), nil
}

func (s *Service) RestoreBackup(data []byte) (BackupReport, error) {
	envelope, err := decodeBackup(data)
	if err != nil {
		return BackupReport{}, err
	}
	if envelope.Project.ProjectID != s.Project.Config.ProjectID {
		return BackupReport{}, fmt.Errorf("backup project %s does not match current project %s", envelope.Project.ProjectID, s.Project.Config.ProjectID)
	}
	if err := s.Journal.RestoreSegments(envelope.Segments); err != nil {
		return BackupReport{}, err
	}
	if err := s.settleAutomatic(); err != nil {
		return BackupReport{}, err
	}
	if err := s.RebuildProjection(); err != nil {
		return BackupReport{}, err
	}
	return backupReport(envelope), nil
}

// AdoptLegacyAuthority retires a pre-v0.22 Project UUID and publishes a fresh
// replacement identity. The old UUID never receives a v0.22 writer claim.
// Its verified journal remains immutable source evidence for graph/lifecycle
// import into the fence-only replacement authority.
func (s *Service) AdoptLegacyAuthority(expectedProjectID, expectedHead, reason, idempotencyKey string) (AuthorityAdoptionReceipt, error) {
	if s.readOnlyInspection {
		return AuthorityAdoptionReceipt{}, fmt.Errorf("read-only inspection cannot adopt legacy authority")
	}
	if strings.TrimSpace(expectedProjectID) == "" || strings.TrimSpace(reason) == "" || len([]byte(reason)) > 1024 || strings.TrimSpace(idempotencyKey) == "" || len([]byte(idempotencyKey)) > 256 {
		return AuthorityAdoptionReceipt{}, fmt.Errorf("expected project ID, reason, and idempotency key are required")
	}
	if expectedProjectID != s.Project.Config.ProjectID {
		return s.retryLegacyAuthorityAdoption(expectedProjectID, expectedHead, reason, idempotencyKey)
	}
	legacyJournal, err := journal.OpenRecovery(s.Project.DataDir, expectedProjectID)
	if err != nil {
		return AuthorityAdoptionReceipt{}, err
	}
	previousUUID, err := uuid.Parse(expectedProjectID)
	if err != nil {
		return AuthorityAdoptionReceipt{}, err
	}
	replacementProjectID := uuid.NewSHA1(previousUUID, []byte("dagrail-legacy-authority-adoption-v1\x00"+idempotencyKey)).String()
	if existingRaw, exists, readErr := legacyJournal.AuthorityRetirement(); readErr != nil {
		return AuthorityAdoptionReceipt{}, readErr
	} else if exists {
		var existing authorityRetirement
		if err := decodeStrictAuthorityJSON(existingRaw, &existing); err != nil {
			return AuthorityAdoptionReceipt{}, err
		}
		if err := validateLegacyRetirementIntent(existing, expectedProjectID, expectedHead, existing.RecoveryBackupDigest, replacementProjectID, reason, idempotencyKey); err != nil {
			return AuthorityAdoptionReceipt{}, err
		}
		operationTime, _ := time.Parse(time.RFC3339Nano, existing.RotatedAt)
		reservationDigest, err := legacyRetirementReservationDigest(existing)
		if err != nil {
			return AuthorityAdoptionReceipt{}, err
		}
		committedRaw, err := legacyJournal.RetireLegacyAuthority(expectedHead, existingRaw, reservationDigest, operationTime, nil, func(candidate []byte) error {
			var committed authorityRetirement
			if err := decodeStrictAuthorityJSON(candidate, &committed); err != nil {
				return err
			}
			return validateLegacyRetirementIntent(committed, expectedProjectID, expectedHead, existing.RecoveryBackupDigest, replacementProjectID, reason, idempotencyKey)
		}, func(committed []byte) error {
			var actual authorityRetirement
			if err := decodeStrictAuthorityJSON(committed, &actual); err != nil {
				return err
			}
			return s.prepareAndPublishReplacement(actual, committed, "legacy-adoption")
		})
		if err != nil {
			return AuthorityAdoptionReceipt{}, err
		}
		if err := decodeStrictAuthorityJSON(committedRaw, &existing); err != nil {
			return AuthorityAdoptionReceipt{}, err
		}
		return authorityAdoptionReceipt(existing)
	}
	operationTime := s.Now().UTC()
	if s.beforeLegacyAuthoritySnapshot != nil {
		s.beforeLegacyAuthoritySnapshot()
	}
	segments, err := legacyJournal.ReadAll()
	if err != nil {
		return AuthorityAdoptionReceipt{}, err
	}
	backupEnvelope := BackupEnvelope{APIVersion: BackupAPIVersion, Kind: "JournalBackup", Project: s.Project.Config, CreatedAt: stableLegacyBackupCreatedAt(segments), Segments: segments}
	backupEnvelope.Digest, err = backupDigest(backupEnvelope)
	if err != nil {
		return AuthorityAdoptionReceipt{}, err
	}
	backup := backupReport(backupEnvelope)
	retirement := authorityRetirement{
		APIVersion: AuthorityAdoptionAPIVersion, Kind: legacyRetirementKind,
		PreviousProjectID: expectedProjectID, PreviousHead: expectedHead,
		RecoveryHead: backup.HeadHash, RecoveryBackupDigest: backup.Digest,
		ReplacementProjectID: replacementProjectID,
		RotatedAt:            operationTime.Format(time.RFC3339Nano), Reason: reason, IdempotencyKey: idempotencyKey,
	}
	retirementRaw, err := json.Marshal(retirement)
	if err != nil {
		return AuthorityAdoptionReceipt{}, err
	}
	retirementRaw, err = jcs.Transform(retirementRaw)
	if err != nil {
		return AuthorityAdoptionReceipt{}, err
	}
	reservationDigest, err := legacyRetirementReservationDigest(retirement)
	if err != nil {
		return AuthorityAdoptionReceipt{}, err
	}
	committedRaw, err := legacyJournal.RetireLegacyAuthority(expectedHead, retirementRaw, reservationDigest, operationTime, func(current []journal.Segment) error {
		if len(current) != len(segments) {
			return fmt.Errorf("legacy authority changed before retirement")
		}
		for index := range current {
			if current[index].SegmentHash != segments[index].SegmentHash {
				return fmt.Errorf("legacy authority changed before retirement")
			}
		}
		state, err := reduceSegments(expectedProjectID, current)
		if err != nil {
			return err
		}
		if err := validateAuthorityQuiescent(state, operationTime, "legacy authority adoption"); err != nil {
			return err
		}
		return project.ReserveLegacyRetirement(s.Project.DataDir, expectedProjectID, reservationDigest)
	}, func(existing []byte) error {
		var committed authorityRetirement
		if err := decodeStrictAuthorityJSON(existing, &committed); err != nil {
			return fmt.Errorf("decode legacy authority retirement: %w", err)
		}
		// A concurrent winner may commit after this caller's initial sidecar
		// check or even before its snapshot. The committed fence is authoritative:
		// expectedProjectID/head plus the closed intent fields bind the exact
		// pre-fence source prefix, while its own recovery digest avoids folding
		// the winner's fence into a loser's newly derived backup.
		return validateLegacyRetirementIntent(committed, expectedProjectID, expectedHead, committed.RecoveryBackupDigest, replacementProjectID, reason, idempotencyKey)
	}, func(committed []byte) error {
		var actual authorityRetirement
		if err := decodeStrictAuthorityJSON(committed, &actual); err != nil {
			return err
		}
		return s.prepareAndPublishReplacement(actual, committed, "legacy-adoption")
	})
	if err != nil {
		return AuthorityAdoptionReceipt{}, err
	}
	if err := decodeStrictAuthorityJSON(committedRaw, &retirement); err != nil {
		return AuthorityAdoptionReceipt{}, err
	}
	return authorityAdoptionReceipt(retirement)
}

// RotateAuthority creates a replacement Project identity from an authenticated
// backup prefix without truncating or rewriting the previous journal.
func (s *Service) RotateAuthority(data []byte, expectedCurrentHead, reason, idempotencyKey string) (AuthorityRotationReceipt, error) {
	if s.readOnlyInspection {
		return AuthorityRotationReceipt{}, fmt.Errorf("read-only inspection cannot rotate authority")
	}
	if expectedCurrentHead == "" || strings.TrimSpace(reason) == "" || len([]byte(reason)) > 1024 || strings.TrimSpace(idempotencyKey) == "" || len([]byte(idempotencyKey)) > 256 {
		return AuthorityRotationReceipt{}, fmt.Errorf("expected current head, reason, and idempotency key are required")
	}
	envelope, err := decodeBackup(data)
	if err != nil {
		return AuthorityRotationReceipt{}, err
	}
	backup := backupReport(envelope)
	if backup.HeadHash == "" {
		return AuthorityRotationReceipt{}, fmt.Errorf("authority rotation requires a non-empty authenticated recovery prefix")
	}
	if err := project.ValidateAuthorityClaim(s.Project.DataDir, s.Project.Config.ProjectID); err != nil {
		if repairErr := s.repairCurrentAuthorityLineage(); repairErr != nil {
			return AuthorityRotationReceipt{}, err
		}
	}
	if envelope.Project.ProjectID != s.Project.Config.ProjectID {
		return s.retryAuthorityRotation(envelope.Project.ProjectID, backup, expectedCurrentHead, reason, idempotencyKey)
	}
	rotationJournal, err := journal.Open(s.Project.DataDir, s.Project.Config.ProjectID)
	if err != nil {
		return AuthorityRotationReceipt{}, err
	}
	previousUUID, parseErr := uuid.Parse(s.Project.Config.ProjectID)
	if parseErr != nil {
		return AuthorityRotationReceipt{}, parseErr
	}
	retirement := authorityRetirement{
		APIVersion: authorityRetirementAPIVersion, Kind: rotationRetirementKind,
		PreviousProjectID: s.Project.Config.ProjectID, PreviousHead: expectedCurrentHead,
		RecoveryHead: backup.HeadHash, RecoveryBackupDigest: backup.Digest,
		ReplacementProjectID: uuid.NewSHA1(previousUUID, []byte("dagrail-authority-rotation-v1\x00"+idempotencyKey)).String(),
		RotatedAt:            s.Now().UTC().Format(time.RFC3339Nano), Reason: reason, IdempotencyKey: idempotencyKey,
	}
	retirementRaw, err := json.Marshal(retirement)
	if err != nil {
		return AuthorityRotationReceipt{}, err
	}
	retirementRaw, err = jcs.Transform(retirementRaw)
	if err != nil {
		return AuthorityRotationReceipt{}, err
	}
	operationTime, err := time.Parse(time.RFC3339Nano, retirement.RotatedAt)
	if err != nil {
		return AuthorityRotationReceipt{}, fmt.Errorf("authority retirement timestamp is invalid")
	}
	committedRaw, err := rotationJournal.RetireAuthority(expectedCurrentHead, retirementRaw, operationTime, func(current []journal.Segment) error {
		state, err := reduceSegments(s.Project.Config.ProjectID, current)
		if err != nil {
			return err
		}
		for _, lease := range state.Leases {
			if lease.Active {
				expires, parseErr := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
				if parseErr != nil || operationTime.Before(expires) {
					return fmt.Errorf("authority rotation requires all Role leases to be inactive or expired")
				}
			}
		}
		for _, effect := range state.Effects {
			if !oneOf(effect.Status, "confirmed", "failed") {
				return fmt.Errorf("authority rotation requires every Effect to be closed")
			}
		}
		for _, resource := range state.Resources {
			if resource.Status == "active" {
				return fmt.Errorf("authority rotation requires every Resource lease to be closed")
			}
		}
		for _, incident := range state.Incidents {
			if incident.Status != "resolved" {
				return fmt.Errorf("authority rotation requires every Incident to be resolved")
			}
		}
		if len(envelope.Segments) > len(current) {
			return fmt.Errorf("recovery backup is not a prefix of current authority")
		}
		for index := range envelope.Segments {
			if envelope.Segments[index].SegmentHash != current[index].SegmentHash {
				return fmt.Errorf("recovery backup is not a prefix of current authority")
			}
		}
		return nil
	}, func(existing []byte) error {
		var committed authorityRetirement
		if err := decodeStrictAuthorityJSON(existing, &committed); err != nil {
			return fmt.Errorf("decode authority retirement marker: %w", err)
		}
		return validateRetirementIntent(committed, retirement.PreviousProjectID, retirement.PreviousHead, retirement.RecoveryHead, retirement.RecoveryBackupDigest, retirement.ReplacementProjectID, retirement.Reason, retirement.IdempotencyKey)
	}, func(committed []byte) error {
		var actual authorityRetirement
		if err := decodeStrictAuthorityJSON(committed, &actual); err != nil {
			return err
		}
		return s.prepareAndPublishReplacement(actual, committed, "rotation")
	})
	if err != nil {
		return AuthorityRotationReceipt{}, err
	}
	if err := decodeStrictAuthorityJSON(committedRaw, &retirement); err != nil {
		return AuthorityRotationReceipt{}, err
	}
	return authorityRotationReceipt(retirement)
}

// RelocateAuthority retires an already-established, claim-authenticated source
// authority discovered through its per-user anchor, establishes a fresh UUID in the
// currently selected DAGRAIL_HOME, and publishes that UUID into targetRoot. It exists
// for a recovery saga whose replacement was durably established before the intended
// repository locator was published; it never rebinds the old UUID or its anchor.
func RelocateAuthority(targetRoot string, data []byte, expectedLocatorProjectID, expectedCurrentHead, reason, idempotencyKey string) (AuthorityRelocationReceipt, error) {
	return relocateAuthority(targetRoot, data, expectedLocatorProjectID, expectedCurrentHead, reason, idempotencyKey, project.SyncProjectLocator)
}

func relocateAuthority(targetRoot string, data []byte, expectedLocatorProjectID, expectedCurrentHead, reason, idempotencyKey string, confirmLocator func(string) error) (AuthorityRelocationReceipt, error) {
	if strings.TrimSpace(expectedLocatorProjectID) == "" || expectedCurrentHead == "" || strings.TrimSpace(reason) == "" || len([]byte(reason)) > 1024 || strings.TrimSpace(idempotencyKey) == "" || len([]byte(idempotencyKey)) > 256 {
		return AuthorityRelocationReceipt{}, fmt.Errorf("expected locator project ID, current head, reason, and idempotency key are required")
	}
	envelope, err := decodeBackup(data)
	if err != nil {
		return AuthorityRelocationReceipt{}, err
	}
	backup := backupReport(envelope)
	if backup.HeadHash == "" {
		return AuthorityRelocationReceipt{}, fmt.Errorf("authority relocation requires a non-empty authenticated recovery prefix")
	}
	sourceProjectID := envelope.Project.ProjectID
	sourceDataDir, err := project.ResolveClaimedAuthority(sourceProjectID)
	if err != nil {
		return AuthorityRelocationReceipt{}, fmt.Errorf("resolve relocation source authority: %w", err)
	}
	if err := authorityDescendsFrom(sourceProjectID, expectedLocatorProjectID); err != nil {
		return AuthorityRelocationReceipt{}, err
	}
	target, err := project.Open(targetRoot)
	if err != nil {
		return AuthorityRelocationReceipt{}, err
	}
	canonicalRoot, err := filepath.EvalSymlinks(target.Root)
	if err != nil {
		return AuthorityRelocationReceipt{}, fmt.Errorf("resolve relocation target root: %w", err)
	}
	targetRootDigest, err := authorityDigest("dagrail-authority-relocation-target-v1\x00", struct {
		Root string `json:"root"`
	}{Root: filepath.Clean(canonicalRoot)})
	if err != nil {
		return AuthorityRelocationReceipt{}, err
	}
	destinationProbe, err := project.DataDirForProjectID(sourceProjectID)
	if err != nil {
		return AuthorityRelocationReceipt{}, err
	}
	destinationRoot, err := filepath.Abs(filepath.Dir(destinationProbe))
	if err != nil {
		return AuthorityRelocationReceipt{}, err
	}
	if err := project.EnsureDurableDirectory(destinationRoot); err != nil {
		return AuthorityRelocationReceipt{}, fmt.Errorf("prepare relocation destination runtime: %w", err)
	}
	destinationRoot, err = filepath.EvalSymlinks(destinationRoot)
	if err != nil {
		return AuthorityRelocationReceipt{}, fmt.Errorf("resolve relocation destination runtime: %w", err)
	}
	destinationRootDigest, err := authorityDigest("dagrail-authority-relocation-runtime-v1\x00", struct {
		Root string `json:"root"`
	}{Root: filepath.Clean(destinationRoot)})
	if err != nil {
		return AuthorityRelocationReceipt{}, err
	}
	sourceUUID, err := uuid.Parse(sourceProjectID)
	if err != nil {
		return AuthorityRelocationReceipt{}, err
	}
	replacementProjectID := uuid.NewSHA1(sourceUUID, []byte("dagrail-authority-relocation-v1\x00"+idempotencyKey+"\x00"+targetRootDigest+"\x00"+destinationRootDigest)).String()
	if target.Config.ProjectID != expectedLocatorProjectID && target.Config.ProjectID != replacementProjectID {
		return AuthorityRelocationReceipt{}, fmt.Errorf("target project locator changed before authority relocation")
	}
	sourceJournal, err := journal.Open(sourceDataDir, sourceProjectID)
	if err != nil {
		return AuthorityRelocationReceipt{}, err
	}
	operationTime := time.Now().UTC()
	retirement := authorityRetirement{
		APIVersion: AuthorityRelocationAPIVersion, Kind: relocationRetirementKind,
		PreviousProjectID: sourceProjectID, PreviousLocatorID: expectedLocatorProjectID,
		PreviousHead: expectedCurrentHead, RecoveryHead: backup.HeadHash,
		RecoveryBackupDigest: backup.Digest, TargetRootDigest: targetRootDigest, DestinationRootDigest: destinationRootDigest,
		ReplacementProjectID: replacementProjectID, RotatedAt: operationTime.Format(time.RFC3339Nano),
		Reason: reason, IdempotencyKey: idempotencyKey,
	}
	retirementRaw, err := json.Marshal(retirement)
	if err != nil {
		return AuthorityRelocationReceipt{}, err
	}
	retirementRaw, err = jcs.Transform(retirementRaw)
	if err != nil {
		return AuthorityRelocationReceipt{}, err
	}
	committedRaw, err := sourceJournal.RetireAuthority(expectedCurrentHead, retirementRaw, operationTime, func(current []journal.Segment) error {
		state, err := reduceSegments(sourceProjectID, current)
		if err != nil {
			return err
		}
		if err := validateAuthorityQuiescent(state, operationTime, "authority relocation"); err != nil {
			return err
		}
		if len(envelope.Segments) > len(current) {
			return fmt.Errorf("recovery backup is not a prefix of relocation source authority")
		}
		for index := range envelope.Segments {
			if envelope.Segments[index].SegmentHash != current[index].SegmentHash {
				return fmt.Errorf("recovery backup is not a prefix of relocation source authority")
			}
		}
		return nil
	}, func(existing []byte) error {
		var committed authorityRetirement
		if err := decodeStrictAuthorityJSON(existing, &committed); err != nil {
			return fmt.Errorf("decode authority relocation marker: %w", err)
		}
		return validateRelocationIntent(committed, sourceProjectID, expectedLocatorProjectID, expectedCurrentHead, backup.HeadHash, backup.Digest, targetRootDigest, destinationRootDigest, replacementProjectID, reason, idempotencyKey)
	}, func(committed []byte) error {
		var actual authorityRetirement
		if err := decodeStrictAuthorityJSON(committed, &actual); err != nil {
			return err
		}
		return prepareAndPublishRelocation(target.Root, actual, committed, confirmLocator)
	})
	if err != nil {
		return AuthorityRelocationReceipt{}, err
	}
	if err := decodeStrictAuthorityJSON(committedRaw, &retirement); err != nil {
		return AuthorityRelocationReceipt{}, err
	}
	return authorityRelocationReceipt(retirement)
}

func authorityDescendsFrom(sourceProjectID, expectedAncestorID string) error {
	currentID := sourceProjectID
	visited := map[string]bool{}
	for len(visited) < maxLocalAuthorityStores && !visited[currentID] {
		visited[currentID] = true
		dataDir, err := project.ResolveClaimedAuthority(currentID)
		if err != nil {
			return fmt.Errorf("resolve authority lineage for %s: %w", currentID, err)
		}
		lineage, err := project.ReadAuthorityLineage(dataDir)
		if err != nil {
			return fmt.Errorf("relocation source has no claim-bound predecessor lineage")
		}
		if lineage.PreviousProjectID == expectedAncestorID {
			return nil
		}
		if lineage.Operation == "legacy-adoption" {
			break
		}
		currentID = lineage.PreviousProjectID
	}
	return fmt.Errorf("relocation source authority is not descended from the target locator identity")
}

func prepareAndPublishRelocation(targetRoot string, retirement authorityRetirement, retirementRaw []byte, confirmLocator func(string) error) error {
	lineage := authorityLineage(retirement, "relocation")
	dataDir, err := project.PrepareReplacementAuthority(retirement.ReplacementProjectID, lineage)
	if err != nil {
		candidateDir, pathErr := project.DataDirForProjectID(retirement.ReplacementProjectID)
		if pathErr != nil || project.RestoreAuthorityLineage(candidateDir, retirement.ReplacementProjectID, lineage) != nil {
			return err
		}
		dataDir = candidateDir
	}
	retirementSum := sha256.Sum256(retirementRaw)
	establishment := authorityEstablishment{
		APIVersion: "dagrail.io/authority-establishment/v1alpha1", Kind: "AuthorityEstablishment",
		ProjectID: retirement.ReplacementProjectID, PreviousProjectID: retirement.PreviousProjectID,
		Operation: "relocation", EstablishedAt: retirement.RotatedAt,
		ProvenanceDigest: "sha256:" + fmt.Sprintf("%x", retirementSum[:]),
	}
	raw, err := json.Marshal(establishment)
	if err != nil {
		return err
	}
	raw, err = jcs.Transform(raw)
	if err != nil {
		return err
	}
	establishedAt, err := time.Parse(time.RFC3339Nano, establishment.EstablishedAt)
	if err != nil {
		return err
	}
	replacementJournal, err := journal.OpenForAuthorityEstablishment(dataDir, retirement.ReplacementProjectID)
	if err != nil {
		return err
	}
	if _, err := replacementJournal.EstablishAuthority(raw, establishedAt); err != nil {
		return err
	}
	if err := prepareReplacementProjection(dataDir, retirement.ReplacementProjectID, replacementJournal); err != nil {
		return err
	}
	if _, err := project.RotateAuthority(targetRoot, retirement.PreviousLocatorID, retirement.ReplacementProjectID, lineage); err != nil {
		return err
	}
	if confirmLocator == nil {
		return project.SyncProjectLocator(targetRoot)
	}
	return confirmLocator(targetRoot)
}

func (s *Service) repairCurrentAuthorityLineage() error {
	currentID := s.Project.Config.ProjectID
	projectsRoot := filepath.Dir(s.Project.DataDir)
	entries, err := os.ReadDir(projectsRoot)
	if err != nil {
		return err
	}
	if len(entries) > maxLocalAuthorityStores {
		return fmt.Errorf("local authority root exceeds bounded recovery scan")
	}
	var recovered *project.AuthorityLineage
	for _, entry := range entries {
		candidateID := entry.Name()
		if candidateID == currentID || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		parsed, parseErr := uuid.Parse(candidateID)
		if parseErr != nil || parsed.String() != candidateID {
			continue
		}
		candidateDir := filepath.Join(projectsRoot, candidateID)
		journalInfo, statErr := os.Lstat(filepath.Join(candidateDir, "journal"))
		if statErr != nil || !journalInfo.IsDir() || journalInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		candidateJournal, openErr := journal.OpenRecovery(candidateDir, candidateID)
		if openErr != nil {
			continue
		}
		raw, exists, readErr := candidateJournal.AuthorityRetirement()
		if readErr != nil || !exists {
			continue
		}
		var retirement authorityRetirement
		if decodeStrictAuthorityJSON(raw, &retirement) != nil || retirement.ReplacementProjectID != currentID || validateAnyRetirement(retirement) != nil {
			continue
		}
		operation := "rotation"
		if retirement.APIVersion == AuthorityAdoptionAPIVersion && retirement.Kind == legacyRetirementKind {
			operation = "legacy-adoption"
		} else if retirement.APIVersion == AuthorityRelocationAPIVersion && retirement.Kind == relocationRetirementKind {
			operation = "relocation"
		}
		lineage := authorityLineage(retirement, operation)
		if recovered != nil && *recovered != lineage {
			return fmt.Errorf("multiple predecessor authorities claim the current replacement identity")
		}
		recovered = &lineage
	}
	if recovered == nil {
		return fmt.Errorf("claim-authenticated predecessor retirement was not found")
	}
	return project.RestoreAuthorityLineage(s.Project.DataDir, currentID, *recovered)
}

func (s *Service) retryAuthorityRotation(previousProjectID string, backup BackupReport, expectedCurrentHead, reason, idempotencyKey string) (AuthorityRotationReceipt, error) {
	currentID := s.Project.Config.ProjectID
	visited := map[string]bool{}
	for len(visited) < maxLocalAuthorityStores && !visited[currentID] {
		visited[currentID] = true
		dataDir, err := project.DataDirForProjectID(currentID)
		if err != nil || project.ValidateAuthorityClaim(dataDir, currentID) != nil {
			break
		}
		lineage, err := project.ReadAuthorityLineage(dataDir)
		if err != nil || lineage.Operation != "rotation" {
			break
		}
		retirement := authorityRetirement{APIVersion: authorityRetirementAPIVersion, Kind: rotationRetirementKind, PreviousProjectID: lineage.PreviousProjectID, PreviousHead: lineage.PreviousHead, RecoveryHead: lineage.RecoveryHead, RecoveryBackupDigest: lineage.RecoveryBackupDigest, ReplacementProjectID: currentID, RotatedAt: lineage.RotatedAt, Reason: lineage.Reason, IdempotencyKey: lineage.IdempotencyKey}
		if lineage.PreviousProjectID == previousProjectID {
			if err := validateRetirementIntent(retirement, previousProjectID, expectedCurrentHead, backup.HeadHash, backup.Digest, currentID, reason, idempotencyKey); err != nil {
				return AuthorityRotationReceipt{}, err
			}
			if err := s.confirmProjectLocator(); err != nil {
				return AuthorityRotationReceipt{}, err
			}
			return authorityRotationReceipt(retirement)
		}
		currentID = lineage.PreviousProjectID
	}
	return AuthorityRotationReceipt{}, fmt.Errorf("backup project %s does not match current project %s", previousProjectID, s.Project.Config.ProjectID)
}

func (s *Service) confirmProjectLocator() error {
	if s.ConfirmLocator != nil {
		return s.ConfirmLocator(s.Project.Root)
	}
	return project.SyncProjectLocator(s.Project.Root)
}

func (s *Service) prepareAndPublishReplacement(retirement authorityRetirement, retirementRaw []byte, operation string) error {
	lineage := authorityLineage(retirement, operation)
	dataDir, err := project.PrepareReplacementAuthority(retirement.ReplacementProjectID, lineage)
	if err != nil {
		return err
	}
	retirementSum := sha256.Sum256(retirementRaw)
	establishment := authorityEstablishment{
		APIVersion: "dagrail.io/authority-establishment/v1alpha1", Kind: "AuthorityEstablishment",
		ProjectID: retirement.ReplacementProjectID, PreviousProjectID: retirement.PreviousProjectID,
		Operation: operation, EstablishedAt: retirement.RotatedAt,
		ProvenanceDigest: "sha256:" + fmt.Sprintf("%x", retirementSum[:]),
	}
	raw, err := json.Marshal(establishment)
	if err != nil {
		return err
	}
	raw, err = jcs.Transform(raw)
	if err != nil {
		return err
	}
	establishedAt, err := time.Parse(time.RFC3339Nano, establishment.EstablishedAt)
	if err != nil {
		return err
	}
	replacementJournal, err := journal.OpenForAuthorityEstablishment(dataDir, retirement.ReplacementProjectID)
	if err != nil {
		return err
	}
	if _, err := replacementJournal.EstablishAuthority(raw, establishedAt); err != nil {
		return err
	}
	if err := prepareReplacementProjection(dataDir, retirement.ReplacementProjectID, replacementJournal); err != nil {
		return err
	}
	if _, err := project.RotateAuthority(s.Project.Root, retirement.PreviousProjectID, retirement.ReplacementProjectID, lineage); err != nil {
		return err
	}
	return s.confirmProjectLocator()
}

func prepareReplacementProjection(dataDir, projectID string, replacementJournal *journal.Store) error {
	return replacementJournal.WithSnapshot(func(segments []journal.Segment) error {
		state, err := reduceSegments(projectID, segments)
		if err != nil {
			return err
		}
		replacementProjection, err := projection.Open(dataDir)
		if err != nil {
			return err
		}
		return replacementProjection.Rebuild(state, segments)
	})
}

func (s *Service) retryLegacyAuthorityAdoption(previousProjectID, previousHead, reason, idempotencyKey string) (AuthorityAdoptionReceipt, error) {
	currentID := s.Project.Config.ProjectID
	visited := map[string]bool{}
	for len(visited) < maxLocalAuthorityStores && !visited[currentID] {
		visited[currentID] = true
		dataDir, err := project.DataDirForProjectID(currentID)
		if err != nil || project.ValidateAuthorityClaim(dataDir, currentID) != nil {
			break
		}
		lineage, err := project.ReadAuthorityLineage(dataDir)
		if err != nil {
			break
		}
		if lineage.PreviousProjectID == previousProjectID {
			if lineage.Operation != "legacy-adoption" {
				return AuthorityAdoptionReceipt{}, fmt.Errorf("project identity was replaced by a different recovery operation")
			}
			retirement := authorityRetirement{APIVersion: AuthorityAdoptionAPIVersion, Kind: legacyRetirementKind, PreviousProjectID: lineage.PreviousProjectID, PreviousHead: lineage.PreviousHead, RecoveryHead: lineage.RecoveryHead, RecoveryBackupDigest: lineage.RecoveryBackupDigest, ReplacementProjectID: currentID, RotatedAt: lineage.RotatedAt, Reason: lineage.Reason, IdempotencyKey: lineage.IdempotencyKey}
			if err := validateLegacyRetirementIntent(retirement, previousProjectID, previousHead, lineage.RecoveryBackupDigest, currentID, reason, idempotencyKey); err != nil {
				return AuthorityAdoptionReceipt{}, err
			}
			if err := s.confirmProjectLocator(); err != nil {
				return AuthorityAdoptionReceipt{}, err
			}
			return authorityAdoptionReceipt(retirement)
		}
		currentID = lineage.PreviousProjectID
	}
	return AuthorityAdoptionReceipt{}, fmt.Errorf("legacy project %s does not match current project %s", previousProjectID, s.Project.Config.ProjectID)
}

func validateAuthorityQuiescent(state domain.State, at time.Time, operation string) error {
	for _, lease := range state.Leases {
		if lease.Active {
			expires, err := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
			if err != nil || at.Before(expires) {
				return fmt.Errorf("%s requires all Role leases to be inactive or expired", operation)
			}
		}
	}
	for _, effect := range state.Effects {
		if !oneOf(effect.Status, "confirmed", "failed") {
			return fmt.Errorf("%s requires every Effect to be closed", operation)
		}
	}
	for _, resource := range state.Resources {
		if resource.Status == "active" {
			return fmt.Errorf("%s requires every Resource lease to be closed", operation)
		}
	}
	for _, incident := range state.Incidents {
		if incident.Status != "resolved" {
			return fmt.Errorf("%s requires every Incident to be resolved", operation)
		}
	}
	return nil
}

func authorityLineage(retirement authorityRetirement, operation string) project.AuthorityLineage {
	return project.AuthorityLineage{Operation: operation, PreviousProjectID: retirement.PreviousProjectID, PreviousLocatorID: retirement.PreviousLocatorID, TargetRootDigest: retirement.TargetRootDigest, DestinationRootDigest: retirement.DestinationRootDigest, PreviousHead: retirement.PreviousHead, RecoveryHead: retirement.RecoveryHead, RecoveryBackupDigest: retirement.RecoveryBackupDigest, RotatedAt: retirement.RotatedAt, Reason: retirement.Reason, IdempotencyKey: retirement.IdempotencyKey}
}

func legacyRetirementReservationDigest(retirement authorityRetirement) (string, error) {
	retirement.RotatedAt = ""
	return authorityDigest("dagrail-legacy-authority-retirement-reservation-v1\x00", retirement)
}

func stableLegacyBackupCreatedAt(segments []journal.Segment) string {
	if len(segments) == 0 {
		return time.Unix(0, 0).UTC().Format(time.RFC3339Nano)
	}
	return segments[len(segments)-1].CommittedAt
}

func validateRetirementIntent(retirement authorityRetirement, previousProjectID, previousHead, recoveryHead, backupDigest, replacementProjectID, reason, idempotencyKey string) error {
	if retirement.APIVersion != authorityRetirementAPIVersion || retirement.Kind != rotationRetirementKind || retirement.PreviousProjectID != previousProjectID || retirement.PreviousHead != previousHead || retirement.RecoveryHead != recoveryHead || retirement.RecoveryBackupDigest != backupDigest || retirement.ReplacementProjectID != replacementProjectID || retirement.Reason != reason || retirement.IdempotencyKey != idempotencyKey {
		return fmt.Errorf("authority rotation idempotency key is already bound to different intent")
	}
	if !validAuthorityHash(retirement.PreviousHead) || !validAuthorityHash(retirement.RecoveryHead) || !validAuthorityDigest(retirement.RecoveryBackupDigest) {
		return fmt.Errorf("authority retirement recovery evidence is invalid")
	}
	if parsed, err := uuid.Parse(retirement.ReplacementProjectID); err != nil || parsed.String() != retirement.ReplacementProjectID || retirement.ReplacementProjectID == retirement.PreviousProjectID {
		return fmt.Errorf("authority retirement replacement identity is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, retirement.RotatedAt); err != nil {
		return fmt.Errorf("authority retirement timestamp is invalid")
	}
	return nil
}

func validateLegacyRetirementIntent(retirement authorityRetirement, previousProjectID, previousHead, backupDigest, replacementProjectID, reason, idempotencyKey string) error {
	if retirement.APIVersion != AuthorityAdoptionAPIVersion || retirement.Kind != legacyRetirementKind || retirement.PreviousProjectID != previousProjectID || retirement.PreviousHead != previousHead || retirement.RecoveryHead != previousHead || retirement.RecoveryBackupDigest != backupDigest || retirement.ReplacementProjectID != replacementProjectID || retirement.Reason != reason || retirement.IdempotencyKey != idempotencyKey {
		return fmt.Errorf("legacy authority adoption idempotency key is already bound to different intent")
	}
	if (retirement.PreviousHead != "" && !validAuthorityHash(retirement.PreviousHead)) || !validAuthorityDigest(retirement.RecoveryBackupDigest) {
		return fmt.Errorf("legacy authority retirement evidence is invalid")
	}
	previous, previousErr := uuid.Parse(retirement.PreviousProjectID)
	replacement, replacementErr := uuid.Parse(retirement.ReplacementProjectID)
	if previousErr != nil || previous.String() != retirement.PreviousProjectID || replacementErr != nil || replacement.String() != retirement.ReplacementProjectID || retirement.PreviousProjectID == retirement.ReplacementProjectID {
		return fmt.Errorf("legacy authority replacement identity is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, retirement.RotatedAt); err != nil {
		return fmt.Errorf("legacy authority retirement timestamp is invalid")
	}
	return nil
}

func validateRelocationIntent(retirement authorityRetirement, sourceProjectID, previousLocatorProjectID, sourceHead, recoveryHead, backupDigest, targetRootDigest, destinationRootDigest, replacementProjectID, reason, idempotencyKey string) error {
	if retirement.APIVersion != AuthorityRelocationAPIVersion || retirement.Kind != relocationRetirementKind || retirement.PreviousProjectID != sourceProjectID || retirement.PreviousLocatorID != previousLocatorProjectID || retirement.PreviousHead != sourceHead || retirement.RecoveryHead != recoveryHead || retirement.RecoveryBackupDigest != backupDigest || retirement.TargetRootDigest != targetRootDigest || retirement.DestinationRootDigest != destinationRootDigest || retirement.ReplacementProjectID != replacementProjectID || retirement.Reason != reason || retirement.IdempotencyKey != idempotencyKey {
		return fmt.Errorf("authority relocation idempotency key is already bound to different intent")
	}
	source, sourceErr := uuid.Parse(retirement.PreviousProjectID)
	locator, locatorErr := uuid.Parse(retirement.PreviousLocatorID)
	replacement, replacementErr := uuid.Parse(retirement.ReplacementProjectID)
	if sourceErr != nil || source.String() != retirement.PreviousProjectID || locatorErr != nil || locator.String() != retirement.PreviousLocatorID || replacementErr != nil || replacement.String() != retirement.ReplacementProjectID || retirement.PreviousProjectID == retirement.PreviousLocatorID || retirement.PreviousProjectID == retirement.ReplacementProjectID || retirement.PreviousLocatorID == retirement.ReplacementProjectID || !validAuthorityHash(retirement.PreviousHead) || !validAuthorityHash(retirement.RecoveryHead) || !validAuthorityDigest(retirement.RecoveryBackupDigest) || !validAuthorityDigest(retirement.TargetRootDigest) || !validAuthorityDigest(retirement.DestinationRootDigest) {
		return fmt.Errorf("authority relocation evidence is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, retirement.RotatedAt); err != nil {
		return fmt.Errorf("authority relocation timestamp is invalid")
	}
	return nil
}

func validateAnyRetirement(retirement authorityRetirement) error {
	switch {
	case retirement.APIVersion == authorityRetirementAPIVersion && retirement.Kind == rotationRetirementKind:
		return validateRetirementIntent(retirement, retirement.PreviousProjectID, retirement.PreviousHead, retirement.RecoveryHead, retirement.RecoveryBackupDigest, retirement.ReplacementProjectID, retirement.Reason, retirement.IdempotencyKey)
	case retirement.APIVersion == AuthorityAdoptionAPIVersion && retirement.Kind == legacyRetirementKind:
		return validateLegacyRetirementIntent(retirement, retirement.PreviousProjectID, retirement.PreviousHead, retirement.RecoveryBackupDigest, retirement.ReplacementProjectID, retirement.Reason, retirement.IdempotencyKey)
	case retirement.APIVersion == AuthorityRelocationAPIVersion && retirement.Kind == relocationRetirementKind:
		return validateRelocationIntent(retirement, retirement.PreviousProjectID, retirement.PreviousLocatorID, retirement.PreviousHead, retirement.RecoveryHead, retirement.RecoveryBackupDigest, retirement.TargetRootDigest, retirement.DestinationRootDigest, retirement.ReplacementProjectID, retirement.Reason, retirement.IdempotencyKey)
	default:
		return fmt.Errorf("authority retirement kind is unsupported")
	}
}

func validateAuthorityEstablishment(establishment authorityEstablishment, projectID string) error {
	current, currentErr := uuid.Parse(establishment.ProjectID)
	if establishment.APIVersion != "dagrail.io/authority-establishment/v1alpha1" || establishment.Kind != "AuthorityEstablishment" || establishment.ProjectID != projectID || currentErr != nil || current.String() != establishment.ProjectID || !validAuthorityDigest(establishment.ProvenanceDigest) {
		return fmt.Errorf("authority establishment is structurally invalid")
	}
	switch establishment.Operation {
	case "initialization":
		if establishment.PreviousProjectID != "" {
			return fmt.Errorf("initial authority establishment has a predecessor")
		}
	case "rotation", "legacy-adoption", "relocation":
		previous, err := uuid.Parse(establishment.PreviousProjectID)
		if err != nil || previous.String() != establishment.PreviousProjectID || establishment.PreviousProjectID == establishment.ProjectID {
			return fmt.Errorf("replacement authority establishment predecessor is invalid")
		}
	default:
		return fmt.Errorf("authority establishment operation is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, establishment.EstablishedAt); err != nil {
		return fmt.Errorf("authority establishment timestamp is invalid")
	}
	return nil
}

func authorityRelocationReceipt(retirement authorityRetirement) (AuthorityRelocationReceipt, error) {
	receipt := AuthorityRelocationReceipt{APIVersion: AuthorityRelocationAPIVersion, Kind: "AuthorityRelocationReceipt", SourceProjectID: retirement.PreviousProjectID, PreviousLocatorProjectID: retirement.PreviousLocatorID, SourceHead: retirement.PreviousHead, RecoveryHead: retirement.RecoveryHead, RecoveryBackupDigest: retirement.RecoveryBackupDigest, TargetRootDigest: retirement.TargetRootDigest, DestinationRootDigest: retirement.DestinationRootDigest, ReplacementProjectID: retirement.ReplacementProjectID, RelocatedAt: retirement.RotatedAt, Reason: retirement.Reason, IdempotencyKey: retirement.IdempotencyKey}
	digest, err := authorityDigest("dagrail-authority-relocation-receipt-v1\x00", receipt)
	if err != nil {
		return AuthorityRelocationReceipt{}, err
	}
	receipt.ReceiptDigest = digest
	return receipt, nil
}

func VerifyAuthorityRelocationReceipt(receipt AuthorityRelocationReceipt) error {
	source, sourceErr := uuid.Parse(receipt.SourceProjectID)
	locator, locatorErr := uuid.Parse(receipt.PreviousLocatorProjectID)
	replacement, replacementErr := uuid.Parse(receipt.ReplacementProjectID)
	_, timeErr := time.Parse(time.RFC3339Nano, receipt.RelocatedAt)
	if receipt.APIVersion != AuthorityRelocationAPIVersion || receipt.Kind != "AuthorityRelocationReceipt" || sourceErr != nil || source.String() != receipt.SourceProjectID || locatorErr != nil || locator.String() != receipt.PreviousLocatorProjectID || replacementErr != nil || replacement.String() != receipt.ReplacementProjectID || receipt.SourceProjectID == receipt.PreviousLocatorProjectID || receipt.SourceProjectID == receipt.ReplacementProjectID || receipt.PreviousLocatorProjectID == receipt.ReplacementProjectID || !validAuthorityHash(receipt.SourceHead) || !validAuthorityHash(receipt.RecoveryHead) || !validAuthorityDigest(receipt.RecoveryBackupDigest) || !validAuthorityDigest(receipt.TargetRootDigest) || !validAuthorityDigest(receipt.DestinationRootDigest) || timeErr != nil || strings.TrimSpace(receipt.Reason) == "" || len([]byte(receipt.Reason)) > 1024 || strings.TrimSpace(receipt.IdempotencyKey) == "" || len([]byte(receipt.IdempotencyKey)) > 256 || !validAuthorityDigest(receipt.ReceiptDigest) {
		return fmt.Errorf("authority relocation receipt is structurally invalid")
	}
	claimed := receipt.ReceiptDigest
	receipt.ReceiptDigest = ""
	expected, err := authorityDigest("dagrail-authority-relocation-receipt-v1\x00", receipt)
	if err != nil {
		return err
	}
	if claimed != expected {
		return fmt.Errorf("authority relocation receipt digest mismatch")
	}
	return nil
}

func authorityRotationReceipt(retirement authorityRetirement) (AuthorityRotationReceipt, error) {
	receipt := AuthorityRotationReceipt{APIVersion: AuthorityRotationAPIVersion, Kind: "AuthorityRotationReceipt", PreviousProjectID: retirement.PreviousProjectID, PreviousHead: retirement.PreviousHead, RecoveryHead: retirement.RecoveryHead, RecoveryBackupDigest: retirement.RecoveryBackupDigest, ReplacementProjectID: retirement.ReplacementProjectID, RotatedAt: retirement.RotatedAt, Reason: retirement.Reason, IdempotencyKey: retirement.IdempotencyKey}
	digest, err := authorityDigest("dagrail-authority-rotation-receipt-v1\x00", receipt)
	if err != nil {
		return AuthorityRotationReceipt{}, err
	}
	receipt.ReceiptDigest = digest
	return receipt, nil
}

func VerifyAuthorityRotationReceipt(receipt AuthorityRotationReceipt) error {
	previous, previousErr := uuid.Parse(receipt.PreviousProjectID)
	replacement, replacementErr := uuid.Parse(receipt.ReplacementProjectID)
	_, timeErr := time.Parse(time.RFC3339Nano, receipt.RotatedAt)
	if receipt.APIVersion != AuthorityRotationAPIVersion || receipt.Kind != "AuthorityRotationReceipt" || previousErr != nil || previous.String() != receipt.PreviousProjectID || replacementErr != nil || replacement.String() != receipt.ReplacementProjectID || receipt.PreviousProjectID == receipt.ReplacementProjectID || !validAuthorityHash(receipt.PreviousHead) || !validAuthorityHash(receipt.RecoveryHead) || !validAuthorityDigest(receipt.RecoveryBackupDigest) || timeErr != nil || strings.TrimSpace(receipt.Reason) == "" || len([]byte(receipt.Reason)) > 1024 || strings.TrimSpace(receipt.IdempotencyKey) == "" || len([]byte(receipt.IdempotencyKey)) > 256 || !validAuthorityDigest(receipt.ReceiptDigest) {
		return fmt.Errorf("authority rotation receipt is structurally invalid")
	}
	claimed := receipt.ReceiptDigest
	receipt.ReceiptDigest = ""
	expected, err := authorityDigest("dagrail-authority-rotation-receipt-v1\x00", receipt)
	if err != nil {
		return err
	}
	if claimed != expected {
		return fmt.Errorf("authority rotation receipt digest mismatch")
	}
	return nil
}

func authorityAdoptionReceipt(retirement authorityRetirement) (AuthorityAdoptionReceipt, error) {
	receipt := AuthorityAdoptionReceipt{APIVersion: AuthorityAdoptionAPIVersion, Kind: "AuthorityAdoptionReceipt", PreviousProjectID: retirement.PreviousProjectID, PreviousHead: retirement.PreviousHead, SourceBackupDigest: retirement.RecoveryBackupDigest, ReplacementProjectID: retirement.ReplacementProjectID, AdoptedAt: retirement.RotatedAt, Reason: retirement.Reason, IdempotencyKey: retirement.IdempotencyKey}
	digest, err := authorityDigest("dagrail-authority-adoption-receipt-v1\x00", receipt)
	if err != nil {
		return AuthorityAdoptionReceipt{}, err
	}
	receipt.ReceiptDigest = digest
	return receipt, nil
}

func VerifyAuthorityAdoptionReceipt(receipt AuthorityAdoptionReceipt) error {
	previous, previousErr := uuid.Parse(receipt.PreviousProjectID)
	replacement, replacementErr := uuid.Parse(receipt.ReplacementProjectID)
	_, timeErr := time.Parse(time.RFC3339Nano, receipt.AdoptedAt)
	if receipt.APIVersion != AuthorityAdoptionAPIVersion || receipt.Kind != "AuthorityAdoptionReceipt" || previousErr != nil || previous.String() != receipt.PreviousProjectID || replacementErr != nil || replacement.String() != receipt.ReplacementProjectID || receipt.PreviousProjectID == receipt.ReplacementProjectID || (receipt.PreviousHead != "" && !validAuthorityHash(receipt.PreviousHead)) || !validAuthorityDigest(receipt.SourceBackupDigest) || timeErr != nil || strings.TrimSpace(receipt.Reason) == "" || len([]byte(receipt.Reason)) > 1024 || strings.TrimSpace(receipt.IdempotencyKey) == "" || len([]byte(receipt.IdempotencyKey)) > 256 || !validAuthorityDigest(receipt.ReceiptDigest) {
		return fmt.Errorf("authority adoption receipt is structurally invalid")
	}
	claimed := receipt.ReceiptDigest
	receipt.ReceiptDigest = ""
	expected, err := authorityDigest("dagrail-authority-adoption-receipt-v1\x00", receipt)
	if err != nil {
		return err
	}
	if claimed != expected {
		return fmt.Errorf("authority adoption receipt digest mismatch")
	}
	return nil
}

func validAuthorityHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validAuthorityDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validAuthorityHash(strings.TrimPrefix(value, "sha256:"))
}

func decodeBackup(data []byte) (BackupEnvelope, error) {
	if len(data) > MaxPortableJournalBytes {
		return BackupEnvelope{}, fmt.Errorf("backup exceeds 256 MiB limit")
	}
	// A portable backup aggregates independently bounded journal segments, so
	// its total value count may legitimately exceed the single-authority-
	// document limit. The byte cap keeps this scan linear and bounded; all
	// other authority JSON guards remain active, and ValidateSegments below
	// revalidates every contained segment. Each counted JSON value consumes at
	// least one input byte, so len(data) admits every valid bounded aggregate
	// without introducing an unbounded work factor.
	if err := domain.ValidateAuthorityJSONWithLimits(data, domain.MaxAuthorityDepth+2, len(data)); err != nil {
		return BackupEnvelope{}, err
	}
	envelope, err := decodeBackupEnvelope(data)
	if err != nil {
		return BackupEnvelope{}, err
	}
	if envelope.APIVersion != BackupAPIVersion || envelope.Kind != "JournalBackup" || envelope.Project.ProjectID == "" || envelope.Digest == "" {
		return envelope, fmt.Errorf("invalid DAGrail backup envelope")
	}
	expected, err := backupDigest(envelope)
	if err != nil {
		return envelope, err
	}
	if envelope.Digest != expected {
		return envelope, fmt.Errorf("backup digest mismatch")
	}
	return envelope, nil
}

func decodeBackupEnvelope(data []byte) (BackupEnvelope, error) {
	var envelope BackupEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return envelope, fmt.Errorf("backup envelope must be a JSON object")
	}
	segmentProjectID := ""
	previousHash := ""
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return envelope, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return envelope, fmt.Errorf("backup envelope contains a non-string key")
		}
		switch key {
		case "apiVersion":
			err = decoder.Decode(&envelope.APIVersion)
		case "kind":
			err = decoder.Decode(&envelope.Kind)
		case "project":
			var raw json.RawMessage
			if err = decoder.Decode(&raw); err == nil {
				err = decodeStrictAuthorityJSON(raw, &envelope.Project)
			}
		case "createdAt":
			err = decoder.Decode(&envelope.CreatedAt)
		case "segments":
			err = decodeBackupSegments(decoder, &envelope, &segmentProjectID, &previousHash)
		case "digest":
			err = decoder.Decode(&envelope.Digest)
		default:
			return envelope, fmt.Errorf("backup envelope contains unknown field %q", key)
		}
		if err != nil {
			return envelope, fmt.Errorf("decode backup field %s: %w", key, err)
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return envelope, fmt.Errorf("backup envelope has an invalid terminator")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return envelope, fmt.Errorf("backup has trailing content")
	}
	if segmentProjectID != "" && envelope.Project.ProjectID != segmentProjectID {
		return envelope, fmt.Errorf("backup project %s does not match journal project %s", envelope.Project.ProjectID, segmentProjectID)
	}
	return envelope, nil
}

func decodeBackupSegments(decoder *json.Decoder, envelope *BackupEnvelope, segmentProjectID, previousHash *string) error {
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('[') {
		return fmt.Errorf("backup segments must be an array")
	}
	for decoder.More() {
		if len(envelope.Segments) >= journal.MaxSegmentCount {
			return fmt.Errorf("journal exceeds %d segments", journal.MaxSegmentCount)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
		var segment journal.Segment
		if err := decodeStrictAuthorityJSON(raw, &segment); err != nil {
			return fmt.Errorf("decode journal segment %d: %w", len(envelope.Segments)+1, err)
		}
		if len(envelope.Segments) == 0 {
			if segment.ProjectID == "" {
				return fmt.Errorf("journal segment project ID is required")
			}
			*segmentProjectID = segment.ProjectID
		}
		hash, err := journal.ValidateSegment(*segmentProjectID, uint64(len(envelope.Segments)+1), *previousHash, segment)
		if err != nil {
			return err
		}
		envelope.Segments = append(envelope.Segments, segment)
		*previousHash = hash
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(']') {
		return fmt.Errorf("backup segments have an invalid terminator")
	}
	return nil
}

func backupDigest(envelope BackupEnvelope) (string, error) {
	envelope.Digest = ""
	return authorityDigest("dagrail-journal-backup-v1\x00", envelope)
}

func backupReport(envelope BackupEnvelope) BackupReport {
	report := BackupReport{Valid: true, ProjectID: envelope.Project.ProjectID, Segments: len(envelope.Segments), Digest: envelope.Digest}
	if len(envelope.Segments) > 0 {
		report.HeadSequence, report.HeadHash = envelope.Segments[len(envelope.Segments)-1].Sequence, envelope.Segments[len(envelope.Segments)-1].SegmentHash
	}
	return report
}

func (s *Service) History(after uint64, limit int) (HistoryPage, error) {
	return s.HistoryContext(context.Background(), after, limit)
}

func (s *Service) HistoryContext(ctx context.Context, after uint64, limit int) (HistoryPage, error) {
	if err := ctx.Err(); err != nil {
		return HistoryPage{}, err
	}
	segments, err := s.Journal.ReadAll()
	if err != nil {
		return HistoryPage{}, err
	}
	return historyFromSegmentsContext(ctx, segments, after, limit)
}

func historyFromSegmentsContext(ctx context.Context, segments []journal.Segment, after uint64, limit int) (HistoryPage, error) {
	if limit == 0 {
		limit = 25
	}
	if limit < 1 || limit > 100 {
		return HistoryPage{}, fmt.Errorf("history limit must be 1..100")
	}
	if after > uint64(len(segments)) {
		return HistoryPage{}, fmt.Errorf("history cursor exceeds journal head")
	}
	end := int(after) + limit
	if end > len(segments) {
		end = len(segments)
	}
	page := HistoryPage{After: after, NextCursor: after, Entries: []HistoryEntry{}, Truncated: end < len(segments)}
	for _, segment := range segments[int(after):end] {
		if err := ctx.Err(); err != nil {
			return HistoryPage{}, err
		}
		eventTypes := make([]string, 0, len(segment.Events))
		for _, event := range segment.Events {
			if err := ctx.Err(); err != nil {
				return HistoryPage{}, err
			}
			eventTypes = append(eventTypes, event.Type)
		}
		page.Entries = append(page.Entries, HistoryEntry{Sequence: segment.Sequence, CommandKind: segment.Command.Kind, ActorRole: segment.Command.ActorRole, EventTypes: eventTypes, CommittedAt: segment.CommittedAt, SegmentHash: segment.SegmentHash})
		page.NextCursor = segment.Sequence
	}
	return page, nil
}

func (s *Service) Status() (OperationalStatus, error) {
	return s.StatusContext(context.Background())
}

func (s *Service) StatusContext(ctx context.Context) (OperationalStatus, error) {
	if err := ctx.Err(); err != nil {
		return OperationalStatus{}, err
	}
	state, _, err := s.load()
	if err != nil {
		return OperationalStatus{}, err
	}
	return operationalStatusFromStateContext(ctx, state, s.Now().UTC())
}

func operationalStatusFromStateContext(ctx context.Context, state domain.State, now time.Time) (OperationalStatus, error) {
	frontier, err := domain.ComputeFrontierContext(ctx, state)
	if err != nil {
		return OperationalStatus{}, err
	}
	result := OperationalStatus{ProjectID: state.ProjectID, GraphRevision: state.GraphRevision, HeadSequence: state.HeadSequence, HeadHash: state.HeadHash, Nodes: map[string]int{}, Attempts: map[string]int{}, Effects: map[string]int{}, Incidents: map[string]int{}, Frontier: frontier}
	for _, node := range state.Nodes {
		if err := ctx.Err(); err != nil {
			return OperationalStatus{}, err
		}
		result.Nodes[node.Status]++
	}
	for _, attempt := range state.Attempts {
		if err := ctx.Err(); err != nil {
			return OperationalStatus{}, err
		}
		result.Attempts[attempt.Status]++
	}
	for _, effect := range state.Effects {
		if err := ctx.Err(); err != nil {
			return OperationalStatus{}, err
		}
		result.Effects[effect.Status]++
	}
	for id, incident := range state.Incidents {
		if err := ctx.Err(); err != nil {
			return OperationalStatus{}, err
		}
		result.Incidents[incident.Status]++
		if incident.Status == "open" {
			if deadline, parseErr := time.Parse(time.RFC3339Nano, incident.Deadline); parseErr == nil && !now.Before(deadline) {
				result.OverdueIncidents = append(result.OverdueIncidents, id)
			}
		}
	}
	for role, lease := range state.Leases {
		if err := ctx.Err(); err != nil {
			return OperationalStatus{}, err
		}
		if lease.Active {
			if expires, parseErr := time.Parse(time.RFC3339Nano, lease.ExpiresAt); parseErr != nil || !now.Before(expires) {
				result.ExpiredRoleLeases = append(result.ExpiredRoleLeases, role)
			}
		}
	}
	sort.Strings(result.OverdueIncidents)
	sort.Strings(result.ExpiredRoleLeases)
	return result, nil
}
