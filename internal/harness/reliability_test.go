package harness

import (
	"encoding/json"
	"testing"

	"github.com/CongBao/dagrail/sdk"
)

func FuzzNativeReceiptConformance(f *testing.F) {
	f.Add([]byte(`{"status":"confirmed","externalId":"session","transportAccepted":true,"recipientVisible":true,"transportStatus":"accepted","sessionStatus":"created","deliveryStatus":"visible","acceptanceStatus":"pending","completionStatus":"completed"}`))
	f.Add([]byte(`{"status":"confirmed","deliveryStatus":"unknown"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 256*1024 {
			t.Skip()
		}
		var receipt sdk.EffectReceipt
		if json.Unmarshal(raw, &receipt) != nil {
			return
		}
		_ = validateNativeReceipt(receipt)
	})
}
