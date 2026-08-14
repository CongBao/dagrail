package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/sdk"
)

type fakeClaudeNative struct{ starts []nativeStartRequest }

func (*fakeClaudeNative) Probe(context.Context, string) nativeCapability {
	return nativeCapability{Available: true, Dispatch: true, Resume: true, Mode: "native-headless-completion", Protocol: claudeProtocol}
}
func (backend *fakeClaudeNative) Start(_ context.Context, _ string, request nativeStartRequest) (sdk.EffectReceipt, error) {
	backend.starts = append(backend.starts, request)
	sessionID := request.SessionID
	if sessionID == "" {
		sessionID = "claude-new"
	}
	return confirmedNativeReceipt(sessionID), nil
}

type fakeCopilotNative struct {
	starts   []nativeStartRequest
	observed string
}

func (*fakeCopilotNative) Probe(context.Context, string) nativeCapability {
	return nativeCapability{Available: true, Dispatch: true, Resume: true, Inspect: true, Mode: "native-acp-stdio-completion", Protocol: copilotProtocol}
}
func (backend *fakeCopilotNative) Start(_ context.Context, _ string, request nativeStartRequest) (sdk.EffectReceipt, error) {
	backend.starts = append(backend.starts, request)
	sessionID := request.SessionID
	if sessionID == "" {
		sessionID = "copilot-new"
	}
	return confirmedNativeReceipt(sessionID), nil
}
func (backend *fakeCopilotNative) Observe(_ context.Context, _ string, sessionID string, _ nativeStartRequest, _ json.RawMessage) (sdk.EffectReceipt, error) {
	backend.observed = sessionID
	return confirmedNativeReceipt(sessionID), nil
}

func confirmedNativeReceipt(sessionID string) sdk.EffectReceipt {
	return sdk.EffectReceipt{Status: "confirmed", ExternalID: sessionID, TransportAccepted: true, TransportStatus: "accepted", SessionStatus: "created", RecipientVisible: true, DeliveryStatus: "visible", AcceptanceStatus: "pending", CompletionStatus: "completed"}
}

func TestClaudeAndCopilotAdaptersBindNativeTurnsToAction(t *testing.T) {
	claude := &fakeClaudeNative{}
	copilot := &fakeCopilotNative{}
	cases := []struct {
		adapter *Adapter
		starts  *[]nativeStartRequest
	}{
		{adapter: &Adapter{id: "claude-code", command: "go", claude: claude}, starts: &claude.starts},
		{adapter: &Adapter{id: "copilot-cli", command: "go", copilot: copilot}, starts: &copilot.starts},
	}
	request := sdk.EffectRequest{ActionID: "action-stable", Request: json.RawMessage(`{"workingDirectory":"/project","roleId":"worker","nodeId":"A","permissionPolicy":"allow-once"}`)}
	for _, item := range cases {
		receipt, err := item.adapter.Dispatch(context.Background(), request)
		if err != nil || receipt.Status != "confirmed" || len(*item.starts) != 1 {
			t.Fatalf("%s native dispatch failed: %+v %v", item.adapter.id, receipt, err)
		}
		start := (*item.starts)[0]
		if start.ActionID != "action-stable" || start.PermissionPolicy != "allow-once" || !strings.Contains(start.Prompt, "action-stable") {
			t.Fatalf("%s native request lacks durable action binding: %+v", item.adapter.id, start)
		}
		if _, err := item.adapter.Resume(context.Background(), item.adapter.id+"-old", request); err != nil || len(*item.starts) != 2 {
			t.Fatalf("%s native resume failed: %v", item.adapter.id, err)
		}
	}
}
