package effects

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CongBao/dagrail/sdk"
)

type fakeHarness struct{}

func (fakeHarness) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "harness.test", Version: "1.0.0", SchemaHash: "test"}
}
func (fakeHarness) Probe(context.Context) (sdk.HarnessCapabilities, error) {
	return sdk.HarnessCapabilities{}, nil
}
func (fakeHarness) Dispatch(context.Context, sdk.EffectRequest) (sdk.EffectReceipt, error) {
	return sdk.EffectReceipt{Status: "unknown", TransportStatus: "accepted", DeliveryStatus: "unknown"}, nil
}
func (fakeHarness) Resume(context.Context, string, sdk.EffectRequest) (sdk.EffectReceipt, error) {
	return sdk.EffectReceipt{Status: "unknown", SessionStatus: "created", DeliveryStatus: "unknown"}, nil
}

func TestHarnessDispatchDoesNotConflateTransportWithVisibleDelivery(t *testing.T) {
	root := t.TempDir()
	adapter := HarnessDispatch{Harness: fakeHarness{}}
	request := sdk.EffectRequest{ProjectRoot: root, Request: json.RawMessage(`{"roleId":"worker","nodeId":"A"}`)}
	prepared, err := adapter.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := adapter.Dispatch(context.Background(), request, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "unknown" || receipt.DeliveryStatus != "unknown" {
		t.Fatalf("transport was treated as delivery: %#v", receipt)
	}
	visible, err := adapter.Reconcile(context.Background(), request, prepared, json.RawMessage(`{"externalId":"thread-1","transportStatus":"accepted","sessionStatus":"created","deliveryStatus":"visible","acceptanceStatus":"pending","completionStatus":"pending"}`))
	if err != nil {
		t.Fatal(err)
	}
	if visible.Status != "confirmed" || visible.AcceptanceStatus != "pending" || visible.CompletionStatus != "pending" {
		t.Fatalf("receipt states were collapsed: %#v", visible)
	}
}
