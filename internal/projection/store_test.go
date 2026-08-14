package projection

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/CongBao/dagrail/internal/domain"
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
