package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/journal"
)

func TestActionSecretRejectsSymlinkSubstitution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not available on every Windows CI host")
	}
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := Init(root, "secret-link")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(svc.Project.DataDir, "action-secret")
	if err := os.Remove(secretPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, secretPath); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.actionSecret(); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("symlink action secret was accepted: %v", err)
	}
}

func TestJournalVerificationDigestMatchesPortableCanonicalExport(t *testing.T) {
	segments := []journal.Segment{
		{Sequence: 1, ProjectID: "project", Command: journal.Command{ID: "one", Kind: "test"}, Events: []journal.Event{{Type: "test", Payload: []byte(`{"value":"first"}`)}}},
		{Sequence: 2, ProjectID: "project", Command: journal.Command{ID: "two", Kind: "test"}, Events: []journal.Event{{Type: "test", Payload: []byte(`{"value":"second"}`)}}},
	}
	exported, err := exportJournalSegments(context.Background(), segments)
	if err != nil {
		t.Fatal(err)
	}
	bytes, digest, err := digestJournalSegments(context.Background(), segments)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(exported)
	if bytes != int64(len(exported)) || hex.EncodeToString(digest) != hex.EncodeToString(want[:]) {
		t.Fatalf("streaming verification digest diverged: bytes=%d/%d digest=%x/%x", bytes, len(exported), digest, want)
	}
}

func TestJournalVerificationDigestHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := digestJournalSegments(ctx, []journal.Segment{{Sequence: 1}}); err != context.Canceled {
		t.Fatalf("digest ignored cancellation: %v", err)
	}
}
