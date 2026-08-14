package projection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/gofrs/flock"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type Store struct{ path string }

var ErrFutureSchema = errors.New("projection schema was created by a newer DAGrail version")

const CurrentSchemaVersion = 3

var projectionOpenLocks sync.Map

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dataDir, "projection.lock")
	processLock, _ := projectionOpenLocks.LoadOrStore(lockPath, &sync.Mutex{})
	processLock.(*sync.Mutex).Lock()
	defer processLock.(*sync.Mutex).Unlock()
	fileLock := flock.New(lockPath)
	if err := fileLock.Lock(); err != nil {
		return nil, err
	}
	defer func() { _ = fileLock.Unlock() }()

	store := &Store{path: filepath.Join(dataDir, "projection.sqlite")}
	if err := store.initialize(); err == nil {
		return store, nil
	} else if errors.Is(err, ErrFutureSchema) || isTransientSQLiteError(err) {
		return nil, err
	}
	backup := store.path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	if renameErr := os.Rename(store.path, backup); renameErr != nil && !os.IsNotExist(renameErr) {
		return nil, fmt.Errorf("preserve corrupt projection: %w", renameErr)
	}
	_ = os.Remove(store.path + "-wal")
	_ = os.Remove(store.path + "-shm")
	if err := store.initialize(); err != nil {
		return nil, err
	}
	return store, nil
}

func isTransientSQLiteError(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	primaryCode := sqliteErr.Code() & 0xff
	return primaryCode == sqlite3.SQLITE_BUSY || primaryCode == sqlite3.SQLITE_LOCKED
}

func (s *Store) initialize() error {
	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		return err
	}
	defer db.Close()
	connection, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), "PRAGMA busy_timeout=5000"); err != nil {
		return err
	}
	if _, err := connection.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
		return err
	}
	if _, err := connection.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var schemaVersion int
	if err := connection.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&schemaVersion); err != nil {
		return err
	}
	if schemaVersion > CurrentSchemaVersion {
		return fmt.Errorf("%w: version %d", ErrFutureSchema, schemaVersion)
	}
	if schemaVersion == 0 {
		if _, err := connection.ExecContext(context.Background(), schemaV1); err != nil {
			return fmt.Errorf("initialize projection schema v1: %w", err)
		}
		schemaVersion = 1
	}
	if schemaVersion == 1 {
		if _, err := connection.ExecContext(context.Background(), migrateV1ToV2); err != nil {
			return fmt.Errorf("migrate projection schema v1 to v2: %w", err)
		}
		schemaVersion = 2
	}
	if schemaVersion == 2 {
		if _, err := connection.ExecContext(context.Background(), migrateV2ToV3); err != nil {
			return fmt.Errorf("migrate projection schema v2 to v3: %w", err)
		}
	}
	if _, err := connection.ExecContext(context.Background(), "COMMIT"); err != nil {
		return fmt.Errorf("commit projection schema migration: %w", err)
	}
	committed = true
	return nil
}

const schemaV1 = `
CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS applied_segments (sequence INTEGER PRIMARY KEY, hash TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS graph_revisions (revision TEXT PRIMARY KEY, graph_json BLOB NOT NULL, sequence INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS nodes (node_id TEXT PRIMARY KEY, kind TEXT NOT NULL, role_id TEXT, status TEXT NOT NULL, outcome TEXT);
CREATE TABLE IF NOT EXISTS edges (edge_id TEXT PRIMARY KEY, source_id TEXT NOT NULL, target_id TEXT NOT NULL, predicate_json BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS roles (role_id TEXT PRIMARY KEY, capabilities_json BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS attempts (attempt_id TEXT PRIMARY KEY, node_id TEXT NOT NULL, status TEXT NOT NULL, outcome TEXT, checkpoint_id TEXT);
CREATE TABLE IF NOT EXISTS role_leases (role_id TEXT PRIMARY KEY, binding_json BLOB NOT NULL, expires_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS checkpoints (checkpoint_id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL, payload_json BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS actions (action_id TEXT PRIMARY KEY, payload_json BLOB NOT NULL, status TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS outbox (action_id TEXT PRIMARY KEY, effect_json BLOB NOT NULL, status TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS incidents (incident_id TEXT PRIMARY KEY, payload_json BLOB NOT NULL, status TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS resources (resource_id TEXT PRIMARY KEY, lease_json BLOB NOT NULL, status TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS evidence_index (digest TEXT PRIMARY KEY, metadata_json BLOB NOT NULL);
PRAGMA user_version=1;
`

const migrateV1ToV2 = `
ALTER TABLE applied_segments ADD COLUMN segment_schema INTEGER NOT NULL DEFAULT 1;
PRAGMA user_version=2;
`

const migrateV2ToV3 = `
CREATE TABLE IF NOT EXISTS evidence_packages (package_id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL, node_id TEXT NOT NULL, core_digest TEXT NOT NULL, package_json BLOB NOT NULL, sequence INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS reuse_decisions (decision_id TEXT PRIMARY KEY, package_id TEXT NOT NULL, result TEXT NOT NULL, decision_json BLOB NOT NULL, sequence INTEGER NOT NULL);
PRAGMA user_version=3;
`

func (s *Store) SchemaVersion() (int, error) {
	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	connection, err := db.Conn(context.Background())
	if err != nil {
		return 0, err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), "PRAGMA busy_timeout=5000"); err != nil {
		return 0, err
	}
	var version int
	if err := connection.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func (s *Store) Sync(state domain.State, segments []journal.Segment) error {
	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"metadata", "applied_segments", "graph_revisions", "nodes", "edges", "roles", "attempts", "role_leases", "checkpoints", "evidence_packages", "reuse_decisions", "actions", "outbox", "incidents", "resources", "evidence_index"} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return err
		}
	}
	if state.Graph != nil {
		graphJSON, _ := json.Marshal(state.Graph)
		seq := 0
		if len(segments) > 0 {
			seq = int(segments[len(segments)-1].Sequence)
		}
		if _, err := tx.Exec("INSERT INTO graph_revisions(revision, graph_json, sequence) VALUES(?,?,?)", state.GraphRevision, graphJSON, seq); err != nil {
			return err
		}
		for _, node := range state.Graph.Spec.Nodes {
			runtime := state.Nodes[node.ID]
			if _, err := tx.Exec("INSERT INTO nodes(node_id,kind,role_id,status,outcome) VALUES(?,?,?,?,?)", node.ID, node.Kind, nullable(node.Role), runtime.Status, nullable(runtime.Outcome)); err != nil {
				return err
			}
		}
		for _, edge := range state.Graph.Spec.Edges {
			predicate, _ := json.Marshal(edge.When)
			if _, err := tx.Exec("INSERT INTO edges(edge_id,source_id,target_id,predicate_json) VALUES(?,?,?,?)", edge.ID, edge.From, edge.To, predicate); err != nil {
				return err
			}
		}
		for _, role := range state.Graph.Spec.Roles {
			capabilities, _ := json.Marshal(role.Capabilities)
			if _, err := tx.Exec("INSERT INTO roles(role_id,capabilities_json) VALUES(?,?)", role.ID, capabilities); err != nil {
				return err
			}
		}
	}
	for _, attempt := range state.Attempts {
		if _, err := tx.Exec("INSERT INTO attempts(attempt_id,node_id,status,outcome,checkpoint_id) VALUES(?,?,?,?,?)", attempt.ID, attempt.NodeID, attempt.Status, nullable(attempt.Outcome), nullable(attempt.CheckpointID)); err != nil {
			return err
		}
	}
	for _, lease := range state.Leases {
		value, _ := json.Marshal(lease)
		if _, err := tx.Exec("INSERT INTO role_leases(role_id,binding_json,expires_at) VALUES(?,?,?)", lease.RoleID, value, lease.ExpiresAt); err != nil {
			return err
		}
	}
	for _, checkpoint := range state.Checkpoints {
		value, _ := json.Marshal(checkpoint)
		if _, err := tx.Exec("INSERT INTO checkpoints(checkpoint_id,attempt_id,payload_json) VALUES(?,?,?)", checkpoint.ID, checkpoint.AttemptID, value); err != nil {
			return err
		}
		for _, evidence := range checkpoint.EvidenceRefs {
			value, _ := json.Marshal(evidence)
			if _, err := tx.Exec("INSERT OR REPLACE INTO evidence_index(digest,metadata_json) VALUES(?,?)", evidence.Digest, value); err != nil {
				return err
			}
		}
	}
	for _, pack := range state.EvidencePackages {
		value, _ := json.Marshal(pack)
		if _, err := tx.Exec("INSERT INTO evidence_packages(package_id,attempt_id,node_id,core_digest,package_json,sequence) VALUES(?,?,?,?,?,?)", pack.ID, pack.AttemptID, pack.NodeID, pack.CoreDigest, value, pack.Sequence); err != nil {
			return err
		}
		artifacts := append([]domain.ArtifactRef{pack.Candidate, pack.ProspectiveTree}, pack.Artifacts...)
		for _, artifact := range artifacts {
			metadata, _ := json.Marshal(artifact)
			if _, err := tx.Exec("INSERT OR REPLACE INTO evidence_index(digest,metadata_json) VALUES(?,?)", artifact.Digest, metadata); err != nil {
				return err
			}
		}
	}
	for _, decision := range state.ReuseDecisions {
		value, _ := json.Marshal(decision)
		if _, err := tx.Exec("INSERT INTO reuse_decisions(decision_id,package_id,result,decision_json,sequence) VALUES(?,?,?,?,?)", decision.ID, decision.PackageID, decision.Result, value, decision.Sequence); err != nil {
			return err
		}
	}
	for _, action := range state.Actions {
		value, _ := json.Marshal(action)
		if _, err := tx.Exec("INSERT INTO actions(action_id,payload_json,status) VALUES(?,?,?)", action.ID, value, action.Status); err != nil {
			return err
		}
	}
	for _, effect := range state.Effects {
		value, _ := json.Marshal(effect)
		if _, err := tx.Exec("INSERT INTO outbox(action_id,effect_json,status) VALUES(?,?,?)", effect.ID, value, effect.Status); err != nil {
			return err
		}
	}
	for _, resource := range state.Resources {
		value, _ := json.Marshal(resource)
		if _, err := tx.Exec("INSERT INTO resources(resource_id,lease_json,status) VALUES(?,?,?)", resource.ID, value, resource.Status); err != nil {
			return err
		}
	}
	for _, incident := range state.Incidents {
		value, _ := json.Marshal(incident)
		if _, err := tx.Exec("INSERT INTO incidents(incident_id,payload_json,status) VALUES(?,?,?)", incident.ID, value, incident.Status); err != nil {
			return err
		}
	}
	stateJSON, _ := json.Marshal(state)
	if _, err := tx.Exec("INSERT INTO metadata(key,value) VALUES('state',?)", string(stateJSON)); err != nil {
		return err
	}
	for _, segment := range segments {
		if _, err := tx.Exec("INSERT INTO applied_segments(sequence,hash,segment_schema) VALUES(?,?,?)", segment.Sequence, segment.SegmentHash, segment.SchemaVersion); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Rebuild(state domain.State, segments []journal.Segment) error {
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(s.path + "-wal")
	_ = os.Remove(s.path + "-shm")
	created, err := Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	return created.Sync(state, segments)
}

func (s *Store) Integrity() error {
	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return err
	}
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("SQLite integrity check returned %s", result)
	}
	return nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
