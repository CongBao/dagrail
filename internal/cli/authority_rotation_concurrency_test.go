package cli_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/cli"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/service"
	"github.com/gowebpki/jcs"
)

const authorityRotationHelperEnv = "DAGRAIL_TEST_AUTHORITY_ROTATION_ARGS"
const authorityRotationReleaseEnv = "DAGRAIL_TEST_AUTHORITY_ROTATION_RELEASE"

type rotationChild struct {
	command *exec.Cmd
	stdout  bytes.Buffer
	stderr  bytes.Buffer
	err     error
}

type legacyUnsignedSegment struct {
	SchemaVersion int             `json:"schemaVersion"`
	Sequence      uint64          `json:"sequence"`
	ProjectID     string          `json:"projectId"`
	PreviousHash  string          `json:"previousHash"`
	Command       journal.Command `json:"command"`
	Events        []journal.Event `json:"events"`
	CommittedAt   string          `json:"committedAt"`
}

type legacyStoredSegment struct {
	legacyUnsignedSegment
	SegmentHash string `json:"segmentHash"`
}

func TestAuthorityRotationCLIHelper(t *testing.T) {
	raw := os.Getenv(authorityRotationHelperEnv)
	if raw == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		os.Exit(125)
	}
	release := os.Getenv(authorityRotationReleaseEnv)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(release); err == nil {
			break
		}
		if time.Now().After(deadline) {
			os.Exit(124)
		}
		time.Sleep(time.Millisecond)
	}
	if err := cli.Run(args, strings.NewReader(""), os.Stdout, os.Stderr); err != nil {
		_ = cli.WriteErrorJSON(os.Stderr, err)
		os.Exit(cli.DescribeError(err).ExitCode)
	}
	os.Exit(0)
}

func TestIdenticalCrossProcessAuthorityRotationsReturnOneReceipt(t *testing.T) {
	if testing.Short() {
		t.Skip("100 cross-process authority rotation races")
	}
	previousHome := os.Getenv("DAGRAIL_HOME")
	defer func() { _ = os.Setenv("DAGRAIL_HOME", previousHome) }()
	for iteration := 0; iteration < 100; iteration++ {
		root := t.TempDir()
		dataRoot := filepath.Join(root, "data")
		if err := os.Setenv("DAGRAIL_HOME", dataRoot); err != nil {
			t.Fatal(err)
		}
		svc, err := service.Init(root, "authority-concurrency")
		if err != nil {
			t.Fatal(err)
		}
		graphPath := filepath.Join(root, "graph.json")
		if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"authority-concurrency"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
			t.Fatal(err)
		}
		backupPath := filepath.Join(root, "backup.json")
		backup, report, err := svc.CreateBackup()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(backupPath, backup, 0o600); err != nil {
			t.Fatal(err)
		}
		args := []string{"recovery", "rotate-authority", "--root", root, "--backup", backupPath, "--expected-current-head", report.HeadHash, "--reason", "concurrent recovery", "--idempotency-key", "rotate/concurrent"}
		children := runConcurrentRotationCommands(t, dataRoot, filepath.Join(root, "release"), args, args)
		receipts := make([]service.AuthorityRotationReceipt, len(children))
		for index, current := range children {
			if current.err != nil {
				t.Fatalf("iteration %d process %d failed: %v stderr=%s", iteration, index, current.err, current.stderr.String())
			}
			if err := json.Unmarshal(current.stdout.Bytes(), &receipts[index]); err != nil {
				t.Fatalf("iteration %d process %d returned invalid receipt: %v stdout=%s", iteration, index, err, current.stdout.String())
			}
		}
		if receipts[0].ReceiptDigest != receipts[1].ReceiptDigest || receipts[0].ReplacementProjectID != receipts[1].ReplacementProjectID {
			t.Fatalf("iteration %d returned different receipts: first=%+v second=%+v", iteration, receipts[0], receipts[1])
		}
	}
}

func TestIdenticalCrossProcessLegacyAdoptionsReturnOneReceipt(t *testing.T) {
	if testing.Short() {
		t.Skip("25 cross-process legacy authority adoption races")
	}
	previousHome := os.Getenv("DAGRAIL_HOME")
	defer func() { _ = os.Setenv("DAGRAIL_HOME", previousHome) }()
	for iteration := 0; iteration < 25; iteration++ {
		root := t.TempDir()
		dataRoot := filepath.Join(root, "data")
		if err := os.Setenv("DAGRAIL_HOME", dataRoot); err != nil {
			t.Fatal(err)
		}
		svc, err := service.Init(root, "legacy-adoption-concurrency")
		if err != nil {
			t.Fatal(err)
		}
		graphPath := filepath.Join(root, "graph.json")
		if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"legacy-adoption-concurrency"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"work","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
			t.Fatal(err)
		}
		previousHead := rewriteCurrentFixtureAsLegacyAuthority(t, svc)
		args := []string{"recovery", "adopt-legacy-authority", "--root", root, "--expected-project-id", svc.Project.Config.ProjectID, "--expected-current-head", previousHead, "--reason", "concurrent legacy adoption", "--idempotency-key", "adopt/concurrent"}
		children := runConcurrentRotationCommands(t, dataRoot, filepath.Join(root, "release"), args, args)
		receipts := make([]service.AuthorityAdoptionReceipt, len(children))
		for index, current := range children {
			if current.err != nil {
				t.Fatalf("iteration %d process %d failed: %v stderr=%s", iteration, index, current.err, current.stderr.String())
			}
			if err := json.Unmarshal(current.stdout.Bytes(), &receipts[index]); err != nil {
				t.Fatalf("iteration %d process %d returned invalid receipt: %v stdout=%s", iteration, index, err, current.stdout.String())
			}
		}
		if receipts[0].ReceiptDigest != receipts[1].ReceiptDigest || receipts[0].ReplacementProjectID != receipts[1].ReplacementProjectID {
			t.Fatalf("iteration %d returned different adoption receipts: first=%+v second=%+v", iteration, receipts[0], receipts[1])
		}
	}
}

func TestDifferentConcurrentAuthorityRotationIntentsFailClosed(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	t.Setenv("DAGRAIL_HOME", dataRoot)
	svc, err := service.Init(root, "authority-conflict")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"authority-conflict"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(root, "backup.json")
	backup, report, err := svc.CreateBackup()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, backup, 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--errors=json", "recovery", "rotate-authority", "--root", root, "--backup", backupPath, "--expected-current-head", report.HeadHash}
	first := append(append([]string{}, base...), "--reason", "first intent", "--idempotency-key", "rotate/first")
	second := append(append([]string{}, base...), "--reason", "second intent", "--idempotency-key", "rotate/second")
	children := runConcurrentRotationCommands(t, dataRoot, filepath.Join(root, "release"), first, second)
	succeeded, conflicted := 0, 0
	for _, child := range children {
		if child.err == nil {
			var receipt service.AuthorityRotationReceipt
			if err := json.Unmarshal(child.stdout.Bytes(), &receipt); err != nil || service.VerifyAuthorityRotationReceipt(receipt) != nil {
				t.Fatalf("successful process returned invalid receipt: %v %s", err, child.stdout.String())
			}
			succeeded++
			continue
		}
		var report cli.ErrorReport
		if err := json.Unmarshal(child.stderr.Bytes(), &report); err != nil || report.Code != "operation_failed" || !strings.Contains(report.Message, "different intent") {
			t.Fatalf("losing process did not return a stable typed conflict: err=%v stderr=%s", child.err, child.stderr.String())
		}
		conflicted++
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("different intents did not resolve one-success/one-conflict: success=%d conflict=%d", succeeded, conflicted)
	}
}

func runConcurrentRotationCommands(t *testing.T, dataRoot, release string, commands ...[]string) []*rotationChild {
	t.Helper()
	children := make([]*rotationChild, len(commands))
	for index, args := range commands {
		rawArgs, _ := json.Marshal(args)
		current := &rotationChild{command: exec.Command(os.Args[0], "-test.run=^TestAuthorityRotationCLIHelper$")}
		current.command.Env = append(os.Environ(), "DAGRAIL_HOME="+dataRoot, authorityRotationHelperEnv+"="+string(rawArgs), authorityRotationReleaseEnv+"="+release)
		current.command.Stdout = &current.stdout
		current.command.Stderr = &current.stderr
		if err := current.command.Start(); err != nil {
			t.Fatal(err)
		}
		children[index] = current
	}
	if err := os.WriteFile(release, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, current := range children {
		current.err = current.command.Wait()
	}
	return children
}

func rewriteCurrentFixtureAsLegacyAuthority(t *testing.T, svc *service.Service) string {
	t.Helper()
	segments, err := svc.Journal.ReadAll()
	if err != nil || len(segments) != 2 || segments[0].Command.Kind != "authority.establish" || segments[1].Command.Kind != "graph.import" {
		t.Fatalf("unexpected current fixture before legacy rewrite: %#v %v", segments, err)
	}
	graph := segments[1]
	unsigned := legacyUnsignedSegment{
		SchemaVersion: graph.SchemaVersion,
		Sequence:      1,
		ProjectID:     graph.ProjectID,
		Command:       graph.Command,
		Events:        graph.Events,
		CommittedAt:   graph.CommittedAt,
	}
	raw, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("dagrail-journal-v1\x00"))
	_, _ = hasher.Write(canonical)
	head := hex.EncodeToString(hasher.Sum(nil))
	storedRaw, err := json.Marshal(legacyStoredSegment{legacyUnsignedSegment: unsigned, SegmentHash: head})
	if err != nil {
		t.Fatal(err)
	}
	storedRaw, err = jcs.Transform(storedRaw)
	if err != nil {
		t.Fatal(err)
	}
	journalDir := filepath.Join(svc.Project.DataDir, "journal")
	entries, err := os.ReadDir(journalDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			if err := os.Remove(filepath.Join(journalDir, entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	path := filepath.Join(journalDir, "000000000001-"+head+".json")
	if err := os.WriteFile(path, storedRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(svc.Project.DataDir, "authority-claim.json")); err != nil {
		t.Fatal(err)
	}
	authorityHome := os.Getenv("DAGRAIL_TEST_AUTHORITY_HOME")
	if authorityHome == "" {
		t.Fatal("isolated authority home is not configured")
	}
	if err := os.Remove(filepath.Join(authorityHome, "anchors", svc.Project.Config.ProjectID+".json")); err != nil {
		t.Fatal(err)
	}
	return head
}
