package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/project"
	"github.com/gowebpki/jcs"
)

const BackupAPIVersion = "dagrail.io/v1alpha1"

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

func decodeBackup(data []byte) (BackupEnvelope, error) {
	if len(data) > 256*1024*1024 {
		return BackupEnvelope{}, fmt.Errorf("backup exceeds 256 MiB limit")
	}
	if err := domain.ValidateAuthorityJSON(data); err != nil {
		return BackupEnvelope{}, err
	}
	var envelope BackupEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return envelope, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return envelope, fmt.Errorf("backup has trailing content")
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
	if err := journal.ValidateSegments(envelope.Project.ProjectID, envelope.Segments); err != nil {
		return envelope, err
	}
	return envelope, nil
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
	if limit == 0 {
		limit = 25
	}
	if limit < 1 || limit > 100 {
		return HistoryPage{}, fmt.Errorf("history limit must be 1..100")
	}
	segments, err := s.Journal.ReadAll()
	if err != nil {
		return HistoryPage{}, err
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
		eventTypes := make([]string, 0, len(segment.Events))
		for _, event := range segment.Events {
			eventTypes = append(eventTypes, event.Type)
		}
		page.Entries = append(page.Entries, HistoryEntry{Sequence: segment.Sequence, CommandKind: segment.Command.Kind, ActorRole: segment.Command.ActorRole, EventTypes: eventTypes, CommittedAt: segment.CommittedAt, SegmentHash: segment.SegmentHash})
		page.NextCursor = segment.Sequence
	}
	return page, nil
}

func (s *Service) Status() (OperationalStatus, error) {
	state, _, err := s.load()
	if err != nil {
		return OperationalStatus{}, err
	}
	result := OperationalStatus{ProjectID: state.ProjectID, GraphRevision: state.GraphRevision, HeadSequence: state.HeadSequence, HeadHash: state.HeadHash, Nodes: map[string]int{}, Attempts: map[string]int{}, Effects: map[string]int{}, Incidents: map[string]int{}, Frontier: domain.ComputeFrontier(state)}
	for _, node := range state.Nodes {
		result.Nodes[node.Status]++
	}
	for _, attempt := range state.Attempts {
		result.Attempts[attempt.Status]++
	}
	for _, effect := range state.Effects {
		result.Effects[effect.Status]++
	}
	now := s.Now().UTC()
	for id, incident := range state.Incidents {
		result.Incidents[incident.Status]++
		if incident.Status == "open" {
			if deadline, parseErr := time.Parse(time.RFC3339Nano, incident.Deadline); parseErr == nil && !now.Before(deadline) {
				result.OverdueIncidents = append(result.OverdueIncidents, id)
			}
		}
	}
	for role, lease := range state.Leases {
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
