package projection

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/version"
	"github.com/gowebpki/jcs"
)

const checkpointAPIVersion = "dagrail.io/verified-snapshot-checkpoint/v1alpha1"

type checkpointPayload struct {
	APIVersion            string `json:"apiVersion"`
	Kind                  string `json:"kind"`
	ProjectID             string `json:"projectId"`
	HeadSequence          uint64 `json:"headSequence"`
	HeadHash              string `json:"headHash"`
	GraphRevision         string `json:"graphRevision"`
	ProviderSet           string `json:"providerSet"`
	StateDigest           string `json:"stateDigest"`
	SegmentIdentityDigest string `json:"segmentIdentityDigest"`
}

type sealedCheckpoint struct {
	checkpointPayload
	HMAC string `json:"hmac"`
}

type CheckpointReport struct {
	Present      bool   `json:"present"`
	Valid        bool   `json:"valid"`
	ProjectID    string `json:"projectId,omitempty"`
	HeadSequence uint64 `json:"headSequence,omitempty"`
	HeadHash     string `json:"headHash,omitempty"`
	ProviderSet  string `json:"providerSet,omitempty"`
}

// SealCheckpoint persists owner-local integrity metadata after a writable
// projection has reached the same journal head. It is a disposable cache aid,
// never journal authority. Missing action-secret is expected during authority
// recovery/bootstrap and simply defers sealing until daemon warm or a later
// ordinary write.
func (s *Store) SealCheckpoint(state domain.State, segments []journal.Segment) error {
	if s.readOnly {
		return fmt.Errorf("read-only projection cannot seal a checkpoint")
	}
	dataDir := filepath.Dir(s.path)
	secret, err := os.ReadFile(filepath.Join(dataDir, "action-secret"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(secret) != 32 {
		return fmt.Errorf("snapshot checkpoint secret is invalid")
	}
	stateRaw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	stateHash := sha256.Sum256(stateRaw)
	payload := checkpointPayload{
		APIVersion: checkpointAPIVersion, Kind: "VerifiedSnapshotCheckpoint", ProjectID: state.ProjectID,
		HeadSequence: state.HeadSequence, HeadHash: state.HeadHash, GraphRevision: state.GraphRevision,
		ProviderSet: checkpointProviderSet(state.Graph), StateDigest: "sha256:" + hex.EncodeToString(stateHash[:]), SegmentIdentityDigest: checkpointSegmentIdentity(dataDir, segments),
	}
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(append([]byte("dagrail-verified-snapshot-checkpoint-v1\x00"), payloadRaw...))
	document, err := json.Marshal(sealedCheckpoint{checkpointPayload: payload, HMAC: "sha256:" + hex.EncodeToString(mac.Sum(nil))})
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, "verified-snapshot-checkpoint.json")
	temporary, err := os.CreateTemp(dataDir, ".verified-snapshot-checkpoint-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(document); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	directory, err := os.Open(dataDir)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// VerifyCheckpoint proves that the sealed cache metadata, projected state, and
// current immutable segment-file identities still describe one head. It never
// upgrades the checkpoint into authority.
func (s *Store) VerifyCheckpoint() (CheckpointReport, error) {
	path := filepath.Join(filepath.Dir(s.path), "verified-snapshot-checkpoint.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return CheckpointReport{}, nil
	}
	if err != nil {
		return CheckpointReport{}, err
	}
	if len(data) > 64*1024 {
		return CheckpointReport{Present: true}, fmt.Errorf("snapshot checkpoint exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var sealed sealedCheckpoint
	if err := decoder.Decode(&sealed); err != nil {
		return CheckpointReport{Present: true}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return CheckpointReport{Present: true}, fmt.Errorf("snapshot checkpoint contains trailing content")
	}
	if sealed.APIVersion != checkpointAPIVersion || sealed.Kind != "VerifiedSnapshotCheckpoint" {
		return CheckpointReport{Present: true}, fmt.Errorf("snapshot checkpoint contract is unsupported")
	}
	secret, err := os.ReadFile(filepath.Join(filepath.Dir(s.path), "action-secret"))
	if err != nil || len(secret) != 32 {
		return CheckpointReport{Present: true}, fmt.Errorf("snapshot checkpoint secret is unavailable or invalid")
	}
	payloadRaw, _ := json.Marshal(sealed.checkpointPayload)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(append([]byte("dagrail-verified-snapshot-checkpoint-v1\x00"), payloadRaw...))
	wantMAC, decodeErr := hex.DecodeString(strings.TrimPrefix(sealed.HMAC, "sha256:"))
	if decodeErr != nil || len(wantMAC) != sha256.Size || !hmac.Equal(mac.Sum(nil), wantMAC) {
		return CheckpointReport{Present: true}, fmt.Errorf("snapshot checkpoint HMAC is invalid")
	}
	unlock, err := s.acquireLock(!s.readOnly)
	if err != nil {
		return CheckpointReport{Present: true}, err
	}
	defer unlock()
	inspection, err := s.beginInspection()
	if err != nil {
		return CheckpointReport{Present: true}, err
	}
	db, err := s.database()
	if err != nil {
		return CheckpointReport{Present: true}, err
	}
	defer db.Close()
	var stateRaw string
	if err := db.QueryRow("SELECT value FROM metadata WHERE key='state'").Scan(&stateRaw); err != nil {
		return CheckpointReport{Present: true}, err
	}
	var state domain.State
	if err := json.Unmarshal([]byte(stateRaw), &state); err != nil {
		return CheckpointReport{Present: true}, err
	}
	rows, err := db.Query("SELECT sequence,hash,segment_schema FROM applied_segments ORDER BY sequence")
	if err != nil {
		return CheckpointReport{Present: true}, err
	}
	segments := []journal.Segment{}
	for rows.Next() {
		var segment journal.Segment
		if err := rows.Scan(&segment.Sequence, &segment.SegmentHash, &segment.SchemaVersion); err != nil {
			_ = rows.Close()
			return CheckpointReport{Present: true}, err
		}
		segments = append(segments, segment)
	}
	if err := rows.Close(); err != nil {
		return CheckpointReport{Present: true}, err
	}
	stateBytes := []byte(stateRaw)
	stateHash := sha256.Sum256(stateBytes)
	identity := checkpointSegmentIdentity(filepath.Dir(s.path), segments)
	if sealed.ProjectID != state.ProjectID || sealed.HeadSequence != state.HeadSequence || sealed.HeadHash != state.HeadHash || sealed.GraphRevision != state.GraphRevision || sealed.ProviderSet != checkpointProviderSet(state.Graph) || sealed.StateDigest != "sha256:"+hex.EncodeToString(stateHash[:]) || sealed.SegmentIdentityDigest != identity {
		return CheckpointReport{Present: true}, fmt.Errorf("snapshot checkpoint does not match projected state and journal identities")
	}
	if err := s.finishInspection(inspection); err != nil {
		return CheckpointReport{Present: true}, err
	}
	return CheckpointReport{Present: true, Valid: true, ProjectID: state.ProjectID, HeadSequence: state.HeadSequence, HeadHash: state.HeadHash, ProviderSet: sealed.ProviderSet}, nil
}

// RestoreCheckpoint loads one locally sealed projection state as a disposable
// replay prefix. Callers must immediately pass the returned snapshot to the
// journal, which checks every file identity and verifies any appended suffix.
// Full journal verification, recovery rehearsal, and security audit never use
// this shortcut.
func (s *Store) RestoreCheckpoint() (domain.State, journal.VerifiedSnapshot, CheckpointReport, error) {
	report, err := s.VerifyCheckpoint()
	if err != nil || !report.Valid {
		return domain.State{}, journal.VerifiedSnapshot{}, report, err
	}
	unlock, err := s.acquireLock(!s.readOnly)
	if err != nil {
		return domain.State{}, journal.VerifiedSnapshot{}, report, err
	}
	defer unlock()
	inspection, err := s.beginInspection()
	if err != nil {
		return domain.State{}, journal.VerifiedSnapshot{}, report, err
	}
	db, err := s.database()
	if err != nil {
		return domain.State{}, journal.VerifiedSnapshot{}, report, err
	}
	defer db.Close()
	var stateRaw string
	if err := db.QueryRow("SELECT value FROM metadata WHERE key='state'").Scan(&stateRaw); err != nil {
		return domain.State{}, journal.VerifiedSnapshot{}, report, err
	}
	var state domain.State
	if err := json.Unmarshal([]byte(stateRaw), &state); err != nil {
		return domain.State{}, journal.VerifiedSnapshot{}, report, err
	}
	if state.ProjectID != report.ProjectID || state.HeadSequence != report.HeadSequence || state.HeadHash != report.HeadHash || checkpointProviderSet(state.Graph) != report.ProviderSet {
		return domain.State{}, journal.VerifiedSnapshot{}, report, fmt.Errorf("snapshot checkpoint changed while it was being restored")
	}
	rows, err := db.Query("SELECT sequence,hash,segment_schema FROM applied_segments ORDER BY sequence")
	if err != nil {
		return domain.State{}, journal.VerifiedSnapshot{}, report, err
	}
	segments := []journal.Segment{}
	files := []journal.SegmentFileIdentity{}
	dataDir := filepath.Dir(s.path)
	for rows.Next() {
		var segment journal.Segment
		if err := rows.Scan(&segment.Sequence, &segment.SegmentHash, &segment.SchemaVersion); err != nil {
			_ = rows.Close()
			return domain.State{}, journal.VerifiedSnapshot{}, report, err
		}
		name := fmt.Sprintf("%012d-%s.json", segment.Sequence, segment.SegmentHash)
		info, err := os.Lstat(filepath.Join(dataDir, "journal", name))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			_ = rows.Close()
			return domain.State{}, journal.VerifiedSnapshot{}, report, fmt.Errorf("checkpoint journal identity is unavailable at sequence %d", segment.Sequence)
		}
		segments = append(segments, segment)
		files = append(files, journal.SegmentFileIdentity{Name: name, Size: info.Size(), ModTimeUnixNano: info.ModTime().UnixNano(), Mode: info.Mode()})
	}
	if err := rows.Close(); err != nil {
		return domain.State{}, journal.VerifiedSnapshot{}, report, err
	}
	if err := s.finishInspection(inspection); err != nil {
		return domain.State{}, journal.VerifiedSnapshot{}, report, err
	}
	return state, journal.VerifiedSnapshot{Segments: segments, Files: files, Incremental: true}, report, nil
}

func checkpointSegmentIdentity(dataDir string, segments []journal.Segment) string {
	hash := sha256.New()
	for _, segment := range segments {
		name := fmt.Sprintf("%012d-%s.json", segment.Sequence, segment.SegmentHash)
		_, _ = fmt.Fprintf(hash, "%d\x00%d\x00%s\x00%s\x00", segment.Sequence, segment.SchemaVersion, segment.SegmentHash, name)
		path := filepath.Join(dataDir, "journal", name)
		if info, err := os.Lstat(path); err == nil {
			_, _ = fmt.Fprintf(hash, "%d\x00%d\x00%d\x00", info.Size(), info.ModTime().UnixNano(), uint32(info.Mode()))
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func checkpointProviderSet(graph *domain.GraphDefinition) string {
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
