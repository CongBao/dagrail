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
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/gofrs/flock"
	"github.com/gowebpki/jcs"
)

const hashDomain = "dagrail-journal-v1\x00"

const (
	MaxSegmentBytes     = 16 * 1024 * 1024
	MaxSegmentCount     = 1_000_000
	MaxEventsPerSegment = 10_000
)

const (
	LegacySegmentSchemaVersion   = 1
	PreviousSegmentSchemaVersion = 2
	CurrentSegmentSchemaVersion  = 3
	CurrentEventSchemaVersion    = 1
)

type Command struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	ActorRole      string `json:"actorRole,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
	ObjectRef      string `json:"objectRef,omitempty"`
	RequestDigest  string `json:"requestDigest,omitempty"`
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
	fault     func(string) error
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
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("journal path must be a non-symlink directory")
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

func (s *Store) injectFault(point string) error {
	if s.fault == nil {
		return nil
	}
	return s.fault(point)
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
	commandRaw, err := json.Marshal(command)
	if err != nil {
		return result, false, err
	}
	if err := domain.RejectSensitiveFields(commandRaw); err != nil {
		return result, false, fmt.Errorf("journal command contains prohibited material: %w", err)
	}
	if command.RequestDigest != "" {
		if len(command.RequestDigest) != len("sha256:")+64 || !strings.HasPrefix(command.RequestDigest, "sha256:") {
			return result, false, fmt.Errorf("journal command request digest is invalid")
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(command.RequestDigest, "sha256:")); err != nil {
			return result, false, fmt.Errorf("journal command request digest is invalid")
		}
	}
	err = s.WithLock(func() error {
		segments, err := s.readAllUnlocked()
		if err != nil {
			return err
		}
		if command.IdempotencyKey != "" {
			for _, existing := range segments {
				if existing.Command.IdempotencyKey != command.IdempotencyKey {
					continue
				}
				if existing.Command.Kind != command.Kind ||
					(existing.Command.ActorRole != "" && existing.Command.ActorRole != command.ActorRole) ||
					(existing.Command.ObjectRef != "" && existing.Command.ObjectRef != command.ObjectRef) ||
					(existing.Command.RequestDigest != "" && existing.Command.RequestDigest != command.RequestDigest) {
					return fmt.Errorf("idempotency key is already bound to another command intent")
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
		if len(canonical) > MaxSegmentBytes {
			return fmt.Errorf("journal segment exceeds %d bytes", MaxSegmentBytes)
		}
		name := fmt.Sprintf("%012d-%s.json", sequence, hash)
		if err := s.injectFault("before-temp-create"); err != nil {
			return err
		}
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
		if err := s.injectFault("before-temp-write"); err != nil {
			_ = tmp.Close()
			return err
		}
		if _, err := tmp.Write(canonical); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := s.injectFault("before-temp-sync"); err != nil {
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
		if err := s.injectFault("before-rename"); err != nil {
			return err
		}
		if err := os.Rename(tmpName, filepath.Join(s.dir, name)); err != nil {
			return err
		}
		created = true
		if err := s.injectFault("after-rename"); err != nil {
			return err
		}
		if err := s.injectFault("before-directory-sync"); err != nil {
			return err
		}
		if err := syncDirectory(s.dir); err != nil {
			return err
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
	if len(segments) > MaxSegmentCount {
		return fmt.Errorf("journal exceeds %d segments", MaxSegmentCount)
	}
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
			if err := syncDirectory(s.dir); err != nil {
				return err
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
	return CompatibilityForSegments(segments)
}

// CompatibilityForSegments analyzes a previously verified immutable snapshot.
// It avoids racing a second journal read while a recovery rehearsal is bound to
// a specific head.
func CompatibilityForSegments(segments []Segment) (CompatibilityReport, error) {
	report := CompatibilityReport{
		Compatible:              true,
		SegmentCount:            len(segments),
		ReadableSegmentSchemas:  []int{LegacySegmentSchemaVersion, PreviousSegmentSchemaVersion, CurrentSegmentSchemaVersion},
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
	directory, err := os.Open(s.dir)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	names := make([]string, 0)
	for {
		entries, readErr := directory.ReadDir(1024)
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
				if entry.Type()&os.ModeSymlink != 0 {
					return nil, fmt.Errorf("journal segment %s must not be a symlink", entry.Name())
				}
				names = append(names, entry.Name())
				if len(names) > MaxSegmentCount {
					return nil, fmt.Errorf("journal exceeds %d segments", MaxSegmentCount)
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	sort.Strings(names)
	segments := make([]Segment, 0, len(names))
	previous := ""
	for index, name := range names {
		path := filepath.Join(s.dir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > MaxSegmentBytes {
			return nil, fmt.Errorf("journal segment %s must be a regular file no larger than %d bytes", name, MaxSegmentBytes)
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(file, MaxSegmentBytes+1))
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		if len(data) > MaxSegmentBytes {
			return nil, fmt.Errorf("journal segment %s exceeds %d bytes", name, MaxSegmentBytes)
		}
		if err := domain.ValidateAuthorityJSON(data); err != nil {
			return nil, fmt.Errorf("decode journal segment %s: %w", name, err)
		}
		var segment Segment
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&segment); err != nil {
			return nil, fmt.Errorf("decode journal segment %s: %w", name, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, fmt.Errorf("decode journal segment %s: trailing content", name)
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
	if len(events) > MaxEventsPerSegment {
		return nil, fmt.Errorf("journal segment exceeds %d events", MaxEventsPerSegment)
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
		if err := domain.RejectSensitiveFields(event.Payload); err != nil {
			return nil, fmt.Errorf("event %d contains prohibited material: %w", index, err)
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
	if len(events) > MaxEventsPerSegment {
		return fmt.Errorf("journal segment exceeds %d events", MaxEventsPerSegment)
	}
	for index, event := range events {
		switch segmentSchema {
		case LegacySegmentSchemaVersion:
			if event.SchemaVersion != 0 {
				return fmt.Errorf("legacy event %d unexpectedly declares schema version %d", index, event.SchemaVersion)
			}
		case PreviousSegmentSchemaVersion, CurrentSegmentSchemaVersion:
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

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
