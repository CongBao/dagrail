package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/commandcatalog"
	"github.com/CongBao/dagrail/internal/version"
	"github.com/gofrs/flock"
)

type Client struct {
	endpoint   string
	executable string
	http       *http.Client
}

const controllerStartupTimeout = 30 * time.Second

func NewClient() (*Client, error) {
	endpoint, err := Endpoint()
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return NewClientFor(endpoint, executable), nil
}

func NewClientFor(endpoint, executable string) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dialEndpoint(ctx, endpoint) }, DisableCompression: true}
	return &Client{endpoint: endpoint, executable: executable, http: &http.Client{Transport: transport}}
}

func (client *Client) Status(ctx context.Context) (Status, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://dagrail.local/v1/status", nil)
	if err != nil {
		return Status{}, err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return Status{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Status{}, fmt.Errorf("controller status returned %s", response.Status)
	}
	var status Status
	if err := decodeResponse(response.Body, &status); err != nil {
		return Status{}, err
	}
	if status.APIVersion != APIVersion || status.Kind != "ControllerStatus" {
		return Status{}, fmt.Errorf("controller returned an incompatible status")
	}
	return status, nil
}

func (client *Client) Ensure(ctx context.Context) (Status, error) {
	expectedNamespace, err := dataNamespaceIdentity()
	if err != nil {
		return Status{}, fmt.Errorf("resolve controller data namespace: %w", err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 350*time.Millisecond)
	status, err := client.Status(probeCtx)
	cancel()
	if err == nil && status.Version == version.Version && status.DataNamespace == expectedNamespace && !status.Draining {
		return status, nil
	}
	if err == nil && !status.Draining {
		if err := rejectControllerDowngrade(status.Version); err != nil {
			return Status{}, err
		}
	}
	directory, dirErr := ensureRuntimeDir()
	if dirErr != nil {
		return Status{}, dirErr
	}
	startLock := flock.New(filepath.Join(directory, "start.lock"))
	locked, lockErr := startLock.TryLockContext(ctx, 25*time.Millisecond)
	if lockErr != nil {
		return Status{}, lockErr
	}
	if !locked {
		if err := ctx.Err(); err != nil {
			return Status{}, err
		}
		return Status{}, fmt.Errorf("controller start lock was not acquired")
	}
	defer func() { _ = startLock.Unlock() }()
	probeCtx, cancel = context.WithTimeout(ctx, 350*time.Millisecond)
	status, err = client.Status(probeCtx)
	cancel()
	if err == nil {
		if status.Version == version.Version && status.DataNamespace == expectedNamespace && !status.Draining {
			return status, nil
		}
		if !status.Draining {
			if versionErr := rejectControllerDowngrade(status.Version); versionErr != nil {
				return Status{}, versionErr
			}
			if stopErr := client.replace(ctx, version.Version, expectedNamespace); stopErr != nil {
				return Status{}, fmt.Errorf("controller %s cannot drain for version or data-namespace change to %s: %w", status.Version, version.Version, stopErr)
			}
		}
		if waitErr := client.waitUnavailable(ctx, 5*time.Second); waitErr != nil {
			return Status{}, waitErr
		}
	}
	readyPath, err := client.spawn()
	if err != nil {
		return Status{}, err
	}
	defer os.Remove(readyPath)
	return client.waitReady(ctx, readyPath, expectedNamespace)
}

func (client *Client) waitReady(ctx context.Context, readyPath, expectedNamespace string) (Status, error) {
	deadline := time.NewTimer(controllerStartupTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	readyObserved := false
	for {
		if !readyObserved {
			if _, err := os.Stat(readyPath); err == nil {
				readyObserved = true
			} else if !os.IsNotExist(err) {
				return Status{}, fmt.Errorf("inspect controller startup readiness: %w", err)
			}
		}
		if readyObserved {
			probeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			status, err := client.Status(probeCtx)
			cancel()
			if err == nil {
				if status.Version != version.Version {
					return Status{}, fmt.Errorf("started controller version %s, expected %s", status.Version, version.Version)
				}
				if status.DataNamespace != expectedNamespace {
					return Status{}, fmt.Errorf("started controller uses a different authority-data namespace")
				}
				return status, nil
			}
		}
		select {
		case <-ctx.Done():
			return Status{}, ctx.Err()
		case <-deadline.C:
			return Status{}, fmt.Errorf("controller did not publish readiness within %s", controllerStartupTimeout)
		case <-ticker.C:
		}
	}
}

func (client *Client) spawn() (string, error) {
	directory, err := ensureRuntimeDir()
	if err != nil {
		return "", err
	}
	readyFile, err := os.CreateTemp(directory, ".controller-ready-*")
	if err != nil {
		return "", fmt.Errorf("reserve controller startup readiness: %w", err)
	}
	readyPath := readyFile.Name()
	if closeErr := readyFile.Close(); closeErr != nil {
		_ = os.Remove(readyPath)
		return "", closeErr
	}
	if err := os.Remove(readyPath); err != nil {
		return "", err
	}
	logName, err := logPath()
	if err != nil {
		return "", err
	}
	logFile, err := os.OpenFile(logName, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	command := exec.Command(client.executable, "daemon", "serve")
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = controllerChildEnvironment(os.Environ(), readyPath)
	detach(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		_ = os.Remove(readyPath)
		return "", fmt.Errorf("start DAGrail controller: %w", err)
	}
	_ = logFile.Close()
	if err := command.Process.Release(); err != nil {
		_ = os.Remove(readyPath)
		return "", err
	}
	return readyPath, nil
}

func controllerChildEnvironment(environment []string, readyPath string) []string {
	values := map[string]string{
		"DAGRAIL_DAEMON_CHILD":      "1",
		"DAGRAIL_DAEMON_READY_FILE": readyPath,
	}
	filtered := make([]string, 0, len(environment)+len(values))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, replace := values[name]; replace {
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	for name, value := range values {
		filtered = append(filtered, name+"="+value)
	}
	return filtered
}

func (client *Client) Stop(ctx context.Context) error {
	return client.requestStop(ctx, StopRequest{Reason: "shutdown"})
}

func (client *Client) Restart(ctx context.Context) error {
	expectedNamespace, err := dataNamespaceIdentity()
	if err != nil {
		return fmt.Errorf("resolve controller data namespace: %w", err)
	}
	return client.requestStop(ctx, StopRequest{Reason: "restart", TargetVersion: version.Version, TargetDataNamespace: expectedNamespace})
}

func (client *Client) replace(ctx context.Context, targetVersion, targetDataNamespace string) error {
	return client.requestStop(ctx, StopRequest{Reason: "replace", TargetVersion: targetVersion, TargetDataNamespace: targetDataNamespace})
}

func (client *Client) requestStop(ctx context.Context, input StopRequest) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dagrail.local/v1/stop", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure ExecuteResponse
		if decodeErr := decodeResponse(response.Body, &failure); decodeErr == nil && failure.Error != "" {
			return &RPCError{Code: failure.ErrorCode, Message: failure.Error, Retryable: failure.Retryable, Hint: failure.Hint}
		}
		return fmt.Errorf("controller stop returned %s", response.Status)
	}
	return nil
}

func rejectControllerDowngrade(runningVersion string) error {
	comparison, valid := compareSemanticVersions(runningVersion, version.Version)
	if !valid {
		return &RPCError{Code: "controller_version_incompatible", Message: fmt.Sprintf("running controller version %q cannot be safely compared with client version %q", runningVersion, version.Version), Hint: "Use the canonical installed DAGrail runtime and run 'dagrail plugin update --check'."}
	}
	if comparison > 0 {
		return &RPCError{Code: "client_outdated", Message: fmt.Sprintf("DAGrail client %s cannot replace newer controller %s", version.Version, runningVersion), Hint: "Use the canonical installed runtime reported by 'dagrail plugin status'; do not use a retained version-pinned binary for live governance."}
	}
	return nil
}

func (client *Client) UIStatus(ctx context.Context) (UIStatus, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://dagrail.local/v1/ui/status", nil)
	if err != nil {
		return UIStatus{}, err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return UIStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return UIStatus{}, fmt.Errorf("controller UI status returned %s", response.Status)
	}
	var status UIStatus
	err = decodeResponse(response.Body, &status)
	return status, err
}

func (client *Client) StartUI(ctx context.Context, input UIRequest) (UIStatus, error) {
	if _, err := client.Ensure(ctx); err != nil {
		return UIStatus{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return UIStatus{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dagrail.local/v1/ui/start", bytes.NewReader(payload))
	if err != nil {
		return UIStatus{}, err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return UIStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure ExecuteResponse
		if decodeErr := decodeResponse(response.Body, &failure); decodeErr == nil && failure.Error != "" {
			return UIStatus{}, &RPCError{Code: failure.ErrorCode, Message: failure.Error, Hint: failure.Hint}
		}
		return UIStatus{}, fmt.Errorf("controller UI start returned %s", response.Status)
	}
	var status UIStatus
	err = decodeResponse(response.Body, &status)
	return status, err
}

func (client *Client) StopUI(ctx context.Context) (UIStatus, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dagrail.local/v1/ui/stop", bytes.NewReader([]byte("{}")))
	if err != nil {
		return UIStatus{}, err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return UIStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return UIStatus{}, fmt.Errorf("controller UI stop returned %s", response.Status)
	}
	var status UIStatus
	err = decodeResponse(response.Body, &status)
	return status, err
}

func (client *Client) Execute(ctx context.Context, args []string, stdin []byte) ([]byte, []byte, error) {
	if _, err := client.Ensure(ctx); err != nil {
		return nil, nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve controller caller working directory: %w", err)
	}
	payload, err := json.Marshal(ExecuteRequest{APIVersion: APIVersion, Args: args, Stdin: stdin, CWD: cwd})
	if err != nil {
		return nil, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dagrail.local/v1/execute", bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	var result ExecuteResponse
	if err := decodeResponse(io.LimitReader(response.Body, MaxResultBytes), &result); err != nil {
		return nil, nil, err
	}
	if result.APIVersion != APIVersion {
		return nil, nil, fmt.Errorf("controller returned an incompatible response")
	}
	if result.Error != "" {
		return result.Stdout, result.Stderr, &RPCError{Code: result.ErrorCode, Message: result.Error, Retryable: result.Retryable, Hint: result.Hint}
	}
	if response.StatusCode != http.StatusOK {
		return result.Stdout, result.Stderr, fmt.Errorf("controller execute returned %s without a structured error", response.Status)
	}
	if !commandcatalog.ProjectReadOnly(args) && len(result.Stdout) == 0 {
		return nil, result.Stderr, &RPCError{
			Code:      "ambiguous_result",
			Message:   "controller mutation returned no receipt",
			Retryable: true,
			Hint:      "Inspect current state before continuing; if an idempotent retry is required, reuse the exact original key and inputs.",
		}
	}
	return result.Stdout, result.Stderr, nil
}

func (client *Client) waitUnavailable(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		_, err := client.Status(probeCtx)
		cancel()
		if err != nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("controller did not stop within %s", timeout)
}

func (client *Client) Logs() ([]byte, error) {
	path, err := logPath()
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	const limit = int64(1024 * 1024)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() > limit {
		_, _ = file.Seek(info.Size()-limit, io.SeekStart)
	}
	return io.ReadAll(io.LimitReader(file, limit))
}

func decodeResponse(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("controller response contains trailing content")
	}
	return nil
}
