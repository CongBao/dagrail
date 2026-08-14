package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if err != nil || status.HeadSequence != 2 || status.Nodes["terminal"] != 1 {
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
	if err != nil || report.Segments != 2 || report.Digest == "" {
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
	if err != nil || restored.HeadSequence != 2 {
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

func jsonString(value any) (string, error) {
	raw, err := json.Marshal(value)
	return string(raw), err
}
