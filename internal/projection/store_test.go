package projection

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

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

func TestOpenRejectsFutureProjectionSchema(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "projection.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version=3"); err != nil {
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
