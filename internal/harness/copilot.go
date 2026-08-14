package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/CongBao/dagrail/internal/version"
	"github.com/CongBao/dagrail/sdk"
)

const (
	copilotProtocol     = "agent-client-protocol-v1"
	copilotProtocolID   = 1
	copilotMessageLimit = 16 * 1024 * 1024
)

type copilotBackend interface {
	Probe(context.Context, string) nativeCapability
	Start(context.Context, string, nativeStartRequest) (sdk.EffectReceipt, error)
	Observe(context.Context, string, string, nativeStartRequest, json.RawMessage) (sdk.EffectReceipt, error)
}

type copilotACPBackend struct{}

type acpInitializeResult struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		LoadSession bool `json:"loadSession"`
	} `json:"agentCapabilities"`
	AgentInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"agentInfo"`
}

func (copilotACPBackend) Probe(ctx context.Context, executable string) nativeCapability {
	result := nativeCapability{Mode: "manual", Stability: "experimental", Execution: "synchronous-turn", Reason: "GitHub Copilot ACP v1 capability unavailable"}
	if executable == "" {
		result.Reason = "GitHub Copilot CLI executable not found"
		return result
	}
	err := withCopilotACP(ctx, executable, "", "deny", func(_ *acpClient, initialized acpInitializeResult) error {
		if initialized.ProtocolVersion != copilotProtocolID {
			return fmt.Errorf("Copilot negotiated unsupported ACP version %d", initialized.ProtocolVersion)
		}
		return nil
	})
	if err != nil {
		result.Reason = boundedText(err.Error(), 1024)
		return result
	}
	result.Available, result.Dispatch = true, true
	result.Mode, result.Protocol, result.Reason = "native-acp-stdio-completion", copilotProtocol, ""
	result.ReceiptProof = []string{"ephemeral-session-created", "bound-prompt-response", "synchronous-stop-reason"}
	return result
}

func (copilotACPBackend) Start(ctx context.Context, executable string, request nativeStartRequest) (sdk.EffectReceipt, error) {
	turnCtx, cancel := context.WithTimeout(ctx, nativeTurnLimit)
	defer cancel()
	return withCopilotACPReceipt(turnCtx, executable, request.WorkingDirectory, request.PermissionPolicy, func(client *acpClient, initialized acpInitializeResult) (sdk.EffectReceipt, error) {
		client.Expect(request.Prompt)
		sessionID := request.SessionID
		if sessionID == "" {
			result, err := client.Call("session/new", map[string]any{"cwd": request.WorkingDirectory, "mcpServers": []any{}})
			if err != nil {
				return sdk.EffectReceipt{}, err
			}
			var response struct {
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal(result, &response) != nil || response.SessionID == "" {
				return sdk.EffectReceipt{}, fmt.Errorf("Copilot ACP session/new returned no session ID")
			}
			sessionID = response.SessionID
		} else {
			if !initialized.AgentCapabilities.LoadSession {
				return sdk.EffectReceipt{}, fmt.Errorf("Copilot ACP resume requires session/load capability")
			}
			if _, err := client.Call("session/load", map[string]any{"sessionId": sessionID, "cwd": request.WorkingDirectory, "mcpServers": []any{}}); err != nil {
				return sdk.EffectReceipt{}, err
			}
		}

		result, err := client.Call("session/prompt", map[string]any{"sessionId": sessionID, "prompt": []map[string]string{{"type": "text", "text": request.Prompt}}})
		if err != nil {
			return sdk.EffectReceipt{ExternalID: sessionID, TransportAccepted: true, TransportStatus: "accepted", SessionStatus: "created", DeliveryStatus: "unknown", AcceptanceStatus: "pending", CompletionStatus: "unknown"}, err
		}
		var response struct {
			StopReason string `json:"stopReason"`
		}
		if json.Unmarshal(result, &response) != nil || response.StopReason == "" {
			return sdk.EffectReceipt{}, fmt.Errorf("Copilot ACP session/prompt returned no stop reason")
		}
		return copilotTurnReceipt(client, request, sessionID, response.StopReason), nil
	})
}

func (copilotACPBackend) Observe(ctx context.Context, executable, sessionID string, request nativeStartRequest, priorRaw json.RawMessage) (sdk.EffectReceipt, error) {
	observeCtx, cancel := context.WithTimeout(ctx, nativeTurnLimit)
	defer cancel()
	var prior sdk.EffectReceipt
	if json.Unmarshal(priorRaw, &prior) != nil {
		return sdk.EffectReceipt{}, fmt.Errorf("decode prior Copilot receipt")
	}
	var detail copilotReceiptDetail
	if json.Unmarshal(prior.Detail, &detail) != nil || detail.Protocol != copilotProtocol || detail.ActionID != request.ActionID || detail.SessionID != sessionID || detail.PromptSHA256 != digestText(request.Prompt) {
		return sdk.EffectReceipt{}, fmt.Errorf("prior receipt is not bound to this Copilot delivery")
	}
	return withCopilotACPReceipt(observeCtx, executable, request.WorkingDirectory, "deny", func(client *acpClient, initialized acpInitializeResult) (sdk.EffectReceipt, error) {
		if !initialized.AgentCapabilities.LoadSession {
			return sdk.EffectReceipt{}, fmt.Errorf("Copilot ACP does not support session/load observation")
		}
		client.Expect(request.Prompt)
		if _, err := client.Call("session/load", map[string]any{"sessionId": sessionID, "cwd": request.WorkingDirectory, "mcpServers": []any{}}); err != nil {
			return sdk.EffectReceipt{}, err
		}
		receipt := sdk.EffectReceipt{Status: "unknown", ExternalID: sessionID, TransportAccepted: true, TransportStatus: "accepted", SessionStatus: "created", DeliveryStatus: "unknown", AcceptanceStatus: prior.AcceptanceStatus, CompletionStatus: "unknown"}
		detail.UpdateCount, detail.PermissionRequests, detail.Observation = client.UpdateCount(), client.PermissionRequests(), "session_load_delivery_unproven"
		if client.SawExpected() {
			receipt.Status, receipt.RecipientVisible, receipt.DeliveryStatus = "confirmed", true, "visible"
			receipt.CompletionStatus = prior.CompletionStatus
			detail.Observation = "session_load_exact_user_message"
		}
		receipt.Detail, _ = json.Marshal(detail)
		return receipt, nil
	})
}

func copilotTurnReceipt(client *acpClient, request nativeStartRequest, sessionID, stopReason string) sdk.EffectReceipt {
	detail := copilotReceiptDetail{
		Mode: "native-acp-stdio-completion", Protocol: copilotProtocol, ActionID: request.ActionID,
		SessionID: sessionID, PromptSHA256: digestText(request.Prompt), StopReason: stopReason,
		UpdateCount: client.UpdateCount(), PermissionRequests: client.PermissionRequests(),
		PermissionPolicy: normalizedPermissionPolicy(request.PermissionPolicy), Observation: "bound_prompt_response_and_stop_reason",
	}
	receipt := sdk.EffectReceipt{Status: "confirmed", ExternalID: sessionID, TransportAccepted: true, TransportStatus: "accepted", SessionStatus: "created", RecipientVisible: true, DeliveryStatus: "visible", AcceptanceStatus: "pending"}
	switch stopReason {
	case "end_turn", "max_tokens", "max_turn_requests":
		receipt.CompletionStatus = "completed"
	default:
		receipt.CompletionStatus = "failed"
	}
	if client.SawExpected() {
		detail.Observation = "exact_user_message_and_stop_reason"
	}
	receipt.Detail, _ = json.Marshal(detail)
	return receipt
}

type copilotReceiptDetail struct {
	Mode               string `json:"mode"`
	Protocol           string `json:"protocol"`
	ActionID           string `json:"actionId"`
	SessionID          string `json:"sessionId"`
	PromptSHA256       string `json:"promptSha256"`
	StopReason         string `json:"stopReason,omitempty"`
	UpdateCount        int    `json:"updateCount"`
	PermissionRequests int    `json:"permissionRequests"`
	PermissionPolicy   string `json:"permissionPolicy,omitempty"`
	Observation        string `json:"observation"`
}

func normalizedPermissionPolicy(value string) string {
	if value == "allow-once" {
		return value
	}
	return "deny"
}

func withCopilotACP(ctx context.Context, executable, workingDirectory, permissionPolicy string, operation func(*acpClient, acpInitializeResult) error) error {
	_, err := withCopilotACPReceipt(ctx, executable, workingDirectory, permissionPolicy, func(client *acpClient, initialized acpInitializeResult) (sdk.EffectReceipt, error) {
		return sdk.EffectReceipt{}, operation(client, initialized)
	})
	return err
}

func withCopilotACPReceipt(ctx context.Context, executable, workingDirectory, permissionPolicy string, operation func(*acpClient, acpInitializeResult) (sdk.EffectReceipt, error)) (sdk.EffectReceipt, error) {
	processCtx, stop := context.WithCancel(ctx)
	defer stop()
	command := exec.CommandContext(processCtx, executable, "--acp", "--stdio", "--no-auto-update")
	if workingDirectory != "" {
		command.Dir = workingDirectory
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return sdk.EffectReceipt{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return sdk.EffectReceipt{}, err
	}
	stderr := &limitedWriter{remaining: 64 * 1024}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return sdk.EffectReceipt{}, err
	}
	defer func() {
		_ = stdin.Close()
		stop()
		_ = command.Wait()
	}()
	client := newACPClient(stdout, stdin, permissionPolicy)
	result, err := client.Call("initialize", map[string]any{"protocolVersion": copilotProtocolID, "clientCapabilities": map[string]any{}, "clientInfo": map[string]string{"name": "dagrail", "version": version.Version}})
	if err != nil {
		return sdk.EffectReceipt{}, fmt.Errorf("initialize Copilot ACP: %w (%s)", err, boundedText(stderr.String(), 1024))
	}
	var initialized acpInitializeResult
	if json.Unmarshal(result, &initialized) != nil || initialized.ProtocolVersion != copilotProtocolID {
		return sdk.EffectReceipt{}, fmt.Errorf("Copilot ACP negotiated an unsupported protocol")
	}
	receipt, err := operation(client, initialized)
	if err != nil {
		return receipt, fmt.Errorf("Copilot ACP operation: %w (%s)", err, boundedText(stderr.String(), 1024))
	}
	return receipt, nil
}

type acpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type acpClient struct {
	scanner            *bufio.Scanner
	encoder            *json.Encoder
	nextID             int64
	permissionPolicy   string
	expected           string
	matchTail          string
	sawExpected        bool
	updateCount        int
	permissionRequests int
}

func newACPClient(reader io.Reader, writer io.Writer, permissionPolicy string) *acpClient {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), copilotMessageLimit)
	return &acpClient{scanner: scanner, encoder: json.NewEncoder(writer), permissionPolicy: normalizedPermissionPolicy(permissionPolicy)}
}

func (client *acpClient) Expect(value string) {
	client.expected, client.matchTail, client.sawExpected = value, "", false
}

func (client *acpClient) SawExpected() bool       { return client.sawExpected }
func (client *acpClient) UpdateCount() int        { return client.updateCount }
func (client *acpClient) PermissionRequests() int { return client.permissionRequests }

func (client *acpClient) Call(method string, params any) (json.RawMessage, error) {
	client.nextID++
	id := client.nextID
	if err := client.encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for client.scanner.Scan() {
		var message acpMessage
		if err := json.Unmarshal(client.scanner.Bytes(), &message); err != nil {
			return nil, fmt.Errorf("decode ACP message: %w", err)
		}
		if string(message.ID) == strconv.FormatInt(id, 10) {
			if message.Error != nil {
				return nil, fmt.Errorf("ACP error %d: %s", message.Error.Code, message.Error.Message)
			}
			return append(json.RawMessage(nil), message.Result...), nil
		}
		if message.Method == "session/request_permission" && len(message.ID) > 0 {
			if err := client.respondPermission(message); err != nil {
				return nil, err
			}
			continue
		}
		if message.Method == "session/update" {
			client.observeUpdate(message.Params)
			continue
		}
		if message.Method != "" && len(message.ID) > 0 {
			if err := client.encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": -32601, "message": "method not supported by DAGrail ACP client"}}); err != nil {
				return nil, err
			}
		}
	}
	if err := client.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (client *acpClient) respondPermission(message acpMessage) error {
	client.permissionRequests++
	var params struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		return client.encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}})
	}
	wanted := "reject_once"
	if client.permissionPolicy == "allow-once" {
		wanted = "allow_once"
	}
	for _, option := range params.Options {
		if option.Kind == wanted && option.OptionID != "" {
			return client.encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": option.OptionID}}})
		}
	}
	return client.encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}})
}

func (client *acpClient) observeUpdate(raw json.RawMessage) {
	client.updateCount++
	var params struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &params) != nil || params.Update.SessionUpdate != "user_message_chunk" || params.Update.Content.Type != "text" || client.expected == "" || client.sawExpected {
		return
	}
	combined := client.matchTail + params.Update.Content.Text
	if strings.Contains(combined, client.expected) {
		client.sawExpected = true
		client.matchTail = ""
		return
	}
	keep := len(client.expected) - 1
	if keep > len(combined) {
		keep = len(combined)
	}
	client.matchTail = combined[len(combined)-keep:]
}
