package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"time"

	"github.com/CongBao/dagrail/internal/version"
	"github.com/gowebpki/jcs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ProbeReport struct {
	APIVersion                     string         `json:"apiVersion"`
	Kind                           string         `json:"kind"`
	ServerHandshakeReady           bool           `json:"serverHandshakeReady"`
	ProjectRoundTripReady          bool           `json:"projectRoundTripReady"`
	ToolCount                      int            `json:"toolCount"`
	ToolContracts                  []ToolContract `json:"toolContracts"`
	HandshakeDurationMillis        int64          `json:"handshakeDurationMillis"`
	ProjectRoundTripDurationMillis int64          `json:"projectRoundTripDurationMillis,omitempty"`
	DurationMillis                 int64          `json:"durationMillis"`
}

const (
	DefaultProbeHandshakeTimeout        = 10 * time.Second
	DefaultProbeProjectRoundTripTimeout = 60 * time.Second
)

type ProbeOptions struct {
	HandshakeTimeout        time.Duration
	ProjectRoundTripTimeout time.Duration
}

func Probe(ctx context.Context, executable, defaultRoot string) (ProbeReport, error) {
	return ProbeWithOptions(ctx, executable, defaultRoot, ProbeOptions{
		HandshakeTimeout:        DefaultProbeHandshakeTimeout,
		ProjectRoundTripTimeout: DefaultProbeProjectRoundTripTimeout,
	})
}

func ProbeWithOptions(ctx context.Context, executable, defaultRoot string, options ProbeOptions) (ProbeReport, error) {
	args := []string{"mcp", "--stdio"}
	if defaultRoot != "" {
		args = append(args, "--root", defaultRoot)
	}
	return probeWithConnector(ctx, defaultRoot, options, func(handshakeCtx context.Context) (*mcp.ClientSession, error) {
		client := mcp.NewClient(&mcp.Implementation{Name: "dagrail-probe", Version: "v" + version.Version}, nil)
		return client.Connect(handshakeCtx, &mcp.CommandTransport{Command: exec.CommandContext(ctx, executable, args...)}, nil)
	})
}

type probeConnector func(context.Context) (*mcp.ClientSession, error)

func probeWithConnector(ctx context.Context, defaultRoot string, options ProbeOptions, connect probeConnector) (ProbeReport, error) {
	started := time.Now()
	if options.HandshakeTimeout <= 0 {
		return ProbeReport{}, fmt.Errorf("MCP probe handshake timeout must be positive")
	}
	if options.ProjectRoundTripTimeout <= 0 {
		return ProbeReport{}, fmt.Errorf("MCP probe project round-trip timeout must be positive")
	}
	handshakeStarted := time.Now()
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, options.HandshakeTimeout)
	session, err := connect(handshakeCtx)
	if err != nil {
		cancelHandshake()
		return ProbeReport{}, fmt.Errorf("initialize fresh DAGrail MCP process: %w", err)
	}
	defer session.Close()
	listed, err := session.ListTools(handshakeCtx, nil)
	if err != nil {
		cancelHandshake()
		return ProbeReport{}, fmt.Errorf("list DAGrail MCP tools: %w", err)
	}
	handshakeDuration := time.Since(handshakeStarted)
	cancelHandshake()
	expected := ToolContracts()
	want := map[string]string{}
	for _, contract := range expected {
		want[contract.Name] = contract.InputSchemaSHA256
	}
	seen := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return ProbeReport{}, err
		}
		canonical, err := jcs.Transform(raw)
		if err != nil {
			return ProbeReport{}, err
		}
		digest := sha256.Sum256(append([]byte("dagrail-mcp-input-schema-v1\x00"), canonical...))
		actual := "sha256:" + hex.EncodeToString(digest[:])
		if expectedDigest, exists := want[tool.Name]; !exists || expectedDigest != actual {
			return ProbeReport{}, fmt.Errorf("MCP tool %s schema digest is not the linked contract", tool.Name)
		}
		seen = append(seen, tool.Name)
	}
	sort.Strings(seen)
	wantNames := make([]string, 0, len(expected))
	for _, contract := range expected {
		wantNames = append(wantNames, contract.Name)
	}
	sort.Strings(wantNames)
	if fmt.Sprint(seen) != fmt.Sprint(wantNames) {
		return ProbeReport{}, fmt.Errorf("MCP tool inventory mismatch: got %v want %v", seen, wantNames)
	}
	projectReady := false
	var projectDuration time.Duration
	if defaultRoot != "" {
		projectStarted := time.Now()
		projectCtx, cancelProject := context.WithTimeout(ctx, options.ProjectRoundTripTimeout)
		result, callErr := session.CallTool(projectCtx, &mcp.CallToolParams{Name: "dag_pre_wait", Arguments: map[string]any{"root": defaultRoot}})
		projectDuration = time.Since(projectStarted)
		cancelProject()
		if callErr != nil {
			return ProbeReport{}, fmt.Errorf("DAGrail MCP project round trip: %w", callErr)
		}
		if result.IsError {
			return ProbeReport{}, fmt.Errorf("DAGrail MCP project round trip returned a tool error")
		}
		projectReady = true
	}
	return ProbeReport{
		APIVersion: "dagrail.io/mcp-probe/v1alpha1", Kind: "MCPProbe",
		ServerHandshakeReady: true, ProjectRoundTripReady: projectReady,
		ToolCount: len(seen), ToolContracts: expected,
		HandshakeDurationMillis:        handshakeDuration.Milliseconds(),
		ProjectRoundTripDurationMillis: projectDuration.Milliseconds(),
		DurationMillis:                 time.Since(started).Milliseconds(),
	}, nil
}
