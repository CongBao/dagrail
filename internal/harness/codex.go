package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CongBao/dagrail/internal/version"
	"github.com/CongBao/dagrail/sdk"
)

const (
	codexProtocol       = "codex-app-server-jsonrpc-v2"
	codexOperationLimit = 15 * time.Second
	codexMessageLimit   = 16 * 1024 * 1024
)

type codexStartRequest struct {
	WorkingDirectory string
	Prompt           string
	SessionID        string
	ClientMessageID  string
}

type codexBackend interface {
	Probe(context.Context, string) nativeCapability
	Start(context.Context, string, codexStartRequest) (sdk.EffectReceipt, error)
	Observe(context.Context, string, string, json.RawMessage) (sdk.EffectReceipt, error)
}

type appServerBackend struct{}

func (appServerBackend) Probe(ctx context.Context, executable string) nativeCapability {
	result := nativeCapability{Mode: "manual", Stability: "stable", Execution: "asynchronous-turn", Reason: "Codex app-server daemon/proxy capability unavailable"}
	if executable == "" {
		result.Reason = "Codex executable not found"
		return result
	}
	for _, args := range [][]string{{"app-server", "daemon", "--help"}, {"app-server", "proxy", "--help"}} {
		output, err := boundedCombinedOutput(ctx, executable, args...)
		if err != nil {
			result.Reason = boundedProtocolError(output, err)
			return result
		}
	}
	result.Available, result.Dispatch, result.Resume, result.Inspect = true, true, true, true
	result.Mode, result.Protocol, result.Reason = "native-daemon-proxy", codexProtocol, ""
	result.ReceiptProof = []string{"session-created", "exact-user-message", "turn-observation"}
	return result
}

func (b appServerBackend) Start(ctx context.Context, executable string, request codexStartRequest) (sdk.EffectReceipt, error) {
	return b.withClient(ctx, executable, func(client *jsonRPCClient) (sdk.EffectReceipt, error) { return startCodexSession(client, request) })
}

func (b appServerBackend) Observe(ctx context.Context, executable, sessionID string, priorReceipt json.RawMessage) (sdk.EffectReceipt, error) {
	prior, detail, err := decodeCodexReceipt(priorReceipt)
	if err != nil {
		return sdk.EffectReceipt{Status: "unknown", ExternalID: sessionID, TransportStatus: "unknown", SessionStatus: "unknown", DeliveryStatus: "unknown", AcceptanceStatus: "unknown", CompletionStatus: "unknown"}, nil
	}
	if prior.ExternalID != "" && prior.ExternalID != sessionID {
		return sdk.EffectReceipt{}, fmt.Errorf("prior receipt belongs to a different Codex thread")
	}
	return b.withClient(ctx, executable, func(client *jsonRPCClient) (sdk.EffectReceipt, error) {
		return observeCodexSession(client, sessionID, prior, detail)
	})
}

func startCodexSession(client *jsonRPCClient, request codexStartRequest) (sdk.EffectReceipt, error) {
	receipt := sdk.EffectReceipt{Status: "unknown", ExternalID: request.SessionID, TransportStatus: "not-attempted", SessionStatus: "not-created", DeliveryStatus: "unknown", AcceptanceStatus: "pending", CompletionStatus: "pending"}
	threadID := request.SessionID
	if threadID == "" {
		result, err := client.Call("thread/start", map[string]any{"cwd": request.WorkingDirectory})
		if err != nil {
			return receipt, err
		}
		var response struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(result, &response); err != nil || response.Thread.ID == "" {
			return receipt, fmt.Errorf("Codex thread/start returned no thread ID")
		}
		threadID = response.Thread.ID
	} else {
		result, err := client.Call("thread/resume", map[string]any{"threadId": threadID, "cwd": request.WorkingDirectory})
		if err != nil {
			receipt.SessionStatus = "unknown"
			return receipt, err
		}
		var response struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(result, &response); err != nil || response.Thread.ID != threadID {
			receipt.SessionStatus = "unknown"
			return receipt, fmt.Errorf("Codex thread/resume did not confirm the requested thread")
		}
	}

	receipt.ExternalID, receipt.TransportAccepted, receipt.TransportStatus, receipt.SessionStatus = threadID, true, "accepted", "created"
	result, err := client.Call("turn/start", map[string]any{
		"threadId":            threadID,
		"input":               []map[string]any{{"type": "text", "text": request.Prompt}},
		"clientUserMessageId": request.ClientMessageID,
	})
	if err != nil {
		receipt.Detail = marshalCodexDetail(codexReceiptDetail{Mode: "native-daemon-proxy", Protocol: codexProtocol, ThreadID: threadID, ClientMessageID: request.ClientMessageID, Observation: "turn_start_ambiguous"})
		return receipt, nil
	}
	var turnResponse struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(result, &turnResponse); err != nil || turnResponse.Turn.ID == "" {
		receipt.Detail = marshalCodexDetail(codexReceiptDetail{Mode: "native-daemon-proxy", Protocol: codexProtocol, ThreadID: threadID, ClientMessageID: request.ClientMessageID, Observation: "turn_id_unavailable"})
		return receipt, nil
	}
	detail := codexReceiptDetail{Mode: "native-daemon-proxy", Protocol: codexProtocol, ThreadID: threadID, TurnID: turnResponse.Turn.ID, ClientMessageID: request.ClientMessageID}
	if client.WaitForVisible(threadID, turnResponse.Turn.ID, request.ClientMessageID) {
		receipt.Status, receipt.RecipientVisible, receipt.DeliveryStatus = "confirmed", true, "visible"
		detail.Observation = "user_message_completed"
	} else {
		detail.Observation = "visible_delivery_unproven"
	}
	receipt.Detail = marshalCodexDetail(detail)
	return receipt, nil
}

func observeCodexSession(client *jsonRPCClient, sessionID string, prior sdk.EffectReceipt, detail codexReceiptDetail) (sdk.EffectReceipt, error) {
	receipt := sdk.EffectReceipt{Status: "unknown", ExternalID: sessionID, TransportAccepted: true, TransportStatus: "accepted", SessionStatus: "unknown", DeliveryStatus: "unknown", AcceptanceStatus: prior.AcceptanceStatus, CompletionStatus: "unknown"}
	if receipt.AcceptanceStatus == "" {
		receipt.AcceptanceStatus = "pending"
	}
	result, err := client.Call("thread/read", map[string]any{"threadId": sessionID, "includeTurns": true})
	if err != nil {
		return receipt, err
	}
	var response codexThreadReadResponse
	if err := json.Unmarshal(result, &response); err != nil || response.Thread.ID != sessionID {
		return receipt, fmt.Errorf("Codex thread/read did not return the requested thread")
	}
	receipt.SessionStatus = "created"
	detail.ThreadStatus = response.Thread.Status.Type
	visible, turnStatus := response.Thread.observed(detail.TurnID, detail.ClientMessageID)
	detail.TurnStatus = turnStatus
	if visible {
		receipt.Status, receipt.RecipientVisible, receipt.DeliveryStatus = "confirmed", true, "visible"
		detail.Observation = "thread_read_verified_user_message"
	}
	switch turnStatus {
	case "completed":
		receipt.CompletionStatus = "completed"
	case "failed", "interrupted":
		receipt.CompletionStatus = "failed"
	case "inProgress":
		receipt.CompletionStatus = "pending"
	}
	receipt.Detail = marshalCodexDetail(detail)
	return receipt, nil
}

func (appServerBackend) withClient(ctx context.Context, executable string, operation func(*jsonRPCClient) (sdk.EffectReceipt, error)) (sdk.EffectReceipt, error) {
	operationCtx, cancel := context.WithTimeout(ctx, codexOperationLimit)
	defer cancel()
	if output, err := boundedCombinedOutput(operationCtx, executable, "app-server", "daemon", "start"); err != nil {
		return sdk.EffectReceipt{}, fmt.Errorf("start Codex app-server daemon: %s", boundedProtocolError(output, err))
	}
	processCtx, stopProcess := context.WithCancel(operationCtx)
	command := exec.CommandContext(processCtx, executable, "app-server", "proxy")
	stdin, err := command.StdinPipe()
	if err != nil {
		stopProcess()
		return sdk.EffectReceipt{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		stopProcess()
		return sdk.EffectReceipt{}, err
	}
	stderr := &limitedWriter{remaining: 8192}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		stopProcess()
		return sdk.EffectReceipt{}, err
	}
	defer func() {
		_ = stdin.Close()
		stopProcess()
		_ = command.Wait()
	}()
	client := newJSONRPCClient(stdout, stdin)
	if _, err := client.Call("initialize", map[string]any{"clientInfo": map[string]string{"name": "dagrail", "title": "DAGrail", "version": version.Version}, "capabilities": map[string]bool{"experimentalApi": false}}); err != nil {
		return sdk.EffectReceipt{}, fmt.Errorf("initialize Codex app-server: %w (%s)", err, stderr.String())
	}
	if err := client.Notify("initialized", nil); err != nil {
		return sdk.EffectReceipt{}, err
	}
	return operation(client)
}

type codexReceiptDetail struct {
	Mode            string `json:"mode"`
	Protocol        string `json:"protocol"`
	ThreadID        string `json:"threadId"`
	TurnID          string `json:"turnId,omitempty"`
	ClientMessageID string `json:"clientMessageId,omitempty"`
	ThreadStatus    string `json:"threadStatus,omitempty"`
	TurnStatus      string `json:"turnStatus,omitempty"`
	Observation     string `json:"observation"`
}

func decodeCodexReceipt(raw json.RawMessage) (sdk.EffectReceipt, codexReceiptDetail, error) {
	var receipt sdk.EffectReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return receipt, codexReceiptDetail{}, fmt.Errorf("decode Codex receipt: %w", err)
	}
	var detail codexReceiptDetail
	if err := json.Unmarshal(receipt.Detail, &detail); err != nil {
		return receipt, detail, fmt.Errorf("decode Codex receipt detail: %w", err)
	}
	if detail.Protocol != codexProtocol || detail.ThreadID == "" {
		return receipt, detail, fmt.Errorf("prior receipt is not a supported Codex native receipt")
	}
	return receipt, detail, nil
}

func marshalCodexDetail(detail codexReceiptDetail) json.RawMessage {
	raw, _ := json.Marshal(detail)
	return raw
}

type codexThreadReadResponse struct {
	Thread codexThread `json:"thread"`
}

type codexThread struct {
	ID     string `json:"id"`
	Status struct {
		Type string `json:"type"`
	} `json:"status"`
	Turns []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Items  []struct {
			Type     string  `json:"type"`
			ClientID *string `json:"clientId"`
		} `json:"items"`
	} `json:"turns"`
}

func (thread codexThread) observed(turnID, clientMessageID string) (bool, string) {
	for _, turn := range thread.Turns {
		if turnID != "" && turn.ID != turnID {
			continue
		}
		for _, item := range turn.Items {
			if item.Type == "userMessage" && item.ClientID != nil && *item.ClientID == clientMessageID {
				return true, turn.Status
			}
		}
	}
	return false, ""
}

type rpcMessage struct {
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

type jsonRPCClient struct {
	scanner       *bufio.Scanner
	encoder       *json.Encoder
	nextID        int64
	notifications []rpcMessage
}

func newJSONRPCClient(reader io.Reader, writer io.Writer) *jsonRPCClient {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), codexMessageLimit)
	return &jsonRPCClient{scanner: scanner, encoder: json.NewEncoder(writer)}
}

func (client *jsonRPCClient) Call(method string, params any) (json.RawMessage, error) {
	client.nextID++
	id := client.nextID
	if err := client.encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for client.scanner.Scan() {
		var message rpcMessage
		if err := json.Unmarshal(client.scanner.Bytes(), &message); err != nil {
			return nil, fmt.Errorf("decode Codex JSON-RPC message: %w", err)
		}
		if string(message.ID) == strconv.FormatInt(id, 10) {
			if message.Error != nil {
				return nil, fmt.Errorf("Codex JSON-RPC error %d: %s", message.Error.Code, message.Error.Message)
			}
			return append(json.RawMessage(nil), message.Result...), nil
		}
		if isUserMessageNotification(message) && len(client.notifications) < 128 {
			client.notifications = append(client.notifications, message)
		}
	}
	if err := client.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (client *jsonRPCClient) Notify(method string, params any) error {
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		message["params"] = params
	}
	return client.encoder.Encode(message)
}

func (client *jsonRPCClient) WaitForVisible(threadID, turnID, clientMessageID string) bool {
	for _, notification := range client.notifications {
		if visibleNotification(notification, threadID, turnID, clientMessageID) {
			return true
		}
	}
	for client.scanner.Scan() {
		var notification rpcMessage
		if json.Unmarshal(client.scanner.Bytes(), &notification) == nil && visibleNotification(notification, threadID, turnID, clientMessageID) {
			return true
		}
	}
	return false
}

func visibleNotification(message rpcMessage, threadID, turnID, clientMessageID string) bool {
	if message.Method != "item/completed" {
		return false
	}
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Item     struct {
			Type     string  `json:"type"`
			ClientID *string `json:"clientId"`
		} `json:"item"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		return false
	}
	return params.ThreadID == threadID && params.TurnID == turnID && params.Item.Type == "userMessage" && params.Item.ClientID != nil && *params.Item.ClientID == clientMessageID
}

func isUserMessageNotification(message rpcMessage) bool {
	if message.Method != "item/completed" {
		return false
	}
	var params struct {
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	return json.Unmarshal(message.Params, &params) == nil && params.Item.Type == "userMessage"
}

type limitedWriter struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int
}

func (writer *limitedWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	original := len(data)
	if len(data) > writer.remaining {
		data = data[:writer.remaining]
	}
	_, _ = writer.buffer.Write(data)
	writer.remaining -= len(data)
	return original, nil
}

func (writer *limitedWriter) String() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.buffer.String()
}

func boundedProtocolError(output []byte, err error) string {
	message := strings.TrimSpace(string(output))
	if len(message) > 1024 {
		message = message[:1024]
	}
	if message == "" && err != nil {
		message = err.Error()
	}
	return message
}
