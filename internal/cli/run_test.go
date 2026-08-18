package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/cli"
	"github.com/CongBao/dagrail/internal/service"
)

func TestRelocationPreflightReadSurfacesDoNotMutateProtectedProjectBytes(t *testing.T) {
	root := t.TempDir()
	artifactRoot := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(t.TempDir(), "runtime"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"read-surface"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &stdout, &stderr)
		return stdout.String(), err
	}
	if _, err := run("init", "--root", root, "--name", "read-surface"); err != nil {
		t.Fatal(err)
	}
	svc, err := service.OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	projectionPath := filepath.Join(svc.Project.DataDir, "projection.sqlite")
	staleProjection, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph/import"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "worker", "--harness", "codex", "--session", "read-surface", "--ttl", "1h", "--idempotency-key", "role/bind"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("action", "list", "--root", root, "--role", "worker", "--node", "task"); err != nil {
		t.Fatal(err)
	}
	seedBackup := filepath.Join(artifactRoot, "seed-backup.json")
	if _, err := run("backup", "create", "--root", root, "--output", seedBackup); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args []string
	}{
		{name: "status", args: []string{"status", "--root", root}},
		{name: "history", args: []string{"history", "--root", root}},
		{name: "provider-list", args: []string{"provider", "list", "--root", root}},
		{name: "provider-check", args: []string{"provider", "check", "--root", root}},
		{name: "evidence-list", args: []string{"evidence", "list", "--root", root}},
		{name: "inspect-node", args: []string{"inspect", "--root", root, "node:task"}},
		{name: "action-list", args: []string{"action", "list", "--root", root, "--role", "worker", "--node", "task"}},
		{name: "context", args: []string{"context", "--root", root, "--view", "orchestrator"}},
		{name: "graph-export", args: []string{"graph", "export", "--root", root, "--format", "json"}},
		{name: "frontier", args: []string{"frontier", "--root", root, "--format", "json"}},
		{name: "journal-verify", args: []string{"journal", "verify", "--root", root}},
		{name: "journal-compatibility", args: []string{"journal", "compatibility", "--root", root}},
		{name: "journal-export", args: []string{"journal", "export", "--root", root}},
		{name: "lifecycle-projection", args: []string{"lifecycle", "projection", "--root", root}},
		{name: "pre-wait", args: []string{"pre-wait", "--root", root}},
		{name: "backup-create", args: []string{"backup", "create", "--root", root, "--output", filepath.Join(artifactRoot, "created-backup.json")}},
		{name: "backup-verify", args: []string{"backup", "verify", "--root", root, "--file", seedBackup}},
		{name: "doctor", args: []string{"doctor", "--root", root}},
		{name: "security-audit", args: []string{"security", "audit", "--root", root}},
		{name: "support-preview", args: []string{"support", "preview", "--root", root}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(projectionPath, staleProjection, 0o600); err != nil {
				t.Fatal(err)
			}
			before := protectedProjectSnapshot(t, root, svc.Project.DataDir)
			runReadSurfaceCLI(t, test.args...)
			after := protectedProjectSnapshot(t, root, svc.Project.DataDir)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("read surface mutated protected bytes: %v", changedSnapshotPaths(before, after))
			}
		})
	}
	if _, err := run("projection", "rebuild", "--root", root); err != nil {
		t.Fatal(err)
	}
	t.Run("recovery-rehearse", func(t *testing.T) {
		before := protectedProjectSnapshot(t, root, svc.Project.DataDir)
		runReadSurfaceCLI(t, "recovery", "rehearse", "--root", root)
		after := protectedProjectSnapshot(t, root, svc.Project.DataDir)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("read surface mutated protected bytes: %v", changedSnapshotPaths(before, after))
		}
	})
}

func TestCLIUsesOpaqueSelectorsForSchemaLegalLargeRoleAndNode(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(t.TempDir(), "runtime"))
	roleID := "role-" + strings.Repeat("r", 30_000)
	nodeID := "node-" + strings.Repeat("n", 30_000)
	graphPath := filepath.Join(root, "graph.json")
	graph, _ := json.Marshal(map[string]any{"apiVersion": "dagrail.io/v1alpha1", "kind": "Graph", "metadata": map[string]any{"name": "opaque-cli"}, "spec": map[string]any{"roles": []map[string]any{{"id": roleID, "capabilities": []string{"node.run"}}}, "nodes": []map[string]any{{"id": nodeID, "kind": "task", "role": roleID, "title": "task", "outcomes": []map[string]any{{"id": "done", "class": "success"}}}}, "edges": []any{}}})
	if err := os.WriteFile(graphPath, graph, 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &stdout, &stderr)
		return stdout.String(), err
	}
	if _, err := run("init", "--root", root, "--name", "opaque-cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "import"); err != nil {
		t.Fatal(err)
	}
	frontier, err := run("frontier", "--root", root)
	if err != nil || len(frontier) > 24*1024 || strings.Contains(frontier, nodeID) || !strings.Contains(frontier, "inspect:") {
		t.Fatalf("default frontier was not bounded and recoverable: bytes=%d err=%v", len(frontier), err)
	}
	frontierJSON, err := run("frontier", "--root", root, "--format", "json")
	if err != nil || len(frontierJSON) > 24*1024 || strings.Contains(frontierJSON, nodeID) || !strings.Contains(frontierJSON, `"detailRef"`) {
		t.Fatalf("JSON frontier was not bounded and recoverable: bytes=%d err=%v", len(frontierJSON), err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	var cancelledOutput, cancelledErrors bytes.Buffer
	err = cli.RunContext(cancelled, []string{"frontier", "--root", root}, strings.NewReader(""), &cancelledOutput, &cancelledErrors)
	if !errors.Is(err, context.Canceled) || cancelledOutput.Len() != 0 {
		t.Fatalf("frontier ignored caller cancellation: output=%q err=%v", cancelledOutput.String(), err)
	}
	svc, err := service.OpenForInspection(root)
	if err != nil {
		t.Fatal(err)
	}
	roleRef, err := svc.EntityRef("role", roleID)
	if err != nil {
		t.Fatal(err)
	}
	nodeRef, err := svc.EntityRef("node", nodeID)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := run("role", "bind", "--root", root, "--role-ref", roleRef, "--harness", "codex", "--session", strings.Repeat("s", 30_000), "--ttl", "1h", "--idempotency-key", "bind")
	if err != nil || len(bound) > 24*1024 || strings.Contains(bound, roleID) {
		t.Fatalf("opaque Role bind was not bounded: bytes=%d err=%v", len(bound), err)
	}
	actions, err := run("action", "list", "--root", root, "--role-ref", roleRef, "--node-ref", nodeRef)
	if err != nil || len(actions) > 24*1024 || strings.Contains(actions, nodeID) {
		t.Fatalf("opaque action list was not bounded: bytes=%d err=%v", len(actions), err)
	}
	contextOutput, err := run("context", "--root", root, "--view", "worker", "--role-ref", roleRef, "--node-ref", nodeRef, "--budget-bytes", "512")
	if err != nil || len(contextOutput) > 513 || strings.Contains(contextOutput, roleID) || strings.Contains(contextOutput, nodeID) {
		t.Fatalf("opaque context was not bounded: bytes=%d err=%v", len(contextOutput), err)
	}
}

func TestMissingActionSecretReadSurfacesFailClosedWithoutMutation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(t.TempDir(), "runtime"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"missing-action-secret"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		var stdout, stderr bytes.Buffer
		return cli.Run(args, strings.NewReader(""), &stdout, &stderr)
	}
	if err := run("init", "--root", root, "--name", "missing-action-secret"); err != nil {
		t.Fatal(err)
	}
	if err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph/import"); err != nil {
		t.Fatal(err)
	}
	if err := run("role", "bind", "--root", root, "--role", "worker", "--harness", "codex", "--session", "session", "--ttl", "1h", "--idempotency-key", "role/bind"); err != nil {
		t.Fatal(err)
	}
	svc, err := service.OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	contextRaw, err := svc.Context("worker", "worker", "task", 512)
	if err != nil {
		t.Fatal(err)
	}
	var contextEnvelope struct {
		Data struct {
			OperationsRef string `json:"operationsRef"`
		} `json:"data"`
	}
	if err := json.Unmarshal(contextRaw, &contextEnvelope); err != nil || contextEnvelope.Data.OperationsRef == "" {
		t.Fatalf("decode operations ref before damage: %v %s", err, contextRaw)
	}
	secretPath := filepath.Join(svc.Project.DataDir, "action-secret")
	if err := os.Remove(secretPath); err != nil {
		t.Fatal(err)
	}
	before := protectedProjectSnapshot(t, root, svc.Project.DataDir)
	for _, args := range [][]string{
		{"action", "list", "--root", root, "--role", "worker", "--node", "task"},
		{"context", "--root", root, "--view", "worker", "--role", "worker", "--node", "task", "--budget-bytes", "512"},
		{"inspect", "--root", root, contextEnvelope.Data.OperationsRef},
	} {
		if err := run(args...); err == nil || !strings.Contains(err.Error(), "action secret is not initialized") {
			t.Fatalf("read surface did not fail closed for missing action secret: args=%v err=%v", args, err)
		}
		after := protectedProjectSnapshot(t, root, svc.Project.DataDir)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("read surface repaired action secret implicitly: args=%v changed=%v", args, changedSnapshotPaths(before, after))
		}
	}
	if _, err := os.Stat(secretPath); !os.IsNotExist(err) {
		t.Fatalf("read surface recreated missing action secret: %v", err)
	}

	for _, test := range []struct {
		name    string
		secret  []byte
		mode    os.FileMode
		message string
		skip    bool
	}{
		{name: "truncated", secret: make([]byte, 31), mode: 0o600, message: "32-byte"},
		{name: "excess-permissions", secret: make([]byte, 32), mode: 0o644, message: "permissions", skip: runtime.GOOS == "windows"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.skip {
				t.Skip("POSIX permission check is not available on Windows")
			}
			if err := os.WriteFile(secretPath, test.secret, test.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(secretPath, test.mode); err != nil {
				t.Fatal(err)
			}
			before := protectedProjectSnapshot(t, root, svc.Project.DataDir)
			if err := run("inspect", "--root", root, contextEnvelope.Data.OperationsRef); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("operations inspection hid %s action secret: %v", test.name, err)
			}
			after := protectedProjectSnapshot(t, root, svc.Project.DataDir)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("operations inspection mutated damaged secret state: %v", changedSnapshotPaths(before, after))
			}
		})
		if err := os.Remove(secretPath); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadSurfaceCLIHelper(t *testing.T) {
	if os.Getenv("DAGRAIL_READ_SURFACE_HELPER") != "1" {
		return
	}
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		t.Fatal("missing helper command")
	}
	if err := cli.Run(os.Args[separator+1:], strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
}

func runReadSurfaceCLI(t *testing.T, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-test.run=^TestReadSurfaceCLIHelper$", "--"}, args...)
	command := exec.Command(os.Args[0], commandArgs...)
	command.Env = append(os.Environ(), "DAGRAIL_READ_SURFACE_HELPER=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("read surface subprocess failed: %v: %s", err, output)
	}
}

func protectedProjectSnapshot(t *testing.T, root, dataDir string) map[string][sha256.Size]byte {
	t.Helper()
	result := map[string][sha256.Size]byte{}
	for label, base := range map[string]string{"project": filepath.Join(root, ".dagrail"), "runtime": dataDir} {
		err := filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			result[label+"/"+filepath.ToSlash(relative)] = sha256.Sum256(raw)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func changedSnapshotPaths(before, after map[string][sha256.Size]byte) []string {
	changed := []string{}
	for path, digest := range before {
		if after[path] != digest {
			changed = append(changed, path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed
}

func TestLifecycleCLIRequiresIndependentTrustAnchorAndImportsAtomically(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"migration"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &stdout, &stderr)
		return stdout.String(), err
	}
	if _, err := run("init", "--root", root, "--name", "migration"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph/import"); err != nil {
		t.Fatal(err)
	}
	svc, err := service.OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 15, 2, 3, 4, 0, time.UTC).Format(time.RFC3339Nano)
	payload := json.RawMessage(`{"roleId":"worker","harness":"manual","sessionId":"imported-session","boundAt":"2026-08-15T02:03:04Z","expiresAt":"2026-08-16T02:03:04Z","active":true}`)
	records := []service.LifecycleMigrationRecord{{SourceSequence: 1, SourceEventID: "source-1", OccurredAt: now, Events: []service.LifecycleMigrationEvent{{Type: "role.bound", Payload: payload}}}}
	eventHash, err := service.LifecycleSourceEventHash(records[0])
	if err != nil {
		t.Fatal(err)
	}
	records[0].SourceEventHash = eventHash
	recordsDigest, err := service.LifecycleRecordsDigest(records)
	if err != nil {
		t.Fatal(err)
	}
	manifest := service.LifecycleMigrationManifest{APIVersion: service.LifecycleMigrationAPIVersion, Kind: "LifecycleMigration", ProjectID: state.ProjectID, GraphRevision: state.GraphRevision, ExpectedJournalHead: state.HeadHash, Source: service.LifecycleMigrationSource{System: "external", Project: "source", HeadSequence: 1, HeadEventID: "source-1", HeadEventHash: eventHash}, RecordsDigest: recordsDigest, Records: records}
	authority, err := service.LifecycleSourceAuthorityHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Source.AuthorityHash = authority
	manifestRaw, _ := json.Marshal(manifest)
	manifestPath := filepath.Join(root, "migration.json")
	if err := os.WriteFile(manifestPath, manifestRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run("lifecycle", "validate-history", "--root", root, "--file", manifestPath); err == nil {
		t.Fatal("lifecycle validation accepted a self-asserted manifest without an out-of-band trust anchor")
	}
	validated, err := run("lifecycle", "validate-history", "--root", root, "--file", manifestPath, "--source-authority-hash", authority)
	if err != nil || !strings.Contains(validated, `"valid":true`) {
		t.Fatalf("lifecycle validation failed: %v %s", err, validated)
	}
	if _, err := run("lifecycle", "import-history", "--root", root, "--file", manifestPath, "--source-authority-hash", authority, "--actor-role", "migration-operator", "--idempotency-key", "migration/source-1"); err != nil {
		t.Fatal(err)
	}
	projection, err := run("lifecycle", "projection", "--root", root)
	if err != nil || !strings.Contains(projection, `"kind":"LifecycleProjection"`) || !strings.Contains(projection, `"sourceAuthorityHash":"`+authority+`"`) {
		t.Fatalf("lifecycle projection failed: %v %s", err, projection)
	}
}

func TestRecoveryRotateAuthorityCLIUsesAuthenticatedPrefix(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"rotation-cli"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &stdout, &stderr)
		return stdout.String(), err
	}
	if _, err := run("init", "--root", root, "--name", "rotation-cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph"); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(root, "backup.json")
	if _, err := run("backup", "create", "--root", root, "--output", backupPath); err != nil {
		t.Fatal(err)
	}
	svc, err := service.OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	output, err := run("recovery", "rotate-authority", "--root", root, "--backup", backupPath, "--expected-current-head", state.HeadHash, "--reason", "cli recovery", "--idempotency-key", "rotate/cli")
	if err != nil || !strings.Contains(output, `"kind":"AuthorityRotationReceipt"`) || !strings.Contains(output, `"previousProjectId":"`+state.ProjectID+`"`) {
		t.Fatalf("authority rotation CLI failed: %v %s", err, output)
	}
	replacement, err := service.OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	replacementState, err := replacement.State()
	if err != nil || replacementState.ProjectID == state.ProjectID || replacementState.HeadSequence != 1 || replacementState.Graph != nil {
		t.Fatalf("authority rotation CLI did not create a fence-only replacement: %+v %v", replacementState, err)
	}
}

func TestRecoveryRelocateAuthorityRehomesEstablishedReplacement(t *testing.T) {
	rootA := t.TempDir()
	targetRoot := t.TempDir()
	homeA := filepath.Join(t.TempDir(), "runtime-a")
	homeB := filepath.Join(t.TempDir(), "runtime-b")
	t.Setenv("DAGRAIL_HOME", homeA)
	run := func(args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &stdout, &stderr)
		return stdout.String(), err
	}
	if _, err := run("init", "--root", rootA, "--name", "relocation-cli"); err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(rootA, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"relocation-cli"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"work","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", rootA, "--file", graphPath, "--idempotency-key", "graph"); err != nil {
		t.Fatal(err)
	}
	legacyService, err := service.OpenForRecovery(rootA)
	if err != nil {
		t.Fatal(err)
	}
	legacyProjectID := legacyService.Project.Config.ProjectID
	legacyHead := rewriteCurrentFixtureAsLegacyAuthority(t, legacyService)
	if err := os.MkdirAll(filepath.Join(targetRoot, ".dagrail"), 0o700); err != nil {
		t.Fatal(err)
	}
	locator, err := os.ReadFile(filepath.Join(rootA, ".dagrail", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, ".dagrail", "project.yaml"), locator, 0o600); err != nil {
		t.Fatal(err)
	}
	adoptedRaw, err := run("recovery", "adopt-legacy-authority", "--root", rootA, "--expected-project-id", legacyProjectID, "--expected-current-head", legacyHead, "--reason", "establish in temporary runtime", "--idempotency-key", "adopt/temporary")
	if err != nil {
		t.Fatal(err)
	}
	var adopted service.AuthorityAdoptionReceipt
	if err := json.Unmarshal([]byte(adoptedRaw), &adopted); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(rootA, "replacement-backup.json")
	if _, err := run("backup", "create", "--root", rootA, "--output", backupPath); err != nil {
		t.Fatal(err)
	}
	source, err := service.OpenForRecovery(rootA)
	if err != nil {
		t.Fatal(err)
	}
	sourceState, err := source.State()
	if err != nil {
		t.Fatal(err)
	}
	if sourceState.ProjectID != adopted.ReplacementProjectID || sourceState.HeadHash == "" {
		t.Fatalf("unexpected established source authority: %+v", sourceState)
	}
	unrelatedRoot := t.TempDir()
	if _, err := run("init", "--root", unrelatedRoot, "--name", "unrelated-relocation-target"); err != nil {
		t.Fatal(err)
	}
	unrelated, err := service.OpenForRecovery(unrelatedRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("DAGRAIL_HOME", homeB); err != nil {
		t.Fatal(err)
	}
	if _, err := run("recovery", "relocate-authority", "--root", unrelatedRoot, "--backup", backupPath, "--expected-project-id", unrelated.Project.Config.ProjectID, "--expected-current-head", sourceState.HeadHash, "--reason", "must not cross authority lineage", "--idempotency-key", "relocate/unrelated"); err == nil || !strings.Contains(err.Error(), "not descended") {
		t.Fatalf("unrelated locator accepted relocation source: source=%s adopted=%s unrelated=%s err=%v", sourceState.ProjectID, adopted.ReplacementProjectID, unrelated.Project.Config.ProjectID, err)
	}
	if _, retired, err := source.Journal.AuthorityRetirement(); err != nil || retired {
		t.Fatalf("rejected relocation changed source retirement: retired=%t err=%v", retired, err)
	}
	relocatedRaw, err := run("recovery", "relocate-authority", "--root", targetRoot, "--backup", backupPath, "--expected-project-id", legacyProjectID, "--expected-current-head", sourceState.HeadHash, "--reason", "move established authority to durable runtime", "--idempotency-key", "relocate/durable")
	if err != nil {
		t.Fatalf("public relocation failed: %v", err)
	}
	var relocated service.AuthorityRelocationReceipt
	if err := json.Unmarshal([]byte(relocatedRaw), &relocated); err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyAuthorityRelocationReceipt(relocated); err != nil {
		t.Fatal(err)
	}
	if relocated.SourceProjectID != adopted.ReplacementProjectID || relocated.PreviousLocatorProjectID != legacyProjectID || relocated.ReplacementProjectID == relocated.SourceProjectID {
		t.Fatalf("unexpected relocation receipt: %+v", relocated)
	}
	relocatedService, err := service.Open(targetRoot)
	if err != nil {
		t.Fatal(err)
	}
	if relocatedService.Project.Config.ProjectID != relocated.ReplacementProjectID || !strings.HasPrefix(relocatedService.Project.DataDir, homeB+string(filepath.Separator)) {
		t.Fatalf("replacement did not move to selected runtime: %+v", relocatedService.Project)
	}
	retriedRaw, err := run("recovery", "relocate-authority", "--root", targetRoot, "--backup", backupPath, "--expected-project-id", legacyProjectID, "--expected-current-head", sourceState.HeadHash, "--reason", "move established authority to durable runtime", "--idempotency-key", "relocate/durable")
	if err != nil {
		t.Fatal(err)
	}
	var retried service.AuthorityRelocationReceipt
	if err := json.Unmarshal([]byte(retriedRaw), &retried); err != nil || retried.ReceiptDigest != relocated.ReceiptDigest {
		t.Fatalf("exact relocation retry changed receipt: %+v %v", retried, err)
	}
	if err := os.Setenv("DAGRAIL_HOME", homeA); err != nil {
		t.Fatal(err)
	}
	retiredSource, err := service.Open(rootA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retiredSource.ImportGraph(graphPath, "relocate/must-not-write", "governor"); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("source authority accepted a write after relocation: %v", err)
	}
}

func TestUserCanInitializeImportGraphAndReadFrontier(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{
  "apiVersion":"dagrail.io/v1alpha1",
  "kind":"Graph",
  "metadata":{"name":"example"},
  "spec":{
    "roles":[{"id":"developer","capabilities":["node.run"]}],
    "nodes":[
      {"id":"A","kind":"task","role":"developer","title":"first","outcomes":[{"id":"success","class":"success"}]},
      {"id":"B","kind":"task","role":"developer","title":"second","outcomes":[{"id":"success","class":"success"}]}
    ],
    "edges":[{"id":"A-to-B","from":"A","to":"B","when":{"outcome":"success"}}]
  }
}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &stdout, &stderr)
		if err != nil && stderr.Len() > 0 {
			t.Log(stderr.String())
		}
		return stdout.String(), err
	}

	if _, err := run("init", "--root", root, "--name", "example"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "import-example"); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "import-example"); err != nil {
		t.Fatalf("idempotent import of the same current source failed: %v", err)
	}
	out, err := run("frontier", "--root", root, "--format", "json")
	if err != nil {
		t.Fatalf("frontier: %v", err)
	}
	if !strings.Contains(out, `"ready":["A"]`) {
		t.Fatalf("expected only A ready, got %s", out)
	}
	status, err := run("status", "--root", root)
	if err != nil || !strings.Contains(status, `"headSequence":2`) || !strings.Contains(status, `"blocked":["B"]`) {
		t.Fatalf("operational status unavailable: %v %s", err, status)
	}
	history, err := run("history", "--root", root, "--after", "1", "--limit", "1")
	if err != nil || !strings.Contains(history, `"commandKind":"graph.import"`) || strings.Contains(history, `"payload"`) {
		t.Fatalf("bounded history contract failed: %v %s", err, history)
	}
	backupPath := filepath.Join(root, "journal-backup.json")
	created, err := run("backup", "create", "--root", root, "--output", backupPath)
	if err != nil || !strings.Contains(created, `"valid":true`) {
		t.Fatalf("backup create failed: %v %s", err, created)
	}
	verified, err := run("backup", "verify", "--root", root, "--file", backupPath)
	if err != nil || !strings.Contains(verified, `"segments":2`) {
		t.Fatalf("backup verify failed: %v %s", err, verified)
	}
	compatibility, err := run("journal", "compatibility", "--root", root)
	if err != nil {
		t.Fatalf("journal compatibility: %v", err)
	}
	if !strings.Contains(compatibility, `"currentWriteSegmentSchema":3`) || !strings.Contains(compatibility, `"projectionSchemaVersion":4`) {
		t.Fatalf("compatibility report lacks current schemas: %s", compatibility)
	}
	verification, err := run("journal", "verify", "--root", root)
	if err != nil || !strings.Contains(verification, `"kind":"JournalVerification"`) || !strings.Contains(verification, `"canonicalExportSha256":"sha256:`) {
		t.Fatalf("journal verification lacks bounded integrity evidence: %v %s", err, verification)
	}
	securityAudit, err := run("security", "audit", "--root", root)
	if err != nil || !strings.Contains(securityAudit, `"secure":true`) || !strings.Contains(securityAudit, `"multiTenantIsolation":false`) || strings.Contains(securityAudit, root) {
		t.Fatalf("security audit is unhealthy or path-leaking: %v %s", err, securityAudit)
	}
	providers, err := run("provider", "list", "--root", root)
	if err != nil || !strings.Contains(providers, `"id":"manual"`) || !strings.Contains(providers, `"stability":"experimental"`) {
		t.Fatalf("provider inventory missing built-ins: %v %s", err, providers)
	}
	conformance, err := run("provider", "check", "--root", root)
	if err != nil || !strings.Contains(conformance, `"healthy":true`) {
		t.Fatalf("provider conformance failed: %v %s", err, conformance)
	}
}

func TestWorkerCanBindStartCheckpointFinishAndUnlockDependentNode(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{
  "apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"lifecycle"},
  "spec":{"roles":[{"id":"developer","capabilities":["node.run"]}],
    "nodes":[
      {"id":"A","kind":"task","role":"developer","title":"first","outcomes":[{"id":"success","class":"success"}]},
      {"id":"B","kind":"task","role":"developer","title":"second","outcomes":[{"id":"success","class":"success"}]}
    ],"edges":[{"id":"A-to-B","from":"A","to":"B","when":{"outcome":"success"}}]}}
`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &stdout, &stderr)
		if err != nil && stderr.Len() > 0 {
			t.Log(stderr.String())
		}
		return stdout.String(), err
	}
	if _, err := run("init", "--root", root, "--name", "lifecycle"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "developer", "--harness", "codex", "--session", "session-A", "--idempotency-key", "bind-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "developer", "--harness", "claude-code", "--session", "session-B", "--idempotency-key", "bind-B"); err == nil {
		t.Fatal("second live binding for the same stable role must fail")
	}

	startRef := allowedActionRef(t, run, root, "developer", "A", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", startRef, "--input", `{}`, "--idempotency-key", "start-A"); err != nil {
		t.Fatalf("start: %v", err)
	}
	checkpointRef := allowedActionRef(t, run, root, "developer", "A", "attempt.checkpoint")
	if _, err := run("action", "apply", "--root", root, "--ref", checkpointRef, "--input", `{"summary":"candidate prepared","evidenceRefs":[{"digest":"sha256:abc","type":"test-report","size":12}]}`, "--idempotency-key", "checkpoint-A"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	submitRef := allowedActionRef(t, run, root, "developer", "A", "attempt.submit")
	if _, err := run("action", "apply", "--root", root, "--ref", submitRef, "--input", `{}`, "--idempotency-key", "submit-A"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	finishRef := allowedActionRef(t, run, root, "developer", "A", "task.complete")
	if _, err := run("action", "apply", "--root", root, "--ref", finishRef, "--input", `{"outcome":"success"}`, "--idempotency-key", "finish-A"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	out, err := run("frontier", "--root", root, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"ready":["B"]`) {
		t.Fatalf("expected B ready after A succeeds, got %s", out)
	}
	context, err := run("context", "--root", root, "--view", "worker", "--role", "developer", "--node", "A", "--budget-bytes", "8192")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(context, `"summary":"candidate prepared"`) || !strings.Contains(context, `"outcome":"success"`) {
		t.Fatalf("successor context must contain checkpoint and terminal result, got %s", context)
	}
}

func TestExecutionEvidenceCanBePublishedInspectedAndReusedAcrossPolicyChanges(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"evidence"},"spec":{"roles":[{"id":"developer","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"developer","title":"A","objective":"build","outcomes":[{"id":"success","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	if _, err := run("init", "--root", root, "--name", "evidence"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "developer", "--harness", "codex", "--session", "evidence-session", "--idempotency-key", "bind-evidence"); err != nil {
		t.Fatal(err)
	}
	startRef := allowedActionRef(t, run, root, "developer", "A", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", startRef, "--idempotency-key", "start"); err != nil {
		t.Fatal(err)
	}
	protected := []map[string]string{{"name": "toolchain", "digest": cliDigest("d")}, {"name": "fixture", "digest": cliDigest("c")}}
	artifact := func(digest, artifactType string) map[string]any {
		return map[string]any{"digest": digest, "type": artifactType, "size": 12, "provenance": map[string]string{"producer": "codex", "revision": "1"}}
	}
	packageInput, _ := json.Marshal(map[string]any{
		"candidate": artifact(cliDigest("a"), "candidate"), "prospectiveTree": artifact(cliDigest("b"), "git-tree"),
		"commandGraphDigest": cliDigest("e"), "protectedInputs": protected,
		"observations": map[string]bool{"exact": true, "clean": true, "depthComplete": true, "sourceUnmodified": true, "resourcesClosed": true},
		"artifacts":    []map[string]any{artifact(cliDigest("f"), "test-report")},
	})
	publishRef := allowedActionRef(t, run, root, "developer", "A", "evidence.publish")
	published, err := run("action", "apply", "--root", root, "--ref", publishRef, "--input", string(packageInput), "--idempotency-key", "publish-package")
	if err != nil {
		t.Fatalf("publish evidence: %v", err)
	}
	var publishResult struct {
		ObjectRef string `json:"objectRef"`
	}
	if err := json.Unmarshal([]byte(published), &publishResult); err != nil || !strings.HasPrefix(publishResult.ObjectRef, "evidence-package:epkg_") {
		t.Fatalf("publish result lacks package ref: %v %s", err, published)
	}
	packageID := strings.TrimPrefix(publishResult.ObjectRef, "evidence-package:")
	listed, err := run("evidence", "list", "--root", root, "--node", "A")
	if err != nil || !strings.Contains(listed, packageID) {
		t.Fatalf("evidence index missing package: %v %s", err, listed)
	}
	inspectedPackage, err := run("inspect", "--root", root, publishResult.ObjectRef)
	if err != nil || !strings.Contains(inspectedPackage, cliDigest("a")) || strings.Contains(inspectedPackage, "prompt") {
		t.Fatalf("inspect package is incomplete or leaked forbidden content: %v %s", err, inspectedPackage)
	}

	reuseInput, _ := json.Marshal(map[string]any{
		"packageId": packageID, "policy": map[string]string{"id": "validator", "version": "2.0.0", "schemaHash": cliDigest("1")},
		"candidateDigest": cliDigest("a"), "prospectiveTreeDigest": cliDigest("b"), "commandGraphDigest": cliDigest("e"), "protectedInputs": protected,
	})
	reuseRef := allowedActionRef(t, run, root, "developer", "A", "evidence.assess-reuse")
	reused, err := run("action", "apply", "--root", root, "--ref", reuseRef, "--input", string(reuseInput), "--idempotency-key", "reuse-policy-v2")
	if err != nil {
		t.Fatalf("assess reuse: %v", err)
	}
	var reuseResult struct {
		ObjectRef string `json:"objectRef"`
	}
	if err := json.Unmarshal([]byte(reused), &reuseResult); err != nil || !strings.HasPrefix(reuseResult.ObjectRef, "reuse-decision:reuse_") {
		t.Fatalf("reuse result lacks decision ref: %v %s", err, reused)
	}
	decision, err := run("inspect", "--root", root, reuseResult.ObjectRef)
	if err != nil || !strings.Contains(decision, `"result":"reuse_execution"`) || !strings.Contains(decision, `"protected_core_unchanged"`) {
		t.Fatalf("policy-only change should reuse execution: %v %s", err, decision)
	}

	changedInput, _ := json.Marshal(map[string]any{
		"packageId": packageID, "policy": map[string]string{"id": "validator", "version": "3.0.0", "schemaHash": cliDigest("2")},
		"candidateDigest": cliDigest("9"), "prospectiveTreeDigest": cliDigest("b"), "commandGraphDigest": cliDigest("e"), "protectedInputs": protected,
	})
	changedRef := allowedActionRef(t, run, root, "developer", "A", "evidence.assess-reuse")
	changed, err := run("action", "apply", "--root", root, "--ref", changedRef, "--input", string(changedInput), "--idempotency-key", "reuse-policy-v3-changed-core")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(changed, `"objectRef":"reuse-decision:`) {
		t.Fatalf("changed-core decision ref missing: %s", changed)
	}
	context, err := run("context", "--root", root, "--view", "worker", "--role", "developer", "--node", "A", "--budget-bytes", "8192")
	if err != nil || !strings.Contains(context, `"result":"rerun_required"`) {
		t.Fatalf("worker context lacks latest reuse decision: %v %s", err, context)
	}
}

func allowedActionRef(t *testing.T, run func(...string) (string, error), root, role, node, kind string) string {
	t.Helper()
	out, err := run("action", "list", "--root", root, "--role", role, "--node", node)
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	var value struct {
		Actions []struct{ Kind, Ref string } `json:"actions"`
	}
	if err := json.Unmarshal([]byte(out), &value); err != nil {
		t.Fatalf("decode actions: %v", err)
	}
	for _, action := range value.Actions {
		if action.Kind == kind {
			return action.Ref
		}
	}
	t.Fatalf("action %s not found in %s", kind, out)
	return ""
}

func cliDigest(character string) string { return "sha256:" + strings.Repeat(character, 64) }

func TestGraphChangeRequiresImpactTokenAndProtectsActiveNodes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"changes"},"spec":{"roles":[{"id":"developer","capabilities":["node.run","graph.change"]}],"nodes":[{"id":"A","kind":"task","role":"developer","title":"A","outcomes":[{"id":"success","class":"success"}]},{"id":"B","kind":"task","role":"developer","title":"B","outcomes":[{"id":"success","class":"success"}]}],"edges":[{"id":"A-B","from":"A","to":"B","when":{"outcome":"success"}}]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(root, "patch.json")
	patch := `{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"updateRole","role":{"id":"developer","capabilities":["node.run","graph.change","incident.manage"]}},{"op":"addNode","node":{"id":"C","kind":"task","role":"developer","title":"C","outcomes":[{"id":"pass","class":"success"}]}},{"op":"addEdge","edge":{"id":"B-C","from":"B","to":"C","when":{"outcome":"success"}}}]}`
	if err := os.WriteFile(patchPath, []byte(patch), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	if _, err := run("init", "--root", root); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "developer", "--harness", "codex", "--session", "graph-session", "--idempotency-key", "bind-graph"); err != nil {
		t.Fatal(err)
	}
	preview, err := run("graph", "preview-change", "--root", root, "--file", patchPath)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	var impact struct{ Token, ProposedRevision string }
	if err := json.Unmarshal([]byte(preview), &impact); err != nil {
		t.Fatal(err)
	}
	if impact.Token == "" || impact.ProposedRevision == "" {
		t.Fatalf("preview must return a bound impact token: %s", preview)
	}
	if _, err := run("graph", "apply-change", "--root", root, "--file", patchPath, "--token", impact.Token, "--actor-role", "developer", "--idempotency-key", "patch-1"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	exported, err := run("graph", "export", "--root", root, "--format", "json")
	if err != nil || !strings.Contains(exported, `"id":"C"`) || !strings.Contains(exported, `"incident.manage"`) {
		t.Fatalf("exported graph lacks C: %v %s", err, exported)
	}
	if _, err := run("graph", "apply-change", "--root", root, "--file", patchPath, "--token", impact.Token, "--actor-role", "developer", "--idempotency-key", "patch-stale"); err == nil {
		t.Fatal("consumed/stale impact token must fail")
	}

	startRef := allowedActionRef(t, run, root, "developer", "A", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", startRef, "--idempotency-key", "start"); err != nil {
		t.Fatal(err)
	}
	updatePath := filepath.Join(root, "update-active.json")
	update := `{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"updateNode","node":{"id":"A","kind":"task","role":"developer","title":"changed","outcomes":[{"id":"success","class":"success"}]}}]}`
	if err := os.WriteFile(updatePath, []byte(update), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "preview-change", "--root", root, "--file", updatePath); err == nil {
		t.Fatal("active node contract must be frozen")
	}
}

func TestContextBudgetInspectAndPreWaitAreMachineDecidable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"context"},"spec":{"roles":[{"id":"developer","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"developer","title":"A","objective":"` + strings.Repeat("bounded-", 3000) + `","outcomes":[{"id":"success","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	if _, err := run("init", "--root", root); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph"); err != nil {
		t.Fatal(err)
	}
	audit, err := run("pre-wait", "--root", root)
	if err != nil || !strings.Contains(audit, `"safeToWait":false`) || !strings.Contains(audit, `"readyNodes":["A"]`) {
		t.Fatalf("ready work must reject passive wait: %v %s", err, audit)
	}
	context, err := run("context", "--root", root, "--view", "worker", "--node", "A", "--budget-bytes", "1024")
	if err != nil {
		t.Fatal(err)
	}
	if len(context) > 1025 || !strings.Contains(context, `"truncated":true`) {
		t.Fatalf("context must honor budget: %d %s", len(context), context)
	}
	if _, err := run("context", "--root", root, "--view", "worker", "--budget-bytes", "-1"); err == nil {
		t.Fatal("CLI accepted a negative context budget")
	}
	inspected, err := run("inspect", "--root", root, "node:A")
	if err != nil || !strings.Contains(inspected, `"objective":"bounded-`) {
		t.Fatalf("inspect must provide opt-in detail: %v %s", err, inspected)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "developer", "--harness", "codex", "--session", "s", "--idempotency-key", "bind"); err != nil {
		t.Fatal(err)
	}
	startRef := allowedActionRef(t, run, root, "developer", "A", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", startRef, "--idempotency-key", "start"); err != nil {
		t.Fatal(err)
	}
	audit, err = run("pre-wait", "--root", root)
	if err != nil || !strings.Contains(audit, `"safeToWait":true`) {
		t.Fatalf("running attempt permits bounded yield: %v %s", err, audit)
	}
	submitRef := allowedActionRef(t, run, root, "developer", "A", "attempt.submit")
	if _, err := run("action", "apply", "--root", root, "--ref", submitRef, "--idempotency-key", "submit"); err != nil {
		t.Fatal(err)
	}
	audit, err = run("pre-wait", "--root", root)
	if err != nil || !strings.Contains(audit, `"safeToWait":false`) || !strings.Contains(audit, `"submittedAttempts"`) {
		t.Fatalf("submitted work must advance before wait: %v %s", err, audit)
	}
}

func TestManualEffectRemainsUnknownUntilRecipientVisibleReceiptIsReconciled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect"},"spec":{"roles":[{"id":"orchestrator","capabilities":["effect.apply","effect.reconcile"]}],"nodes":[{"id":"deliver","kind":"effect","role":"orchestrator","title":"deliver handoff","inputs":{"adapter":"manual","request":{"instruction":"Deliver work package to reviewer"}},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	if _, err := run("init", "--root", root); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "orchestrator", "--harness", "codex", "--session", "central", "--idempotency-key", "bind"); err != nil {
		t.Fatal(err)
	}
	startRef := allowedActionRef(t, run, root, "orchestrator", "deliver", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", startRef, "--idempotency-key", "start"); err != nil {
		t.Fatal(err)
	}
	prepareRef := allowedActionRef(t, run, root, "orchestrator", "deliver", "effect.prepare")
	prepared, err := run("action", "apply", "--root", root, "--ref", prepareRef, "--idempotency-key", "deliver-1")
	if err != nil {
		t.Fatalf("prepare effect: %v", err)
	}
	var result struct{ ActionID, Status string }
	if err := json.Unmarshal([]byte(prepared), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "unknown" || result.ActionID == "" {
		t.Fatalf("manual dispatch cannot self-confirm delivery: %s", prepared)
	}
	audit, err := run("pre-wait", "--root", root)
	if err != nil || !strings.Contains(audit, `"pendingEffects"`) || !strings.Contains(audit, `"safeToWait":false`) {
		t.Fatalf("unknown effect must block dependent control action: %v %s", err, audit)
	}
	if _, err := run("reconcile", "--root", root, "--action", result.ActionID, "--receipt", `{"externalId":"receipt-1","recipientVisible":true}`, "--idempotency-key", "reconcile-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	inspected, err := run("inspect", "--root", root, "effect:"+result.ActionID)
	if err != nil || !strings.Contains(inspected, `"status":"confirmed"`) {
		t.Fatalf("effect should be confirmed from visible receipt: %v %s", err, inspected)
	}
	if ref := allowedActionRef(t, run, root, "orchestrator", "deliver", "effect.complete"); ref == "" {
		t.Fatal("confirmed effect should allow explicit terminal outcome")
	}
}

func TestSignatureCLIProducesPortableDetachedVerification(t *testing.T) {
	root := t.TempDir()
	payload := filepath.Join(root, "journal.ndjson")
	privateKey := filepath.Join(root, "private.pem")
	publicKey := filepath.Join(root, "public.pem")
	signature := filepath.Join(root, "journal.ndjson.sig.json")
	if err := os.WriteFile(payload, []byte("portable journal export\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	if output, err := run("signature", "keygen", "--private-key", privateKey, "--public-key", publicKey); err != nil || !strings.Contains(output, `"valid":true`) {
		t.Fatalf("keygen: %v %s", err, output)
	}
	if output, err := run("signature", "sign", "--file", payload, "--private-key", privateKey, "--output", signature); err != nil || !strings.Contains(output, `"payloadSha256":"sha256:`) {
		t.Fatalf("sign: %v %s", err, output)
	}
	if output, err := run("signature", "verify", "--file", payload, "--signature", signature, "--public-key", publicKey); err != nil || !strings.Contains(output, `"valid":true`) {
		t.Fatalf("verify: %v %s", err, output)
	}
}

func TestContractCLIReportsTheClosedMCPBetaSurface(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := cli.Run([]string{"contract"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"apiVersion":"dagrail.io/v1beta1"`) || !strings.Contains(out.String(), `"name":"dag_pre_wait"`) || !strings.Contains(out.String(), `"providerSdk":{"apiVersion":"dagrail.io/provider/v1alpha1"`) {
		t.Fatalf("unexpected compatibility contract: %s", out.String())
	}
}

func TestPluginBundleCanBeMaterializedWithoutAHostOrNetwork(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime-data"))
	var out, errOut bytes.Buffer
	if err := cli.Run([]string{"plugin", "materialize"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"status":"materialized"`) || !strings.Contains(out.String(), `"digest":"sha256:`) {
		t.Fatalf("unexpected bundle receipt: %s", out.String())
	}
	out.Reset()
	if err := cli.Run([]string{"plugin", "bundle-status"}, strings.NewReader(""), &out, &errOut); err != nil || !strings.Contains(out.String(), `"status":"verified"`) {
		t.Fatalf("bundle status: %v %s", err, out.String())
	}
}

func TestSupportCLIExportsOnceWithoutPrivateAuthority(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "private-runtime"))
	var out, errOut bytes.Buffer
	if err := cli.Run([]string{"init", "--root", root, "--name", "private-project"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "support.json")
	out.Reset()
	args := []string{"support", "export", "--root", root, "--output", output}
	if err := cli.Run(args, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(raw), `"apiVersion":"dagrail.io/support/v1alpha1"`) || strings.Contains(string(raw), root) || strings.Contains(string(raw), "private-project") {
		t.Fatalf("unsafe support export: %v %s", err, raw)
	}
	if err := cli.Run(args, strings.NewReader(""), &out, &errOut); err == nil {
		t.Fatal("support export overwrote an existing report")
	}
}

func TestRecoveryCLIEmitsSchemaBoundReadOnlyRehearsal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime-data"))
	var out, errOut bytes.Buffer
	if err := cli.Run([]string{"init", "--root", root, "--name", "recovery"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := cli.Run([]string{"recovery", "rehearse", "--root", root}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"apiVersion":"dagrail.io/recovery-rehearsal/v1alpha1"`) || !strings.Contains(out.String(), `"ready":true`) || !strings.Contains(out.String(), `"projectionEquivalent":true`) {
		t.Fatalf("unexpected recovery rehearsal: %s", out.String())
	}
}

func TestQualifyReleaseCLIDistinguishesCandidateFromProductionEvidence(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := cli.Run([]string{"qualify", "release", "--source", root}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"apiVersion":"dagrail.io/release-qualification/v1alpha1"`) || !strings.Contains(out.String(), `"structuralCandidate":true`) || !strings.Contains(out.String(), `"productionValidated":false`) {
		t.Fatalf("unexpected release qualification: %s", out.String())
	}
}

func TestQualifyReleaseCLIRejectsTrailingArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	err := cli.Run([]string{"qualify", "release", "unexpected"}, strings.NewReader(""), &out, &errOut)
	if err == nil || out.Len() != 0 {
		t.Fatalf("trailing qualification argument was not rejected: err=%v output=%s", err, out.String())
	}
}

func TestReleaseVerificationCLIEmitsPathFreeFailureEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-release-root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	err := cli.Run([]string{"release", "verify", "--directory", root}, strings.NewReader(""), &out, &errOut)
	if err == nil || !strings.Contains(out.String(), `"apiVersion":"dagrail.io/release-verification/v1alpha1"`) || !strings.Contains(out.String(), `"verified":false`) || strings.Contains(out.String(), root) || strings.Contains(out.String(), "private-release-root") {
		t.Fatalf("unexpected release verification failure: err=%v output=%s", err, out.String())
	}
}

func TestReleaseManifestCLIRejectsVersionDriftAndTrailingArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	err := cli.Run([]string{"release", "manifest", "--version", "99.0.0", "--commit", "0123456789abcdef0123456789abcdef01234567", "--source-date-epoch", "1786665600"}, strings.NewReader(""), &out, &errOut)
	if err == nil || out.Len() != 0 {
		t.Fatalf("release version drift was accepted: err=%v output=%s", err, out.String())
	}
	err = cli.Run([]string{"release", "verify", "unexpected"}, strings.NewReader(""), &out, &errOut)
	if err == nil {
		t.Fatal("release CLI accepted a trailing argument")
	}
	err = cli.Run([]string{"release", "verify", "--commit", "0123456789abcdef0123456789abcdef01234567"}, strings.NewReader(""), &out, &errOut)
	if err == nil {
		t.Fatal("release verification accepted a manifest-only flag")
	}
}

func TestObserveCLIRecordsOnlyAnIsolatedShadow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime-data"))
	source := filepath.Join(root, "existing-project")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "requirements.md"), []byte("requirement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "converted.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"shadow"},"spec":{"roles":[{"id":"dev","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"dev","title":"A","outcomes":[{"id":"ok","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	shadow := filepath.Join(root, "shadow")
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	assessed, err := run("observe", "assess", "--source-root", source, "--graph", graphPath, "--authority", "requirements.md")
	if err != nil || !strings.Contains(assessed, `"kind":"ObservationSnapshot"`) {
		t.Fatalf("assess: %v %s", err, assessed)
	}
	created, err := run("observe", "create-shadow", "--source-root", source, "--graph", graphPath, "--shadow-root", shadow, "--authority", "requirements.md")
	if err != nil || !strings.Contains(created, `"status":"created"`) {
		t.Fatalf("create shadow: %v %s", err, created)
	}
	verified, err := run("observe", "verify-shadow", "--shadow-root", shadow)
	if err != nil || !strings.Contains(verified, `"valid":true`) {
		t.Fatalf("verify shadow: %v %s", err, verified)
	}
	if _, err := os.Stat(filepath.Join(source, ".dagrail")); !os.IsNotExist(err) {
		t.Fatalf("observe CLI wrote into source: %v", err)
	}
}
