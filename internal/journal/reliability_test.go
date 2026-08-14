package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const reliabilityProjectID = "11111111-1111-4111-8111-111111111111"

func TestAppendFaultMatrixPreservesRenameCommitBoundary(t *testing.T) {
	beforeCommit := []string{"before-temp-create", "before-temp-write", "before-temp-sync", "before-rename"}
	for _, point := range beforeCommit {
		t.Run(point, func(t *testing.T) {
			store, err := Open(t.TempDir(), reliabilityProjectID)
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
			store, err := Open(t.TempDir(), reliabilityProjectID)
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
			store, err := Open(dataDir, reliabilityProjectID)
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
	if _, err := Open(dataDir, reliabilityProjectID); err != nil {
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
	store, err := Open(dataDir, reliabilityProjectID)
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
			store, err := Open(t.TempDir(), reliabilityProjectID)
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
	store, err := Open(t.TempDir(), reliabilityProjectID)
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
	store, err := Open(t.TempDir(), reliabilityProjectID)
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

func TestJournalProcessHelper(t *testing.T) {
	mode := os.Getenv("DAGRAIL_JOURNAL_HELPER")
	if mode == "" {
		return
	}
	store, err := Open(os.Getenv("DAGRAIL_JOURNAL_DATA"), reliabilityProjectID)
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
