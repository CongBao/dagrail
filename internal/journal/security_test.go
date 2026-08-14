package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gowebpki/jcs"
)

func TestReadRejectsCanonicalUnknownFieldsThatAreOutsideTheHashEnvelope(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root, reliabilityProjectID)
	if err != nil {
		t.Fatal(err)
	}
	segment, err := store.Append(Command{ID: "command", Kind: "test", IdempotencyKey: "test"}, []Event{{Type: "test.recorded", Payload: json.RawMessage(`{"ok":true}`)}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "journal", "000000000001-"+segment.SegmentHash+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	value["unexpected"] = "not-covered-by-the-typed-hash"
	mutated, _ := json.Marshal(value)
	canonical, err := jcs.Transform(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, canonical, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadAll(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown canonical segment field was accepted: %v", err)
	}
}

func TestAppendRejectsSensitiveCommandAndEventMaterial(t *testing.T) {
	store, err := Open(t.TempDir(), reliabilityProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(Command{ID: "command", Kind: "test", IdempotencyKey: "Bearer abcdefghijklmnopqrstuvwxyz"}, []Event{{Type: "test.recorded", Payload: json.RawMessage(`{"ok":true}`)}}, time.Now()); err == nil || !strings.Contains(err.Error(), "prohibited") {
		t.Fatalf("sensitive command was accepted: %v", err)
	}
	if _, err := store.Append(Command{ID: "command", Kind: "test", IdempotencyKey: "safe"}, []Event{{Type: "test.recorded", Payload: json.RawMessage(`{"note":"github_pat_abcdefghijklmnopqrstuvwxyz"}`)}}, time.Now()); err == nil || !strings.Contains(err.Error(), "prohibited") {
		t.Fatalf("sensitive event was accepted: %v", err)
	}
}
