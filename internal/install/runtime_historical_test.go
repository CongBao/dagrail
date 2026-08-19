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
	"gopkg.in/yaml.v3"
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
	t.Setenv("DAGRAIL_TEST_AUTHORITY_HOME", filepath.Join(buildRoot, "authority"))
	binaries := make([]historicalBinary, 0, len(window.Entries)+1)
	binaryByVersion := map[string]historicalBinary{}
	sources := map[string]string{}
	for _, entry := range window.Entries {
		source := filepath.Join(buildRoot, "source-"+entry.Version)
		extractRevision(t, repository, entry.Commit, source)
		sources[entry.Version] = source
		binary := filepath.Join(buildRoot, "dagrail-"+entry.Version)
		buildDAGrail(t, source, binary)
		assertBinaryVersion(t, binary, entry.Version)
		built := historicalBinary{version: entry.Version, path: binary}
		binaries = append(binaries, built)
		binaryByVersion[entry.Version] = built
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
	var legacyVerifyOutput string
	for _, binary := range binaries {
		if binary.version == "0.22.0" {
			break
		}
		legacyVerifyOutput = runHistorical(t, binary.path, dataRoot, "journal", "verify", "--root", projectRoot)
	}
	for _, releaseVersion := range []string{"0.22.0", "0.22.1"} {
		runHistoricalFailure(t, binaryByVersion[releaseVersion].path, dataRoot, "journal", "verify", "--root", projectRoot)
	}
	runHistoricalFailure(t, currentBinary, dataRoot, "journal", "verify", "--root", projectRoot)
	legacyLocator, err := os.ReadFile(filepath.Join(projectRoot, ".dagrail", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var legacyProject struct {
		ProjectID string `yaml:"projectId"`
	}
	if err := yaml.Unmarshal(legacyLocator, &legacyProject); err != nil || legacyProject.ProjectID == "" {
		t.Fatalf("decode legacy compatibility locator: %v", err)
	}
	var legacyVerification struct {
		HeadHash string `json:"headHash"`
	}
	if err := json.Unmarshal([]byte(legacyVerifyOutput), &legacyVerification); err != nil || legacyVerification.HeadHash == "" {
		t.Fatalf("decode legacy compatibility verification: %v %s", err, legacyVerifyOutput)
	}
	runHistorical(t, currentBinary, dataRoot, "recovery", "adopt-legacy-authority", "--root", projectRoot, "--expected-project-id", legacyProject.ProjectID, "--expected-current-head", legacyVerification.HeadHash, "--reason", "adopt historical compatibility window", "--idempotency-key", "adopt/historical-window")
	runHistorical(t, currentBinary, dataRoot, "journal", "verify", "--root", projectRoot)
	runHistorical(t, currentBinary, dataRoot, "recovery", "rehearse", "--root", projectRoot)

	// Authority migration keeps the Project v1alpha1 locator parseable while
	// schema-4 fences deliberately stop the immediately previous runtime from
	// reading or writing either retired or replacement authority.
	previousBinary := binaryByVersion["0.21.0"]
	if previousBinary.version != "0.21.0" {
		t.Fatalf("authority rollback fixture expected v0.21.0, got %s", previousBinary.version)
	}
	t.Run("v0.21-cannot-open-new-v0.22-authority", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "current-project")
		dataRoot := filepath.Join(t.TempDir(), "current-data")
		graphPath := filepath.Join(t.TempDir(), "current-graph.json")
		if err := os.WriteFile(graphPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"current-authority"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		runHistorical(t, currentBinary, dataRoot, "init", "--root", root, "--name", "current-authority")
		runHistoricalFailure(t, previousBinary.path, dataRoot, "journal", "verify", "--root", root)
		runHistoricalFailure(t, previousBinary.path, dataRoot, "graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "stale-v021-new-authority")
		runHistorical(t, currentBinary, dataRoot, "journal", "verify", "--root", root)
		runHistorical(t, currentBinary, dataRoot, "graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "current-authority-graph")
	})
	waitingWriterBinary := filepath.Join(buildRoot, "dagrail-0.21.0-waiting-writer")
	injectHistoricalWriterAdmissionHook(t, sources["0.21.0"])
	buildDAGrail(t, sources["0.21.0"], waitingWriterBinary)
	rotationRoot := filepath.Join(t.TempDir(), "rotation-project")
	rotationData := filepath.Join(t.TempDir(), "rotation-data")
	rotationGraph := filepath.Join(t.TempDir(), "rotation-graph.json")
	rotationDefinition := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"rotation-rollback"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(rotationGraph, []byte(rotationDefinition), 0o600); err != nil {
		t.Fatal(err)
	}
	runHistorical(t, previousBinary.path, rotationData, "init", "--root", rotationRoot, "--name", "rotation-rollback")
	runHistorical(t, previousBinary.path, rotationData, "graph", "import", "--root", rotationRoot, "--file", rotationGraph, "--idempotency-key", "rotation-graph")
	oldLocator, err := os.ReadFile(filepath.Join(rotationRoot, ".dagrail", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	backupOutput := runHistorical(t, previousBinary.path, rotationData, "journal", "verify", "--root", rotationRoot)
	var created struct {
		HeadHash string `json:"headHash"`
	}
	if err := json.Unmarshal([]byte(backupOutput), &created); err != nil || created.HeadHash == "" {
		t.Fatalf("decode authority rollback verification: %v %s", err, backupOutput)
	}
	var oldProject struct {
		ProjectID string `yaml:"projectId"`
	}
	if err := yaml.Unmarshal(oldLocator, &oldProject); err != nil || oldProject.ProjectID == "" {
		t.Fatalf("decode v0.21 project locator: %v", err)
	}
	adoptionOutput := runHistorical(t, currentBinary, rotationData, "recovery", "adopt-legacy-authority", "--root", rotationRoot, "--expected-project-id", oldProject.ProjectID, "--expected-current-head", created.HeadHash, "--reason", "adopt v0.21 rotation fixture", "--idempotency-key", "adopt/historical-rotation")
	var adoption struct {
		PreviousProjectID    string `json:"previousProjectId"`
		ReplacementProjectID string `json:"replacementProjectId"`
	}
	if err := json.Unmarshal([]byte(adoptionOutput), &adoption); err != nil || adoption.PreviousProjectID != oldProject.ProjectID || adoption.ReplacementProjectID == "" || adoption.ReplacementProjectID == oldProject.ProjectID {
		t.Fatalf("decode authority adoption receipt: %v %s", err, adoptionOutput)
	}
	// Both the retired source journal and the fresh replacement journal begin or
	// end with schema-4 fences. The exact v0.21 binary can parse Project v1alpha1
	// but cannot read or append either authority.
	runHistoricalFailure(t, previousBinary.path, rotationData, "journal", "verify", "--root", rotationRoot)
	runHistorical(t, currentBinary, rotationData, "journal", "verify", "--root", rotationRoot)
	staleRoot := filepath.Join(t.TempDir(), "stale-authority")
	if err := os.MkdirAll(filepath.Join(staleRoot, ".dagrail"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleRoot, ".dagrail", "project.yaml"), oldLocator, 0o600); err != nil {
		t.Fatal(err)
	}
	runHistoricalFailure(t, previousBinary.path, rotationData, "role", "bind", "--root", staleRoot, "--role", "worker", "--harness", "codex", "--session", "stale-v021", "--idempotency-key", "stale-v021-bind")
	runHistoricalFailure(t, currentBinary, rotationData, "journal", "verify", "--root", staleRoot)
	runHistorical(t, currentBinary, rotationData, "graph", "import", "--root", rotationRoot, "--file", rotationGraph, "--idempotency-key", "replacement-graph")
	replacementBackupPath := filepath.Join(t.TempDir(), "replacement-backup.json")
	replacementBackupOutput := runHistorical(t, currentBinary, rotationData, "backup", "create", "--root", rotationRoot, "--output", replacementBackupPath)
	var replacementBackup struct {
		Report struct {
			HeadHash string `json:"headHash"`
		} `json:"report"`
	}
	if err := json.Unmarshal([]byte(replacementBackupOutput), &replacementBackup); err != nil || replacementBackup.Report.HeadHash == "" {
		t.Fatalf("decode replacement authority backup: %v %s", err, replacementBackupOutput)
	}
	runHistorical(t, currentBinary, rotationData, "recovery", "rotate-authority", "--root", rotationRoot, "--backup", replacementBackupPath, "--expected-current-head", replacementBackup.Report.HeadHash, "--reason", "historical rollback proof", "--idempotency-key", "rotate/historical")
	runHistoricalFailure(t, previousBinary.path, rotationData, "journal", "verify", "--root", rotationRoot)
	runHistorical(t, currentBinary, rotationData, "journal", "verify", "--root", rotationRoot)

	t.Run("v0.22-established-replacement-relocates", func(t *testing.T) {
		v022 := binaryByVersion["0.22.0"]
		sourceRoot := filepath.Join(t.TempDir(), "source-project")
		targetRoot := filepath.Join(t.TempDir(), "target-project")
		sourceData := filepath.Join(t.TempDir(), "source-data")
		destinationData := filepath.Join(t.TempDir(), "destination-data")
		graphPath := filepath.Join(t.TempDir(), "relocation-graph.json")
		if err := os.WriteFile(graphPath, []byte(rotationDefinition), 0o600); err != nil {
			t.Fatal(err)
		}
		runHistorical(t, previousBinary.path, sourceData, "init", "--root", sourceRoot, "--name", "historical-relocation")
		runHistorical(t, previousBinary.path, sourceData, "graph", "import", "--root", sourceRoot, "--file", graphPath, "--idempotency-key", "relocation/legacy-graph")
		legacyLocator, err := os.ReadFile(filepath.Join(sourceRoot, ".dagrail", "project.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var legacyProject struct {
			ProjectID string `yaml:"projectId"`
		}
		if err := yaml.Unmarshal(legacyLocator, &legacyProject); err != nil || legacyProject.ProjectID == "" {
			t.Fatalf("decode relocation legacy locator: %v", err)
		}
		var legacyVerification struct {
			HeadHash string `json:"headHash"`
		}
		legacyOutput := runHistorical(t, previousBinary.path, sourceData, "journal", "verify", "--root", sourceRoot)
		if err := json.Unmarshal([]byte(legacyOutput), &legacyVerification); err != nil || legacyVerification.HeadHash == "" {
			t.Fatalf("decode relocation legacy verification: %v %s", err, legacyOutput)
		}
		runHistorical(t, v022.path, sourceData, "recovery", "adopt-legacy-authority", "--root", sourceRoot, "--expected-project-id", legacyProject.ProjectID, "--expected-current-head", legacyVerification.HeadHash, "--reason", "establish released v0.22 replacement", "--idempotency-key", "relocation/adopt-v022")
		runHistorical(t, v022.path, sourceData, "graph", "import", "--root", sourceRoot, "--file", graphPath, "--idempotency-key", "relocation/replacement-graph")
		backupPath := filepath.Join(t.TempDir(), "replacement-backup.json")
		backupOutput := runHistorical(t, v022.path, sourceData, "backup", "create", "--root", sourceRoot, "--output", backupPath)
		var backup struct {
			Report struct {
				HeadHash string `json:"headHash"`
			} `json:"report"`
		}
		if err := json.Unmarshal([]byte(backupOutput), &backup); err != nil || backup.Report.HeadHash == "" {
			t.Fatalf("decode released replacement backup: %v %s", err, backupOutput)
		}
		if err := os.MkdirAll(filepath.Join(targetRoot, ".dagrail"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(targetRoot, ".dagrail", "project.yaml"), legacyLocator, 0o600); err != nil {
			t.Fatal(err)
		}
		args := []string{"recovery", "relocate-authority", "--root", targetRoot, "--backup", backupPath, "--expected-project-id", legacyProject.ProjectID, "--expected-current-head", backup.Report.HeadHash, "--reason", "relocate released replacement", "--idempotency-key", "relocation/from-v022"}
		firstReceipt := runHistorical(t, currentBinary, destinationData, args...)
		runHistoricalFailure(t, v022.path, sourceData, "role", "bind", "--root", sourceRoot, "--role", "worker", "--harness", "codex", "--session", "stale-v022", "--idempotency-key", "relocation/stale-v022")
		runHistorical(t, currentBinary, destinationData, "graph", "import", "--root", targetRoot, "--file", graphPath, "--idempotency-key", "relocation/target-graph")
		if retried := runHistorical(t, currentBinary, destinationData, args...); retried != firstReceipt {
			t.Fatalf("exact relocation retry changed released receipt: first=%s retry=%s", firstReceipt, retried)
		}
	})

	emptyLegacyRoot := filepath.Join(t.TempDir(), "empty-v021")
	emptyLegacyData := filepath.Join(t.TempDir(), "empty-v021-data")
	runHistorical(t, previousBinary.path, emptyLegacyData, "init", "--root", emptyLegacyRoot, "--name", "empty-v021")
	emptyLocator, err := os.ReadFile(filepath.Join(emptyLegacyRoot, ".dagrail", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var emptyProject struct {
		ProjectID string `yaml:"projectId"`
	}
	if err := yaml.Unmarshal(emptyLocator, &emptyProject); err != nil || emptyProject.ProjectID == "" {
		t.Fatalf("decode empty v0.21 project locator: %v", err)
	}
	runHistoricalFailure(t, currentBinary, emptyLegacyData, "graph", "import", "--root", emptyLegacyRoot, "--file", rotationGraph, "--idempotency-key", "must-adopt-first")
	runHistorical(t, currentBinary, emptyLegacyData, "recovery", "adopt-legacy-authority", "--root", emptyLegacyRoot, "--expected-project-id", emptyProject.ProjectID, "--expected-current-head", "empty", "--reason", "adopt empty v0.21 fixture", "--idempotency-key", "adopt/empty-v021")
	runHistoricalFailure(t, previousBinary.path, emptyLegacyData, "graph", "import", "--root", emptyLegacyRoot, "--file", rotationGraph, "--idempotency-key", "stale-v021-after-empty-adoption")
	runHistorical(t, currentBinary, emptyLegacyData, "graph", "import", "--root", emptyLegacyRoot, "--file", rotationGraph, "--idempotency-key", "after-explicit-adoption")

	t.Run("v0.21-waiting-writer-orderings", func(t *testing.T) {
		testHistoricalWaitingWriterOrderings(t, waitingWriterBinary, currentBinary, rotationDefinition)
	})
}

func injectHistoricalWriterAdmissionHook(t *testing.T, source string) {
	t.Helper()
	path := filepath.Join(source, "internal", "journal", "journal.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	needle := "\terr = s.WithLock(func() error {"
	hook := `	if ready := os.Getenv("DAGRAIL_TEST_V021_APPEND_READY"); ready != "" {
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			return result, false, err
		}
		release := os.Getenv("DAGRAIL_TEST_V021_APPEND_RELEASE")
		for {
			if _, err := os.Stat(release); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	err = s.WithLock(func() error {`
	updated := strings.Replace(string(raw), needle, hook, 1)
	if updated == string(raw) {
		t.Fatal("v0.21 AppendOnce hook point was not found")
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testHistoricalWaitingWriterOrderings(t *testing.T, previousBinary, currentBinary, graph string) {
	for _, fenceFirst := range []bool{true, false} {
		name := "writer-first"
		if fenceFirst {
			name = "fence-first"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dataRoot := filepath.Join(t.TempDir(), "data")
			graphPath := filepath.Join(t.TempDir(), "graph.json")
			if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
				t.Fatal(err)
			}
			runHistorical(t, previousBinary, dataRoot, "init", "--root", root, "--name", name)
			runHistorical(t, previousBinary, dataRoot, "graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph")
			locator, err := os.ReadFile(filepath.Join(root, ".dagrail", "project.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var projectConfig struct {
				ProjectID string `yaml:"projectId"`
			}
			if err := yaml.Unmarshal(locator, &projectConfig); err != nil {
				t.Fatal(err)
			}
			backupOutput := runHistorical(t, previousBinary, dataRoot, "journal", "verify", "--root", root)
			var backup struct {
				HeadHash string `json:"headHash"`
			}
			if err := json.Unmarshal([]byte(backupOutput), &backup); err != nil || backup.HeadHash == "" {
				t.Fatalf("decode waiting-writer head: %v %s", err, backupOutput)
			}
			ready := filepath.Join(t.TempDir(), "ready")
			release := filepath.Join(t.TempDir(), "release")
			command := exec.Command(previousBinary, "role", "bind", "--root", root, "--role", "worker", "--harness", "codex", "--session", name, "--idempotency-key", "bind/"+name)
			command.Env = append(os.Environ(), "DAGRAIL_HOME="+dataRoot, "DAGRAIL_TEST_V021_APPEND_READY="+ready, "DAGRAIL_TEST_V021_APPEND_RELEASE="+release)
			var output boundedBuffer
			output.remaining = 64 * 1024
			command.Stdout, command.Stderr = &output, &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = command.Process.Kill() }()
			waitForHistoricalFile(t, ready)
			if fenceFirst {
				runHistorical(t, currentBinary, dataRoot, "recovery", "adopt-legacy-authority", "--root", root, "--expected-project-id", projectConfig.ProjectID, "--expected-current-head", backup.HeadHash, "--reason", "fence admitted v0.21 writer", "--idempotency-key", "adopt/fence-first")
				if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := command.Wait(); err == nil {
					t.Fatalf("admitted v0.21 writer appended after retirement fence: %s", output.String())
				}
				return
			}
			if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := command.Wait(); err != nil {
				t.Fatalf("writer-first ordering did not commit: %v %s", err, output.String())
			}
			runHistoricalFailure(t, currentBinary, dataRoot, "recovery", "adopt-legacy-authority", "--root", root, "--expected-project-id", projectConfig.ProjectID, "--expected-current-head", backup.HeadHash, "--reason", "stale fence must fail", "--idempotency-key", "adopt/writer-first")
		})
	}
}

func waitForHistoricalFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for historical helper %s", filepath.Base(path))
		}
		time.Sleep(time.Millisecond)
	}
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
	command := exec.CommandContext(ctx, "go", "build", "-tags=dagrail_testauthority", "-buildvcs=false", "-trimpath", "-o", output, "./cmd/dagrail")
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
	command.Env = historicalEnvironment(dataRoot)
	output := &boundedBuffer{remaining: 64 * 1024}
	command.Stdout, command.Stderr = output, output
	if err := command.Run(); err != nil {
		t.Fatalf("%s %s: %v: %s", binary, strings.Join(args, " "), err, output.String())
	}
	registerHistoricalDaemonCleanup(t, binary, dataRoot)
	return output.String()
}

func runHistoricalFailure(t *testing.T, binary, dataRoot string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = historicalEnvironment(dataRoot)
	output := &boundedBuffer{remaining: 64 * 1024}
	command.Stdout, command.Stderr = output, output
	if err := command.Run(); err == nil {
		t.Fatalf("%s %s unexpectedly succeeded: %s", binary, strings.Join(args, " "), output.String())
	}
	registerHistoricalDaemonCleanup(t, binary, dataRoot)
	return output.String()
}

func historicalEnvironment(dataRoot string) []string {
	return append(os.Environ(),
		"DAGRAIL_HOME="+dataRoot,
		"DAGRAIL_TEST_CONTROLLER_DIR="+filepath.Join(dataRoot, "controller"),
	)
}

func registerHistoricalDaemonCleanup(t *testing.T, binary, dataRoot string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, "daemon", "stop")
		command.Env = historicalEnvironment(dataRoot)
		_, _ = command.CombinedOutput()
	})
}
