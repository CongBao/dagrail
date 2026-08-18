//go:build !windows

package gitartifact

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDecodeManifestRejectsFIFOAndDeviceWithoutBlocking(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "manifest.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := DecodeManifestContext(ctx, fifo); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("FIFO manifest was not rejected as non-regular: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("FIFO rejection blocked for %s", elapsed)
	}

	if _, err := os.Stat("/dev/null"); err == nil {
		if _, err := DecodeManifest("/dev/null"); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("device manifest was not rejected as non-regular: %v", err)
		}
	}
}
