package project

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenDiscoversProjectFromDescendant(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	created, err := Init(root, "nested")
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "src", "package")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	found, err := Open(nested)
	if err != nil {
		t.Fatal(err)
	}
	if found.Root != created.Root || found.Config.ProjectID != created.Config.ProjectID {
		t.Fatalf("found wrong project: %#v", found)
	}
}

func TestOpenRejectsUnknownLocatorFieldsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	if _, err := Init(root, "strict"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, ".dagrail", "project.yaml")
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, append(raw, []byte("unexpected: true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err == nil || !strings.Contains(err.Error(), "field unexpected") {
		t.Fatalf("unknown project locator field was accepted: %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.WriteFile(marker, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "locator.yaml")
	if err := os.Rename(marker, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, marker); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("symlink project locator was accepted: %v", err)
	}
}

func TestOpenRejectsProjectIDPathTraversal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	if _, err := Init(root, "strict-id"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, ".dagrail", "project.yaml")
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	for index, line := range lines {
		if strings.HasPrefix(line, "projectId:") {
			lines[index] = "projectId: ../../outside"
		}
	}
	if err := os.WriteFile(marker, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err == nil || !strings.Contains(err.Error(), "project UUID") {
		t.Fatalf("path-traversing project ID was accepted: %v", err)
	}
}

func TestProjectLocatorRejectsSensitiveMaterial(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	if _, err := Init(root, "Bearer abcdefghijklmnopqrstuvwxyz"); err == nil || !strings.Contains(err.Error(), "prohibited") {
		t.Fatalf("sensitive project name was accepted: %v", err)
	}
}
