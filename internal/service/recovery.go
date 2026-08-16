package service

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/projection"
	"github.com/CongBao/dagrail/internal/version"
)

const RecoveryAPIVersion = "dagrail.io/recovery-rehearsal/v1alpha1"

type RecoveryCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Code   string `json:"code"`
}

type RecoverySnapshot struct {
	HeadSequence  uint64 `json:"headSequence"`
	HeadHash      string `json:"headHash,omitempty"`
	GraphRevision string `json:"graphRevision,omitempty"`
	Segments      int    `json:"segments"`
	StateDigest   string `json:"stateDigest"`
}

type RecoveryProjectionProof struct {
	Integrity    bool           `json:"integrity"`
	Schema       int            `json:"schema"`
	HeadSequence uint64         `json:"headSequence"`
	HeadHash     string         `json:"headHash,omitempty"`
	Fingerprint  string         `json:"fingerprint,omitempty"`
	Rows         map[string]int `json:"rows,omitempty"`
}

type RecoveryReport struct {
	APIVersion           string                      `json:"apiVersion"`
	Kind                 string                      `json:"kind"`
	Ready                bool                        `json:"ready"`
	ProjectRef           string                      `json:"projectRef"`
	BuildVersion         string                      `json:"buildVersion"`
	Snapshot             RecoverySnapshot            `json:"snapshot"`
	Compatibility        journal.CompatibilityReport `json:"compatibility"`
	SourceProjection     RecoveryProjectionProof     `json:"sourceProjection"`
	RebuiltProjection    RecoveryProjectionProof     `json:"rebuiltProjection"`
	ProjectionEquivalent bool                        `json:"projectionEquivalent"`
	Checks               []RecoveryCheck             `json:"checks"`
}

// RehearseRecovery performs a read-only rehearsal against an immutable journal
// snapshot. All restore and rebuild writes go to a disposable directory; the live
// journal and projection are never replaced or truncated.
func (s *Service) RehearseRecovery() (RecoveryReport, error) {
	projectDigest := sha256.Sum256(append([]byte("dagrail-recovery-project-v1\x00"), []byte(s.Project.Config.ProjectID)...))
	report := RecoveryReport{
		APIVersion:   RecoveryAPIVersion,
		Kind:         "RecoveryRehearsal",
		ProjectRef:   "sha256:" + hex.EncodeToString(projectDigest[:]),
		BuildVersion: version.Version,
		Checks:       []RecoveryCheck{},
	}
	add := func(name, status, code string) {
		report.Checks = append(report.Checks, RecoveryCheck{Name: name, Status: status, Code: code})
	}
	state, segments, err := s.load()
	if err != nil {
		return report, err
	}
	stateDigest, err := authorityDigest("dagrail-recovery-state-v1\x00", state)
	if err != nil {
		return report, err
	}
	report.Snapshot = RecoverySnapshot{
		HeadSequence: state.HeadSequence, HeadHash: state.HeadHash,
		GraphRevision: state.GraphRevision, Segments: len(segments), StateDigest: stateDigest,
	}
	add("journal-snapshot", "pass", "verified_immutable_prefix")

	compatibility, err := journal.CompatibilityForSegments(segments)
	if err != nil {
		add("schema-compatibility", "fail", "unreadable_schema")
		return report, nil
	}
	report.Compatibility = compatibility
	add("schema-compatibility", "pass", "all_events_upcastable")

	if err := s.Projection.Integrity(); err != nil {
		add("source-projection", "fail", "integrity_failed")
	} else if fingerprint, fingerprintErr := s.Projection.Fingerprint(); fingerprintErr != nil {
		add("source-projection", "fail", "fingerprint_failed")
	} else {
		report.SourceProjection = recoveryProjectionProof(fingerprint)
		report.SourceProjection.Integrity = true
		if fingerprint.Schema > projection.CurrentSchemaVersion {
			add("source-projection", "fail", "future_schema_unsupported")
		} else if fingerprint.HeadSequence != state.HeadSequence || fingerprint.HeadHash != state.HeadHash {
			add("source-projection", "fail", "snapshot_head_mismatch")
		} else {
			add("source-projection", "pass", "snapshot_bound")
		}
	}

	temporary, err := os.MkdirTemp("", "dagrail-recovery-rehearsal-*")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(temporary)
	rehearsalData := filepath.Join(temporary, "journal-data")
	restoredJournal, err := journal.OpenRehearsal(rehearsalData, s.Project.Config.ProjectID)
	if err != nil {
		return report, err
	}
	if err := restoredJournal.RestoreSegments(segments); err != nil {
		add("journal-restore", "fail", "restore_rejected")
		return report, nil
	}
	restored, err := restoredJournal.ReadAll()
	if err != nil || !sameJournalSnapshot(segments, restored) {
		add("journal-restore", "fail", "restored_prefix_mismatch")
		return report, nil
	}
	add("journal-restore", "pass", "exact_prefix_restored")
	restoredService := *s
	restoredService.Journal = restoredJournal
	restoredState, replayedSegments, err := restoredService.load()
	if err != nil || !sameJournalSnapshot(restored, replayedSegments) {
		add("state-replay", "fail", "reducer_replay_failed")
		return report, nil
	}
	restoredStateDigest, err := authorityDigest("dagrail-recovery-state-v1\x00", restoredState)
	if err != nil || restoredStateDigest != stateDigest {
		add("state-replay", "fail", "state_digest_mismatch")
		return report, nil
	}
	add("state-replay", "pass", "state_digest_reproduced")

	rebuilt, err := projection.Open(filepath.Join(temporary, "projection-data"))
	if err != nil {
		return report, err
	}
	if err := rebuilt.Rebuild(restoredState, restored); err != nil {
		add("projection-rebuild", "fail", "rebuild_failed")
		return report, nil
	}
	if err := rebuilt.Integrity(); err != nil {
		add("projection-rebuild", "fail", "integrity_failed")
		return report, nil
	}
	rebuiltFingerprint, err := rebuilt.Fingerprint()
	if err != nil {
		add("projection-rebuild", "fail", "fingerprint_failed")
		return report, nil
	}
	report.RebuiltProjection = recoveryProjectionProof(rebuiltFingerprint)
	report.RebuiltProjection.Integrity = true
	if rebuiltFingerprint.HeadSequence != state.HeadSequence || rebuiltFingerprint.HeadHash != state.HeadHash {
		add("projection-rebuild", "fail", "rebuilt_head_mismatch")
		return report, nil
	}
	add("projection-rebuild", "pass", "disposable_rebuild_verified")

	report.ProjectionEquivalent = report.SourceProjection.Integrity && report.SourceProjection.Fingerprint == report.RebuiltProjection.Fingerprint
	if !report.ProjectionEquivalent {
		add("projection-equivalence", "fail", "logical_fingerprint_mismatch")
		return report, nil
	}
	add("projection-equivalence", "pass", "logical_fingerprints_match")
	report.Ready = true
	return report, nil
}

func recoveryProjectionProof(value projection.LogicalFingerprint) RecoveryProjectionProof {
	return RecoveryProjectionProof{
		Schema: value.Schema, HeadSequence: value.HeadSequence, HeadHash: value.HeadHash,
		Fingerprint: value.Digest, Rows: value.Rows,
	}
}

func sameJournalSnapshot(expected, actual []journal.Segment) bool {
	if len(expected) != len(actual) {
		return false
	}
	for index := range expected {
		if expected[index].Sequence != actual[index].Sequence || expected[index].SegmentHash != actual[index].SegmentHash {
			return false
		}
	}
	return true
}
