//go:build !windows

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

func platformEndpoint(directory string) (string, error) {
	direct := filepath.Join(directory, "controller.sock")
	// sockaddr_un.sun_path is only 104 bytes on macOS (108 on Linux). Keep
	// caller-selected long runtime directories useful by moving only the
	// disposable socket into an owner-private, path-bound directory.
	if len(direct) <= 96 {
		return direct, nil
	}
	digest := sha256.Sum256([]byte(directory))
	name := fmt.Sprintf("dagrail-controller-%d-%s", os.Geteuid(), hex.EncodeToString(digest[:8]))
	// Use the conventional short Unix spelling rather than os.TempDir(): on
	// macOS TMPDIR itself lives below a very long per-user path and would still
	// overflow sun_path. The hashed directory is verified owner-only below.
	return filepath.Join("/tmp", name, "controller.sock"), nil
}

func listenEndpoint(endpoint string) (net.Listener, error) {
	directory := filepath.Dir(endpoint)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Stat(directory)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("controller endpoint directory is not owner-private: %s", directory)
	}
	if info, err := os.Lstat(endpoint); err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refuse to replace unsafe controller endpoint %s", endpoint)
		}
		if err := os.Remove(endpoint); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(endpoint, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

func dialEndpoint(ctx context.Context, endpoint string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", endpoint)
}

func removeEndpoint(endpoint string) { _ = os.Remove(endpoint) }
