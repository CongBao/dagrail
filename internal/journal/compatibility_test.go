package journal

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gowebpki/jcs"
)

func TestImmutableLegacyFixtureVerifies(t *testing.T) {
	encoded, err := os.ReadFile(filepath.Join("testdata", "v1-segment.hex"))
	if err != nil {
		t.Fatal(err)
	}
	segmentBytes, err := hex.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir(), "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.dir, "000000000001-39feca660a92c1fa150901f61348559680ad377ae51e85029c293e67fe449e43.json")
	if err := os.WriteFile(path, segmentBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	segments, err := store.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || segments[0].SchemaVersion != LegacySegmentSchemaVersion {
		t.Fatalf("legacy fixture did not verify: %#v", segments)
	}
}

func TestMixedVersionJournalPreservesLegacyBytes(t *testing.T) {
	store, err := Open(t.TempDir(), "project-mixed")
	if err != nil {
		t.Fatal(err)
	}
	legacyPayload := json.RawMessage(`{"arbitrary":[1,true,{"x":"y"}]}`)
	legacyBytes, legacyPath := writeSegment(t, store, unsignedSegment{
		SchemaVersion: LegacySegmentSchemaVersion,
		Sequence:      1,
		ProjectID:     "project-mixed",
		Command:       Command{ID: "command-1", Kind: "legacy", IdempotencyKey: "legacy-1"},
		Events:        []Event{{Type: "legacy.recorded", Payload: legacyPayload}},
		CommittedAt:   "2026-01-01T00:00:00Z",
	})

	segments, err := store.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || segments[0].SchemaVersion != LegacySegmentSchemaVersion {
		t.Fatalf("unexpected legacy segments: %#v", segments)
	}
	normalized, err := UpcastEvent(segments[0].SchemaVersion, segments[0].Events[0])
	if err != nil {
		t.Fatal(err)
	}
	if normalized.SchemaVersion != CurrentEventSchemaVersion {
		t.Fatalf("normalized event schema = %d", normalized.SchemaVersion)
	}
	var storedTree, normalizedTree any
	if err := json.Unmarshal(segments[0].Events[0].Payload, &storedTree); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(normalized.Payload, &normalizedTree); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(storedTree, normalizedTree) {
		t.Fatalf("upcast changed payload tree: %#v != %#v", storedTree, normalizedTree)
	}

	appended, created, err := store.AppendOnce(
		Command{ID: "command-2", Kind: "current", IdempotencyKey: "current-1"},
		[]Event{{Type: "current.recorded", Payload: json.RawMessage(`{"ok":true}`)}},
		time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !created || appended.SchemaVersion != CurrentSegmentSchemaVersion || appended.Events[0].SchemaVersion != CurrentEventSchemaVersion {
		t.Fatalf("unexpected current segment: %#v", appended)
	}
	after, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, legacyBytes) {
		t.Fatal("appending a current segment rewrote legacy journal bytes")
	}

	mixed, err := store.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(mixed) != 2 || mixed[0].SchemaVersion != 1 || mixed[1].SchemaVersion != 2 {
		t.Fatalf("mixed journal was not readable: %#v", mixed)
	}
	report, err := store.Compatibility()
	if err != nil {
		t.Fatal(err)
	}
	if !report.Compatible || report.LegacySegmentCount != 1 || report.UpcastedEventCount != 1 || report.EventCount != 2 {
		t.Fatalf("unexpected compatibility report: %#v", report)
	}
}

func TestJournalRejectsFutureSegmentAndEventSchemas(t *testing.T) {
	tests := []struct {
		name          string
		segmentSchema int
		eventSchema   int
		want          string
	}{
		{name: "future segment", segmentSchema: CurrentSegmentSchemaVersion + 1, eventSchema: CurrentEventSchemaVersion, want: "unsupported segment schema version"},
		{name: "future event", segmentSchema: CurrentSegmentSchemaVersion, eventSchema: CurrentEventSchemaVersion + 1, want: "unsupported schema version"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(t.TempDir(), "project-future")
			if err != nil {
				t.Fatal(err)
			}
			writeSegment(t, store, unsignedSegment{
				SchemaVersion: test.segmentSchema,
				Sequence:      1,
				ProjectID:     "project-future",
				Command:       Command{ID: "command-1", Kind: "future", IdempotencyKey: "future-1"},
				Events:        []Event{{Type: "future.recorded", SchemaVersion: test.eventSchema, Payload: json.RawMessage(`{"ok":true}`)}},
				CommittedAt:   "2026-01-01T00:00:00Z",
			})
			_, err = store.ReadAll()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ReadAll error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestAppendRejectsInvalidEventWithoutCommitting(t *testing.T) {
	store, err := Open(t.TempDir(), "project-invalid")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.AppendOnce(
		Command{ID: "command-1", Kind: "invalid", IdempotencyKey: "invalid-1"},
		[]Event{{Type: "invalid.recorded", SchemaVersion: CurrentEventSchemaVersion + 1, Payload: json.RawMessage(`{}`)}},
		time.Now(),
		nil,
	)
	if err == nil {
		t.Fatal("AppendOnce accepted a future event schema")
	}
	segments, readErr := store.ReadAll()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(segments) != 0 {
		t.Fatalf("invalid append committed %d segments", len(segments))
	}
}

func writeSegment(t *testing.T, store *Store, unsigned unsignedSegment) ([]byte, string) {
	t.Helper()
	hash, err := computeHash(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	segment := Segment{
		SchemaVersion: unsigned.SchemaVersion,
		Sequence:      unsigned.Sequence,
		ProjectID:     unsigned.ProjectID,
		PreviousHash:  unsigned.PreviousHash,
		Command:       unsigned.Command,
		Events:        unsigned.Events,
		CommittedAt:   unsigned.CommittedAt,
		SegmentHash:   hash,
	}
	raw, err := json.Marshal(segment)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.dir, formatFilename(segment.Sequence, hash))
	if err := os.WriteFile(path, canonical, 0o600); err != nil {
		t.Fatal(err)
	}
	return canonical, path
}

func formatFilename(sequence uint64, hash string) string {
	return fmt.Sprintf("%012d-%s.json", sequence, hash)
}
