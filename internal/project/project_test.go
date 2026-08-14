package project

import (
	"os"
	"path/filepath"
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
