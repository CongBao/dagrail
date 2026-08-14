package harness

import (
	"errors"
	"strings"
	"testing"
)

func TestClaudeJSONResultBindsSessionAndStoresOnlyDigests(t *testing.T) {
	request := nativeStartRequest{Prompt: "work bound to action-1", ActionID: "action-1"}
	receipt, err := claudeResultReceipt(`{"type":"result","subtype":"success","is_error":false,"session_id":"11111111-1111-4111-8111-111111111111","result":"secret model output"}`, request, "11111111-1111-4111-8111-111111111111", nil)
	if err != nil || receipt.Status != "confirmed" || receipt.CompletionStatus != "completed" || !receipt.RecipientVisible {
		t.Fatalf("valid Claude result not confirmed: %+v %v", receipt, err)
	}
	if string(receipt.Detail) == "" || strings.Contains(string(receipt.Detail), "secret model output") {
		t.Fatalf("Claude result body leaked into receipt: %s", receipt.Detail)
	}
	if _, err := claudeResultReceipt(`{"type":"result","session_id":"other","result":"ok"}`, request, "expected", nil); err == nil {
		t.Fatal("mismatched Claude session was accepted")
	}
	failed, err := claudeResultReceipt(`{"type":"result","subtype":"error","is_error":true,"session_id":"expected","result":"failure"}`, request, "expected", errors.New("exit 1"))
	if err != nil || failed.Status != "failed" || failed.CompletionStatus != "failed" {
		t.Fatalf("structured Claude failure was not preserved: %+v %v", failed, err)
	}
}
