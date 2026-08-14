package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/version"
	"github.com/google/uuid"
)

const runtimeReceiptSchemaVersion = 2

type RuntimeArtifact struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Path    string `json:"path"`
}

type RuntimeReceipt struct {
	SchemaVersion int              `json:"schemaVersion"`
	RuntimePath   string           `json:"runtimePath"`
	Current       RuntimeArtifact  `json:"current"`
	Previous      *RuntimeArtifact `json:"previous,omitempty"`
	UpdatedAt     string           `json:"updatedAt"`
}

type runtimeProbe func(context.Context, string) (string, error)

func InstallRuntime() (RuntimeResult, error) {
	source, err := os.Executable()
	if err != nil {
		return RuntimeResult{}, err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return RuntimeResult{}, err
	}
	destination, err := DefaultRuntimePath()
	if err != nil {
		return RuntimeResult{}, err
	}
	dataRoot, err := runtimeDataRoot()
	if err != nil {
		return RuntimeResult{}, err
	}
	result, err := installRuntimeFrom(context.Background(), source, destination, dataRoot, runtimeVersion)
	if err == nil && result.Version != version.Version {
		return RuntimeResult{}, fmt.Errorf("running source version %s does not match linked version %s", result.Version, version.Version)
	}
	return result, err
}

func RollbackRuntime() (RuntimeResult, error) {
	dataRoot, err := runtimeDataRoot()
	if err != nil {
		return RuntimeResult{}, err
	}
	return rollbackRuntime(context.Background(), dataRoot, runtimeVersion)
}

func RuntimeStatus() (RuntimeResult, error) {
	dataRoot, err := runtimeDataRoot()
	if err != nil {
		return RuntimeResult{}, err
	}
	receipt, err := readRuntimeReceipt(dataRoot)
	if err != nil {
		return RuntimeResult{}, err
	}
	if err := verifyRuntimeArtifact(context.Background(), receipt.Current, runtimeVersion); err != nil {
		return RuntimeResult{}, fmt.Errorf("current runtime verification failed: %w", err)
	}
	if receipt.Previous != nil {
		if err := verifyRuntimeArtifact(context.Background(), *receipt.Previous, runtimeVersion); err != nil {
			return RuntimeResult{}, fmt.Errorf("rollback runtime verification failed: %w", err)
		}
	}
	return runtimeResult("verified", receipt.Current, receipt.Previous), nil
}

func installRuntimeFrom(ctx context.Context, source, destination, dataRoot string, probe runtimeProbe) (RuntimeResult, error) {
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) || !filepath.IsAbs(dataRoot) {
		return RuntimeResult{}, fmt.Errorf("source, runtime, and data paths must be absolute")
	}
	sourceVersion, err := probe(ctx, source)
	if err != nil {
		return RuntimeResult{}, fmt.Errorf("candidate runtime failed fresh-process validation: %w", err)
	}
	if sourceVersion == "" {
		return RuntimeResult{}, fmt.Errorf("candidate runtime fresh-process validation returned no version")
	}
	sourceDigest, err := fileSHA256(source)
	if err != nil {
		return RuntimeResult{}, err
	}
	status := "installed"
	var previous *RuntimeArtifact
	if destinationDigest, digestErr := fileSHA256(destination); digestErr == nil {
		destinationVersion, probeErr := probe(ctx, destination)
		if probeErr != nil {
			return RuntimeResult{}, fmt.Errorf("installed runtime is not safely upgradable: %w", probeErr)
		}
		if destinationDigest == sourceDigest {
			status = "noop"
			if receipt, readErr := readRuntimeReceipt(dataRoot); readErr == nil && receipt.Current.SHA256 == sourceDigest {
				previous = receipt.Previous
			}
			current := RuntimeArtifact{Version: sourceVersion, SHA256: sourceDigest, Path: destination}
			if err := writeRuntimeReceipt(dataRoot, RuntimeReceipt{SchemaVersion: runtimeReceiptSchemaVersion, RuntimePath: destination, Current: current, Previous: previous, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
				return RuntimeResult{}, err
			}
			return runtimeResult(status, current, previous), nil
		}
		backup, backupErr := storeRuntimeBackup(ctx, dataRoot, destination, RuntimeArtifact{Version: destinationVersion, SHA256: destinationDigest}, probe)
		if backupErr != nil {
			return RuntimeResult{}, fmt.Errorf("preserve rollback runtime: %w", backupErr)
		}
		previous, status = &backup, "upgraded"
	} else if !os.IsNotExist(digestErr) {
		return RuntimeResult{}, fmt.Errorf("inspect runtime destination: %w", digestErr)
	}

	if err := publishExecutable(source, destination); err != nil {
		if previous != nil {
			_ = publishExecutable(previous.Path, destination)
		}
		return RuntimeResult{}, err
	}
	current := RuntimeArtifact{Version: sourceVersion, SHA256: sourceDigest, Path: destination}
	if err := verifyRuntimeArtifact(ctx, current, probe); err != nil {
		if previous != nil {
			_ = publishExecutable(previous.Path, destination)
		} else {
			removeRuntimeIfDigest(destination, sourceDigest)
		}
		return RuntimeResult{}, fmt.Errorf("published runtime validation failed; previous runtime restored when available: %w", err)
	}
	receipt := RuntimeReceipt{SchemaVersion: runtimeReceiptSchemaVersion, RuntimePath: destination, Current: current, Previous: previous, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := writeRuntimeReceipt(dataRoot, receipt); err != nil {
		if previous != nil {
			_ = publishExecutable(previous.Path, destination)
		} else {
			removeRuntimeIfDigest(destination, sourceDigest)
		}
		return RuntimeResult{}, fmt.Errorf("write install receipt; previous runtime restored when available: %w", err)
	}
	return runtimeResult(status, current, previous), nil
}

func rollbackRuntime(ctx context.Context, dataRoot string, probe runtimeProbe) (RuntimeResult, error) {
	receipt, err := readRuntimeReceipt(dataRoot)
	if err != nil {
		return RuntimeResult{}, err
	}
	if receipt.SchemaVersion != runtimeReceiptSchemaVersion || receipt.Previous == nil {
		return RuntimeResult{}, fmt.Errorf("no verified rollback runtime is available")
	}
	if err := verifyRuntimeArtifact(ctx, receipt.Current, probe); err != nil {
		return RuntimeResult{}, fmt.Errorf("current runtime does not match install receipt: %w", err)
	}
	if err := verifyRuntimeArtifact(ctx, *receipt.Previous, probe); err != nil {
		return RuntimeResult{}, fmt.Errorf("rollback runtime does not match install receipt: %w", err)
	}
	currentBackup, err := storeRuntimeBackup(ctx, dataRoot, receipt.Current.Path, receipt.Current, probe)
	if err != nil {
		return RuntimeResult{}, err
	}
	if err := publishExecutable(receipt.Previous.Path, receipt.RuntimePath); err != nil {
		_ = publishExecutable(currentBackup.Path, receipt.RuntimePath)
		return RuntimeResult{}, err
	}
	newCurrent := RuntimeArtifact{Version: receipt.Previous.Version, SHA256: receipt.Previous.SHA256, Path: receipt.RuntimePath}
	if err := verifyRuntimeArtifact(ctx, newCurrent, probe); err != nil {
		_ = publishExecutable(currentBackup.Path, receipt.RuntimePath)
		return RuntimeResult{}, fmt.Errorf("rollback validation failed; current runtime restored: %w", err)
	}
	newReceipt := RuntimeReceipt{SchemaVersion: runtimeReceiptSchemaVersion, RuntimePath: receipt.RuntimePath, Current: newCurrent, Previous: &currentBackup, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := writeRuntimeReceipt(dataRoot, newReceipt); err != nil {
		_ = publishExecutable(currentBackup.Path, receipt.RuntimePath)
		return RuntimeResult{}, fmt.Errorf("rollback receipt failed; current runtime restored: %w", err)
	}
	return runtimeResult("rolled_back", newCurrent, &currentBackup), nil
}

func runtimeResult(status string, current RuntimeArtifact, previous *RuntimeArtifact) RuntimeResult {
	result := RuntimeResult{Status: status, Version: current.Version, RuntimePath: current.Path, SHA256: current.SHA256, RollbackAvailable: previous != nil}
	if previous != nil {
		result.PreviousVersion = previous.Version
	}
	return result
}

func storeRuntimeBackup(ctx context.Context, dataRoot, source string, artifact RuntimeArtifact, probe runtimeProbe) (RuntimeArtifact, error) {
	if !validHexDigest(artifact.SHA256) {
		return RuntimeArtifact{}, fmt.Errorf("invalid runtime digest")
	}
	path := runtimeBackupPath(dataRoot, artifact.SHA256)
	artifact.Path = path
	if existingDigest, err := fileSHA256(path); err == nil {
		if existingDigest != artifact.SHA256 {
			return RuntimeArtifact{}, fmt.Errorf("digest-addressed runtime backup is corrupt")
		}
		if err := verifyRuntimeArtifact(ctx, artifact, probe); err != nil {
			return RuntimeArtifact{}, err
		}
		return artifact, nil
	} else if !os.IsNotExist(err) {
		return RuntimeArtifact{}, err
	}
	if err := publishExecutable(source, path); err != nil {
		return RuntimeArtifact{}, err
	}
	if err := verifyRuntimeArtifact(ctx, artifact, probe); err != nil {
		removeRuntimeIfDigest(path, artifact.SHA256)
		return RuntimeArtifact{}, err
	}
	return artifact, nil
}

func verifyRuntimeArtifact(ctx context.Context, artifact RuntimeArtifact, probe runtimeProbe) error {
	digest, err := fileSHA256(artifact.Path)
	if err != nil || digest != artifact.SHA256 {
		return fmt.Errorf("runtime digest mismatch")
	}
	observedVersion, err := probe(ctx, artifact.Path)
	if err != nil {
		return err
	}
	if observedVersion != artifact.Version {
		return fmt.Errorf("runtime version mismatch: expected %s, observed %s", artifact.Version, observedVersion)
	}
	return nil
}

func validateFreshRuntime(ctx context.Context, path string) error {
	_, err := runtimeVersion(ctx, path)
	return err
}

func runtimeVersion(ctx context.Context, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("runtime path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("runtime %s is not a regular executable file", path)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(probeCtx, path, "version")
	command.Env = append(os.Environ(), "DAGRAIL_FRESH_PROCESS_PROBE=1")
	stdout, stderr := &boundedBuffer{remaining: 64 * 1024}, &boundedBuffer{remaining: 16 * 1024}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("fresh runtime probe failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var value struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &value); err != nil || value.Version == "" {
		return "", fmt.Errorf("fresh runtime probe returned an invalid version envelope")
	}
	return value.Version, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("runtime must be a regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func removeRuntimeIfDigest(path, expectedDigest string) {
	if digest, err := fileSHA256(path); err == nil && digest == expectedDigest {
		_ = os.Remove(path)
	}
}

func publishExecutable(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".dagrail-runtime-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	sourceFile, err := os.Open(source)
	if err != nil {
		_ = temporary.Close()
		return err
	}
	_, copyErr := io.Copy(temporary, sourceFile)
	closeSourceErr := sourceFile.Close()
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := errors.Join(copyErr, closeSourceErr, syncErr, closeErr); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(temporaryPath, 0o755); err != nil {
			return err
		}
	}
	return replaceFile(temporaryPath, destination)
}

func replaceFile(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return syncDirectory(filepath.Dir(destination))
	} else if _, statErr := os.Stat(destination); os.IsNotExist(statErr) {
		return fmt.Errorf("publish runtime: %w", err)
	}
	swap := destination + ".swap-" + uuid.NewString()
	if err := os.Rename(destination, swap); err != nil {
		return fmt.Errorf("stage existing runtime: %w", err)
	}
	if err := os.Rename(source, destination); err != nil {
		_ = os.Rename(swap, destination)
		return fmt.Errorf("publish runtime: %w", err)
	}
	if err := os.Remove(swap); err != nil {
		return fmt.Errorf("remove replaced runtime: %w", err)
	}
	return syncDirectory(filepath.Dir(destination))
}

func writeRuntimeReceipt(dataRoot string, receipt RuntimeReceipt) error {
	if receipt.SchemaVersion != runtimeReceiptSchemaVersion || !validRuntimeArtifact(receipt.Current) || !filepath.IsAbs(receipt.RuntimePath) || receipt.Current.Path != receipt.RuntimePath {
		return fmt.Errorf("invalid runtime receipt")
	}
	if receipt.Previous != nil && !validRuntimeArtifact(*receipt.Previous) {
		return fmt.Errorf("invalid rollback artifact in runtime receipt")
	}
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dataRoot, ".install-receipt-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceFile(temporaryPath, filepath.Join(dataRoot, "install-receipt.json"))
}

func readRuntimeReceipt(dataRoot string) (RuntimeReceipt, error) {
	path := filepath.Join(dataRoot, "install-receipt.json")
	info, err := os.Lstat(path)
	if err != nil {
		return RuntimeReceipt{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64*1024 {
		return RuntimeReceipt{}, fmt.Errorf("invalid install receipt file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return RuntimeReceipt{}, err
	}
	var header struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return RuntimeReceipt{}, fmt.Errorf("invalid install receipt JSON")
	}
	if header.SchemaVersion == 1 {
		var legacy struct {
			SchemaVersion int    `json:"schemaVersion"`
			Version       string `json:"version"`
			RuntimePath   string `json:"runtimePath"`
			SHA256        string `json:"sha256"`
			InstalledAt   string `json:"installedAt"`
		}
		if json.Unmarshal(raw, &legacy) != nil || legacy.Version == "" || !filepath.IsAbs(legacy.RuntimePath) || !validHexDigest(legacy.SHA256) {
			return RuntimeReceipt{}, fmt.Errorf("invalid legacy install receipt")
		}
		return RuntimeReceipt{SchemaVersion: 1, RuntimePath: legacy.RuntimePath, Current: RuntimeArtifact{Version: legacy.Version, SHA256: legacy.SHA256, Path: legacy.RuntimePath}, UpdatedAt: legacy.InstalledAt}, nil
	}
	if header.SchemaVersion != runtimeReceiptSchemaVersion {
		return RuntimeReceipt{}, fmt.Errorf("unsupported install receipt schema %d", header.SchemaVersion)
	}
	var receipt RuntimeReceipt
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil || !filepath.IsAbs(receipt.RuntimePath) || receipt.Current.Path != receipt.RuntimePath || !validRuntimeArtifact(receipt.Current) {
		return RuntimeReceipt{}, fmt.Errorf("invalid install receipt")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return RuntimeReceipt{}, fmt.Errorf("install receipt has trailing content")
	}
	if receipt.Previous != nil && (!validRuntimeArtifact(*receipt.Previous) || receipt.Previous.Path != runtimeBackupPath(dataRoot, receipt.Previous.SHA256)) {
		return RuntimeReceipt{}, fmt.Errorf("invalid rollback artifact in install receipt")
	}
	return receipt, nil
}

func validRuntimeArtifact(artifact RuntimeArtifact) bool {
	return artifact.Version != "" && filepath.IsAbs(artifact.Path) && validHexDigest(artifact.SHA256)
}

func runtimeBackupPath(dataRoot, digest string) string {
	name := "dagrail"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dataRoot, "runtime-backups", digest, name)
}

func runtimeDataRoot() (string, error) {
	if root := os.Getenv("DAGRAIL_HOME"); root != "" {
		absolute, err := filepath.Abs(root)
		return filepath.Clean(absolute), err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "dagrail"), nil
	case "windows":
		root := os.Getenv("LOCALAPPDATA")
		if root == "" {
			root = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(root, "DAGrail"), nil
	default:
		root := os.Getenv("XDG_DATA_HOME")
		if root == "" {
			root = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(root, "dagrail"), nil
	}
}

func validHexDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (writer *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	if len(data) > writer.remaining {
		data = data[:writer.remaining]
	}
	_, _ = writer.buffer.Write(data)
	writer.remaining -= len(data)
	return original, nil
}

func (writer *boundedBuffer) Bytes() []byte  { return writer.buffer.Bytes() }
func (writer *boundedBuffer) String() string { return writer.buffer.String() }
