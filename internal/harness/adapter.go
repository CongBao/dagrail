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
	claude  claudeBackend
	copilot copilotBackend
}

type ProbeResult struct {
	Harness      string                  `json:"harness"`
	Detected     bool                    `json:"detected"`
	Executable   string                  `json:"executable,omitempty"`
	Version      string                  `json:"version,omitempty"`
	Capabilities sdk.HarnessCapabilities `json:"capabilities"`
	NativeMode   string                  `json:"nativeMode,omitempty"`
	Protocol     string                  `json:"protocol,omitempty"`
	Stability    string                  `json:"stability,omitempty"`
	Execution    string                  `json:"execution,omitempty"`
	ReceiptProof []string                `json:"receiptProof,omitempty"`
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
		return &Adapter{id: "claude-code", command: "claude", claude: claudeCLIBackend{}}, nil
	case "copilot", "copilot-cli":
		return &Adapter{id: "copilot-cli", command: "copilot", copilot: copilotACPBackend{}}, nil
	default:
		return nil, fmt.Errorf("unsupported harness %s", id)
	}
}

func (a *Adapter) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "harness." + a.id, Version: "0.3.0", SchemaHash: "sha256:harness-envelope-v3"}
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
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	path, err := exec.LookPath(a.command)
	result := ProbeResult{Harness: a.id, Detected: err == nil, Executable: path, Capabilities: sdk.HarnessCapabilities{ContextHook: true}, Fallback: "explicit launch envelope"}
	if err != nil && a.id == "codex" {
		for _, fallback := range []string{"/Applications/ChatGPT.app/Contents/Resources/codex", "/Applications/Codex.app/Contents/Resources/codex"} {
			if output, runErr := exec.CommandContext(probeCtx, fallback, "--version").CombinedOutput(); runErr == nil {
				result.Detected = true
				result.Executable = fallback
				result.Version = strings.TrimSpace(string(output))
				path, err = fallback, nil
				break
			}
		}
	}
	if err == nil {
		if output, runErr := exec.CommandContext(probeCtx, path, "--version").CombinedOutput(); runErr == nil {
			result.Version = strings.TrimSpace(string(output))
		}
	}
	var capability nativeCapability
	if result.Detected {
		switch a.id {
		case "codex":
			if a.codex != nil {
				capability = a.codex.Probe(probeCtx, result.Executable)
			}
		case "claude-code":
			if a.claude != nil {
				capability = a.claude.Probe(probeCtx, result.Executable)
			}
		case "copilot-cli":
			if a.copilot != nil {
				capability = a.copilot.Probe(probeCtx, result.Executable)
			}
		}
		result.NativeMode, result.Protocol, result.Reason = capability.Mode, capability.Protocol, capability.Reason
		result.Stability, result.Execution, result.ReceiptProof = capability.Stability, capability.Execution, capability.ReceiptProof
		if capability.Available {
			result.Capabilities.Dispatch = capability.Dispatch
			result.Capabilities.Resume = capability.Resume
			result.Capabilities.Inspect = capability.Inspect
			result.Capabilities.Cancel = capability.Cancel
		}
	} else {
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
	prompt := a.workPrompt(value.RoleID, value.NodeID, request.ActionID)
	probe := a.probeResult(ctx)
	if probe.Capabilities.Dispatch && (sessionID == "" || probe.Capabilities.Resume) {
		var receipt sdk.EffectReceipt
		switch a.id {
		case "codex":
			receipt, err = a.codex.Start(ctx, probe.Executable, codexStartRequest{WorkingDirectory: value.WorkingDirectory, Prompt: prompt, SessionID: sessionID, ClientMessageID: request.ActionID})
		case "claude-code":
			receipt, err = a.claude.Start(ctx, probe.Executable, nativeStartRequest{WorkingDirectory: value.WorkingDirectory, Prompt: prompt, SessionID: sessionID, ActionID: request.ActionID, PermissionPolicy: value.PermissionPolicy})
		case "copilot-cli":
			receipt, err = a.copilot.Start(ctx, probe.Executable, nativeStartRequest{WorkingDirectory: value.WorkingDirectory, Prompt: prompt, SessionID: sessionID, ActionID: request.ActionID, PermissionPolicy: value.PermissionPolicy})
		}
		if err != nil {
			return receipt, err
		}
		if err := validateNativeReceipt(receipt); err != nil {
			return sdk.EffectReceipt{}, fmt.Errorf("%s native receipt failed conformance: %w", a.id, err)
		}
		return receipt, nil
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
	probe := a.probeResult(ctx)
	if !probe.Capabilities.Inspect {
		return sdk.EffectReceipt{Status: "unknown", ExternalID: sessionID, SessionStatus: "unknown", DeliveryStatus: "unknown", AcceptanceStatus: "unknown", CompletionStatus: "unknown"}, nil
	}
	var receipt sdk.EffectReceipt
	var err error
	switch a.id {
	case "codex":
		receipt, err = a.codex.Observe(ctx, probe.Executable, sessionID, request.PriorReceipt)
	case "copilot-cli":
		value, decodeErr := decodeDispatchRequest(request.Request)
		if decodeErr != nil {
			return sdk.EffectReceipt{}, decodeErr
		}
		receipt, err = a.copilot.Observe(ctx, probe.Executable, sessionID, nativeStartRequest{WorkingDirectory: value.WorkingDirectory, Prompt: a.workPrompt(value.RoleID, value.NodeID, request.ActionID), SessionID: sessionID, ActionID: request.ActionID, PermissionPolicy: value.PermissionPolicy}, request.PriorReceipt)
	default:
		return sdk.EffectReceipt{Status: "unknown", ExternalID: sessionID, SessionStatus: "unknown", DeliveryStatus: "unknown", AcceptanceStatus: "unknown", CompletionStatus: "unknown"}, nil
	}
	if err != nil {
		return receipt, err
	}
	if err := validateNativeReceipt(receipt); err != nil {
		return sdk.EffectReceipt{}, fmt.Errorf("%s observed receipt failed conformance: %w", a.id, err)
	}
	return receipt, nil
}

type dispatchRequest struct {
	WorkingDirectory string `json:"workingDirectory"`
	RoleID           string `json:"roleId"`
	NodeID           string `json:"nodeId"`
	PermissionPolicy string `json:"permissionPolicy,omitempty"`
}

func decodeDispatchRequest(raw json.RawMessage) (dispatchRequest, error) {
	var value dispatchRequest
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, err
	}
	if value.PermissionPolicy != "" && value.PermissionPolicy != "deny" && value.PermissionPolicy != "allow-once" {
		return value, fmt.Errorf("permissionPolicy must be deny or allow-once")
	}
	return value, nil
}

func (a *Adapter) workPrompt(roleID, nodeID, actionID string) string {
	prompt := fmt.Sprintf("Use $execute-dag-node for DAGrail role %s and node %s. Read dag_context first; execute only returned allowed actions and checkpoint before yielding.", roleID, nodeID)
	if actionID != "" {
		prompt += " This delivery is bound to DAGrail action " + actionID + "."
	}
	return prompt
}
