package harness

import (
	"testing"

	"github.com/CongBao/dagrail/sdk"
)

func TestNativeReceiptConformanceSeparatesDeliveryCompletionAndAcceptance(t *testing.T) {
	valid := sdk.EffectReceipt{Status: "confirmed", ExternalID: "session", TransportAccepted: true, TransportStatus: "accepted", SessionStatus: "created", RecipientVisible: true, DeliveryStatus: "visible", AcceptanceStatus: "pending", CompletionStatus: "completed"}
	if err := validateNativeReceipt(valid); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	cases := []sdk.EffectReceipt{
		{Status: "confirmed", ExternalID: "session", TransportStatus: "accepted", SessionStatus: "created", DeliveryStatus: "unknown"},
		{Status: "confirmed", ExternalID: "session", TransportStatus: "accepted", SessionStatus: "created", RecipientVisible: true, DeliveryStatus: "visible", AcceptanceStatus: "accepted"},
		{Status: "confirmed", ExternalID: "", TransportStatus: "accepted", SessionStatus: "created", RecipientVisible: true, DeliveryStatus: "visible"},
		{Status: "unknown", ExternalID: "session", TransportStatus: "accepted", SessionStatus: "created", CompletionStatus: "completed", DeliveryStatus: "unknown"},
	}
	for index, receipt := range cases {
		if err := validateNativeReceipt(receipt); err == nil {
			t.Fatalf("unsafe receipt %d passed conformance: %+v", index, receipt)
		}
	}
}
