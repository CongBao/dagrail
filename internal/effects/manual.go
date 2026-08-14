package effects

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CongBao/dagrail/sdk"
)

type Manual struct{}

func (Manual) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "manual", Version: "0.1.0", SchemaHash: "sha256:manual-effect-v1"}
}

func (Manual) Prepare(_ context.Context, request sdk.EffectRequest) (sdk.PreparedEffect, error) {
	if len(request.Request) == 0 || !json.Valid(request.Request) {
		return sdk.PreparedEffect{}, fmt.Errorf("manual effect request must be valid JSON")
	}
	return sdk.PreparedEffect{AdapterID: "manual", Binding: request.Request}, nil
}

func (Manual) Dispatch(_ context.Context, _ sdk.EffectRequest, prepared sdk.PreparedEffect) (sdk.EffectReceipt, error) {
	return sdk.EffectReceipt{Status: "unknown", Detail: prepared.Binding}, nil
}

func (Manual) Reconcile(_ context.Context, _ sdk.EffectRequest, _ sdk.PreparedEffect, evidence json.RawMessage) (sdk.EffectReceipt, error) {
	var receipt struct {
		ExternalID        string          `json:"externalId"`
		TransportAccepted bool            `json:"transportAccepted"`
		RecipientVisible  bool            `json:"recipientVisible"`
		TransportStatus   string          `json:"transportStatus"`
		SessionStatus     string          `json:"sessionStatus"`
		DeliveryStatus    string          `json:"deliveryStatus"`
		AcceptanceStatus  string          `json:"acceptanceStatus"`
		CompletionStatus  string          `json:"completionStatus"`
		Detail            json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(evidence, &receipt); err != nil {
		return sdk.EffectReceipt{}, fmt.Errorf("decode manual receipt: %w", err)
	}
	status := "unknown"
	if receipt.ExternalID != "" && (receipt.RecipientVisible || receipt.DeliveryStatus == "visible") {
		status = "confirmed"
	}
	return sdk.EffectReceipt{Status: status, ExternalID: receipt.ExternalID, TransportAccepted: receipt.TransportAccepted, RecipientVisible: receipt.RecipientVisible, TransportStatus: receipt.TransportStatus, SessionStatus: receipt.SessionStatus, DeliveryStatus: receipt.DeliveryStatus, AcceptanceStatus: receipt.AcceptanceStatus, CompletionStatus: receipt.CompletionStatus, Detail: receipt.Detail}, nil
}
