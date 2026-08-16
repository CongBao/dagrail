package projection

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	_ "modernc.org/sqlite"
)

func TestOpenMigratesV1ProjectionInPlace(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "projection.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO applied_segments(sequence,hash) VALUES(1,'legacy-hash')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	version, err := store.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if version != CurrentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, CurrentSchemaVersion)
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var hash string
	var segmentSchema int
	if err := db.QueryRow("SELECT hash,segment_schema FROM applied_segments WHERE sequence=1").Scan(&hash, &segmentSchema); err != nil {
		t.Fatal(err)
	}
	if hash != "legacy-hash" || segmentSchema != 1 {
		t.Fatalf("migration changed legacy cursor: hash=%q schema=%d", hash, segmentSchema)
	}
}

func TestConcurrentOpenSerializesProjectionMigration(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "projection.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	results := make(chan error, 4)
	for range 4 {
		go func() {
			store, openErr := Open(dataDir)
			if openErr != nil {
				results <- openErr
				return
			}
			version, versionErr := store.SchemaVersion()
			if versionErr == nil && version != CurrentSchemaVersion {
				versionErr = fmt.Errorf("schema version = %d, want %d", version, CurrentSchemaVersion)
			}
			results <- versionErr
		}()
	}
	for range 4 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenMigratesV2ProjectionAndPreservesCursor(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "projection.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrateV1ToV2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO applied_segments(sequence,hash,segment_schema) VALUES(1,'v2-hash',2)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	version, err := store.SchemaVersion()
	if err != nil || version != CurrentSchemaVersion {
		t.Fatalf("schema version = %d, error = %v", version, err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var hash string
	if err := db.QueryRow("SELECT hash FROM applied_segments WHERE sequence=1").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != "v2-hash" {
		t.Fatalf("v2 cursor changed to %q", hash)
	}
	for _, table := range []string{"evidence_packages", "reuse_decisions"} {
		var name string
		if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name); err != nil {
			t.Fatalf("missing v3 table %s: %v", table, err)
		}
	}
}

func TestOpenRejectsFutureProjectionSchema(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "projection.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", CurrentSchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Open(dataDir)
	if !errors.Is(err, ErrFutureSchema) {
		t.Fatalf("Open error = %v, want ErrFutureSchema", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("future projection should not be moved or deleted: %v", statErr)
	}
}

func TestEvidenceProjectionRebuildsFromMaterializedState(t *testing.T) {
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	state := domain.NewState("project")
	pack := domain.ExecutionPackage{
		ID: "epkg_test", NodeID: "node", AttemptID: "attempt", CoreDigest: "sha256:core", Sequence: 1,
		Candidate:       domain.ArtifactRef{Digest: "sha256:candidate", Type: "candidate"},
		ProspectiveTree: domain.ArtifactRef{Digest: "sha256:tree", Type: "git-tree"},
	}
	decision := domain.ReuseDecision{ID: "reuse_test", PackageID: pack.ID, Result: "reuse_execution", Sequence: 2}
	state.EvidencePackages[pack.ID] = pack
	state.ReuseDecisions[decision.ID] = decision
	if err := store.Sync(state, nil); err != nil {
		t.Fatal(err)
	}
	assertEvidenceProjectionCounts(t, store.path, 1, 1, 2)
	if err := store.Rebuild(state, nil); err != nil {
		t.Fatal(err)
	}
	assertEvidenceProjectionCounts(t, store.path, 1, 1, 2)
}

func TestLogicalFingerprintMatchesIndependentRebuildAndDetectsChange(t *testing.T) {
	state := domain.NewState("project")
	state.Incidents["incident"] = domain.Incident{ID: "incident", Status: "open"}
	first, err := Open(filepath.Join(t.TempDir(), "first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(filepath.Join(t.TempDir(), "second"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Sync(state, nil); err != nil {
		t.Fatal(err)
	}
	if err := second.Sync(state, nil); err != nil {
		t.Fatal(err)
	}
	one, err := first.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	two, err := second.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if one.Digest != two.Digest || one.Schema != CurrentSchemaVersion || one.Rows["incidents"] != 1 {
		t.Fatalf("independent fingerprints differ: %+v %+v", one, two)
	}
	state.Incidents["incident"] = domain.Incident{ID: "incident", Status: "resolved"}
	if err := second.Sync(state, nil); err != nil {
		t.Fatal(err)
	}
	changed, err := second.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if changed.Digest == one.Digest {
		t.Fatal("logical state change did not change the projection fingerprint")
	}
}

func TestSyncDoesNotRegressNewerJournalCursor(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	newer := domain.NewState("project")
	newer.HeadSequence = 3
	newer.HeadHash = "head-3"
	newer.Incidents["newer"] = domain.Incident{ID: "newer", Status: "open"}
	newerSegments := []journal.Segment{
		{Sequence: 1, SegmentHash: "head-1", SchemaVersion: 4},
		{Sequence: 2, SegmentHash: "head-2", SchemaVersion: 4},
		{Sequence: 3, SegmentHash: "head-3", SchemaVersion: 4},
	}
	if err := store.Sync(newer, newerSegments); err != nil {
		t.Fatal(err)
	}
	older := domain.NewState("project")
	older.HeadSequence = 1
	older.HeadHash = "head-1"
	if err := store.Sync(older, newerSegments[:1]); err != nil {
		t.Fatalf("stale projection sync should be an idempotent no-op: %v", err)
	}
	fingerprint, err := store.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint.HeadSequence != 3 || fingerprint.HeadHash != "head-3" || fingerprint.Rows["incidents"] != 1 {
		t.Fatalf("stale sync regressed a newer projection: %+v", fingerprint)
	}
}

func TestReadOnlyProjectionDSNIsAPortableFileURI(t *testing.T) {
	if observed := readOnlyDSN(`C:\Users\runner\DAG rail\projection.sqlite`, "windows"); observed != "file:///C:/Users/runner/DAG%20rail/projection.sqlite?immutable=1&mode=ro" {
		t.Fatalf("Windows read-only DSN = %q", observed)
	}
	if observed := readOnlyDSN("/tmp/DAG rail/projection.sqlite", "linux"); observed != "file:///tmp/DAG%20rail/projection.sqlite?immutable=1&mode=ro" {
		t.Fatalf("Unix read-only DSN = %q", observed)
	}
}

func TestInspectionFailsClosedWithoutMutatingActiveWAL(t *testing.T) {
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Sync(domain.NewState("project"), nil); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	connection, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(t.Context(), "PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), "PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), fmt.Sprintf("PRAGMA user_version=%d", CurrentSchemaVersion+995)); err != nil {
		t.Fatal(err)
	}
	walPath := store.path + "-wal"
	walInfo, err := os.Stat(walPath)
	if err != nil || walInfo.Size() == 0 {
		t.Fatalf("test did not create a non-empty WAL: info=%v err=%v", walInfo, err)
	}
	before := readProjectionFiles(t, store.path)

	inspection, err := Inspect(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := inspection.SchemaVersion(); err == nil || !strings.Contains(err.Error(), "WAL") {
		t.Fatalf("inspection ignored active future-schema WAL: version=%d err=%v", version, err)
	}
	if err := inspection.Integrity(); err == nil || !strings.Contains(err.Error(), "WAL") {
		t.Fatalf("integrity ignored active WAL: %v", err)
	}
	if fingerprint, err := inspection.Fingerprint(); err == nil || !strings.Contains(err.Error(), "WAL") {
		t.Fatalf("fingerprint ignored active WAL: %+v %v", fingerprint, err)
	}
	after := readProjectionFiles(t, store.path)
	if len(before) != len(after) {
		t.Fatalf("inspection changed projection file set: before=%v after=%v", mapKeys(before), mapKeys(after))
	}
	for path, contents := range before {
		if !bytes.Equal(contents, after[path]) {
			t.Fatalf("inspection changed protected projection bytes at %s", path)
		}
	}
}

func TestInspectionFailsClosedOnCurrentSchemaWAL(t *testing.T) {
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Sync(domain.NewState("project"), nil); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	connection, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(t.Context(), "PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), "PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), "UPDATE metadata SET value='wal-current' WHERE key='state'"); err != nil {
		t.Fatal(err)
	}
	walInfo, err := os.Stat(store.path + "-wal")
	if err != nil || walInfo.Size() == 0 {
		t.Fatalf("test did not create a current-schema WAL: info=%v err=%v", walInfo, err)
	}
	inspection, err := Inspect(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := inspection.Integrity(); !errors.Is(err, ErrUncheckpointed) {
		t.Fatalf("inspection error = %v, want ErrUncheckpointed", err)
	}
}

func TestInspectionFailsClosedOnCorruptWALSidecar(t *testing.T) {
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	walPath := store.path + "-wal"
	if err := os.WriteFile(walPath, []byte("not-a-sqlite-wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := inspection.Integrity(); err == nil || !strings.Contains(err.Error(), "WAL") {
		t.Fatalf("inspection accepted corrupt WAL sidecar: %v", err)
	}
	contents, err := os.ReadFile(walPath)
	if err != nil || string(contents) != "not-a-sqlite-wal" {
		t.Fatalf("inspection repaired or changed corrupt WAL: %q %v", contents, err)
	}
}

func readProjectionFiles(t *testing.T, path string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	for _, candidate := range []string{path, path + "-wal", path + "-shm", filepath.Join(filepath.Dir(path), "projection.lock")} {
		contents, err := os.ReadFile(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		result[candidate] = contents
	}
	return result
}

func mapKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func assertEvidenceProjectionCounts(t *testing.T, path string, packages, decisions, artifacts int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for table, expected := range map[string]int{"evidence_packages": packages, "reuse_decisions": decisions, "evidence_index": artifacts} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != expected {
			t.Fatalf("%s count = %d, want %d", table, count, expected)
		}
	}
}
