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

	"github.com/gowebpki/jcs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ProbeReport struct {
	APIVersion            string         `json:"apiVersion"`
	Kind                  string         `json:"kind"`
	ServerHandshakeReady  bool           `json:"serverHandshakeReady"`
	ProjectRoundTripReady bool           `json:"projectRoundTripReady"`
	ToolCount             int            `json:"toolCount"`
	ToolContracts         []ToolContract `json:"toolContracts"`
	DurationMillis        int64          `json:"durationMillis"`
}

func Probe(ctx context.Context, executable, defaultRoot string) (ProbeReport, error) {
	started := time.Now()
	args := []string{"mcp", "--stdio"}
	if defaultRoot != "" {
		args = append(args, "--root", defaultRoot)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "dagrail-probe", Version: "v0.26.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.CommandContext(ctx, executable, args...)}, nil)
	if err != nil {
		return ProbeReport{}, fmt.Errorf("initialize fresh DAGrail MCP process: %w", err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return ProbeReport{}, fmt.Errorf("list DAGrail MCP tools: %w", err)
	}
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
	if defaultRoot != "" {
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "dag_pre_wait", Arguments: map[string]any{"root": defaultRoot}})
		if callErr != nil {
			return ProbeReport{}, fmt.Errorf("DAGrail MCP project round trip: %w", callErr)
		}
		if result.IsError {
			return ProbeReport{}, fmt.Errorf("DAGrail MCP project round trip returned a tool error")
		}
		projectReady = true
	}
	return ProbeReport{APIVersion: "dagrail.io/mcp-probe/v1alpha1", Kind: "MCPProbe", ServerHandshakeReady: true, ProjectRoundTripReady: projectReady, ToolCount: len(seen), ToolContracts: expected, DurationMillis: time.Since(started).Milliseconds()}, nil
}
