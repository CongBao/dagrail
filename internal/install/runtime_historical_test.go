//go:build historical

package install

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/compatibility"
	"github.com/CongBao/dagrail/internal/version"
)

type historicalBinary struct {
	version string
	path    string
}

func TestHistoricalBinaryCompatibilityWindow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("historical source archive matrix runs on the release Linux host")
	}
	repository := repositoryRoot(t)
	window, _, err := compatibility.Current()
	if err != nil {
		t.Fatal(err)
	}
	buildRoot := t.TempDir()
	binaries := make([]historicalBinary, 0, len(window.Entries)+1)
	for _, entry := range window.Entries {
		source := filepath.Join(buildRoot, "source-"+entry.Version)
		extractRevision(t, repository, entry.Commit, source)
		binary := filepath.Join(buildRoot, "dagrail-"+entry.Version)
		buildDAGrail(t, source, binary)
		assertBinaryVersion(t, binary, entry.Version)
		binaries = append(binaries, historicalBinary{version: entry.Version, path: binary})
	}
	currentBinary := filepath.Join(buildRoot, "dagrail-"+version.Version)
	buildDAGrail(t, repository, currentBinary)
	assertBinaryVersion(t, currentBinary, version.Version)
	binaries = append(binaries, historicalBinary{version: version.Version, path: currentBinary})

	for index := 0; index+1 < len(binaries); index++ {
		t.Run(binaries[index].version+"-to-"+binaries[index+1].version, func(t *testing.T) {
			root := t.TempDir()
			destination := filepath.Join(root, "bin", "dagrail")
			dataRoot := filepath.Join(root, "runtime-data")
			installed, err := installRuntimeFrom(context.Background(), binaries[index].path, destination, dataRoot, runtimeVersion)
			if err != nil || installed.Version != binaries[index].version {
				t.Fatalf("install historical runtime: %+v %v", installed, err)
			}
			upgraded, err := installRuntimeFrom(context.Background(), binaries[index+1].path, destination, dataRoot, runtimeVersion)
			if err != nil || upgraded.Version != binaries[index+1].version || !upgraded.RollbackAvailable {
				t.Fatalf("upgrade runtime: %+v %v", upgraded, err)
			}
			rolledBack, err := rollbackRuntime(context.Background(), dataRoot, runtimeVersion)
			if err != nil || rolledBack.Version != binaries[index].version {
				t.Fatalf("rollback runtime: %+v %v", rolledBack, err)
			}
			rolledForward, err := rollbackRuntime(context.Background(), dataRoot, runtimeVersion)
			if err != nil || rolledForward.Version != binaries[index+1].version {
				t.Fatalf("reversible rollback runtime: %+v %v", rolledForward, err)
			}
		})
	}

	projectRoot := filepath.Join(t.TempDir(), "project")
	dataRoot := filepath.Join(t.TempDir(), "data")
	graphPath := filepath.Join(t.TempDir(), "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"beta-window"},"spec":{"roles":[],"nodes":[{"id":"done","kind":"milestone","title":"done","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	runHistorical(t, binaries[0].path, dataRoot, "init", "--root", projectRoot, "--name", "beta-window")
	runHistorical(t, binaries[0].path, dataRoot, "graph", "import", "--root", projectRoot, "--file", graphPath, "--idempotency-key", "beta-window-import")
	for _, binary := range binaries {
		runHistorical(t, binary.path, dataRoot, "journal", "verify", "--root", projectRoot)
	}
	runHistorical(t, currentBinary, dataRoot, "recovery", "rehearse", "--root", projectRoot)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	command := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	root := strings.TrimSpace(string(output))
	if !filepath.IsAbs(root) {
		t.Fatalf("repository root is not absolute: %q", root)
	}
	return root
}

func extractRevision(t *testing.T, repository, commit, destination string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "-C", repository, "archive", "--format=tar", commit)
	archive, err := command.Output()
	if err != nil {
		t.Fatalf("archive %s: %v", commit, err)
	}
	if len(archive) > 128*1024*1024 {
		t.Fatal("historical source archive exceeds 128 MiB")
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	entries, total := 0, int64(0)
	for {
		header, readErr := reader.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		entries++
		if entries > 10000 {
			t.Fatal("historical source archive has too many entries")
		}
		clean := filepath.Clean(filepath.FromSlash(header.Name))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			t.Fatalf("unsafe historical archive path %q", header.Name)
		}
		target := filepath.Join(destination, clean)
		switch header.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > 32*1024*1024 || total+header.Size > 128*1024*1024 {
				t.Fatal("historical source file exceeds extraction limit")
			}
			total += header.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0o644)
			if header.Mode&0o111 != 0 {
				mode = 0o755
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				t.Fatal(err)
			}
			written, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil || written != header.Size {
				t.Fatalf("extract %s: copied=%d copy=%v close=%v", header.Name, written, copyErr, closeErr)
			}
		default:
			t.Fatalf("unsupported historical archive entry %q type %d", header.Name, header.Typeflag)
		}
	}
}

func buildDAGrail(t *testing.T, source, output string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-trimpath", "-o", output, "./cmd/dagrail")
	command.Dir = source
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	combined := &boundedBuffer{remaining: 64 * 1024}
	command.Stdout, command.Stderr = combined, combined
	if err := command.Run(); err != nil {
		t.Fatalf("build historical source %s: %v: %s", source, err, combined.String())
	}
}

func assertBinaryVersion(t *testing.T, binary, expected string) {
	t.Helper()
	output := runHistorical(t, binary, t.TempDir(), "version")
	var report map[string]string
	if err := json.Unmarshal([]byte(output), &report); err != nil || report["version"] != expected {
		t.Fatalf("binary version mismatch: expected=%s output=%s err=%v", expected, output, err)
	}
}

func runHistorical(t *testing.T, binary, dataRoot string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append(os.Environ(), "DAGRAIL_HOME="+dataRoot)
	output := &boundedBuffer{remaining: 64 * 1024}
	command.Stdout, command.Stderr = output, output
	if err := command.Run(); err != nil {
		t.Fatalf("%s %s: %v: %s", binary, strings.Join(args, " "), err, output.String())
	}
	return output.String()
}
