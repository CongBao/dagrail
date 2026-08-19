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

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/project"
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
	AuthorityFenceSchemaVersion  = 4
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

// Head is a bounded observation of the append-only journal tail. It is intended
// for cache invalidation and liveness polling after a Store has already completed
// full authority validation. It never replaces ReadAll for verification.
type Head struct {
	Sequence uint64 `json:"sequence"`
	Hash     string `json:"hash"`
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

type storeCapability uint8

const (
	storeCapabilityOrdinary storeCapability = iota
	storeCapabilityEstablishment
	storeCapabilityInspection
	storeCapabilityRecovery
	storeCapabilityRehearsal
)

type Store struct {
	dir                    string
	projectID              string
	capability             storeCapability
	testAllowUnestablished bool
	lock                   *flock.Flock
	mu                     *sync.Mutex
	fault                  func(string) error
}

const authorityRetirementFile = "authority-retired.json"

var processLocks sync.Map

type eventUpcaster func(Event) (Event, error)

var eventUpcasters = map[int]eventUpcaster{
	0: func(stored Event) (Event, error) {
		stored.SchemaVersion = 1
		return stored, nil
	},
}

func Open(dataDir, projectID string) (*Store, error) {
	store, err := open(dataDir, projectID, storeCapabilityOrdinary)
	if err != nil {
		return nil, err
	}
	if err := project.ValidateAuthorityClaim(dataDir, projectID); err != nil {
		return nil, err
	}
	segments, err := store.ReadAll()
	if err != nil {
		return nil, err
	}
	if err := validateEstablishedAuthorityPrefix(projectID, segments); err != nil {
		return nil, err
	}
	return store, nil
}

// OpenInspection applies the same claim and establishment validation as Open,
// but the returned handle cannot append or restore journal content.
func OpenInspection(dataDir, projectID string) (*Store, error) {
	store, err := openExisting(dataDir, projectID, storeCapabilityInspection)
	if err != nil {
		return nil, err
	}
	if err := project.ValidateAuthorityClaim(dataDir, projectID); err != nil {
		return nil, err
	}
	segments, err := store.ReadAll()
	if err != nil {
		return nil, err
	}
	if err := validateEstablishedAuthorityPrefix(projectID, segments); err != nil {
		return nil, err
	}
	return store, nil
}

func openExisting(dataDir, projectID string, capability storeCapability) (*Store, error) {
	dir := filepath.Join(dataDir, "journal")
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("journal path must be an existing non-symlink directory")
	}
	lockPath := filepath.Join(dataDir, "writer.lock")
	if info, err := os.Lstat(lockPath); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("writer lock must be an existing regular non-symlink file")
	}
	mutex, _ := processLocks.LoadOrStore(lockPath, &sync.Mutex{})
	return &Store{dir: dir, projectID: projectID, capability: capability, lock: flock.New(lockPath), mu: mutex.(*sync.Mutex)}, nil
}

// OpenForAuthorityEstablishment opens a claimed, empty authority only for its
// schema-4 bootstrap transaction. Ordinary writes remain unavailable until a
// fresh Open verifies the committed establishment prefix.
func OpenForAuthorityEstablishment(dataDir, projectID string) (*Store, error) {
	store, err := open(dataDir, projectID, storeCapabilityEstablishment)
	if err != nil {
		return nil, err
	}
	if err := project.ValidateAuthorityClaim(dataDir, projectID); err != nil {
		return nil, err
	}
	segments, err := store.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(segments) != 0 {
		if err := validateEstablishedAuthorityPrefix(projectID, segments); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// OpenRecovery permits only the dedicated authority retirement/rotation
// transactions. Generic append and restore remain disabled even if a caller
// accidentally invokes a normal Service mutation through an inspection handle.
func OpenRecovery(dataDir, projectID string) (*Store, error) {
	return open(dataDir, projectID, storeCapabilityRecovery)
}

// OpenRehearsal creates a disposable store that can restore one verified
// snapshot but cannot append ordinary commands. It deliberately does not mint
// or validate a writer claim for the duplicated Project UUID.
func OpenRehearsal(dataDir, projectID string) (*Store, error) {
	if err := project.EnsureDurableDirectory(dataDir); err != nil {
		return nil, err
	}
	return open(dataDir, projectID, storeCapabilityRehearsal)
}

func open(dataDir, projectID string, capability storeCapability) (*Store, error) {
	dir := filepath.Join(dataDir, "journal")
	if err := project.EnsureDurableDirectoryWithin(dir, dataDir); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("journal path must be a non-symlink directory")
	}
	lockPath := filepath.Join(dataDir, "writer.lock")
	mutex, _ := processLocks.LoadOrStore(lockPath, &sync.Mutex{})
	return &Store{dir: dir, projectID: projectID, capability: capability, lock: flock.New(lockPath), mu: mutex.(*sync.Mutex)}, nil
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
	if s.capability == storeCapabilityEstablishment {
		return result, false, fmt.Errorf("authority establishment store cannot accept ordinary journal writes")
	}
	if s.capability != storeCapabilityOrdinary {
		return result, false, fmt.Errorf("recovery inspection store cannot accept ordinary journal writes")
	}
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
		if err := project.ValidateAuthorityClaim(filepath.Dir(s.dir), s.projectID); err != nil {
			return err
		}
		if _, retired, retirementErr := s.authorityRetirementUnlocked(); retirementErr != nil {
			return retirementErr
		} else if retired {
			return fmt.Errorf("project authority is retired and cannot accept journal writes")
		}
		segments, err := s.readAllUnlocked()
		if err != nil {
			return err
		}
		if !s.testAllowUnestablished {
			if err := validateEstablishedAuthorityPrefix(s.projectID, segments); err != nil {
				return err
			}
		}
		if _, retired := retirementEvent(segments); retired {
			return fmt.Errorf("project authority is retired and cannot accept journal writes")
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
		previous := ""
		sequence := uint64(1)
		if len(segments) > 0 {
			previous = segments[len(segments)-1].SegmentHash
			sequence = segments[len(segments)-1].Sequence + 1
		}
		if expectedHead != nil && previous != *expectedHead {
			return fmt.Errorf("journal head changed; refresh context before retrying")
		}
		result, created, err = s.appendSegmentUnlocked(command, events, now, previous, sequence)
		return err
	})
	return result, created, err
}

func (s *Store) appendSegmentUnlocked(command Command, events []Event, now time.Time, previous string, sequence uint64) (Segment, bool, error) {
	return s.appendSegmentWithSchemaUnlocked(CurrentSegmentSchemaVersion, command, events, now, previous, sequence)
}

func (s *Store) appendSegmentWithSchemaUnlocked(segmentSchema int, command Command, events []Event, now time.Time, previous string, sequence uint64) (Segment, bool, error) {
	preparedEvents, err := prepareEvents(events)
	if err != nil {
		return Segment{}, false, err
	}
	unsigned := unsignedSegment{SchemaVersion: segmentSchema, Sequence: sequence, ProjectID: s.projectID, PreviousHash: previous, Command: command, Events: preparedEvents, CommittedAt: now.UTC().Format(time.RFC3339Nano)}
	hash, err := computeHash(unsigned)
	if err != nil {
		return Segment{}, false, err
	}
	result := Segment{SchemaVersion: unsigned.SchemaVersion, Sequence: unsigned.Sequence, ProjectID: unsigned.ProjectID, PreviousHash: unsigned.PreviousHash, Command: unsigned.Command, Events: unsigned.Events, CommittedAt: unsigned.CommittedAt, SegmentHash: hash}
	data, err := json.Marshal(result)
	if err != nil {
		return Segment{}, false, err
	}
	canonical, err := jcs.Transform(data)
	if err != nil {
		return Segment{}, false, err
	}
	if err := validateCanonicalSegmentBounds(canonical); err != nil {
		return Segment{}, false, err
	}
	name := fmt.Sprintf("%012d-%s.json", sequence, hash)
	if err := s.injectFault("before-temp-create"); err != nil {
		return Segment{}, false, err
	}
	tmp, err := os.CreateTemp(s.dir, ".segment-*.tmp")
	if err != nil {
		return Segment{}, false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return Segment{}, false, err
	}
	if err := s.injectFault("before-temp-write"); err != nil {
		_ = tmp.Close()
		return Segment{}, false, err
	}
	if _, err := tmp.Write(canonical); err != nil {
		_ = tmp.Close()
		return Segment{}, false, err
	}
	if err := s.injectFault("before-temp-sync"); err != nil {
		_ = tmp.Close()
		return Segment{}, false, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return Segment{}, false, err
	}
	if err := tmp.Close(); err != nil {
		return Segment{}, false, err
	}
	if err := s.injectFault("before-rename"); err != nil {
		return Segment{}, false, err
	}
	if err := os.Rename(tmpName, filepath.Join(s.dir, name)); err != nil {
		return Segment{}, false, err
	}
	if err := s.injectFault("after-rename"); err != nil {
		return result, true, err
	}
	if err := s.injectFault("before-directory-sync"); err != nil {
		return result, true, err
	}
	if err := syncDirectory(s.dir); err != nil {
		return result, true, err
	}
	return result, true, nil
}

func retirementEvent(segments []Segment) ([]byte, bool) {
	for _, segment := range segments {
		for _, event := range segment.Events {
			if event.Type == "authority.retired" {
				return event.Payload, true
			}
		}
	}
	return nil, false
}

// EstablishAuthority writes the first and only bootstrap fence for a fresh
// replacement UUID. It must commit before the repository locator is published,
// so pre-v0.22 binaries cannot treat the replacement journal as writable.
func (s *Store) EstablishAuthority(establishment []byte, now time.Time) (Segment, error) {
	var result Segment
	if s.capability != storeCapabilityEstablishment {
		return result, fmt.Errorf("authority establishment requires an establishment-only store")
	}
	if len(establishment) == 0 || len(establishment) > 64*1024 || !json.Valid(establishment) {
		return result, fmt.Errorf("authority establishment is invalid")
	}
	if err := domain.ValidateAuthorityJSON(establishment); err != nil {
		return result, fmt.Errorf("authority establishment: %w", err)
	}
	if err := domain.RejectSensitiveFields(establishment); err != nil {
		return result, fmt.Errorf("authority establishment: %w", err)
	}
	establishment, err := jcs.Transform(establishment)
	if err != nil {
		return result, fmt.Errorf("authority establishment: %w", err)
	}
	err = s.WithLock(func() error {
		if err := project.ValidateAuthorityClaim(filepath.Dir(s.dir), s.projectID); err != nil {
			return err
		}
		segments, err := s.readAllUnlocked()
		if err != nil {
			return err
		}
		if len(segments) != 0 {
			if segments[0].SchemaVersion == AuthorityFenceSchemaVersion && len(segments[0].Events) == 1 && segments[0].Events[0].Type == "authority.established" && bytes.Equal(segments[0].Events[0].Payload, establishment) {
				result = segments[0]
				return syncDirectory(s.dir)
			}
			return fmt.Errorf("replacement authority journal is not pristine")
		}
		sum := sha256.Sum256(establishment)
		digest := hex.EncodeToString(sum[:])
		command := Command{ID: "authority-establish-" + hex.EncodeToString(sum[:16]), Kind: "authority.establish", ActorRole: "dagrail.recovery", IdempotencyKey: "authority-establish/" + digest, ObjectRef: "project:" + s.projectID, RequestDigest: "sha256:" + digest}
		segment, _, err := s.appendSegmentWithSchemaUnlocked(AuthorityFenceSchemaVersion, command, []Event{{Type: "authority.established", Payload: establishment}}, now, "", 1)
		if err != nil {
			return err
		}
		result = segment
		return nil
	})
	return result, err
}

// AuthorityRetirement returns the immutable retirement evidence. The journal
// fence is authoritative; the sidecar is a rebuildable crash-resume index.
func (s *Store) AuthorityRetirement() ([]byte, bool, error) {
	var raw []byte
	var exists bool
	err := s.WithLock(func() error {
		sidecar, sidecarExists, err := s.authorityRetirementUnlocked()
		if err != nil {
			return err
		}
		segments, readErr := s.readAllUnlocked()
		if readErr != nil {
			return readErr
		}
		fenced, fencedExists := retirementEvent(segments)
		if sidecarExists && !fencedExists {
			return fmt.Errorf("authority retirement sidecar has no journal fence")
		}
		if sidecarExists && !bytes.Equal(sidecar, fenced) {
			return fmt.Errorf("authority retirement sidecar does not match journal fence")
		}
		raw, exists = fenced, fencedExists
		return nil
	})
	return raw, exists, err
}

// RetireAuthority appends an immutable authority.retired fence before applying
// the locator change. Historical reducers reject the unknown fence before any
// append, while current writers recognize it explicitly. The sidecar is written
// only after the fence and can be reconstructed by an exact retry.
func (s *Store) RetireAuthority(expectedHead string, retirement []byte, now time.Time, validate func([]Segment) error, sameIntent func([]byte) error, apply func([]byte) error) ([]byte, error) {
	if s.capability != storeCapabilityOrdinary {
		return nil, fmt.Errorf("claimed authority retirement requires an ordinary journal store")
	}
	return s.retireAuthority(true, expectedHead, retirement, "", now, validate, sameIntent, apply)
}

// RetireLegacyAuthority fences a pre-v0.22 store that has no current writer
// claim. The caller must positively reserve the exact legacy data root from
// validate while the historical writer lock is held. The old UUID never gains
// a v0.22 claim; apply must publish a fresh replacement identity.
func (s *Store) RetireLegacyAuthority(expectedHead string, retirement []byte, reservationDigest string, now time.Time, validate func([]Segment) error, sameIntent func([]byte) error, apply func([]byte) error) ([]byte, error) {
	if s.capability != storeCapabilityRecovery {
		return nil, fmt.Errorf("legacy authority retirement requires a recovery journal store")
	}
	return s.retireAuthority(false, expectedHead, retirement, reservationDigest, now, validate, sameIntent, apply)
}

func (s *Store) retireAuthority(requireClaim bool, expectedHead string, retirement []byte, reservationDigest string, now time.Time, validate func([]Segment) error, sameIntent func([]byte) error, apply func([]byte) error) ([]byte, error) {
	if apply == nil {
		return nil, fmt.Errorf("authority retirement requires replacement application")
	}
	if len(retirement) == 0 || len(retirement) > 64*1024 || !json.Valid(retirement) {
		return nil, fmt.Errorf("authority retirement marker is invalid")
	}
	if err := domain.ValidateAuthorityJSON(retirement); err != nil {
		return nil, fmt.Errorf("authority retirement marker: %w", err)
	}
	if err := domain.RejectSensitiveFields(retirement); err != nil {
		return nil, fmt.Errorf("authority retirement marker: %w", err)
	}
	retirement, err := jcs.Transform(retirement)
	if err != nil {
		return nil, fmt.Errorf("authority retirement marker: %w", err)
	}
	var committed []byte
	err = s.WithLock(func() error {
		if requireClaim {
			if err := project.ValidateAuthorityClaim(filepath.Dir(s.dir), s.projectID); err != nil {
				return err
			}
		}
		segments, err := s.readAllUnlocked()
		if err != nil {
			return err
		}
		if requireClaim && !s.testAllowUnestablished {
			if err := validateEstablishedAuthorityPrefix(s.projectID, segments); err != nil {
				return err
			}
		}
		sidecar, sidecarExists, err := s.authorityRetirementUnlocked()
		if err != nil {
			return err
		}
		fenced, fenceExists := retirementEvent(segments)
		if sidecarExists && !fenceExists {
			return fmt.Errorf("authority retirement sidecar has no journal fence")
		}
		if sidecarExists && !bytes.Equal(sidecar, fenced) {
			return fmt.Errorf("authority retirement sidecar does not match journal fence")
		}
		if fenceExists {
			if !requireClaim {
				if err := project.ValidateLegacyRetirementReservation(filepath.Dir(s.dir), s.projectID, ""); err != nil {
					return err
				}
			}
			if sameIntent == nil {
				if !bytes.Equal(fenced, retirement) {
					return fmt.Errorf("project authority is already retired with different intent")
				}
			} else if err := sameIntent(fenced); err != nil {
				return err
			}
			committed = fenced
			if err := syncDirectory(s.dir); err != nil {
				return err
			}
		} else {
			head := ""
			sequence := uint64(1)
			if len(segments) != 0 {
				head = segments[len(segments)-1].SegmentHash
				sequence = segments[len(segments)-1].Sequence + 1
			}
			if head != expectedHead {
				return fmt.Errorf("project authority changed before retirement")
			}
			if validate == nil {
				return fmt.Errorf("first authority retirement requires a reservation validator")
			}
			if err := validate(segments); err != nil {
				return err
			}
			if !requireClaim {
				if err := project.ValidateLegacyRetirementReservation(filepath.Dir(s.dir), s.projectID, reservationDigest); err != nil {
					return err
				}
			}
			sum := sha256.Sum256(retirement)
			command := Command{ID: "authority-retire-" + hex.EncodeToString(sum[:16]), Kind: "authority.retire", ActorRole: "dagrail.recovery", IdempotencyKey: "authority-retire/" + hex.EncodeToString(sum[:]), ObjectRef: "project:" + s.projectID, RequestDigest: "sha256:" + hex.EncodeToString(sum[:])}
			if _, _, err := s.appendSegmentWithSchemaUnlocked(AuthorityFenceSchemaVersion, command, []Event{{Type: "authority.retired", Payload: retirement}}, now, head, sequence); err != nil {
				return err
			}
			committed = retirement
		}
		existing, markerExists, err := s.authorityRetirementUnlocked()
		if err != nil {
			return err
		}
		if markerExists {
			if sameIntent == nil {
				if !bytes.Equal(existing, committed) {
					return fmt.Errorf("project authority is already retired with different intent")
				}
			} else if err := sameIntent(existing); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(s.dir)); err != nil {
				return err
			}
		} else if err := writeExclusiveAtomic(filepath.Join(filepath.Dir(s.dir), authorityRetirementFile), committed, 0o600); err != nil {
			return err
		}
		return apply(committed)
	})
	return committed, err
}

func (s *Store) authorityRetirementUnlocked() ([]byte, bool, error) {
	path := filepath.Join(filepath.Dir(s.dir), authorityRetirementFile)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 64*1024 {
		return nil, false, fmt.Errorf("authority retirement marker is not a bounded regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	if err := domain.ValidateAuthorityJSON(raw); err != nil {
		return nil, false, fmt.Errorf("authority retirement marker: %w", err)
	}
	return raw, true, nil
}

func writeExclusiveAtomic(path string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".authority-retired-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
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
	return syncDirectory(filepath.Dir(path))
}

func (s *Store) ReadAll() ([]Segment, error) {
	var result []Segment
	err := s.WithLock(func() error { var err error; result, err = s.readAllUnlocked(); return err })
	return result, err
}

// InspectHead observes the current append-only tail without replaying every
// segment. The writer lock makes the directory listing and tail read one stable
// observation. Every filename is still checked for a continuous sequence and a
// canonical digest-shaped suffix, while the tail's canonical bytes and self-hash
// are verified. Callers must perform a full ReadAll whenever the returned head
// differs from their previously verified snapshot.
func (s *Store) InspectHead() (Head, error) {
	var result Head
	err := s.WithLock(func() error {
		directory, err := os.Open(s.dir)
		if err != nil {
			return err
		}
		defer directory.Close()
		names := make([]string, 0)
		for {
			entries, readErr := directory.ReadDir(1024)
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				if entry.Type()&os.ModeSymlink != 0 {
					return fmt.Errorf("journal segment %s must not be a symlink", entry.Name())
				}
				names = append(names, entry.Name())
				if len(names) > MaxSegmentCount {
					return fmt.Errorf("journal exceeds %d segments", MaxSegmentCount)
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return readErr
			}
		}
		sort.Strings(names)
		for index, name := range names {
			prefix := fmt.Sprintf("%012d-", index+1)
			digest := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json")
			decoded, decodeErr := hex.DecodeString(digest)
			if !strings.HasPrefix(name, prefix) || len(decoded) != sha256.Size || decodeErr != nil {
				return fmt.Errorf("journal segment filename is invalid: %s", name)
			}
		}
		if len(names) == 0 {
			result = Head{}
			return nil
		}
		name := names[len(names)-1]
		path := filepath.Join(s.dir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > MaxSegmentBytes {
			return fmt.Errorf("journal segment %s must be a regular file no larger than %d bytes", name, MaxSegmentBytes)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := domain.ValidateAuthorityJSON(data); err != nil {
			return fmt.Errorf("decode journal segment %s: %w", name, err)
		}
		var segment Segment
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&segment); err != nil {
			return fmt.Errorf("decode journal segment %s: %w", name, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return fmt.Errorf("decode journal segment %s: trailing content", name)
		}
		canonical, err := jcs.Transform(data)
		if err != nil || !bytes.Equal(data, canonical) {
			return fmt.Errorf("journal segment %s is not canonical RFC 8785 JSON", name)
		}
		if segment.Sequence != uint64(len(names)) || segment.ProjectID != s.projectID {
			return fmt.Errorf("journal chain mismatch at %s", name)
		}
		unsigned := unsignedSegment{SchemaVersion: segment.SchemaVersion, Sequence: segment.Sequence, ProjectID: segment.ProjectID, PreviousHash: segment.PreviousHash, Command: segment.Command, Events: segment.Events, CommittedAt: segment.CommittedAt}
		hash, err := computeHash(unsigned)
		if err != nil {
			return err
		}
		if segment.SegmentHash != hash || name != fmt.Sprintf("%012d-%s.json", segment.Sequence, hash) {
			return fmt.Errorf("journal hash mismatch at %s", name)
		}
		if err := validateStoredCommand(segment.SchemaVersion, segment.Command); err != nil {
			return fmt.Errorf("journal compatibility error at %s: %w", name, err)
		}
		if err := validateStoredEvents(segment.SchemaVersion, segment.Events); err != nil {
			return fmt.Errorf("journal compatibility error at %s: %w", name, err)
		}
		result = Head{Sequence: segment.Sequence, Hash: segment.SegmentHash}
		return nil
	})
	return result, err
}

// WithSnapshot verifies the current journal and keeps the writer lock held while
// the caller consumes that exact prefix. It is used when a derived projection must
// be published without allowing a newer journal prefix to be overwritten by the
// older snapshot.
func (s *Store) WithSnapshot(consume func([]Segment) error) error {
	if consume == nil {
		return fmt.Errorf("journal snapshot consumer is required")
	}
	return s.WithLock(func() error {
		segments, err := s.readAllUnlocked()
		if err != nil {
			return err
		}
		return consume(segments)
	})
}

// ValidateSegments verifies an in-memory portable journal without writing it.
func ValidateSegments(projectID string, segments []Segment) error {
	if len(segments) > MaxSegmentCount {
		return fmt.Errorf("journal exceeds %d segments", MaxSegmentCount)
	}
	previous := ""
	for index, segment := range segments {
		hash, err := ValidateSegment(projectID, uint64(index+1), previous, segment)
		if err != nil {
			return err
		}
		previous = hash
	}
	return nil
}

// ValidateSegment verifies one in-memory segment against its exact chain
// position without retaining any other segment. Aggregate containers use it to
// reject malformed entries before growing a typed segment slice.
func ValidateSegment(projectID string, expectedSequence uint64, previousHash string, segment Segment) (string, error) {
	if segment.Sequence != expectedSequence || segment.ProjectID != projectID || segment.PreviousHash != previousHash {
		return "", fmt.Errorf("journal chain mismatch at sequence %d", segment.Sequence)
	}
	data, err := json.Marshal(segment)
	if err != nil {
		return "", fmt.Errorf("encode journal segment at sequence %d: %w", segment.Sequence, err)
	}
	canonical, err := jcs.Transform(data)
	if err != nil {
		return "", fmt.Errorf("canonicalize journal segment at sequence %d: %w", segment.Sequence, err)
	}
	if err := validateCanonicalSegmentBounds(canonical); err != nil {
		return "", fmt.Errorf("journal segment at sequence %d: %w", segment.Sequence, err)
	}
	if err := validateStoredCommand(segment.SchemaVersion, segment.Command); err != nil {
		return "", fmt.Errorf("journal compatibility error at sequence %d: %w", segment.Sequence, err)
	}
	if err := validateStoredEvents(segment.SchemaVersion, segment.Events); err != nil {
		return "", fmt.Errorf("journal compatibility error at sequence %d: %w", segment.Sequence, err)
	}
	unsigned := unsignedSegment{SchemaVersion: segment.SchemaVersion, Sequence: segment.Sequence, ProjectID: segment.ProjectID, PreviousHash: segment.PreviousHash, Command: segment.Command, Events: segment.Events, CommittedAt: segment.CommittedAt}
	hash, err := computeHash(unsigned)
	if err != nil {
		return "", err
	}
	if segment.SegmentHash != hash {
		return "", fmt.Errorf("journal hash mismatch at sequence %d", segment.Sequence)
	}
	return hash, nil
}

func validateCanonicalSegmentBounds(canonical []byte) error {
	if len(canonical) > MaxSegmentBytes {
		return fmt.Errorf("journal segment exceeds %d bytes", MaxSegmentBytes)
	}
	if err := domain.ValidateAuthorityJSON(canonical); err != nil {
		return fmt.Errorf("journal segment authority JSON: %w", err)
	}
	return nil
}

type authorityEstablishmentEnvelope struct {
	APIVersion        string `json:"apiVersion"`
	Kind              string `json:"kind"`
	ProjectID         string `json:"projectId"`
	PreviousProjectID string `json:"previousProjectId,omitempty"`
	Operation         string `json:"operation"`
	EstablishedAt     string `json:"establishedAt"`
	ProvenanceDigest  string `json:"provenanceDigest"`
}

func validateEstablishedAuthorityPrefix(projectID string, segments []Segment) error {
	if len(segments) == 0 {
		return fmt.Errorf("claimed authority is missing its schema-4 establishment fence")
	}
	first := segments[0]
	if first.SchemaVersion != AuthorityFenceSchemaVersion || first.Sequence != 1 || first.PreviousHash != "" || first.ProjectID != projectID || first.Command.Kind != "authority.establish" || first.Command.ActorRole != "dagrail.recovery" || first.Command.ObjectRef != "project:"+projectID || len(first.Events) != 1 || first.Events[0].Type != "authority.established" || first.Events[0].SchemaVersion != CurrentEventSchemaVersion {
		return fmt.Errorf("claimed authority has an invalid schema-4 establishment prefix")
	}
	decoder := json.NewDecoder(bytes.NewReader(first.Events[0].Payload))
	decoder.DisallowUnknownFields()
	var establishment authorityEstablishmentEnvelope
	if err := decoder.Decode(&establishment); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("claimed authority establishment payload is invalid")
	}
	if establishment.APIVersion != "dagrail.io/authority-establishment/v1alpha1" || establishment.Kind != "AuthorityEstablishment" || establishment.ProjectID != projectID || establishment.EstablishedAt != first.CommittedAt {
		return fmt.Errorf("claimed authority establishment is bound to different authority")
	}
	switch establishment.Operation {
	case "initialization":
		if establishment.PreviousProjectID != "" {
			return fmt.Errorf("initial authority establishment has a predecessor")
		}
	case "rotation", "legacy-adoption", "relocation":
		if establishment.PreviousProjectID == "" || establishment.PreviousProjectID == projectID {
			return fmt.Errorf("replacement authority establishment predecessor is invalid")
		}
	default:
		return fmt.Errorf("claimed authority establishment operation is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, establishment.EstablishedAt); err != nil || len(establishment.ProvenanceDigest) != len("sha256:")+64 || !strings.HasPrefix(establishment.ProvenanceDigest, "sha256:") {
		return fmt.Errorf("claimed authority establishment evidence is invalid")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(establishment.ProvenanceDigest, "sha256:")); err != nil {
		return fmt.Errorf("claimed authority establishment evidence is invalid")
	}
	sum := sha256.Sum256(first.Events[0].Payload)
	digest := hex.EncodeToString(sum[:])
	if first.Command.ID != "authority-establish-"+hex.EncodeToString(sum[:16]) || first.Command.IdempotencyKey != "authority-establish/"+digest || first.Command.RequestDigest != "sha256:"+digest {
		return fmt.Errorf("claimed authority establishment command binding is invalid")
	}
	return nil
}

// RestoreSegments resumes an exact-prefix restore. It never overwrites an
// existing segment and refuses any divergent journal.
func (s *Store) RestoreSegments(segments []Segment) error {
	if s.capability == storeCapabilityEstablishment {
		return fmt.Errorf("authority establishment store cannot restore journal writes")
	}
	if s.capability != storeCapabilityOrdinary && s.capability != storeCapabilityRehearsal {
		return fmt.Errorf("recovery inspection store cannot restore journal writes")
	}
	if err := ValidateSegments(s.projectID, segments); err != nil {
		return err
	}
	if s.capability != storeCapabilityRehearsal && !s.testAllowUnestablished {
		if err := validateEstablishedAuthorityPrefix(s.projectID, segments); err != nil {
			return err
		}
	}
	return s.WithLock(func() error {
		if s.capability != storeCapabilityRehearsal {
			if err := project.ValidateAuthorityClaim(filepath.Dir(s.dir), s.projectID); err != nil {
				return err
			}
			if _, retired, retirementErr := s.authorityRetirementUnlocked(); retirementErr != nil {
				return retirementErr
			} else if retired {
				return fmt.Errorf("project authority is retired and cannot restore journal writes")
			}
		}
		existing, err := s.readAllUnlocked()
		if err != nil {
			return err
		}
		if _, retired := retirementEvent(existing); retired && s.capability != storeCapabilityRehearsal {
			return fmt.Errorf("project authority is retired and cannot restore journal writes")
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
		ReadableSegmentSchemas:  []int{LegacySegmentSchemaVersion, PreviousSegmentSchemaVersion, CurrentSegmentSchemaVersion, AuthorityFenceSchemaVersion},
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
		if err := validateStoredCommand(segment.SchemaVersion, segment.Command); err != nil {
			return nil, fmt.Errorf("journal compatibility error at %s: %w", name, err)
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
	if segmentSchema < LegacySegmentSchemaVersion || segmentSchema > AuthorityFenceSchemaVersion {
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
		case PreviousSegmentSchemaVersion, CurrentSegmentSchemaVersion, AuthorityFenceSchemaVersion:
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

func validateStoredCommand(segmentSchema int, command Command) error {
	if segmentSchema < LegacySegmentSchemaVersion || segmentSchema > AuthorityFenceSchemaVersion {
		return fmt.Errorf("unsupported segment schema version %d", segmentSchema)
	}
	if segmentSchema < CurrentSegmentSchemaVersion && (command.ObjectRef != "" || command.RequestDigest != "") {
		return fmt.Errorf("segment schema version %d cannot contain v3 command intent fields", segmentSchema)
	}
	if command.RequestDigest != "" {
		if len(command.RequestDigest) != len("sha256:")+64 || !strings.HasPrefix(command.RequestDigest, "sha256:") {
			return fmt.Errorf("journal command request digest is invalid")
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(command.RequestDigest, "sha256:")); err != nil {
			return fmt.Errorf("journal command request digest is invalid")
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
	return project.SyncDirectory(path)
}
