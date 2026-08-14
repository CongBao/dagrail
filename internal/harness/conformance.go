package harness

import (
	"fmt"

	"github.com/CongBao/dagrail/sdk"
)

// nativeCapability describes only behavior that an adapter has proved against
// the executable it found. Version strings alone never enable a capability.
type nativeCapability struct {
	Available    bool
	Dispatch     bool
	Resume       bool
	Inspect      bool
	Cancel       bool
	Mode         string
	Protocol     string
	Stability    string
	Execution    string
	ReceiptProof []string
	Reason       string
}

type nativeStartRequest struct {
	WorkingDirectory string
	Prompt           string
	SessionID        string
	ActionID         string
	PermissionPolicy string
}

// validateNativeReceipt is the shared safety contract for all first-party
// native harness adapters. It intentionally does not accept DAG-level
// acceptance or completion as a transport-side inference.
func validateNativeReceipt(receipt sdk.EffectReceipt) error {
	if receipt.AcceptanceStatus != "" && receipt.AcceptanceStatus != "pending" && receipt.AcceptanceStatus != "unknown" {
		return fmt.Errorf("native transport cannot decide DAG acceptance")
	}
	if receipt.Status == "confirmed" && (receipt.DeliveryStatus != "visible" || !receipt.RecipientVisible) {
		return fmt.Errorf("confirmed status requires recipient-visible delivery")
	}
	if receipt.RecipientVisible != (receipt.DeliveryStatus == "visible") {
		return fmt.Errorf("recipientVisible and deliveryStatus disagree")
	}
	if receipt.DeliveryStatus == "visible" {
		if receipt.ExternalID == "" {
			return fmt.Errorf("visible delivery requires a stable external session ID")
		}
		if receipt.TransportStatus != "accepted" || receipt.SessionStatus != "created" {
			return fmt.Errorf("visible delivery requires accepted transport and created session")
		}
	}
	if receipt.CompletionStatus == "completed" && receipt.DeliveryStatus != "visible" {
		return fmt.Errorf("completed native turn requires proven delivery")
	}
	return nil
}
