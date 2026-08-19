package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

var runtimeDirForTesting struct {
	sync.RWMutex
	path string
}

// SetRuntimeDirForTesting isolates controller processes started by a test binary.
// Production callers have no environment-variable override: one OS user has one
// controller namespace. TestMain must explicitly opt in before spawning children.
func SetRuntimeDirForTesting(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("test controller runtime directory must be absolute")
	}
	runtimeDirForTesting.Lock()
	runtimeDirForTesting.path = filepath.Clean(path)
	runtimeDirForTesting.Unlock()
	return nil
}

func RuntimeDir() (string, error) {
	runtimeDirForTesting.RLock()
	override := runtimeDirForTesting.path
	runtimeDirForTesting.RUnlock()
	if override == "" {
		override = taggedRuntimeDir()
	}
	if override != "" {
		if !filepath.IsAbs(override) {
			return "", fmt.Errorf("test controller runtime directory must be absolute")
		}
		return override, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory: %w", err)
	}
	return filepath.Join(base, "dagrail", "controller"), nil
}

// dataNamespaceIdentity binds a daemon process to the authority-data namespace
// selected by its environment. The value is deliberately a digest: status and
// logs need enough information to prevent a client with a different
// DAGRAIL_HOME from reusing the process, but never need to disclose the path.
func dataNamespaceIdentity() (string, error) {
	root, err := dataNamespaceRoot()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte("dagrail-data-namespace-v1\x00" + filepath.Clean(root)))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func dataNamespaceRoot() (string, error) {
	if override := os.Getenv("DAGRAIL_HOME"); override != "" {
		return filepath.Abs(override)
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

func Endpoint() (string, error) {
	directory, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	return platformEndpoint(directory)
}

func logPath() (string, error) {
	directory, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "controller.log"), nil
}

func ensureRuntimeDir() (string, error) {
	directory, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return "", err
		}
	}
	return directory, nil
}
