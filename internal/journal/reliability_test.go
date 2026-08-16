package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/project"
)

const reliabilityProjectID = "11111111-1111-4111-8111-111111111111"

func TestAppendFaultMatrixPreservesRenameCommitBoundary(t *testing.T) {
	beforeCommit := []string{"before-temp-create", "before-temp-write", "before-temp-sync", "before-rename"}
	for _, point := range beforeCommit {
		t.Run(point, func(t *testing.T) {
			store, err := openClaimed(t, t.TempDir(), reliabilityProjectID)
			if err != nil {
				t.Fatal(err)
			}
			store.fault = oneShotFault(point, syscall.ENOSPC)
			if _, created, err := appendReliabilityEvent(store, "fault-before"); err == nil || created {
				t.Fatalf("fault %s returned created=%t err=%v", point, created, err)
			}
			store.fault = nil
			segments, err := store.ReadAll()
			if err != nil || len(segments) != 0 {
				t.Fatalf("pre-commit fault left authority: %d %v", len(segments), err)
			}
			if _, created, err := appendReliabilityEvent(store, "fault-before"); err != nil || !created {
				t.Fatalf("retry after %s did not commit once: created=%t err=%v", point, created, err)
			}
		})
	}

	afterCommit := []string{"after-rename", "before-directory-sync"}
	for _, point := range afterCommit {
		t.Run(point, func(t *testing.T) {
			store, err := openClaimed(t, t.TempDir(), reliabilityProjectID)
			if err != nil {
				t.Fatal(err)
			}
			store.fault = oneShotFault(point, syscall.EIO)
			first, created, err := appendReliabilityEvent(store, "fault-after")
			if err == nil || !created {
				t.Fatalf("post-rename fault %s returned created=%t err=%v", point, created, err)
			}
			store.fault = nil
			segments, readErr := store.ReadAll()
			if readErr != nil || len(segments) != 1 || segments[0].SegmentHash != first.SegmentHash {
				t.Fatalf("post-rename commit was not recoverable: %#v %v", segments, readErr)
			}
			retried, retriedCreated, retryErr := appendReliabilityEvent(store, "fault-after")
			if retryErr != nil || retriedCreated || retried.SegmentHash != first.SegmentHash {
				t.Fatalf("ambiguous retry duplicated commit: %#v %t %v", retried, retriedCreated, retryErr)
			}
		})
	}
}

func TestProcessCrashBeforeAndAfterRenameIsRecoverable(t *testing.T) {
	for _, scenario := range []struct {
		point      string
		committed  int
		retryAdded bool
	}{{"before-rename", 0, true}, {"after-rename", 1, false}} {
		t.Run(scenario.point, func(t *testing.T) {
			dataDir := t.TempDir()
			store, err := openClaimed(t, dataDir, reliabilityProjectID)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=TestJournalProcessHelper")
			command.Env = append(os.Environ(),
				"DAGRAIL_JOURNAL_HELPER=crash",
				"DAGRAIL_JOURNAL_DATA="+dataDir,
				"DAGRAIL_JOURNAL_POINT="+scenario.point,
				"DAGRAIL_JOURNAL_KEY=crash-key",
			)
			err = command.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 91 {
				t.Fatalf("crash helper exit = %v", err)
			}
			segments, err := store.ReadAll()
			if err != nil || len(segments) != scenario.committed {
				t.Fatalf("crash at %s left %d committed segments: %v", scenario.point, len(segments), err)
			}
			_, created, err := appendReliabilityEvent(store, "crash-key")
			if err != nil || created != scenario.retryAdded {
				t.Fatalf("retry after process crash: created=%t err=%v", created, err)
			}
			segments, err = store.ReadAll()
			if err != nil || len(segments) != 1 {
				t.Fatalf("recovery did not converge to one segment: %d %v", len(segments), err)
			}
		})
	}
}

func TestCrossProcessWriterContentionSerializesAndDeduplicates(t *testing.T) {
	dataDir := t.TempDir()
	if _, err := openClaimed(t, dataDir, reliabilityProjectID); err != nil {
		t.Fatal(err)
	}
	runBatch := func(keys []string) {
		t.Helper()
		type runningCommand struct {
			command *exec.Cmd
			output  *strings.Builder
		}
		commands := make([]runningCommand, 0, len(keys))
		for _, key := range keys {
			command := exec.Command(os.Args[0], "-test.run=TestJournalProcessHelper")
			output := &strings.Builder{}
			command.Stdout, command.Stderr = output, output
			command.Env = append(os.Environ(),
				"DAGRAIL_JOURNAL_HELPER=append",
				"DAGRAIL_JOURNAL_DATA="+dataDir,
				"DAGRAIL_JOURNAL_KEY="+key,
			)
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			commands = append(commands, runningCommand{command: command, output: output})
		}
		for _, running := range commands {
			if err := running.command.Wait(); err != nil {
				t.Fatalf("writer helper failed: %v %s", err, running.output.String())
			}
		}
	}

	same := make([]string, 12)
	for index := range same {
		same[index] = "same-key"
	}
	runBatch(same)
	store, err := openClaimed(t, dataDir, reliabilityProjectID)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := store.ReadAll()
	if err != nil || len(segments) != 1 {
		t.Fatalf("same-key contention produced %d segments: %v", len(segments), err)
	}
	unique := make([]string, 12)
	for index := range unique {
		unique[index] = fmt.Sprintf("unique-%02d", index)
	}
	runBatch(unique)
	segments, err = store.ReadAll()
	if err != nil || len(segments) != 13 {
		t.Fatalf("unique contention produced %d segments: %v", len(segments), err)
	}
	seen := map[string]bool{}
	for index, segment := range segments {
		if segment.Sequence != uint64(index+1) || seen[segment.Command.IdempotencyKey] {
			t.Fatalf("non-serial contention result at %d: %#v", index, segment.Command)
		}
		seen[segment.Command.IdempotencyKey] = true
	}
}

func TestJournalCorruptionMatrixFailsClosedWhileTempFilesAreIgnored(t *testing.T) {
	mutations := map[string]func(string, []byte) (string, []byte){
		"truncated":     func(path string, raw []byte) (string, []byte) { return path, raw[:len(raw)/2] },
		"non-canonical": func(path string, raw []byte) (string, []byte) { return path, append(raw, ' ') },
		"content": func(path string, raw []byte) (string, []byte) {
			return path, []byte(strings.Replace(string(raw), `"kind":"reliability"`, `"kind":"changed"`, 1))
		},
		"filename": func(path string, raw []byte) (string, []byte) { return path + ".wrong.json", raw },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			store, err := openClaimed(t, t.TempDir(), reliabilityProjectID)
			if err != nil {
				t.Fatal(err)
			}
			segment, _, err := appendReliabilityEvent(store, "corrupt")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.dir, fmt.Sprintf("%012d-%s.json", segment.Sequence, segment.SegmentHash))
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			newPath, modified := mutate(path, raw)
			if newPath != path {
				if err := os.Rename(path, newPath); err != nil {
					t.Fatal(err)
				}
				path = newPath
			}
			if err := os.WriteFile(path, modified, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReadAll(); err == nil {
				t.Fatal("corrupt journal was accepted")
			}
		})
	}
	store, err := openClaimed(t, t.TempDir(), reliabilityProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, ".segment-interrupted.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if segments, err := store.ReadAll(); err != nil || len(segments) != 0 {
		t.Fatalf("uncommitted temp file affected authority: %#v %v", segments, err)
	}
}

func TestLongJournalMaintainsSequenceHashChainAndIdempotency(t *testing.T) {
	store, err := openClaimed(t, t.TempDir(), reliabilityProjectID)
	if err != nil {
		t.Fatal(err)
	}
	const count = 256
	for index := range count {
		key := fmt.Sprintf("long-%03d", index)
		if _, created, err := appendReliabilityEvent(store, key); err != nil || !created {
			t.Fatalf("append %d: created=%t err=%v", index, created, err)
		}
	}
	segments, err := store.ReadAll()
	if err != nil || len(segments) != count {
		t.Fatalf("long journal has %d segments: %v", len(segments), err)
	}
	if err := ValidateSegments(reliabilityProjectID, segments); err != nil {
		t.Fatal(err)
	}
	last, created, err := appendReliabilityEvent(store, "long-255")
	if err != nil || created || last.Sequence != count {
		t.Fatalf("long-journal retry was not stable: %#v %t %v", last, created, err)
	}
}

func TestAuthorityRetirementFenceRecoversEveryJournalCommitFault(t *testing.T) {
	for _, point := range []string{"before-temp-create", "before-temp-write", "before-temp-sync", "before-rename", "after-rename", "before-directory-sync"} {
		t.Run(point, func(t *testing.T) {
			dataDir := t.TempDir()
			store, err := openClaimed(t, dataDir, reliabilityProjectID)
			if err != nil {
				t.Fatal(err)
			}
			initial, err := store.Append(Command{ID: "initial", Kind: "initial", IdempotencyKey: "initial"}, []Event{{Type: "initial", Payload: json.RawMessage(`{}`)}}, time.Unix(1, 0))
			if err != nil {
				t.Fatal(err)
			}
			retirement := []byte(`{"apiVersion":"test/v1","kind":"AuthorityRetirement","intent":"same"}`)
			sameRetirement := func(existing []byte) error {
				var got, want any
				if json.Unmarshal(existing, &got) != nil || json.Unmarshal(retirement, &want) != nil || !reflect.DeepEqual(got, want) {
					return fmt.Errorf("different intent")
				}
				return nil
			}
			injected := true
			store.fault = func(candidate string) error {
				if injected && candidate == point {
					injected = false
					return fmt.Errorf("injected %s", point)
				}
				return nil
			}
			applied := 0
			apply := func(committed []byte) error {
				if err := sameRetirement(committed); err != nil {
					t.Fatalf("retirement intent changed: %s", committed)
				}
				applied++
				return nil
			}
			if _, err := store.RetireAuthority(initial.SegmentHash, retirement, time.Unix(2, 0), func([]Segment) error { return nil }, sameRetirement, apply); err == nil {
				t.Fatalf("fault %s did not interrupt first retirement", point)
			}
			store.fault = nil
			committed, err := store.RetireAuthority(initial.SegmentHash, retirement, time.Unix(3, 0), func([]Segment) error { return nil }, sameRetirement, apply)
			if err != nil || sameRetirement(committed) != nil || applied != 1 {
				t.Fatalf("retirement retry after %s: committed=%s applied=%d err=%v", point, committed, applied, err)
			}
			segments, err := store.ReadAll()
			if err != nil || len(segments) != 2 || segments[1].SchemaVersion != AuthorityFenceSchemaVersion || len(segments[1].Events) != 1 || segments[1].Events[0].Type != "authority.retired" {
				t.Fatalf("retirement fence after %s is not exactly once: segments=%d err=%v", point, len(segments), err)
			}
		})
	}
}

func TestLegacyAuthorityRetirementRecoversEveryJournalCommitFault(t *testing.T) {
	for _, point := range []string{"before-temp-create", "before-temp-write", "before-temp-sync", "before-rename", "after-rename", "before-directory-sync"} {
		t.Run(point, func(t *testing.T) {
			dataDir := t.TempDir()
			store, err := openClaimed(t, dataDir, reliabilityProjectID)
			if err != nil {
				t.Fatal(err)
			}
			initial, err := store.Append(Command{ID: "initial", Kind: "initial", IdempotencyKey: "initial"}, []Event{{Type: "initial", Payload: json.RawMessage(`{}`)}}, time.Unix(1, 0))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(dataDir, "authority-claim.json")); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(dataDir, ".test-authority-home", "anchors", reliabilityProjectID+".json")); err != nil {
				t.Fatal(err)
			}
			store.capability = storeCapabilityRecovery
			retirement := []byte(`{"apiVersion":"test/v1","kind":"LegacyAuthorityRetirement","intent":"same"}`)
			reservationDigest := "sha256:" + strings.Repeat("7", 64)
			sameRetirement := func(existing []byte) error {
				var got, want any
				if json.Unmarshal(existing, &got) != nil || json.Unmarshal(retirement, &want) != nil || !reflect.DeepEqual(got, want) {
					return fmt.Errorf("different intent")
				}
				return nil
			}
			injected := true
			store.fault = func(candidate string) error {
				if injected && candidate == point {
					injected = false
					return fmt.Errorf("injected %s", point)
				}
				return nil
			}
			applied := 0
			apply := func(committed []byte) error {
				if err := sameRetirement(committed); err != nil {
					t.Fatalf("retirement intent changed: %s", committed)
				}
				applied++
				return nil
			}
			if _, err := store.RetireLegacyAuthority(initial.SegmentHash, retirement, reservationDigest, time.Unix(2, 0), func([]Segment) error {
				return project.ReserveLegacyRetirement(dataDir, reliabilityProjectID, reservationDigest)
			}, sameRetirement, apply); err == nil {
				t.Fatalf("fault %s did not interrupt first retirement", point)
			}
			store.fault = nil
			committed, err := store.RetireLegacyAuthority(initial.SegmentHash, retirement, reservationDigest, time.Unix(3, 0), func([]Segment) error {
				return project.ReserveLegacyRetirement(dataDir, reliabilityProjectID, reservationDigest)
			}, sameRetirement, apply)
			if err != nil || sameRetirement(committed) != nil || applied != 1 {
				t.Fatalf("retirement retry after %s: committed=%s applied=%d err=%v", point, committed, applied, err)
			}
			segments, err := store.ReadAll()
			if err != nil || len(segments) != 2 || segments[1].SchemaVersion != AuthorityFenceSchemaVersion || len(segments[1].Events) != 1 || segments[1].Events[0].Type != "authority.retired" {
				t.Fatalf("retirement barrier after %s is not exactly once: segments=%d err=%v", point, len(segments), err)
			}
		})
	}
}

func TestAuthorityEstablishmentRecoversEveryJournalCommitFault(t *testing.T) {
	for _, point := range []string{"before-temp-create", "before-temp-write", "before-temp-sync", "before-rename", "after-rename", "before-directory-sync"} {
		t.Run(point, func(t *testing.T) {
			store, err := openClaimed(t, t.TempDir(), reliabilityProjectID)
			if err != nil {
				t.Fatal(err)
			}
			store.capability = storeCapabilityEstablishment
			establishment := []byte(`{"apiVersion":"test/v1","kind":"AuthorityEstablishment","intent":"same"}`)
			injected := true
			store.fault = func(candidate string) error {
				if injected && candidate == point {
					injected = false
					return fmt.Errorf("injected %s", point)
				}
				return nil
			}
			if _, err := store.EstablishAuthority(establishment, time.Unix(1, 0)); err == nil {
				t.Fatalf("fault %s did not interrupt first establishment", point)
			}
			store.fault = nil
			segment, err := store.EstablishAuthority(establishment, time.Unix(2, 0))
			if err != nil || segment.Sequence != 1 || segment.SchemaVersion != AuthorityFenceSchemaVersion {
				t.Fatalf("establishment retry after %s: segment=%+v err=%v", point, segment, err)
			}
			segments, err := store.ReadAll()
			if err != nil || len(segments) != 1 || len(segments[0].Events) != 1 || segments[0].Events[0].Type != "authority.established" {
				t.Fatalf("establishment fence after %s is not exactly once: %+v %v", point, segments, err)
			}
		})
	}
}

func TestAuthorityEstablishmentRequiresPristineJournalAndExactRetry(t *testing.T) {
	establishment := []byte(`{"apiVersion":"test/v1","kind":"AuthorityEstablishment","intent":"one"}`)
	store, err := openClaimed(t, t.TempDir(), reliabilityProjectID)
	if err != nil {
		t.Fatal(err)
	}
	store.capability = storeCapabilityEstablishment
	if _, err := store.EstablishAuthority(establishment, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EstablishAuthority([]byte(`{"apiVersion":"test/v1","kind":"AuthorityEstablishment","intent":"two"}`), time.Unix(2, 0)); err == nil || !strings.Contains(err.Error(), "not pristine") {
		t.Fatalf("different establishment intent was accepted: %v", err)
	}

	nonPristine, err := openClaimed(t, t.TempDir(), "22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nonPristine.Append(Command{ID: "ordinary", Kind: "ordinary", IdempotencyKey: "ordinary"}, []Event{{Type: "ordinary", Payload: json.RawMessage(`{}`)}}, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	nonPristine.capability = storeCapabilityEstablishment
	if _, err := nonPristine.EstablishAuthority(establishment, time.Unix(2, 0)); err == nil || !strings.Contains(err.Error(), "not pristine") {
		t.Fatalf("non-pristine journal was established: %v", err)
	}
}

func TestAuthorityRetirementSidecarCannotCreateOrReplaceTheJournalFence(t *testing.T) {
	store, err := openClaimed(t, t.TempDir(), reliabilityProjectID)
	if err != nil {
		t.Fatal(err)
	}
	retirement := []byte(`{"apiVersion":"test/v1","kind":"AuthorityRetirement","intent":"sidecar-only"}`)
	if err := os.WriteFile(filepath.Join(filepath.Dir(store.dir), authorityRetirementFile), retirement, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AuthorityRetirement(); err == nil || !strings.Contains(err.Error(), "no journal fence") {
		t.Fatalf("orphan retirement sidecar was accepted: %v", err)
	}
	if _, err := store.RetireAuthority("", retirement, time.Unix(1, 0), func([]Segment) error { return nil }, nil, func([]byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "no journal fence") {
		t.Fatalf("orphan retirement sidecar created a fence: %v", err)
	}
	segments, err := store.ReadAll()
	if err != nil || len(segments) != 0 {
		t.Fatalf("orphan sidecar changed the journal: %+v %v", segments, err)
	}
}

func TestJournalProcessHelper(t *testing.T) {
	mode := os.Getenv("DAGRAIL_JOURNAL_HELPER")
	if mode == "" {
		return
	}
	store, err := openClaimed(t, os.Getenv("DAGRAIL_JOURNAL_DATA"), reliabilityProjectID)
	if err != nil {
		os.Exit(92)
	}
	if mode == "crash" {
		point := os.Getenv("DAGRAIL_JOURNAL_POINT")
		store.fault = func(observed string) error {
			if observed == point {
				os.Exit(91)
			}
			return nil
		}
	}
	if _, _, err := appendReliabilityEvent(store, os.Getenv("DAGRAIL_JOURNAL_KEY")); err != nil {
		os.Exit(93)
	}
}

func FuzzValidateSegments(f *testing.F) {
	validPayload, _ := json.Marshal([]Segment{})
	f.Add(validPayload)
	f.Add([]byte(`[{"schemaVersion":2,"sequence":1}]`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		var segments []Segment
		if json.Unmarshal(raw, &segments) != nil {
			return
		}
		_ = ValidateSegments(reliabilityProjectID, segments)
	})
}

func appendReliabilityEvent(store *Store, key string) (Segment, bool, error) {
	return store.AppendOnce(
		Command{ID: "command-" + key, Kind: "reliability", IdempotencyKey: key},
		[]Event{{Type: "reliability.recorded", Payload: json.RawMessage(`{"ok":true}`)}},
		time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
		nil,
	)
}

func oneShotFault(target string, injected error) func(string) error {
	fired := false
	return func(point string) error {
		if point == target && !fired {
			fired = true
			return injected
		}
		return nil
	}
}
