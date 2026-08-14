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

const (
	LegacySegmentSchemaVersion  = 1
	CurrentSegmentSchemaVersion = 2
	CurrentEventSchemaVersion   = 1
)

type Command struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	ActorRole      string `json:"actorRole,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type Event struct {
	Type          string          `json:"type"`
	SchemaVersion int             `json:"schemaVersion,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

type CompatibilityReport struct {
	Compatible              bool        `json:"compatible"`
	SegmentCount            int         `json:"segmentCount"`
	EventCount              int         `json:"eventCount"`
	LegacySegmentCount      int         `json:"legacySegmentCount"`
	UpcastedEventCount      int         `json:"upcastedEventCount"`
	ReadableSegmentSchemas  []int       `json:"readableSegmentSchemas"`
	CurrentWriteSchema      int         `json:"currentWriteSegmentSchema"`
	CurrentWriteEventSchema int         `json:"currentWriteEventSchema"`
	SegmentSchemas          map[int]int `json:"segmentSchemas"`
	EventSchemas            map[int]int `json:"eventSchemas"`
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

type eventUpcaster func(Event) (Event, error)

var eventUpcasters = map[int]eventUpcaster{
	0: func(stored Event) (Event, error) {
		stored.SchemaVersion = 1
		return stored, nil
	},
}

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
		preparedEvents, err := prepareEvents(events)
		if err != nil {
			return err
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
		unsigned := unsignedSegment{SchemaVersion: CurrentSegmentSchemaVersion, Sequence: sequence, ProjectID: s.projectID, PreviousHash: previous, Command: command, Events: preparedEvents, CommittedAt: now.UTC().Format(time.RFC3339Nano)}
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

// ValidateSegments verifies an in-memory portable journal without writing it.
func ValidateSegments(projectID string, segments []Segment) error {
	previous := ""
	for index, segment := range segments {
		if segment.Sequence != uint64(index+1) || segment.ProjectID != projectID || segment.PreviousHash != previous {
			return fmt.Errorf("journal chain mismatch at sequence %d", segment.Sequence)
		}
		if err := validateStoredEvents(segment.SchemaVersion, segment.Events); err != nil {
			return fmt.Errorf("journal compatibility error at sequence %d: %w", segment.Sequence, err)
		}
		unsigned := unsignedSegment{SchemaVersion: segment.SchemaVersion, Sequence: segment.Sequence, ProjectID: segment.ProjectID, PreviousHash: segment.PreviousHash, Command: segment.Command, Events: segment.Events, CommittedAt: segment.CommittedAt}
		hash, err := computeHash(unsigned)
		if err != nil {
			return err
		}
		if segment.SegmentHash != hash {
			return fmt.Errorf("journal hash mismatch at sequence %d", segment.Sequence)
		}
		previous = hash
	}
	return nil
}

// RestoreSegments resumes an exact-prefix restore. It never overwrites an
// existing segment and refuses any divergent journal.
func (s *Store) RestoreSegments(segments []Segment) error {
	if err := ValidateSegments(s.projectID, segments); err != nil {
		return err
	}
	return s.WithLock(func() error {
		existing, err := s.readAllUnlocked()
		if err != nil {
			return err
		}
		if len(existing) > len(segments) {
			return fmt.Errorf("existing journal is longer than backup")
		}
		for index := range existing {
			if existing[index].SegmentHash != segments[index].SegmentHash {
				return fmt.Errorf("existing journal diverges at sequence %d", index+1)
			}
		}
		for _, segment := range segments[len(existing):] {
			data, err := json.Marshal(segment)
			if err != nil {
				return err
			}
			canonical, err := jcs.Transform(data)
			if err != nil {
				return err
			}
			name := fmt.Sprintf("%012d-%s.json", segment.Sequence, segment.SegmentHash)
			tmp, err := os.CreateTemp(s.dir, ".restore-*.tmp")
			if err != nil {
				return err
			}
			tmpName := tmp.Name()
			cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }
			if err := tmp.Chmod(0o600); err != nil {
				cleanup()
				return err
			}
			if _, err := tmp.Write(canonical); err != nil {
				cleanup()
				return err
			}
			if err := tmp.Sync(); err != nil {
				cleanup()
				return err
			}
			if err := tmp.Close(); err != nil {
				_ = os.Remove(tmpName)
				return err
			}
			if err := os.Rename(tmpName, filepath.Join(s.dir, name)); err != nil {
				_ = os.Remove(tmpName)
				return err
			}
			if dir, err := os.Open(s.dir); err == nil {
				_ = dir.Sync()
				_ = dir.Close()
			}
		}
		return nil
	})
}

func (s *Store) Compatibility() (CompatibilityReport, error) {
	segments, err := s.ReadAll()
	if err != nil {
		return CompatibilityReport{}, err
	}
	report := CompatibilityReport{
		Compatible:              true,
		SegmentCount:            len(segments),
		ReadableSegmentSchemas:  []int{LegacySegmentSchemaVersion, CurrentSegmentSchemaVersion},
		CurrentWriteSchema:      CurrentSegmentSchemaVersion,
		CurrentWriteEventSchema: CurrentEventSchemaVersion,
		SegmentSchemas:          map[int]int{},
		EventSchemas:            map[int]int{},
	}
	for _, segment := range segments {
		report.SegmentSchemas[segment.SchemaVersion]++
		if segment.SchemaVersion == LegacySegmentSchemaVersion {
			report.LegacySegmentCount++
		}
		for _, stored := range segment.Events {
			normalized, err := UpcastEvent(segment.SchemaVersion, stored)
			if err != nil {
				return CompatibilityReport{}, err
			}
			report.EventCount++
			report.EventSchemas[normalized.SchemaVersion]++
			if stored.SchemaVersion != normalized.SchemaVersion {
				report.UpcastedEventCount++
			}
		}
	}
	return report, nil
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
		if segment.Sequence != uint64(index+1) || segment.ProjectID != s.projectID || segment.PreviousHash != previous {
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
		if err := validateStoredEvents(segment.SchemaVersion, segment.Events); err != nil {
			return nil, fmt.Errorf("journal compatibility error at %s: %w", name, err)
		}
		previous = hash
		segments = append(segments, segment)
	}
	return segments, nil
}

func prepareEvents(events []Event) ([]Event, error) {
	if len(events) == 0 {
		return nil, errors.New("journal segment must contain at least one event")
	}
	prepared := make([]Event, len(events))
	for index, event := range events {
		if event.SchemaVersion == 0 {
			event.SchemaVersion = CurrentEventSchemaVersion
		}
		if event.SchemaVersion != CurrentEventSchemaVersion {
			return nil, fmt.Errorf("event %d uses unsupported schema version %d", index, event.SchemaVersion)
		}
		if err := validateEvent(event); err != nil {
			return nil, fmt.Errorf("event %d: %w", index, err)
		}
		event.Payload = append(json.RawMessage(nil), event.Payload...)
		prepared[index] = event
	}
	return prepared, nil
}

func validateStoredEvents(segmentSchema int, events []Event) error {
	if segmentSchema < LegacySegmentSchemaVersion || segmentSchema > CurrentSegmentSchemaVersion {
		return fmt.Errorf("unsupported segment schema version %d", segmentSchema)
	}
	if len(events) == 0 {
		return errors.New("journal segment must contain at least one event")
	}
	for index, event := range events {
		switch segmentSchema {
		case LegacySegmentSchemaVersion:
			if event.SchemaVersion != 0 {
				return fmt.Errorf("legacy event %d unexpectedly declares schema version %d", index, event.SchemaVersion)
			}
		case CurrentSegmentSchemaVersion:
			if event.SchemaVersion != CurrentEventSchemaVersion {
				return fmt.Errorf("event %d uses unsupported schema version %d", index, event.SchemaVersion)
			}
		}
		if err := validateEvent(event); err != nil {
			return fmt.Errorf("event %d: %w", index, err)
		}
	}
	return nil
}

func validateEvent(event Event) error {
	if strings.TrimSpace(event.Type) == "" {
		return errors.New("event type is required")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload == nil {
		return errors.New("event payload must be a JSON object")
	}
	return nil
}

// UpcastEvent normalizes a verified stored event for the reducer. It never
// mutates journal bytes and deliberately preserves the payload tree unchanged.
func UpcastEvent(segmentSchema int, stored Event) (Event, error) {
	if err := validateStoredEvents(segmentSchema, []Event{stored}); err != nil {
		return Event{}, err
	}
	normalized := stored
	for normalized.SchemaVersion < CurrentEventSchemaVersion {
		upcast, ok := eventUpcasters[normalized.SchemaVersion]
		if !ok {
			return Event{}, fmt.Errorf("no upcaster from event schema version %d", normalized.SchemaVersion)
		}
		previousVersion := normalized.SchemaVersion
		var err error
		normalized, err = upcast(normalized)
		if err != nil {
			return Event{}, fmt.Errorf("upcast event schema version %d: %w", previousVersion, err)
		}
		if normalized.SchemaVersion != previousVersion+1 {
			return Event{}, fmt.Errorf("upcaster from event schema version %d returned version %d", previousVersion, normalized.SchemaVersion)
		}
	}
	normalized.Payload = append(json.RawMessage(nil), stored.Payload...)
	return normalized, nil
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
