package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
