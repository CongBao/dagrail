package service

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestReadOnlyOpenDoesNotSettlePendingAutomaticNode(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime"))
	svc, err := Init(root, "read-only-open")
	if err != nil {
		t.Fatal(err)
	}
	graph := domain.GraphDefinition{APIVersion: "dagrail.io/v1alpha1", Kind: "Graph", Metadata: domain.GraphMetadata{Name: "pending automatic"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{}, Nodes: []domain.NodeDefinition{{ID: "done", Kind: "milestone", Title: "done", Outcomes: []domain.Outcome{{ID: "reached", Class: "success"}}}}, Edges: []domain.EdgeDefinition{}}}
	revision, err := graphRevision(graph)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"graph": graph, "revision": revision})
	if _, err := svc.Journal.Append(journal.Command{ID: "read-only-gap", Kind: "graph.import", ActorRole: "bootstrap", IdempotencyKey: "read-only-gap", ObjectRef: "graph:" + revision}, []journal.Event{{Type: "graph.imported", Payload: payload}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	inspected, err := OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := inspected.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.HeadSequence != 1 || state.Nodes["done"].Status != "planned" {
		t.Fatalf("read-only open settled authority: head=%d node=%+v", state.HeadSequence, state.Nodes["done"])
	}
	segments, err := inspected.VerifyJournal()
	if err != nil || len(segments) != 1 {
		t.Fatalf("read-only open changed journal: segments=%d err=%v", len(segments), err)
	}
}

func TestRecoveryRehearsalRestoresReplaysAndRebuildsWithoutLiveMutation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime"))
	svc, err := Init(root, "recovery")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"recovery"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","title":"task","role":"worker","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "recovery-graph", "governor"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "replacement-safe-session", time.Hour, false, "recovery-bind"); err != nil {
		t.Fatal(err)
	}
	start := findActionRef(t, svc, "worker", "task", "node.start")
	if _, err := svc.ApplyAction(start, json.RawMessage(`{}`), "recovery-start"); err != nil {
		t.Fatal(err)
	}
	checkpoint := findActionRef(t, svc, "worker", "task", "attempt.checkpoint")
	if _, err := svc.ApplyAction(checkpoint, json.RawMessage(`{"summary":"durable recovery point"}`), "recovery-checkpoint"); err != nil {
		t.Fatal(err)
	}
	before, err := svc.Projection.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	inspected, err := OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	report, err := inspected.RehearseRecovery()
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || !report.ProjectionEquivalent || report.Snapshot.HeadSequence < 4 || report.RebuiltProjection.HeadHash != report.Snapshot.HeadHash || report.RebuiltProjection.Rows["checkpoints"] != 1 {
		t.Fatalf("recovery rehearsal was not ready: %+v", report)
	}
	after, err := svc.Projection.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if before.Digest != after.Digest {
		t.Fatalf("read-only rehearsal mutated the live projection: %s != %s", before.Digest, after.Digest)
	}
	validateRecoverySchema(t, report)
}

func TestRecoveryRehearsalDetectsStaleProjectionAndPassesAfterRebuild(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime"))
	svc, err := Init(root, "recovery")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(svc.Project.DataDir, "projection.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE metadata SET value='{}' WHERE key='state'"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := svc.RehearseRecovery()
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || report.ProjectionEquivalent || !hasRecoveryCode(report, "logical_fingerprint_mismatch") {
		t.Fatalf("stale projection was not detected: %+v", report)
	}
	db, err = sql.Open("sqlite", filepath.Join(svc.Project.DataDir, "projection.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var retained string
	if err := db.QueryRow("SELECT value FROM metadata WHERE key='state'").Scan(&retained); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()
	if retained != "{}" {
		t.Fatalf("recovery inspection repaired live evidence unexpectedly: %q", retained)
	}
	if err := svc.RebuildProjection(); err != nil {
		t.Fatal(err)
	}
	repaired, err := svc.RehearseRecovery()
	if err != nil || !repaired.Ready {
		t.Fatalf("rehearsal did not pass after rebuild: %+v %v", repaired, err)
	}
}

func TestRecoveryInspectionFailsClosedOnFutureProjectionSchema(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime"))
	svc, err := Init(root, "future-projection")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(svc.Project.DataDir, "projection.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version=999"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()
	inspected, err := OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	report, err := inspected.RehearseRecovery()
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || !hasRecoveryCode(report, "future_schema_unsupported") {
		t.Fatalf("future projection schema was not rejected: %+v", report)
	}
	db, err = sql.Open("sqlite", filepath.Join(svc.Project.DataDir, "projection.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var schema int
	if err := db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil || schema != 999 {
		t.Fatalf("inspection mutated future schema: %d %v", schema, err)
	}
}

func TestRecoveryRehearsalProvesRebuildWhenLiveProjectionIsMissing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime"))
	svc, err := Init(root, "missing-projection")
	if err != nil {
		t.Fatal(err)
	}
	projectionPath := filepath.Join(svc.Project.DataDir, "projection.sqlite")
	if err := os.Remove(projectionPath); err != nil {
		t.Fatal(err)
	}
	inspected, err := OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	report, err := inspected.RehearseRecovery()
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || report.RebuiltProjection.Fingerprint == "" || !report.RebuiltProjection.Integrity || !hasRecoveryCode(report, "integrity_failed") {
		t.Fatalf("missing live projection did not retain disposable rebuild proof: %+v", report)
	}
	if _, err := os.Stat(projectionPath); !os.IsNotExist(err) {
		t.Fatalf("inspection recreated the missing live projection: %v", err)
	}
	repaired, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := repaired.RehearseRecovery()
	if err != nil || !verified.Ready {
		t.Fatalf("ordinary open did not rebuild the disposable projection: %+v %v", verified, err)
	}
}

func hasRecoveryCode(report RecoveryReport, code string) bool {
	for _, check := range report.Checks {
		if check.Code == code {
			return true
		}
	}
	return false
}

func validateRecoverySchema(t *testing.T, report RecoveryReport) {
	t.Helper()
	raw, err := os.ReadFile("../../schemas/recovery-rehearsal-v1alpha1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:recovery", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:recovery")
	if err != nil {
		t.Fatal(err)
	}
	instanceRaw, _ := json.Marshal(report)
	var instance any
	if err := json.Unmarshal(instanceRaw, &instance); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("recovery report does not match published schema: %v", err)
	}
}
