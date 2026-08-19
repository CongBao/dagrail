//go:build windows

package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestWindowsWriteThroughPathPublication(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.WriteFile(source, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishPathExclusive(source, destination); err != nil {
		t.Fatalf("publish exclusive: %v", err)
	}
	if err := SyncDirectory(root); err != nil {
		t.Fatalf("confirm containing directory: %v", err)
	}
	if raw, err := os.ReadFile(destination); err != nil || string(raw) != "first" {
		t.Fatalf("published content = %q, %v", raw, err)
	}

	second := filepath.Join(root, "second")
	if err := os.WriteFile(second, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishPathExclusive(second, destination); err == nil {
		t.Fatal("exclusive publication replaced an existing destination")
	}
	if raw, err := os.ReadFile(destination); err != nil || string(raw) != "first" {
		t.Fatalf("failed exclusive publication changed destination = %q, %v", raw, err)
	}

	if err := ReplacePathAtomic(second, destination); err != nil {
		t.Fatalf("replace write-through path: %v", err)
	}
	if err := SyncDirectory(root); err != nil {
		t.Fatalf("confirm replaced path: %v", err)
	}
	if raw, err := os.ReadFile(destination); err != nil || string(raw) != "second" {
		t.Fatalf("replaced content = %q, %v", raw, err)
	}
}

func TestWindowsDirectorySyncRejectsFilesAndReparsePoints(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "regular")
	if err := os.WriteFile(regular, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syncDirectoryPath(regular); err == nil {
		t.Fatal("regular file was accepted as a directory durability handle")
	}

	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	if err := syncDirectoryPath(link); err == nil {
		t.Fatal("directory reparse point was accepted as a durability handle")
	}
}

func TestWindowsDurabilityPrimitivesSupportExtendedPaths(t *testing.T) {
	root := t.TempDir()
	target := root
	for len(target) < 320 {
		target = filepath.Join(target, strings.Repeat("segment", 5))
	}
	if err := EnsureDurableDirectoryWithin(target, root); err != nil {
		t.Fatalf("create extended directory chain: %v", err)
	}
	if err := SyncDirectory(target); err != nil {
		t.Fatalf("confirm extended directory chain: %v", err)
	}

	source := filepath.Join(target, "source")
	destination := filepath.Join(target, "destination")
	if err := os.WriteFile(source, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishPathExclusive(source, destination); err != nil {
		t.Fatalf("publish extended path: %v", err)
	}
	replacement := filepath.Join(target, "replacement")
	if err := os.WriteFile(replacement, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReplacePathAtomic(replacement, destination); err != nil {
		t.Fatalf("replace extended path: %v", err)
	}
	if raw, err := os.ReadFile(destination); err != nil || string(raw) != "second" {
		t.Fatalf("extended replacement content = %q, %v", raw, err)
	}
}

func TestExtendedWindowsPathConversionIsClosed(t *testing.T) {
	longTail := strings.Repeat("a", 260)
	relative := filepath.Join("relative", "child")
	relativeAbsolute, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "short drive", path: `C:\short`, want: `C:\short`},
		{name: "long drive", path: `C:\` + longTail, want: `\\?\C:\` + longTail},
		{name: "long UNC", path: `\\server\share\` + longTail, want: `\\?\UNC\server\share\` + longTail},
		{name: "extended", path: `\\?\C:\` + longTail, want: `\\?\C:\` + longTail},
		{name: "NT extended", path: `\??\C:\` + longTail, want: `\??\C:\` + longTail},
		{name: "device", path: `\\.\PIPE\dagrail`, want: `\\.\PIPE\dagrail`},
		{name: "relative", path: relative, want: strings.ReplaceAll(filepath.Clean(relativeAbsolute), "/", `\`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := extendedWindowsPath(test.path)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("extendedWindowsPath(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

func TestWindowsDirectoryAttributeAdmissionIsClosed(t *testing.T) {
	for name, test := range map[string]struct {
		attributes uint32
		want       bool
	}{
		"directory":          {attributes: windows.FILE_ATTRIBUTE_DIRECTORY, want: true},
		"directory readonly": {attributes: windows.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_READONLY, want: true},
		"regular file":       {attributes: windows.FILE_ATTRIBUTE_NORMAL, want: false},
		"reparse directory":  {attributes: windows.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_REPARSE_POINT, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := validDirectoryAttributes(test.attributes); got != test.want {
				t.Fatalf("validDirectoryAttributes(%#x) = %v, want %v", test.attributes, got, test.want)
			}
		})
	}
}
