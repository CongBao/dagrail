// Package controller implements DAGrail's owner-local, multi-project daemon.
// It deliberately transports typed CLI operations without becoming a scheduler:
// command semantics remain in the service/reducer layer and the journal remains
// the only canonical authority.
package controller

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"
)

const (
	APIVersion      = "dagrail.io/local-controller/v1alpha1"
	MaxRequestBytes = 4 * 1024 * 1024
	MaxResultBytes  = 32 * 1024 * 1024
)

type Status struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	PID        int    `json:"pid"`
	StartedAt  string `json:"startedAt"`
	Endpoint   string `json:"endpoint"`
	// DataNamespace is a path-hiding digest of the authority-data root used by
	// this process. Clients with another DAGRAIL_HOME drain and restart instead
	// of silently opening the same Project locator against the wrong store.
	DataNamespace string `json:"dataNamespace"`
	Draining      bool   `json:"draining"`
	Projects      int    `json:"projects"`
	Requests      uint64 `json:"requests"`
}

type ExecuteRequest struct {
	APIVersion string          `json:"apiVersion"`
	Args       []string        `json:"args"`
	Stdin      json.RawMessage `json:"stdin,omitempty"`
	CWD        string          `json:"cwd"`
}

type ExecuteResponse struct {
	APIVersion string `json:"apiVersion"`
	Stdout     []byte `json:"stdout,omitempty"`
	Stderr     []byte `json:"stderr,omitempty"`
	Error      string `json:"error,omitempty"`
	ErrorCode  string `json:"errorCode,omitempty"`
	Hint       string `json:"hint,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`
}

type StopRequest struct {
	Reason              string `json:"reason"`
	TargetVersion       string `json:"targetVersion,omitempty"`
	TargetDataNamespace string `json:"targetDataNamespace,omitempty"`
}

type UIRequest struct {
	Root   string `json:"root"`
	Listen string `json:"listen"`
	Open   bool   `json:"open"`
}

type UIStatus struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Running    bool   `json:"running"`
	Root       string `json:"root,omitempty"`
	URL        string `json:"url,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
}

type RPCError struct {
	Code      string
	Message   string
	Retryable bool
	Hint      string
}

func (e *RPCError) Error() string { return e.Message }

func validateExecuteRequest(request ExecuteRequest) error {
	if request.APIVersion != APIVersion {
		return fmt.Errorf("unsupported local controller API %q", request.APIVersion)
	}
	if len(request.Args) == 0 {
		return fmt.Errorf("controller command is required")
	}
	if !filepath.IsAbs(request.CWD) {
		return fmt.Errorf("controller caller working directory must be absolute")
	}
	for _, arg := range request.Args {
		if len(arg) > 1024*1024 {
			return fmt.Errorf("controller argument exceeds 1 MiB")
		}
	}
	if len(request.Stdin) > MaxRequestBytes {
		return fmt.Errorf("controller stdin exceeds %d bytes", MaxRequestBytes)
	}
	return nil
}

func nowString() string { return time.Now().UTC().Format(time.RFC3339Nano) }
