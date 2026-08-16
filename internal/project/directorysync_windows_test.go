//go:build windows

package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestEnsureDurableDirectoryRejectsAncestorAccessDenied(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "restricted", "leaf")
	deniedAncestor := filepath.Join(base, "restricted")
	originalSync := directorySync
	defer func() { directorySync = originalSync }()
	directorySync = func(path string) error {
		if filepath.Clean(path) == filepath.Clean(deniedAncestor) {
			return windows.ERROR_ACCESS_DENIED
		}
		return syncDirectoryPath(path)
	}
	if err := EnsureDurableDirectoryWithin(target, base); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("ancestor access denial was treated as a durable boundary: %v", err)
	}
}

func TestEnsureDurableDirectoryWorksUnderStandardUserRoots(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	for name, root := range map[string]string{"cache": cache, "temp": os.TempDir()} {
		t.Run(name, func(t *testing.T) {
			base, err := os.MkdirTemp(root, "dagrail-directory-sync-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(base)
			target := filepath.Join(base, "dagrail", "projects", "project", "journal")
			if err := EnsureDurableDirectoryWithin(target, base); err != nil {
				t.Fatalf("fresh standard-user directory: %v", err)
			}
			if err := EnsureDurableDirectoryWithin(target, base); err != nil {
				t.Fatalf("existing standard-user directory: %v", err)
			}
		})
	}
}
