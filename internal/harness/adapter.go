package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/CongBao/dagrail/sdk"
)

type Adapter struct {
	id      string
	command string
}

type ProbeResult struct {
	Harness      string                  `json:"harness"`
	Detected     bool                    `json:"detected"`
	Executable   string                  `json:"executable,omitempty"`
	Version      string                  `json:"version,omitempty"`
	Capabilities sdk.HarnessCapabilities `json:"capabilities"`
	Fallback     string                  `json:"fallback"`
}

type Envelope struct {
	Harness           string   `json:"harness"`
	Command           string   `json:"command"`
	Arguments         []string `json:"arguments"`
	Prompt            string   `json:"prompt"`
	SessionID         string   `json:"sessionId,omitempty"`
	TransportAccepted bool     `json:"transportAccepted"`
	RecipientVisible  bool     `json:"recipientVisible"`
}

func New(id string) (*Adapter, error) {
	switch id {
	case "codex":
		return &Adapter{id: id, command: "codex"}, nil
	case "claude", "claude-code":
		return &Adapter{id: "claude-code", command: "claude"}, nil
	case "copilot", "copilot-cli":
		return &Adapter{id: "copilot-cli", command: "copilot"}, nil
	default:
		return nil, fmt.Errorf("unsupported harness %s", id)
	}
}

func (a *Adapter) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "harness." + a.id, Version: "0.1.0", SchemaHash: "sha256:harness-envelope-v1"}
}

func (a *Adapter) Probe(_ context.Context) (sdk.HarnessCapabilities, error) {
	result := a.ProbeResult()
	return result.Capabilities, nil
}

func (a *Adapter) ProbeResult() ProbeResult {
	path, err := exec.LookPath(a.command)
	result := ProbeResult{Harness: a.id, Detected: err == nil, Executable: path, Capabilities: sdk.HarnessCapabilities{ContextHook: true}, Fallback: "explicit launch envelope"}
	if err != nil && a.id == "codex" {
		for _, fallback := range []string{"/Applications/ChatGPT.app/Contents/Resources/codex", "/Applications/Codex.app/Contents/Resources/codex"} {
			if output, runErr := exec.Command(fallback, "--version").CombinedOutput(); runErr == nil {
				result.Detected = true
				result.Executable = fallback
				result.Version = strings.TrimSpace(string(output))
				return result
			}
		}
	}
	if err == nil {
		if output, runErr := exec.Command(path, "--version").CombinedOutput(); runErr == nil {
			result.Version = strings.TrimSpace(string(output))
		}
	}
	return result
}

func (a *Adapter) Envelope(root, roleID, nodeID, sessionID string) Envelope {
	prompt := fmt.Sprintf("Use $execute-dag-node for DAGrail role %s and node %s. Read dag_context first; execute only returned allowed actions and checkpoint before yielding.", roleID, nodeID)
	arguments := []string{}
	if sessionID != "" {
		switch a.id {
		case "codex":
			arguments = []string{"resume", sessionID}
		case "claude-code":
			arguments = []string{"--resume", sessionID}
		case "copilot-cli":
			arguments = []string{"--resume", sessionID}
		}
	} else {
		switch a.id {
		case "codex":
			arguments = []string{"-C", root, prompt}
		case "claude-code":
			arguments = []string{prompt}
		case "copilot-cli":
			arguments = []string{"-C", root, "-i", prompt}
		}
	}
	return Envelope{Harness: a.id, Command: a.command, Arguments: arguments, Prompt: prompt, SessionID: sessionID, TransportAccepted: false, RecipientVisible: false}
}

func (a *Adapter) Dispatch(_ context.Context, request sdk.EffectRequest) (sdk.EffectReceipt, error) {
	var value struct {
		WorkingDirectory string `json:"workingDirectory"`
		RoleID           string `json:"roleId"`
		NodeID           string `json:"nodeId"`
	}
	if err := json.Unmarshal(request.Request, &value); err != nil {
		return sdk.EffectReceipt{}, err
	}
	envelope := a.Envelope(value.WorkingDirectory, value.RoleID, value.NodeID, "")
	raw, _ := json.Marshal(envelope)
	return sdk.EffectReceipt{Status: "unknown", TransportStatus: "not-attempted", SessionStatus: "not-created", DeliveryStatus: "unknown", AcceptanceStatus: "unknown", CompletionStatus: "unknown", Detail: raw}, nil
}

func (a *Adapter) Resume(_ context.Context, sessionID string, request sdk.EffectRequest) (sdk.EffectReceipt, error) {
	var value struct {
		WorkingDirectory string `json:"workingDirectory"`
		RoleID           string `json:"roleId"`
		NodeID           string `json:"nodeId"`
	}
	if err := json.Unmarshal(request.Request, &value); err != nil {
		return sdk.EffectReceipt{}, err
	}
	envelope := a.Envelope(value.WorkingDirectory, value.RoleID, value.NodeID, sessionID)
	raw, _ := json.Marshal(envelope)
	return sdk.EffectReceipt{Status: "unknown", ExternalID: sessionID, TransportStatus: "not-attempted", SessionStatus: "unknown", DeliveryStatus: "unknown", AcceptanceStatus: "unknown", CompletionStatus: "unknown", Detail: raw}, nil
}
