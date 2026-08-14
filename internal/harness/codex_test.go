package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/sdk"
)

type fakeCodexBackend struct {
	capability nativeCapability
	starts     []codexStartRequest
	observed   string
}

func (backend *fakeCodexBackend) Probe(context.Context, string) nativeCapability {
	return backend.capability
}

func (backend *fakeCodexBackend) Start(_ context.Context, _ string, request codexStartRequest) (sdk.EffectReceipt, error) {
	backend.starts = append(backend.starts, request)
	sessionID := request.SessionID
	if sessionID == "" {
		sessionID = "thread-new"
	}
	return confirmedNativeReceipt(sessionID), nil
}

func (backend *fakeCodexBackend) Observe(_ context.Context, _ string, sessionID string, _ json.RawMessage) (sdk.EffectReceipt, error) {
	backend.observed = sessionID
	return confirmedNativeReceipt(sessionID), nil
}

func TestCodexAdapterGatesNativeStartResumeAndObservation(t *testing.T) {
	backend := &fakeCodexBackend{capability: nativeCapability{Available: true, Dispatch: true, Resume: true, Inspect: true, Mode: "native-daemon-proxy", Protocol: codexProtocol}}
	adapter := &Adapter{id: "codex", command: "go", codex: backend}
	request := sdk.EffectRequest{ActionID: "action-stable", Request: json.RawMessage(`{"workingDirectory":"/project","roleId":"worker","nodeId":"A"}`), PriorReceipt: json.RawMessage(`{"externalId":"thread-old"}`)}

	capabilities, err := adapter.Probe(context.Background())
	if err != nil || !capabilities.Dispatch || !capabilities.Resume || !capabilities.Inspect {
		t.Fatalf("native capabilities unavailable: %+v %v", capabilities, err)
	}
	created, err := adapter.Dispatch(context.Background(), request)
	if err != nil || created.ExternalID != "thread-new" || len(backend.starts) != 1 || backend.starts[0].ClientMessageID != "action-stable" {
		t.Fatalf("native start mismatch: %+v %#v %v", created, backend.starts, err)
	}
	resumed, err := adapter.Resume(context.Background(), "thread-old", request)
	if err != nil || resumed.ExternalID != "thread-old" || backend.starts[1].SessionID != "thread-old" {
		t.Fatalf("native resume mismatch: %+v %#v %v", resumed, backend.starts, err)
	}
	observed, err := adapter.Observe(context.Background(), "thread-old", request)
	if err != nil || observed.DeliveryStatus != "visible" || backend.observed != "thread-old" {
		t.Fatalf("native observe mismatch: %+v %v", observed, err)
	}
}

func TestCodexAdapterFallsBackWithoutNativeCapability(t *testing.T) {
	backend := &fakeCodexBackend{capability: nativeCapability{Mode: "manual", Reason: "not supported"}}
	adapter := &Adapter{id: "codex", command: "go", codex: backend}
	request := sdk.EffectRequest{ActionID: "action", Request: json.RawMessage(`{"workingDirectory":"/project","roleId":"worker","nodeId":"A"}`)}
	receipt, err := adapter.Dispatch(context.Background(), request)
	if err != nil || receipt.Status != "unknown" || receipt.TransportStatus != "not-attempted" || receipt.RecipientVisible || len(backend.starts) != 0 {
		t.Fatalf("manual fallback was not preserved: %+v %v", receipt, err)
	}
}

func TestJSONRPCVisibilityRequiresMatchingCompletedUserMessage(t *testing.T) {
	stream := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":{"type":"userMessage","clientId":"wrong"}}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"turn":{"id":"turn"}}}`,
		`{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":{"type":"userMessage","clientId":"action"}}}`,
	}, "\n") + "\n"
	var requests bytes.Buffer
	client := newJSONRPCClient(strings.NewReader(stream), &requests)
	if _, err := client.Call("turn/start", map[string]string{"threadId": "thread"}); err != nil {
		t.Fatal(err)
	}
	if !client.WaitForVisible("thread", "turn", "action") {
		t.Fatal("matching recipient-visible notification was not recognized")
	}
	if err := client.Notify("initialized", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requests.String(), `"method":"turn/start"`) {
		t.Fatalf("request was not encoded as JSON-RPC: %s", requests.String())
	}
	if strings.Contains(requests.String(), `"method":"initialized","params"`) {
		t.Fatalf("parameterless notification drifted from stable schema: %s", requests.String())
	}
}

func TestCodexNativeLifecycleProvesDeliveryAndObservesCompletion(t *testing.T) {
	startStream := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"thread":{"id":"thread-1"}}}`,
		`{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"type":"userMessage","clientId":"action-1"}}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"turn":{"id":"turn-1"}}}`,
	}, "\n") + "\n"
	var requests bytes.Buffer
	client := newJSONRPCClient(strings.NewReader(startStream), &requests)
	receipt, err := startCodexSession(client, codexStartRequest{WorkingDirectory: "/project", Prompt: "bounded work package", ClientMessageID: "action-1"})
	if err != nil || receipt.Status != "confirmed" || receipt.ExternalID != "thread-1" || !receipt.RecipientVisible {
		t.Fatalf("native delivery was not proven: %+v %v", receipt, err)
	}
	if !strings.Contains(requests.String(), `"clientUserMessageId":"action-1"`) || !strings.Contains(requests.String(), `"cwd":"/project"`) {
		t.Fatalf("native requests lack durable bindings: %s", requests.String())
	}

	readResult := `{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[{"id":"turn-1","status":"completed","items":[{"type":"userMessage","clientId":"action-1"}]}]}}`
	readStream := `{"jsonrpc":"2.0","id":1,"result":` + readResult + "}\n"
	client = newJSONRPCClient(strings.NewReader(readStream), &bytes.Buffer{})
	prior, detail, err := decodeCodexReceipt(mustReceiptJSON(t, receipt))
	if err != nil {
		t.Fatal(err)
	}
	observed, err := observeCodexSession(client, "thread-1", prior, detail)
	if err != nil || observed.Status != "confirmed" || observed.CompletionStatus != "completed" || observed.AcceptanceStatus != "pending" {
		t.Fatalf("native observation collapsed receipt states: %+v %v", observed, err)
	}
}

func TestCodexResumeUsesOnlyStableProtocolFields(t *testing.T) {
	stream := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"thread":{"id":"thread-1","turns":[]}}}`,
		`{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-2","item":{"type":"userMessage","clientId":"action-2"}}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"turn":{"id":"turn-2"}}}`,
	}, "\n") + "\n"
	var requests bytes.Buffer
	client := newJSONRPCClient(strings.NewReader(stream), &requests)
	receipt, err := startCodexSession(client, codexStartRequest{WorkingDirectory: "/project", Prompt: "resume", SessionID: "thread-1", ClientMessageID: "action-2"})
	if err != nil || receipt.Status != "confirmed" {
		t.Fatalf("native resume failed: %+v %v", receipt, err)
	}
	if strings.Contains(requests.String(), "excludeTurns") || !strings.Contains(requests.String(), `"method":"thread/resume"`) {
		t.Fatalf("resume used an experimental or missing method field: %s", requests.String())
	}
}

func TestThreadObservationRequiresBoundTurnAndClientMessage(t *testing.T) {
	clientID := "action"
	thread := codexThread{ID: "thread"}
	thread.Turns = append(thread.Turns, struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Items  []struct {
			Type     string  `json:"type"`
			ClientID *string `json:"clientId"`
		} `json:"items"`
	}{ID: "turn", Status: "completed", Items: []struct {
		Type     string  `json:"type"`
		ClientID *string `json:"clientId"`
	}{{Type: "userMessage", ClientID: &clientID}}})
	visible, status := thread.observed("turn", "action")
	if !visible || status != "completed" {
		t.Fatalf("bound observation failed: visible=%v status=%s", visible, status)
	}
	if visible, _ := thread.observed("other-turn", "action"); visible {
		t.Fatal("unrelated turn was accepted as delivery evidence")
	}
}

func mustReceiptJSON(t *testing.T, receipt sdk.EffectReceipt) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
