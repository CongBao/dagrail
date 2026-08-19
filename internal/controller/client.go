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
	"time"

	"github.com/CongBao/dagrail/internal/version"
	"github.com/gofrs/flock"
)

type Client struct {
	endpoint   string
	executable string
	http       *http.Client
}

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
			if stopErr := client.Stop(ctx); stopErr != nil {
				return Status{}, fmt.Errorf("controller %s cannot drain for version or data-namespace change to %s: %w", status.Version, version.Version, stopErr)
			}
		}
		if waitErr := client.waitUnavailable(ctx, 5*time.Second); waitErr != nil {
			return Status{}, waitErr
		}
	}
	if err := client.spawn(); err != nil {
		return Status{}, err
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return Status{}, err
		}
		probeCtx, cancel = context.WithTimeout(ctx, 300*time.Millisecond)
		status, err = client.Status(probeCtx)
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
		time.Sleep(25 * time.Millisecond)
	}
	return Status{}, fmt.Errorf("controller did not become ready within 8 seconds")
}

func (client *Client) spawn() error {
	logName, err := logPath()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logName, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	command := exec.Command(client.executable, "daemon", "serve")
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = append(os.Environ(), "DAGRAIL_DAEMON_CHILD=1")
	detach(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start DAGrail controller: %w", err)
	}
	_ = logFile.Close()
	return command.Process.Release()
}

func (client *Client) Stop(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dagrail.local/v1/stop", bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("controller stop returned %s", response.Status)
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
