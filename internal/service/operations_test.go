package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/project"
	"github.com/google/uuid"
	"github.com/gowebpki/jcs"
)

func TestInitEstablishesAuthorityBeforePublishingUsableProject(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "established")
	if err != nil {
		t.Fatal(err)
	}
	segments, err := svc.VerifyJournal()
	if err != nil || len(segments) != 1 {
		t.Fatalf("new authority must contain exactly one bootstrap fence: %#v %v", segments, err)
	}
	segment := segments[0]
	if segment.SchemaVersion != journal.AuthorityFenceSchemaVersion || segment.Sequence != 1 || segment.PreviousHash != "" || segment.Command.Kind != "authority.establish" || len(segment.Events) != 1 || segment.Events[0].Type != "authority.established" {
		t.Fatalf("new authority bootstrap is not a closed schema-4 fence: %#v", segment)
	}
	var establishment authorityEstablishment
	if err := decodeStrictAuthorityJSON(segment.Events[0].Payload, &establishment); err != nil || establishment.ProjectID != svc.Project.Config.ProjectID || establishment.Operation != "initialization" || establishment.EstablishedAt != segment.CommittedAt || establishment.ProvenanceDigest == "" {
		t.Fatalf("new authority establishment is not bound to its project: %#v %v", establishment, err)
	}
	state, err := svc.State()
	if err != nil || state.HeadSequence != 1 || state.Graph != nil {
		t.Fatalf("new authority state is not fence-only: head=%d graph=%#v err=%v", state.HeadSequence, state.Graph, err)
	}
}

func TestMissingAuthorityEstablishmentFenceFailsClosed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "missing-establishment")
	if err != nil {
		t.Fatal(err)
	}
	segmentPaths, err := filepath.Glob(filepath.Join(svc.Project.DataDir, "journal", "*.json"))
	if err != nil || len(segmentPaths) != 1 {
		t.Fatalf("locate establishment segment: %#v %v", segmentPaths, err)
	}
	if err := os.Remove(segmentPaths[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err == nil || !strings.Contains(err.Error(), "establishment") {
		t.Fatalf("ordinary open accepted a claimed authority without its establishment fence: %v", err)
	}
	if _, err := Init(root, "missing-establishment"); err == nil || !strings.Contains(err.Error(), "establishment") {
		t.Fatalf("init silently healed a claimed authority without its establishment fence: %v", err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"missing-establishment"},"spec":{"roles":[],"nodes":[],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "must-not-write", "governor"); err == nil || !strings.Contains(err.Error(), "fence") {
		t.Fatalf("already-open service wrote after establishment loss: %v", err)
	}
	remaining, err := filepath.Glob(filepath.Join(svc.Project.DataDir, "journal", "*.json"))
	if err != nil || len(remaining) != 0 {
		t.Fatalf("failed-closed retries changed the empty journal: %#v %v", remaining, err)
	}
}

func TestMissingReplacementEstablishmentFenceFailsClosed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "missing-replacement-establishment")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"missing-replacement-establishment"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"work","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	backup, report, err := svc.CreateBackup()
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := svc.RotateAuthority(backup, report.HeadHash, "test missing replacement fence", "rotate/missing-fence")
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := OpenForRecovery(root)
	if err != nil || replacement.Project.Config.ProjectID != receipt.ReplacementProjectID {
		t.Fatalf("open replacement inspection: %+v %v", replacement, err)
	}
	segmentPaths, err := filepath.Glob(filepath.Join(replacement.Project.DataDir, "journal", "*.json"))
	if err != nil || len(segmentPaths) != 1 {
		t.Fatalf("locate replacement establishment segment: %#v %v", segmentPaths, err)
	}
	if err := os.Remove(segmentPaths[0]); err != nil {
		t.Fatal(err)
	}
	if err := project.ValidateAuthorityClaim(replacement.Project.DataDir, receipt.ReplacementProjectID); err != nil {
		t.Fatalf("test did not preserve the replacement claim and lineage: %v", err)
	}
	if _, err := Open(root); err == nil || !strings.Contains(err.Error(), "establishment") {
		t.Fatalf("ordinary open accepted a replacement without its establishment fence: %v", err)
	}
	if _, err := Init(root, "missing-replacement-establishment"); err == nil || !strings.Contains(err.Error(), "establishment") {
		t.Fatalf("init silently healed a replacement without its establishment fence: %v", err)
	}
}

func TestBackupRestoreStatusAndBoundedHistory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "operations")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"ops"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}

	status, err := svc.Status()
	if err != nil || status.HeadSequence != 3 || status.Nodes["terminal"] != 1 {
		t.Fatalf("unexpected status: %+v %v", status, err)
	}
	history, err := svc.History(0, 1)
	if err != nil || len(history.Entries) != 1 || !history.Truncated || history.NextCursor != 1 {
		t.Fatalf("unexpected history: %+v %v", history, err)
	}
	encoded, _ := jsonString(history)
	if strings.Contains(encoded, "graphRevision\"") || strings.Contains(encoded, "payload") {
		t.Fatalf("bounded history leaked event payload: %s", encoded)
	}

	backup, report, err := svc.CreateBackup()
	if err != nil || report.Segments != 3 || report.Digest == "" {
		t.Fatalf("create backup: %+v %v", report, err)
	}
	if verified, err := svc.VerifyBackup(backup); err != nil || verified.Digest != report.Digest {
		t.Fatalf("verify backup: %+v %v", verified, err)
	}
	corrupt := append([]byte(nil), backup...)
	index := strings.Index(string(corrupt), `"digest":"sha256:`)
	if index < 0 {
		t.Fatal("backup digest missing")
	}
	digestByte := index + len(`"digest":"sha256:`)
	if corrupt[digestByte] == '0' {
		corrupt[digestByte] = '1'
	} else {
		corrupt[digestByte] = '0'
	}
	if _, err := svc.VerifyBackup(corrupt); err == nil {
		t.Fatal("corrupt backup was accepted")
	}

	entries, err := os.ReadDir(filepath.Join(svc.Project.DataDir, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			if err := os.Remove(filepath.Join(svc.Project.DataDir, "journal", entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	restored, err := svc.RestoreBackup(backup)
	if err != nil || restored.HeadSequence != 3 {
		t.Fatalf("restore backup: %+v %v", restored, err)
	}
	if _, err := svc.RestoreBackup(backup); err != nil {
		t.Fatalf("prefix-idempotent restore failed: %v", err)
	}
	state, err := svc.State()
	if err != nil || state.Nodes["done"].Status != "terminal" {
		t.Fatalf("restored state mismatch: %+v %v", state.Nodes["done"], err)
	}
}

func TestAuthorityRotationPreservesOldJournalAndCreatesFenceOnlyReplacement(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "rotation")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"rotation"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	backup, backupReport, err := svc.CreateBackup()
	if err != nil {
		t.Fatal(err)
	}
	oldID, oldDataDir := svc.Project.Config.ProjectID, svc.Project.DataDir
	svc.Now = func() time.Time { return time.Date(2026, 8, 16, 1, 2, 3, 0, time.UTC) }
	receipt, err := svc.RotateAuthority(backup, backupReport.HeadHash, "replace contaminated local authority", "rotate/1")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.PreviousProjectID != oldID || receipt.ReplacementProjectID == oldID || receipt.RecoveryHead != backupReport.HeadHash || receipt.ReceiptDigest == "" {
		t.Fatalf("unexpected rotation receipt: %+v", receipt)
	}
	validateLifecycleSchema(t, filepath.Join("..", "..", "schemas", "authority-rotation-v1alpha1.schema.json"), "urn:dagrail:authority-rotation", receipt)
	if err := VerifyAuthorityRotationReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	tampered := receipt
	tampered.Reason = "different reason"
	if err := VerifyAuthorityRotationReceipt(tampered); err == nil {
		t.Fatal("tampered authority rotation receipt was accepted")
	}
	structurallyInvalid := receipt
	structurallyInvalid.PreviousProjectID = "not-a-uuid"
	structurallyInvalid.ReceiptDigest = ""
	invalidDigest, err := authorityDigest("dagrail-authority-rotation-receipt-v1\x00", structurallyInvalid)
	if err != nil {
		t.Fatal(err)
	}
	structurallyInvalid.ReceiptDigest = invalidDigest
	if err := VerifyAuthorityRotationReceipt(structurallyInvalid); err == nil {
		t.Fatal("self-consistent but structurally invalid authority receipt was accepted")
	}
	oldEntries, err := os.ReadDir(filepath.Join(oldDataDir, "journal"))
	if err != nil || len(oldEntries) == 0 {
		t.Fatalf("old journal was not preserved: %v %d", err, len(oldEntries))
	}
	if _, err := svc.Journal.Append(journal.Command{ID: "late", Kind: "late", IdempotencyKey: "late"}, []journal.Event{{Type: "late", Payload: json.RawMessage(`{}`)}}, svc.Now()); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("old authority accepted a post-rotation write: %v", err)
	}
	if err := os.Remove(filepath.Join(oldDataDir, "authority-retired.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Journal.Append(journal.Command{ID: "late-after-marker-loss", Kind: "late", IdempotencyKey: "late-after-marker-loss"}, []journal.Event{{Type: "late", Payload: json.RawMessage(`{}`)}}, svc.Now()); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("retirement fence was revivable by deleting its sidecar: %v", err)
	}
	if _, exists, err := svc.Journal.AuthorityRetirement(); err != nil || !exists {
		t.Fatalf("journal retirement fence did not reconstruct missing sidecar evidence: exists=%v err=%v", exists, err)
	}
	replacement, err := OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := replacement.State()
	if err != nil || state.ProjectID != receipt.ReplacementProjectID || state.HeadSequence != 1 || state.Graph != nil {
		t.Fatalf("replacement authority is not fence-only and lineage-bound: %+v %v", state, err)
	}
	idempotentInit, err := Init(root, "rotation")
	if err != nil || idempotentInit.Project.Config.ProjectID != receipt.ReplacementProjectID {
		t.Fatalf("init did not reopen the rotated authority idempotently: %+v %v", idempotentInit, err)
	}
	idempotentState, err := idempotentInit.State()
	if err != nil || idempotentState.HeadHash != state.HeadHash || idempotentState.HeadSequence != state.HeadSequence {
		t.Fatalf("init changed the rotated authority: before=%+v after=%+v err=%v", state, idempotentState, err)
	}
	retried, err := replacement.RotateAuthority(backup, backupReport.HeadHash, "replace contaminated local authority", "rotate/1")
	if err != nil || retried.ReceiptDigest != receipt.ReceiptDigest {
		t.Fatalf("rotation retry changed receipt: %+v %v", retried, err)
	}
	if _, err := replacement.RotateAuthority(backup, backupReport.HeadHash, "changed intent", "rotate/1"); err == nil || !strings.Contains(err.Error(), "different intent") {
		t.Fatalf("rotation retry accepted changed intent: %v", err)
	}
}

func TestAuthorityRotationResumesAfterRetirementBeforeLocatorCommit(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, ".data")
	t.Setenv("DAGRAIL_HOME", dataRoot)
	svc, err := Init(root, "rotation-resume")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"rotation-resume"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	backup, report, err := svc.CreateBackup()
	if err != nil {
		t.Fatal(err)
	}
	key := "rotate/crash-window"
	previousUUID := uuid.MustParse(svc.Project.Config.ProjectID)
	replacementID := uuid.NewSHA1(previousUUID, []byte("dagrail-authority-rotation-v1\x00"+key)).String()
	blockedTarget := filepath.Join(dataRoot, "projects", replacementID)
	if err := os.MkdirAll(filepath.Dir(blockedTarget), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockedTarget, []byte("injected replacement-path fault"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc.Now = func() time.Time { return time.Date(2026, 8, 16, 2, 3, 4, 0, time.UTC) }
	if _, err := svc.RotateAuthority(backup, report.HeadHash, "resume prepared rotation", key); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("prepared rotation did not stop at locator failure: %v", err)
	}
	if _, err := svc.Journal.Append(journal.Command{ID: "late", Kind: "late", IdempotencyKey: "late"}, []journal.Event{{Type: "late", Payload: json.RawMessage(`{}`)}}, svc.Now()); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("prepared retirement did not fail closed: %v", err)
	}
	if err := os.Remove(blockedTarget); err != nil {
		t.Fatal(err)
	}
	receipt, err := svc.RotateAuthority(backup, report.HeadHash, "resume prepared rotation", key)
	if err != nil || receipt.ReplacementProjectID != replacementID {
		t.Fatalf("prepared rotation did not resume: %+v %v", receipt, err)
	}
}

func TestFreshRotationRetryReconfirmsLocatorDurability(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "data"))
	svc, err := Init(root, "rotation-locator-sync")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"rotation-locator-sync"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	backup, report, err := svc.CreateBackup()
	if err != nil {
		t.Fatal(err)
	}
	svc.ConfirmLocator = func(string) error { return os.ErrClosed }
	if _, err := svc.RotateAuthority(backup, report.HeadHash, "confirm locator durability", "rotate/locator-sync"); err == nil {
		t.Fatal("rotation reported success after locator durability confirmation failed")
	}
	fresh, err := OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	fresh.ConfirmLocator = func(string) error { return os.ErrPermission }
	if _, err := fresh.RotateAuthority(backup, report.HeadHash, "confirm locator durability", "rotate/locator-sync"); err == nil {
		t.Fatal("fresh retry ignored a repeated locator sync failure")
	}
	fresh.ConfirmLocator = project.SyncProjectLocator
	receipt, err := fresh.RotateAuthority(backup, report.HeadHash, "confirm locator durability", "rotate/locator-sync")
	if err != nil || receipt.ReplacementProjectID != fresh.Project.Config.ProjectID {
		t.Fatalf("fresh retry did not reconfirm locator durability: %+v %v", receipt, err)
	}
}

func TestAuthorityRelocationResumesAfterLocatorConfirmationFailure(t *testing.T) {
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	sourceHome := filepath.Join(t.TempDir(), "source-data")
	destinationHome := filepath.Join(t.TempDir(), "destination-data")
	t.Setenv("DAGRAIL_HOME", sourceHome)
	seed, err := Init(sourceRoot, "relocation-resume")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(sourceRoot, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"relocation-resume"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"work","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	legacyProjectID := seed.Project.Config.ProjectID
	legacyHead := simulatePreV022Authority(t, seed, testAuthorityHome(t))
	if err := os.MkdirAll(filepath.Join(targetRoot, ".dagrail"), 0o700); err != nil {
		t.Fatal(err)
	}
	locator, err := os.ReadFile(filepath.Join(sourceRoot, ".dagrail", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, ".dagrail", "project.yaml"), locator, 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := OpenForRecovery(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.AdoptLegacyAuthority(legacyProjectID, legacyHead, "temporary establishment", "adopt/temporary"); err != nil {
		t.Fatal(err)
	}
	source, err := Open(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ImportGraph(graphPath, "replacement-graph", "governor"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.BindRole("worker", "manual", "relocation-session", time.Hour, false, "relocation/bind"); err != nil {
		t.Fatal(err)
	}
	activeBackup, activeReport, err := source.CreateBackup()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("DAGRAIL_HOME", destinationHome); err != nil {
		t.Fatal(err)
	}
	if _, err := RelocateAuthority(targetRoot, activeBackup, legacyProjectID, activeReport.HeadHash, "must reject active lease", "relocate/active"); err == nil || !strings.Contains(err.Error(), "Role leases") {
		t.Fatalf("relocation accepted an active Role lease: %v", err)
	}
	if _, retired, err := source.Journal.AuthorityRetirement(); err != nil || retired {
		t.Fatalf("rejected active relocation changed source retirement: retired=%t err=%v", retired, err)
	}
	if err := source.ReleaseRole("worker", "relocation-session", "relocation/release"); err != nil {
		t.Fatal(err)
	}
	backup, report, err := source.CreateBackup()
	if err != nil {
		t.Fatal(err)
	}
	const reason = "resume relocation after locator confirmation"
	const key = "relocate/resume"
	if _, err := relocateAuthority(targetRoot, backup, legacyProjectID, report.HeadHash, reason, key, func(string) error { return os.ErrPermission }); err == nil {
		t.Fatal("relocation reported success after locator confirmation failure")
	}
	visible, err := project.Open(targetRoot)
	if err != nil || visible.Config.ProjectID == legacyProjectID {
		t.Fatalf("relocation did not reach the durable visible-locator prefix: %+v %v", visible, err)
	}
	advanced, err := Open(targetRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := advanced.ImportGraph(graphPath, "relocation/advanced-graph", "governor"); err != nil {
		t.Fatalf("replacement could not legally advance after locator publication: %v", err)
	}
	receipt, err := RelocateAuthority(targetRoot, backup, legacyProjectID, report.HeadHash, reason, key)
	if err != nil || receipt.ReplacementProjectID != visible.Config.ProjectID || VerifyAuthorityRelocationReceipt(receipt) != nil {
		t.Fatalf("exact retry did not finish relocation: %+v %v", receipt, err)
	}
	validateLifecycleSchema(t, filepath.Join("..", "..", "schemas", "authority-relocation-v1alpha1.schema.json"), "urn:dagrail:authority-relocation", receipt)
	otherTarget := t.TempDir()
	if err := os.MkdirAll(filepath.Join(otherTarget, ".dagrail"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherTarget, ".dagrail", "project.yaml"), locator, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RelocateAuthority(otherTarget, backup, legacyProjectID, report.HeadHash, reason, key); err == nil || !strings.Contains(err.Error(), "different intent") {
		t.Fatalf("same relocation intent was reusable at another target root: %v", err)
	}
	other, err := project.Open(otherTarget)
	if err != nil || other.Config.ProjectID != legacyProjectID {
		t.Fatalf("rejected second target changed its locator: %+v %v", other, err)
	}
	if err := os.Setenv("DAGRAIL_HOME", filepath.Join(t.TempDir(), "different-destination-data")); err != nil {
		t.Fatal(err)
	}
	if _, err := RelocateAuthority(targetRoot, backup, legacyProjectID, report.HeadHash, reason, key); err == nil {
		t.Fatal("completed relocation was reusable from a different destination runtime")
	}
	if err := os.Setenv("DAGRAIL_HOME", destinationHome); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(destinationHome, "projects", receipt.ReplacementProjectID, "authority-lineage.json")); err != nil {
		t.Fatal(err)
	}
	retried, err := RelocateAuthority(targetRoot, backup, legacyProjectID, report.HeadHash, reason, key)
	if err != nil || retried.ReceiptDigest != receipt.ReceiptDigest {
		t.Fatalf("completed relocation retry did not reconstruct exact lineage: %+v %v", retried, err)
	}
}

func TestAuthorityRotationRejectsNonPrefixBackup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "rotation-reject")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"rotation-reject"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"work","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph/original", "governor"); err != nil {
		t.Fatal(err)
	}
	backup, report, err := svc.CreateBackup()
	if err != nil || report.HeadSequence != 2 {
		t.Fatalf("unexpected source backup: %+v %v", report, err)
	}
	segments, err := svc.Journal.ReadAll()
	if err != nil || len(segments) != 2 {
		t.Fatalf("read source authority: %#v %v", segments, err)
	}
	originalGraphSegments, err := filepath.Glob(filepath.Join(svc.Project.DataDir, "journal", "000000000002-*.json"))
	if err != nil || len(originalGraphSegments) != 1 {
		t.Fatalf("locate original branch segment: %#v %v", originalGraphSegments, err)
	}
	if err := os.Remove(originalGraphSegments[0]); err != nil {
		t.Fatal(err)
	}
	expected := segments[0].SegmentHash
	alternative, _, err := svc.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "graph.import", ActorRole: "governor", IdempotencyKey: "graph/alternative", ObjectRef: segments[1].Command.ObjectRef, RequestDigest: segments[1].Command.RequestDigest}, segments[1].Events, time.Now().UTC(), &expected)
	if err != nil {
		t.Fatal(err)
	}
	if alternative.SegmentHash == report.HeadHash {
		t.Fatal("alternate branch did not produce a distinct authenticated head")
	}
	if _, err := svc.RotateAuthority(backup, alternative.SegmentHash, "reject non-prefix backup", "rotate/reject"); err == nil || !strings.Contains(err.Error(), "not a prefix") {
		t.Fatalf("non-prefix recovery point was accepted: %v", err)
	}
}

func TestAuthorityCanRotateAcrossMultipleGenerations(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "multi-rotation")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"multi-rotation"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph/a", "governor"); err != nil {
		t.Fatal(err)
	}
	backupA, reportA, err := svc.CreateBackup()
	if err != nil {
		t.Fatal(err)
	}
	receiptAB, err := svc.RotateAuthority(backupA, reportA.HeadHash, "rotate A to B", "rotate/a-b")
	if err != nil {
		t.Fatal(err)
	}
	serviceB, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serviceB.ImportGraph(graphPath, "graph/b", "governor"); err != nil {
		t.Fatal(err)
	}
	backupB, reportB, err := serviceB.CreateBackup()
	if err != nil {
		t.Fatal(err)
	}
	receiptBC, err := serviceB.RotateAuthority(backupB, reportB.HeadHash, "rotate B to C", "rotate/b-c")
	if err != nil {
		t.Fatalf("second independent authority rotation failed: %v", err)
	}
	if receiptBC.PreviousProjectID != receiptAB.ReplacementProjectID || receiptBC.ReplacementProjectID == receiptAB.ReplacementProjectID || receiptBC.ReplacementProjectID == receiptAB.PreviousProjectID {
		t.Fatalf("unexpected authority generations: A-B=%+v B-C=%+v", receiptAB, receiptBC)
	}
	for _, dataDir := range []string{svc.Project.DataDir, serviceB.Project.DataDir} {
		store, err := journal.Open(dataDir, filepath.Base(dataDir))
		if err != nil {
			t.Fatal(err)
		}
		if _, retired, err := store.AuthorityRetirement(); err != nil || !retired {
			t.Fatalf("prior generation was not durably retired: dir=%s retired=%v err=%v", dataDir, retired, err)
		}
	}
	serviceC, err := OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	stateC, err := serviceC.State()
	if err != nil || stateC.ProjectID != receiptBC.ReplacementProjectID || stateC.HeadSequence != 1 || stateC.Graph != nil {
		t.Fatalf("third generation is not a fence-only replacement: %+v %v", stateC, err)
	}
	delayedAB, err := serviceC.RotateAuthority(backupA, reportA.HeadHash, "rotate A to B", "rotate/a-b")
	if err != nil || delayedAB.ReceiptDigest != receiptAB.ReceiptDigest {
		t.Fatalf("delayed A-to-B retry after B-to-C changed its receipt: %+v %v", delayedAB, err)
	}
	if _, err := serviceC.RotateAuthority(backupA, reportA.HeadHash, "changed delayed intent", "rotate/a-b"); err == nil || !strings.Contains(err.Error(), "different intent") {
		t.Fatalf("delayed changed intent did not fail closed: %v", err)
	}
}

func TestPortableBackupCannotReviveAuthorityInAnotherDataRoot(t *testing.T) {
	root := t.TempDir()
	firstHome := filepath.Join(root, "home-a")
	t.Setenv("DAGRAIL_HOME", firstHome)
	svc, err := Init(root, "portable-retirement")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"portable-retirement"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	oldLocator, err := os.ReadFile(filepath.Join(root, ".dagrail", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	backup, report, err := svc.CreateBackup()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RotateAuthority(backup, report.HeadHash, "retire portable authority", "rotate/portable"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".dagrail", "project.yaml"), oldLocator, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "home-b"))
	if _, err := Open(root); err == nil || !strings.Contains(err.Error(), "authority claim") {
		t.Fatalf("portable backup locator opened its old writable UUID in another data root: %v", err)
	}
}

func TestCopiedCompleteDataDirectoryDoesNotDuplicateWriterAuthority(t *testing.T) {
	root := t.TempDir()
	firstHome := filepath.Join(root, "home-a")
	t.Setenv("DAGRAIL_HOME", firstHome)
	svc, err := Init(root, "full-copy-fence")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"full-copy-fence"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	secondHome := filepath.Join(root, "home-b")
	cloneData := filepath.Join(secondHome, "projects", svc.Project.Config.ProjectID)
	if err := os.MkdirAll(filepath.Dir(cloneData), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(cloneData, os.DirFS(svc.Project.DataDir)); err != nil {
		t.Fatal(err)
	}
	cloneRoot := filepath.Join(root, "clone-root")
	if err := os.MkdirAll(filepath.Join(cloneRoot, ".dagrail"), 0o700); err != nil {
		t.Fatal(err)
	}
	locator, err := os.ReadFile(filepath.Join(root, ".dagrail", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, ".dagrail", "project.yaml"), locator, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "original", time.Hour, false, "bind/original"); err != nil {
		t.Fatalf("original authority stopped writing after an ordinary copy: %v", err)
	}
	t.Setenv("DAGRAIL_HOME", secondHome)
	if _, err := Open(cloneRoot); err == nil || !strings.Contains(err.Error(), "authority claim") {
		t.Fatalf("copied complete data directory opened as a second writer: %v", err)
	}
}

func TestMissingClaimOrRotatedLineageNeverAutoRepairsOnOpen(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "data"))
	svc, err := Init(root, "claim-deletion")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"claim-deletion"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(svc.Project.DataDir, "authority-claim.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err == nil || !strings.Contains(err.Error(), "authority claim") {
		t.Fatalf("ordinary open accepted a deleted claim: %v", err)
	}
	if _, err := os.Stat(filepath.Join(svc.Project.DataDir, "authority-claim.json")); !os.IsNotExist(err) {
		t.Fatalf("deleted claim was silently recreated: %v", err)
	}

	rotatedRoot := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(rotatedRoot, "data"))
	rotated, err := Init(rotatedRoot, "lineage-deletion")
	if err != nil {
		t.Fatal(err)
	}
	rotatedGraph := filepath.Join(rotatedRoot, "graph.json")
	if err := os.WriteFile(rotatedGraph, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"lineage-deletion"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rotated.ImportGraph(rotatedGraph, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	backup, report, err := rotated.CreateBackup()
	if err != nil {
		t.Fatal(err)
	}
	rotationReceipt, err := rotated.RotateAuthority(backup, report.HeadHash, "lineage deletion", "rotate/lineage-deletion")
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := OpenForRecovery(rotatedRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(replacement.Project.DataDir, "authority-lineage.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(rotatedRoot); err == nil || !strings.Contains(err.Error(), "lineage") {
		t.Fatalf("rotated authority opened without its claim-bound lineage: %v", err)
	}
	retried, err := replacement.RotateAuthority(backup, report.HeadHash, "lineage deletion", "rotate/lineage-deletion")
	if err != nil || retried.ReceiptDigest != rotationReceipt.ReceiptDigest {
		t.Fatalf("exact rotation retry did not reconstruct claim-authenticated lineage: %+v %v", retried, err)
	}
	if err := os.WriteFile(filepath.Join(replacement.Project.DataDir, "authority-lineage.json"), []byte(`{"corrupt":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(rotatedRoot); err == nil || !strings.Contains(err.Error(), "lineage") {
		t.Fatalf("rotated authority opened with corrupt lineage: %v", err)
	}
	retried, err = replacement.RotateAuthority(backup, report.HeadHash, "lineage deletion", "rotate/lineage-deletion")
	if err != nil || retried.ReceiptDigest != rotationReceipt.ReceiptDigest {
		t.Fatalf("exact rotation retry did not replace corrupt claim-authenticated lineage: %+v %v", retried, err)
	}
	writableReplacement, err := Open(rotatedRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writableReplacement.ImportGraph(rotatedGraph, "replacement-graph", "governor"); err != nil {
		t.Fatalf("replacement remained unwritable after explicit lineage recovery: %v", err)
	}
}

func TestLegacyAuthorityAdoptionIsExplicitExactAndIdempotent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "data"))
	svc, err := Init(root, "legacy-adoption")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"legacy-adoption"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "legacy-graph", "governor"); err != nil {
		t.Fatal(err)
	}
	previousHead := simulatePreV022Authority(t, svc, testAuthorityHome(t))
	previousProjectID := svc.Project.Config.ProjectID
	svc.Now = func() time.Time { return time.Date(2026, 8, 16, 6, 7, 8, 0, time.UTC) }
	receipt, err := svc.AdoptLegacyAuthority(previousProjectID, previousHead, "explicit v0.21 adoption", "adopt/legacy")
	if err != nil || receipt.PreviousProjectID != previousProjectID || receipt.ReplacementProjectID == previousProjectID || receipt.PreviousHead != previousHead || receipt.SourceBackupDigest == "" || receipt.ReceiptDigest == "" {
		t.Fatalf("explicit legacy adoption failed: %+v %v", receipt, err)
	}
	validateLifecycleSchema(t, filepath.Join("..", "..", "schemas", "authority-adoption-v1alpha1.schema.json"), "urn:dagrail:authority-adoption", receipt)
	if err := VerifyAuthorityAdoptionReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	replacement, err := OpenForRecovery(root)
	if err != nil || replacement.Project.Config.ProjectID != receipt.ReplacementProjectID {
		t.Fatalf("replacement authority was not published: %+v %v", replacement, err)
	}
	idempotentInit, err := Init(root, "legacy-adoption")
	if err != nil || idempotentInit.Project.Config.ProjectID != receipt.ReplacementProjectID {
		t.Fatalf("init did not reopen the adopted authority idempotently: %+v %v", idempotentInit, err)
	}
	idempotentState, err := idempotentInit.State()
	if err != nil || idempotentState.HeadSequence != 1 {
		t.Fatalf("init changed the adopted authority: %+v %v", idempotentState, err)
	}
	svc.Now = func() time.Time { return time.Date(2026, 8, 16, 7, 8, 9, 0, time.UTC) }
	retried, err := replacement.AdoptLegacyAuthority(previousProjectID, previousHead, "explicit v0.21 adoption", "adopt/legacy")
	if err != nil || retried.ReceiptDigest != receipt.ReceiptDigest {
		t.Fatalf("legacy adoption retry changed receipt: %+v %v", retried, err)
	}
	if _, err := replacement.AdoptLegacyAuthority(previousProjectID, previousHead, "changed adoption", "adopt/legacy"); err == nil || !strings.Contains(err.Error(), "different intent") {
		t.Fatalf("changed legacy adoption intent was accepted: %v", err)
	}
	writableReplacement, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writableReplacement.ImportGraph(graphPath, "replacement-graph", "governor"); err != nil {
		t.Fatalf("replacement graph bootstrap failed: %v", err)
	}
	if _, err := writableReplacement.BindRole("worker", "codex", "adopted-worker", time.Hour, false, "bind/after-adoption"); err != nil {
		t.Fatalf("adopted legacy authority remained unwritable: %v", err)
	}
}

func TestLegacyAuthorityAdoptionResumesBarrierBeforeClaim(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "data"))
	svc, err := Init(root, "legacy-adoption-crash")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"legacy-adoption-crash"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "legacy-graph", "governor"); err != nil {
		t.Fatal(err)
	}
	previousHead := simulatePreV022Authority(t, svc, testAuthorityHome(t))
	previousProjectID := svc.Project.Config.ProjectID
	adoptedAt := time.Date(2026, 8, 16, 6, 7, 8, 0, time.UTC)
	segments, err := svc.Journal.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	backupEnvelope := BackupEnvelope{APIVersion: BackupAPIVersion, Kind: "JournalBackup", Project: svc.Project.Config, CreatedAt: adoptedAt.Format(time.RFC3339Nano), Segments: segments}
	backupEnvelope.Digest, err = backupDigest(backupEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	replacementProjectID := uuid.NewSHA1(uuid.MustParse(previousProjectID), []byte("dagrail-legacy-authority-adoption-v1\x00adopt/barrier")).String()
	retirement := authorityRetirement{APIVersion: AuthorityAdoptionAPIVersion, Kind: legacyRetirementKind, PreviousProjectID: previousProjectID, PreviousHead: previousHead, RecoveryHead: previousHead, RecoveryBackupDigest: backupEnvelope.Digest, ReplacementProjectID: replacementProjectID, RotatedAt: adoptedAt.Format(time.RFC3339Nano), Reason: "resume barrier", IdempotencyKey: "adopt/barrier"}
	raw, err := json.Marshal(retirement)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = jcs.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	reservationDigest, err := legacyRetirementReservationDigest(retirement)
	if err != nil {
		t.Fatal(err)
	}
	legacyJournal, err := journal.OpenRecovery(svc.Project.DataDir, previousProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyJournal.RetireLegacyAuthority(previousHead, raw, reservationDigest, adoptedAt, func([]journal.Segment) error {
		return project.ReserveLegacyRetirement(svc.Project.DataDir, previousProjectID, reservationDigest)
	}, nil, func([]byte) error {
		return os.ErrClosed
	}); err == nil {
		t.Fatal("injected crash prefix unexpectedly completed")
	}
	segments, err = svc.Journal.ReadAll()
	if err != nil || len(segments) != 3 || segments[len(segments)-1].Events[0].Type != "authority.retired" {
		t.Fatalf("retirement barrier crash prefix is invalid: %#v %v", segments, err)
	}
	svc.Now = func() time.Time { return adoptedAt.Add(time.Hour) }
	receipt, err := svc.AdoptLegacyAuthority(previousProjectID, previousHead, "resume barrier", "adopt/barrier")
	if err != nil || receipt.AdoptedAt != adoptedAt.Format(time.RFC3339Nano) || receipt.ReplacementProjectID != replacementProjectID {
		t.Fatalf("exact retry did not resume committed barrier: %+v %v", receipt, err)
	}
	replacement, err := project.Open(root)
	if err != nil || replacement.Config.ProjectID != replacementProjectID || project.ValidateAuthorityClaim(replacement.DataDir, replacementProjectID) != nil {
		t.Fatalf("resumed adoption did not publish a fresh authority: %+v %v", replacement, err)
	}
}

func TestCopiedV022LocatorCannotBeAdoptedInAnotherDataHome(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	homeA := filepath.Join(t.TempDir(), "home-a")
	homeB := filepath.Join(t.TempDir(), "home-b")
	t.Setenv("DAGRAIL_HOME", homeA)
	svcA, err := Init(rootA, "locator-copy")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootB, ".dagrail"), 0o700); err != nil {
		t.Fatal(err)
	}
	locator, err := os.ReadFile(filepath.Join(rootA, ".dagrail", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, ".dagrail", "project.yaml"), locator, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAGRAIL_HOME", homeB)
	t.Setenv("DAGRAIL_AUTHORITY_HOME", filepath.Join(t.TempDir(), "alternate-authority"))
	t.Setenv("HOME", filepath.Join(t.TempDir(), "alternate-home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "alternate-xdg"))
	if _, err := Init(rootB, "copied-locator"); err == nil || !strings.Contains(err.Error(), "claim") {
		t.Fatalf("init reported a copied locator as an established local authority: %v", err)
	}
	copyService, err := OpenForRecovery(rootB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := copyService.AdoptLegacyAuthority(svcA.Project.Config.ProjectID, "", "must not adopt copied locator", "adopt/copied-locator"); err == nil || !strings.Contains(err.Error(), "bound to different content") {
		t.Fatalf("copied v0.22 locator became a second writer: %v", err)
	}
}

func TestLegacyAuthorityAdoptionIsConcurrentSameIntentIdempotent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "data"))
	seed, err := Init(root, "legacy-adoption-concurrency")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"legacy-adoption-concurrency"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.ImportGraph(graphPath, "legacy-graph", "governor"); err != nil {
		t.Fatal(err)
	}
	previousHead := simulatePreV022Authority(t, seed, testAuthorityHome(t))
	previousProjectID := seed.Project.Config.ProjectID
	first, err := OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	first.Now = func() time.Time { return time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC) }
	second.Now = func() time.Time { return time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC) }
	second.beforeLegacyAuthoritySnapshot = func() {
		close(entered)
		<-release
	}
	type result struct {
		receipt AuthorityAdoptionReceipt
		err     error
	}
	results := make(chan result, 1)
	go func() {
		receipt, err := second.AdoptLegacyAuthority(previousProjectID, previousHead, "concurrent same intent", "adopt/concurrent-same-intent")
		results <- result{receipt: receipt, err: err}
	}()
	<-entered
	aReceipt, aErr := first.AdoptLegacyAuthority(previousProjectID, previousHead, "concurrent same intent", "adopt/concurrent-same-intent")
	close(release)
	b := <-results
	if aErr != nil || b.err != nil || aReceipt.ReceiptDigest == "" || aReceipt.ReceiptDigest != b.receipt.ReceiptDigest {
		t.Fatalf("concurrent same-intent adoption was not receipt-idempotent: a=%+v/%v b=%+v/%v", aReceipt, aErr, b.receipt, b.err)
	}
}

func TestLegacyAuthorityAdoptionRecoversReservationBeforeFence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission fault injection is not portable to Windows")
	}
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "data"))
	svc, err := Init(root, "legacy-adoption-precommit")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"legacy-adoption-precommit"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "legacy-graph", "governor"); err != nil {
		t.Fatal(err)
	}
	previousHead := simulatePreV022Authority(t, svc, testAuthorityHome(t))
	previousProjectID := svc.Project.Config.ProjectID
	journalDir := filepath.Join(svc.Project.DataDir, "journal")
	if err := os.Chmod(journalDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(journalDir, 0o700) }()
	svc.Now = func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) }
	if _, err := svc.AdoptLegacyAuthority(previousProjectID, previousHead, "recover reservation before fence", "adopt/precommit-crash"); err == nil {
		t.Fatal("read-only journal did not inject a failure after legacy retirement reservation")
	}
	anchor := filepath.Join(testAuthorityHome(t), "anchors", previousProjectID+".json")
	if _, err := os.Lstat(anchor); err != nil {
		t.Fatalf("legacy retirement reservation was not durable before the injected failure: %v", err)
	}
	if err := os.Chmod(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	svc.Now = func() time.Time { return time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC) }
	receipt, err := svc.AdoptLegacyAuthority(previousProjectID, previousHead, "recover reservation before fence", "adopt/precommit-crash")
	if err != nil || receipt.PreviousProjectID != previousProjectID || receipt.PreviousHead != previousHead {
		t.Fatalf("exact retry did not recover the reservation-before-fence prefix: %+v %v", receipt, err)
	}
}

func TestDelayedAuthorityRetriesTraverseMoreThan128Generations(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "data"))
	legacy, err := Init(root, "long-authority-lineage")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"long-authority-lineage"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ImportGraph(graphPath, "legacy-graph", "governor"); err != nil {
		t.Fatal(err)
	}
	legacyHead := simulatePreV022Authority(t, legacy, testAuthorityHome(t))
	legacyID := legacy.Project.Config.ProjectID
	adoptionReason, adoptionKey := "start long lineage", "adopt/long-lineage"
	if _, err := legacy.AdoptLegacyAuthority(legacyID, legacyHead, adoptionReason, adoptionKey); err != nil {
		t.Fatal(err)
	}
	current, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	firstBackup, firstReport, err := current.CreateBackup()
	if err != nil {
		t.Fatal(err)
	}
	firstReason, firstKey := "first long-lineage rotation", "rotate/long-lineage/first"
	if _, err := current.RotateAuthority(firstBackup, firstReport.HeadHash, firstReason, firstKey); err != nil {
		t.Fatal(err)
	}
	for generation := 2; generation <= 130; generation++ {
		current, err = Open(root)
		if err != nil {
			t.Fatal(err)
		}
		backup, report, err := current.CreateBackup()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := current.RotateAuthority(backup, report.HeadHash, "continue long lineage", fmt.Sprintf("rotate/long-lineage/%03d", generation)); err != nil {
			t.Fatalf("rotation generation %d: %v", generation, err)
		}
	}
	current, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := current.RotateAuthority(firstBackup, firstReport.HeadHash, firstReason, firstKey); err != nil || receipt.PreviousProjectID != firstReport.ProjectID {
		t.Fatalf("delayed rotation retry did not cross 128 generations: %+v %v", receipt, err)
	}
	if receipt, err := current.AdoptLegacyAuthority(legacyID, legacyHead, adoptionReason, adoptionKey); err != nil || receipt.PreviousProjectID != legacyID {
		t.Fatalf("delayed adoption retry did not cross 128 generations: %+v %v", receipt, err)
	}
}

func TestDeletedV022ClaimCannotBeReclassifiedAsLegacy(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "data"))
	svc, err := Init(root, "claim-deletion")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(svc.Project.DataDir, "authority-claim.json")); err != nil {
		t.Fatal(err)
	}
	recovery, err := OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.AdoptLegacyAuthority(svc.Project.Config.ProjectID, state.HeadHash, "must not reclassify v0.22", "adopt/deleted-claim"); err == nil || !strings.Contains(err.Error(), "bound to different content") {
		t.Fatalf("claim deletion reclassified v0.22 state as legacy: %v", err)
	}
}

func TestLegacyCopiesCannotBothAdoptTheSameProjectIdentity(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	authorityHome := testAuthorityHome(t)
	homeA := filepath.Join(t.TempDir(), "home-a")
	homeB := filepath.Join(t.TempDir(), "home-b")
	t.Setenv("DAGRAIL_HOME", homeA)
	svcA, err := Init(rootA, "legacy-copy")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(rootA, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"legacy-copy"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svcA.ImportGraph(graphPath, "legacy-copy/graph", "governor"); err != nil {
		t.Fatal(err)
	}
	oldID := svcA.Project.Config.ProjectID
	oldHead := simulatePreV022Authority(t, svcA, authorityHome)
	if err := os.MkdirAll(filepath.Join(rootB, ".dagrail"), 0o700); err != nil {
		t.Fatal(err)
	}
	locator, err := os.ReadFile(filepath.Join(rootA, ".dagrail", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, ".dagrail", "project.yaml"), locator, 0o600); err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(homeA, "projects", oldID)
	destinationDir := filepath.Join(homeB, "projects", oldID)
	if err := os.MkdirAll(destinationDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(destinationDir, os.DirFS(sourceDir)); err != nil {
		t.Fatal(err)
	}
	receipt, err := svcA.AdoptLegacyAuthority(oldID, oldHead, "select legacy copy A", "adopt/copied-legacy")
	if err != nil || receipt.ReplacementProjectID == oldID {
		t.Fatalf("first legacy copy adoption failed: %+v %v", receipt, err)
	}
	t.Setenv("DAGRAIL_HOME", homeB)
	svcB, err := OpenForRecovery(rootB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svcB.AdoptLegacyAuthority(oldID, oldHead, "select legacy copy A", "adopt/copied-legacy"); err == nil || !strings.Contains(err.Error(), "bound to different content") {
		t.Fatalf("second legacy copy was adopted: %v", err)
	}
}

func TestEmptyLegacyAuthorityAdoptionCreatesFreshIdentity(t *testing.T) {
	root := t.TempDir()
	authorityHome := testAuthorityHome(t)
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "data"))
	legacy, err := project.Init(root, "empty-legacy")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	oldID := legacy.Config.ProjectID
	if err := os.Remove(filepath.Join(legacy.DataDir, "authority-claim.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(authorityHome, "anchors", oldID+".json")); err != nil {
		t.Fatal(err)
	}
	receipt, err := svc.AdoptLegacyAuthority(oldID, "", "adopt empty v0.21 store", "adopt/empty-legacy")
	if err != nil || receipt.PreviousHead != "" || receipt.ReplacementProjectID == oldID {
		t.Fatalf("empty legacy adoption failed: %+v %v", receipt, err)
	}
	replacement, err := Open(root)
	if err != nil || replacement.Project.Config.ProjectID != receipt.ReplacementProjectID {
		t.Fatalf("empty legacy replacement is not writable: %+v %v", replacement, err)
	}
}

func simulatePreV022Authority(t *testing.T, svc *Service, authorityHome string) string {
	t.Helper()
	segments, err := svc.Journal.ReadAll()
	if err != nil || len(segments) < 2 || segments[0].SchemaVersion != journal.AuthorityFenceSchemaVersion || segments[0].Events[0].Type != "authority.established" {
		t.Fatalf("legacy fixture must have a non-empty verified journal: %v", err)
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
	previous := ""
	for index, segment := range segments[1:] {
		unsigned := legacyUnsignedSegment{
			SchemaVersion: segment.SchemaVersion,
			Sequence:      uint64(index + 1),
			ProjectID:     segment.ProjectID,
			PreviousHash:  previous,
			Command:       segment.Command,
			Events:        segment.Events,
			CommittedAt:   segment.CommittedAt,
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
		previousHash, err := hex.DecodeString(unsigned.PreviousHash)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = hasher.Write(previousHash)
		_, _ = hasher.Write(canonical)
		previous = hex.EncodeToString(hasher.Sum(nil))
		stored := legacyStoredSegment{
			SchemaVersion: unsigned.SchemaVersion,
			Sequence:      unsigned.Sequence,
			ProjectID:     unsigned.ProjectID,
			PreviousHash:  unsigned.PreviousHash,
			Command:       unsigned.Command,
			Events:        unsigned.Events,
			CommittedAt:   unsigned.CommittedAt,
			SegmentHash:   previous,
		}
		storedRaw, err := json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		storedRaw, err = jcs.Transform(storedRaw)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(journalDir, fmt.Sprintf("%012d-%s.json", stored.Sequence, stored.SegmentHash))
		if err := os.WriteFile(path, storedRaw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(svc.Project.DataDir, "authority-claim.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(authorityHome, "anchors", svc.Project.Config.ProjectID+".json")); err != nil {
		t.Fatal(err)
	}
	return previous
}

func testAuthorityHome(t testing.TB) string {
	t.Helper()
	root := os.Getenv("DAGRAIL_TEST_AUTHORITY_HOME")
	if root == "" {
		t.Fatal("DAGRAIL_TEST_AUTHORITY_HOME is not configured by TestMain")
	}
	return root
}

func TestRecoveryInspectionHandleRejectsOrdinaryMutation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "data"))
	if _, err := Init(root, "recovery-read-only"); err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"recovery-read-only"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	recovery, err := OpenForRecovery(root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := recovery.Journal.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.ImportGraph(graphPath, "must-not-write", "governor"); err == nil || !strings.Contains(err.Error(), "recovery inspection") {
		t.Fatalf("recovery inspection handle accepted ordinary mutation: %v", err)
	}
	after, err := recovery.Journal.ReadAll()
	if err != nil || len(after) != len(before) {
		t.Fatalf("recovery inspection mutation changed journal: before=%d after=%d err=%v", len(before), len(after), err)
	}
}

func jsonString(value any) (string, error) {
	raw, err := json.Marshal(value)
	return string(raw), err
}
