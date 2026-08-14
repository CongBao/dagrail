package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/CongBao/dagrail/sdk"
)

type Adapter struct {
	id      string
	command string
	codex   codexBackend
}

type ProbeResult struct {
	Harness      string                  `json:"harness"`
	Detected     bool                    `json:"detected"`
	Executable   string                  `json:"executable,omitempty"`
	Version      string                  `json:"version,omitempty"`
	Capabilities sdk.HarnessCapabilities `json:"capabilities"`
	NativeMode   string                  `json:"nativeMode,omitempty"`
	Protocol     string                  `json:"protocol,omitempty"`
	Reason       string                  `json:"reason,omitempty"`
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
		return &Adapter{id: id, command: "codex", codex: appServerBackend{}}, nil
	case "claude", "claude-code":
		return &Adapter{id: "claude-code", command: "claude"}, nil
	case "copilot", "copilot-cli":
		return &Adapter{id: "copilot-cli", command: "copilot"}, nil
	default:
		return nil, fmt.Errorf("unsupported harness %s", id)
	}
}

func (a *Adapter) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "harness." + a.id, Version: "0.2.0", SchemaHash: "sha256:harness-envelope-v2"}
}

func (a *Adapter) Probe(ctx context.Context) (sdk.HarnessCapabilities, error) {
	result := a.probeResult(ctx)
	return result.Capabilities, nil
}

func (a *Adapter) ProbeResult() ProbeResult {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.probeResult(ctx)
}

func (a *Adapter) probeResult(ctx context.Context) ProbeResult {
	path, err := exec.LookPath(a.command)
	result := ProbeResult{Harness: a.id, Detected: err == nil, Executable: path, Capabilities: sdk.HarnessCapabilities{ContextHook: true}, Fallback: "explicit launch envelope"}
	if err != nil && a.id == "codex" {
		for _, fallback := range []string{"/Applications/ChatGPT.app/Contents/Resources/codex", "/Applications/Codex.app/Contents/Resources/codex"} {
			if output, runErr := exec.CommandContext(ctx, fallback, "--version").CombinedOutput(); runErr == nil {
				result.Detected = true
				result.Executable = fallback
				result.Version = strings.TrimSpace(string(output))
				path, err = fallback, nil
				break
			}
		}
	}
	if err == nil {
		if output, runErr := exec.CommandContext(ctx, path, "--version").CombinedOutput(); runErr == nil {
			result.Version = strings.TrimSpace(string(output))
		}
	}
	if a.id == "codex" && result.Detected && a.codex != nil {
		capability := a.codex.Probe(ctx, result.Executable)
		result.NativeMode, result.Protocol, result.Reason = capability.Mode, capability.Protocol, capability.Reason
		if capability.Available {
			result.Capabilities.Dispatch, result.Capabilities.Resume, result.Capabilities.Inspect = true, true, true
		}
	} else if !result.Detected {
		result.Reason = "harness executable not found"
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

func (a *Adapter) Dispatch(ctx context.Context, request sdk.EffectRequest) (sdk.EffectReceipt, error) {
	return a.dispatch(ctx, "", request)
}

func (a *Adapter) dispatch(ctx context.Context, sessionID string, request sdk.EffectRequest) (sdk.EffectReceipt, error) {
	value, err := decodeDispatchRequest(request.Request)
	if err != nil {
		return sdk.EffectReceipt{}, err
	}
	if a.id == "codex" && a.codex != nil {
		probe := a.probeResult(ctx)
		if probe.Capabilities.Dispatch && (sessionID == "" || probe.Capabilities.Resume) {
			return a.codex.Start(ctx, probe.Executable, codexStartRequest{WorkingDirectory: value.WorkingDirectory, Prompt: a.Envelope(value.WorkingDirectory, value.RoleID, value.NodeID, sessionID).Prompt, SessionID: sessionID, ClientMessageID: request.ActionID})
		}
	}
	envelope := a.Envelope(value.WorkingDirectory, value.RoleID, value.NodeID, sessionID)
	raw, _ := json.Marshal(envelope)
	sessionStatus := "not-created"
	if sessionID != "" {
		sessionStatus = "unknown"
	}
	return sdk.EffectReceipt{Status: "unknown", ExternalID: sessionID, TransportStatus: "not-attempted", SessionStatus: sessionStatus, DeliveryStatus: "unknown", AcceptanceStatus: "unknown", CompletionStatus: "unknown", Detail: raw}, nil
}

func (a *Adapter) Resume(ctx context.Context, sessionID string, request sdk.EffectRequest) (sdk.EffectReceipt, error) {
	return a.dispatch(ctx, sessionID, request)
}

func (a *Adapter) Observe(ctx context.Context, sessionID string, request sdk.EffectRequest) (sdk.EffectReceipt, error) {
	if a.id != "codex" || a.codex == nil {
		return sdk.EffectReceipt{Status: "unknown", ExternalID: sessionID, SessionStatus: "unknown", DeliveryStatus: "unknown", AcceptanceStatus: "unknown", CompletionStatus: "unknown"}, nil
	}
	probe := a.probeResult(ctx)
	if !probe.Capabilities.Inspect {
		return sdk.EffectReceipt{Status: "unknown", ExternalID: sessionID, SessionStatus: "unknown", DeliveryStatus: "unknown", AcceptanceStatus: "unknown", CompletionStatus: "unknown"}, nil
	}
	return a.codex.Observe(ctx, probe.Executable, sessionID, request.PriorReceipt)
}

type dispatchRequest struct {
	WorkingDirectory string `json:"workingDirectory"`
	RoleID           string `json:"roleId"`
	NodeID           string `json:"nodeId"`
}

func decodeDispatchRequest(raw json.RawMessage) (dispatchRequest, error) {
	var value dispatchRequest
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, err
	}
	return value, nil
}
