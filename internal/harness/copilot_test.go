package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCopilotACPIntegration(t *testing.T) {
	if os.Getenv("DAGRAIL_TEST_COPILOT_ACP") != "1" {
		t.Skip("set DAGRAIL_TEST_COPILOT_ACP=1 for the installed Copilot ACP smoke test")
	}
	executable, err := exec.LookPath("copilot")
	if err != nil {
		t.Skip("Copilot CLI is not installed")
	}
	backend := copilotACPBackend{}
	request := nativeStartRequest{WorkingDirectory: t.TempDir(), Prompt: "Reply with the single word DAGrail. Do not call tools. This message is bound to DAGrail action integration-smoke.", ActionID: "integration-smoke", PermissionPolicy: "deny"}
	receipt, err := backend.Start(context.Background(), executable, request)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "confirmed" || !receipt.RecipientVisible || receipt.CompletionStatus != "completed" {
		t.Fatalf("installed Copilot ACP did not satisfy native receipt conformance: %+v", receipt)
	}
}

func TestACPClientRequiresExactUserMessageAndUsesOneShotPermission(t *testing.T) {
	stream := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"session-1"}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"bound "}}}}`,
		`{"jsonrpc":"2.0","id":"permission-1","method":"session/request_permission","params":{"sessionId":"session-1","toolCall":{"toolCallId":"tool"},"options":[{"optionId":"always","name":"Always","kind":"allow_always"},{"optionId":"once","name":"Once","kind":"allow_once"}]}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"action-1"}}}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`,
	}, "\n") + "\n"
	var requests bytes.Buffer
	client := newACPClient(strings.NewReader(stream), &requests, "allow-once")
	client.Expect("bound action-1")
	if _, err := client.Call("initialize", map[string]any{"protocolVersion": 1}); err != nil {
		t.Fatal(err)
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	result, err := client.Call("session/new", map[string]any{"cwd": "/project", "mcpServers": []any{}})
	if err != nil || json.Unmarshal(result, &session) != nil || session.SessionID != "session-1" {
		t.Fatalf("session/new failed: %s %v", result, err)
	}
	result, err = client.Call("session/prompt", map[string]any{"sessionId": session.SessionID})
	if err != nil || !client.SawExpected() || client.PermissionRequests() != 1 {
		t.Fatalf("ACP evidence mismatch: result=%s visible=%v permissions=%d err=%v", result, client.SawExpected(), client.PermissionRequests(), err)
	}
	encoded := requests.String()
	if !strings.Contains(encoded, `"optionId":"once"`) || strings.Contains(encoded, `"optionId":"always"`) {
		t.Fatalf("ACP client did not enforce one-shot permission: %s", encoded)
	}
	receipt := copilotTurnReceipt(client, nativeStartRequest{Prompt: "bound action-1", ActionID: "action-1", PermissionPolicy: "allow-once"}, session.SessionID, "end_turn")
	if receipt.Status != "confirmed" || receipt.CompletionStatus != "completed" || receipt.AcceptanceStatus != "pending" {
		t.Fatalf("ACP receipt collapsed states: %+v", receipt)
	}
}

func TestACPClientDefaultsToRejectOnce(t *testing.T) {
	stream := strings.Join([]string{
		`{"jsonrpc":"2.0","id":"permission","method":"session/request_permission","params":{"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"},{"optionId":"reject","name":"Reject","kind":"reject_once"}]}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"refusal"}}`,
	}, "\n") + "\n"
	var requests bytes.Buffer
	client := newACPClient(strings.NewReader(stream), &requests, "")
	if _, err := client.Call("session/prompt", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requests.String(), `"optionId":"reject"`) || strings.Contains(requests.String(), `"optionId":"allow"`) {
		t.Fatalf("default permission was not deny: %s", requests.String())
	}
}
