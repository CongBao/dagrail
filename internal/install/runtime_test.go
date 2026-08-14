package install

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeUpgradeAndRollbackAreDigestVerified(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	destination := filepath.Join(root, "bin", runtimeFilename())
	versionOne := filepath.Join(root, "source-v1")
	versionTwo := filepath.Join(root, "source-v2")
	if err := os.WriteFile(versionOne, []byte("runtime-v1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(versionTwo, []byte("runtime-v2"), 0o700); err != nil {
		t.Fatal(err)
	}
	probe := fixtureRuntimeProbe
	installed, err := installRuntimeFrom(context.Background(), versionOne, destination, dataRoot, probe)
	if err != nil || installed.Status != "installed" || installed.Version != "1.0.0" || installed.RollbackAvailable {
		t.Fatalf("initial install: %+v %v", installed, err)
	}
	upgraded, err := installRuntimeFrom(context.Background(), versionTwo, destination, dataRoot, probe)
	if err != nil || upgraded.Status != "upgraded" || upgraded.Version != "2.0.0" || upgraded.PreviousVersion != "1.0.0" || !upgraded.RollbackAvailable {
		t.Fatalf("upgrade: %+v %v", upgraded, err)
	}
	assertFileText(t, destination, "runtime-v2")
	receipt, err := readRuntimeReceipt(dataRoot)
	if err != nil || receipt.Previous == nil || !strings.Contains(receipt.Previous.Path, receipt.Previous.SHA256) {
		t.Fatalf("digest-addressed rollback receipt: %+v %v", receipt, err)
	}

	rolledBack, err := rollbackRuntime(context.Background(), dataRoot, probe)
	if err != nil || rolledBack.Status != "rolled_back" || rolledBack.Version != "1.0.0" || rolledBack.PreviousVersion != "2.0.0" {
		t.Fatalf("rollback: %+v %v", rolledBack, err)
	}
	assertFileText(t, destination, "runtime-v1")
	rolledForward, err := rollbackRuntime(context.Background(), dataRoot, probe)
	if err != nil || rolledForward.Version != "2.0.0" {
		t.Fatalf("verified rollback must remain reversible: %+v %v", rolledForward, err)
	}
	assertFileText(t, destination, "runtime-v2")
}

func TestInvalidCandidateAndCorruptBackupFailBeforeReplacement(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	destination := filepath.Join(root, "bin", runtimeFilename())
	versionOne := filepath.Join(root, "source-v1")
	versionTwo := filepath.Join(root, "source-v2")
	invalid := filepath.Join(root, "invalid")
	for path, value := range map[string]string{versionOne: "runtime-v1", versionTwo: "runtime-v2", invalid: "not-a-runtime"} {
		if err := os.WriteFile(path, []byte(value), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := installRuntimeFrom(context.Background(), versionOne, destination, dataRoot, fixtureRuntimeProbe); err != nil {
		t.Fatal(err)
	}
	if _, err := installRuntimeFrom(context.Background(), invalid, destination, dataRoot, fixtureRuntimeProbe); err == nil {
		t.Fatal("invalid candidate was installed")
	}
	assertFileText(t, destination, "runtime-v1")
	if _, err := installRuntimeFrom(context.Background(), versionTwo, destination, dataRoot, fixtureRuntimeProbe); err != nil {
		t.Fatal(err)
	}
	receipt, err := readRuntimeReceipt(dataRoot)
	if err != nil || receipt.Previous == nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receipt.Previous.Path, []byte("corrupt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := rollbackRuntime(context.Background(), dataRoot, fixtureRuntimeProbe); err == nil {
		t.Fatal("corrupt rollback artifact was used")
	}
	assertFileText(t, destination, "runtime-v2")
}

func TestInitialInstallIsRemovedWhenReceiptCannotCommit(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "not-a-directory")
	destination := filepath.Join(root, "bin", runtimeFilename())
	source := filepath.Join(root, "source-v1")
	if err := os.WriteFile(source, []byte("runtime-v1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataRoot, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := installRuntimeFrom(context.Background(), source, destination, dataRoot, fixtureRuntimeProbe); err == nil {
		t.Fatal("install succeeded without a durable receipt")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("unreceipted initial runtime remained at destination: %v", err)
	}
}

func TestRuntimeReceiptRejectsTrailingContentAndUnboundBackupPath(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	destination := filepath.Join(root, "bin", runtimeFilename())
	source := filepath.Join(root, "source-v1")
	if err := os.WriteFile(source, []byte("runtime-v1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := installRuntimeFrom(context.Background(), source, destination, dataRoot, fixtureRuntimeProbe); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(dataRoot, "install-receipt.json")
	raw, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, append(raw, []byte("{}")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRuntimeReceipt(dataRoot); err == nil {
		t.Fatal("receipt with trailing content was accepted")
	}

	var receipt RuntimeReceipt
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&receipt); err != nil {
		t.Fatal(err)
	}
	receipt.Previous = &RuntimeArtifact{Version: "1.0.0", SHA256: receipt.Current.SHA256, Path: source}
	modified, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, modified, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRuntimeReceipt(dataRoot); err == nil {
		t.Fatal("receipt with rollback path outside digest-addressed storage was accepted")
	}
}

func fixtureRuntimeProbe(_ context.Context, path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	switch string(raw) {
	case "runtime-v1":
		return "1.0.0", nil
	case "runtime-v2":
		return "2.0.0", nil
	default:
		return "", fmt.Errorf("invalid runtime fixture")
	}
}

func runtimeFilename() string {
	return "dagrail-test-runtime"
}

func assertFileText(t *testing.T, path, expected string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != expected {
		t.Fatalf("%s = %q, %v; want %q", path, raw, err, expected)
	}
}
