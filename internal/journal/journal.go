package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/gowebpki/jcs"
)

const hashDomain = "dagrail-journal-v1\x00"

type Command struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	ActorRole      string `json:"actorRole,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type Event struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type Segment struct {
	SchemaVersion int     `json:"schemaVersion"`
	Sequence      uint64  `json:"sequence"`
	ProjectID     string  `json:"projectId"`
	PreviousHash  string  `json:"previousHash"`
	Command       Command `json:"command"`
	Events        []Event `json:"events"`
	CommittedAt   string  `json:"committedAt"`
	SegmentHash   string  `json:"segmentHash"`
}

type unsignedSegment struct {
	SchemaVersion int     `json:"schemaVersion"`
	Sequence      uint64  `json:"sequence"`
	ProjectID     string  `json:"projectId"`
	PreviousHash  string  `json:"previousHash"`
	Command       Command `json:"command"`
	Events        []Event `json:"events"`
	CommittedAt   string  `json:"committedAt"`
}

type Store struct {
	dir       string
	projectID string
	lock      *flock.Flock
	mu        *sync.Mutex
}

var processLocks sync.Map

func Open(dataDir, projectID string) (*Store, error) {
	dir := filepath.Join(dataDir, "journal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dataDir, "writer.lock")
	mutex, _ := processLocks.LoadOrStore(lockPath, &sync.Mutex{})
	return &Store{dir: dir, projectID: projectID, lock: flock.New(lockPath), mu: mutex.(*sync.Mutex)}, nil
}

func (s *Store) WithLock(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.lock.Lock(); err != nil {
		return err
	}
	defer func() { _ = s.lock.Unlock() }()
	return fn()
}

func (s *Store) Append(command Command, events []Event, now time.Time) (Segment, error) {
	segment, _, err := s.AppendOnce(command, events, now, nil)
	return segment, err
}

// AppendOnce atomically deduplicates an idempotency key and optionally binds
// the append to an expected journal head. The boolean is true only when a new
// immutable segment was committed.
func (s *Store) AppendOnce(command Command, events []Event, now time.Time, expectedHead *string) (Segment, bool, error) {
	var result Segment
	created := false
	err := s.WithLock(func() error {
		segments, err := s.readAllUnlocked()
		if err != nil {
			return err
		}
		if command.IdempotencyKey != "" {
			for _, existing := range segments {
				if existing.Command.IdempotencyKey != command.IdempotencyKey {
					continue
				}
				if existing.Command.Kind != command.Kind {
					return fmt.Errorf("idempotency key is already bound to command %s", existing.Command.Kind)
				}
				result = existing
				return nil
			}
		}
		previous := ""
		sequence := uint64(1)
		if len(segments) > 0 {
			previous = segments[len(segments)-1].SegmentHash
			sequence = segments[len(segments)-1].Sequence + 1
		}
		if expectedHead != nil && previous != *expectedHead {
			return fmt.Errorf("journal head changed; refresh context before retrying")
		}
		unsigned := unsignedSegment{SchemaVersion: 1, Sequence: sequence, ProjectID: s.projectID, PreviousHash: previous, Command: command, Events: events, CommittedAt: now.UTC().Format(time.RFC3339Nano)}
		hash, err := computeHash(unsigned)
		if err != nil {
			return err
		}
		result = Segment{SchemaVersion: unsigned.SchemaVersion, Sequence: unsigned.Sequence, ProjectID: unsigned.ProjectID, PreviousHash: unsigned.PreviousHash, Command: unsigned.Command, Events: unsigned.Events, CommittedAt: unsigned.CommittedAt, SegmentHash: hash}
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		canonical, err := jcs.Transform(data)
		if err != nil {
			return err
		}
		name := fmt.Sprintf("%012d-%s.json", sequence, hash)
		tmp, err := os.CreateTemp(s.dir, ".segment-*.tmp")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)
		if err := tmp.Chmod(0o600); err != nil {
			_ = tmp.Close()
			return err
		}
		if _, err := tmp.Write(canonical); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := os.Rename(tmpName, filepath.Join(s.dir, name)); err != nil {
			return err
		}
		created = true
		if dir, err := os.Open(s.dir); err == nil {
			_ = dir.Sync()
			_ = dir.Close()
		}
		return nil
	})
	return result, created, err
}

func (s *Store) ReadAll() ([]Segment, error) {
	var result []Segment
	err := s.WithLock(func() error { var err error; result, err = s.readAllUnlocked(); return err })
	return result, err
}

func (s *Store) readAllUnlocked() ([]Segment, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	segments := make([]Segment, 0, len(names))
	previous := ""
	for index, name := range names {
		file, err := os.Open(filepath.Join(s.dir, name))
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		var segment Segment
		if err := json.Unmarshal(data, &segment); err != nil {
			return nil, fmt.Errorf("decode journal segment %s: %w", name, err)
		}
		canonical, err := jcs.Transform(data)
		if err != nil || !bytes.Equal(data, canonical) {
			return nil, fmt.Errorf("journal segment %s is not canonical RFC 8785 JSON", name)
		}
		if segment.Sequence != uint64(index+1) || segment.ProjectID != s.projectID || segment.PreviousHash != previous || segment.SchemaVersion != 1 {
			return nil, fmt.Errorf("journal chain mismatch at %s", name)
		}
		unsigned := unsignedSegment{SchemaVersion: segment.SchemaVersion, Sequence: segment.Sequence, ProjectID: segment.ProjectID, PreviousHash: segment.PreviousHash, Command: segment.Command, Events: segment.Events, CommittedAt: segment.CommittedAt}
		hash, err := computeHash(unsigned)
		if err != nil {
			return nil, err
		}
		if hash != segment.SegmentHash || name != fmt.Sprintf("%012d-%s.json", segment.Sequence, hash) {
			return nil, fmt.Errorf("journal hash mismatch at %s", name)
		}
		previous = hash
		segments = append(segments, segment)
	}
	return segments, nil
}

func computeHash(value unsignedSegment) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", err
	}
	previous, err := hex.DecodeString(value.PreviousHash)
	if value.PreviousHash == "" {
		previous, err = nil, nil
	}
	if err != nil {
		return "", errors.New("previous journal hash is invalid")
	}
	h := sha256.New()
	_, _ = h.Write([]byte(hashDomain))
	_, _ = h.Write(previous)
	_, _ = h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil)), nil
}
