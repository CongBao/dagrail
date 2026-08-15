package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/version"
)

func TestHookLauncherMustResolveToVerifiedRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable script fixture is POSIX-only")
	}
	root := t.TempDir()
	verifiedDir := filepath.Join(root, "verified")
	wrongDir := filepath.Join(root, "wrong")
	for _, dir := range []string{verifiedDir, wrongDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	verified := filepath.Join(verifiedDir, "dagrail")
	wrong := filepath.Join(wrongDir, "dagrail")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = version ]; then printf '%%s\\n' '{\"version\":\"%s\"}'; exit 0; fi\nexit 1\n", version.Version)
	for _, path := range []string{verified, wrong} {
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", wrongDir)
	if err := validateHookLauncher(context.Background(), verified); err == nil || !strings.Contains(err.Error(), "does not resolve") {
		t.Fatalf("mismatched textual hook launcher was accepted: %v", err)
	}
	t.Setenv("PATH", verifiedDir)
	if err := validateHookLauncher(context.Background(), verified); err != nil {
		t.Fatalf("verified textual hook launcher was rejected: %v", err)
	}
}
