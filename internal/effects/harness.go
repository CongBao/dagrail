package effects

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/CongBao/dagrail/sdk"
)

// HarnessDispatch adapts a harness transport to the same prepared/dispatched/
// reconciled outbox used by every other external effect.
type HarnessDispatch struct{ Harness sdk.HarnessAdapter }

func (h HarnessDispatch) Metadata() sdk.Metadata { return h.Harness.Metadata() }

type harnessBinding struct {
	WorkingDirectory string `json:"workingDirectory"`
	RoleID           string `json:"roleId"`
	NodeID           string `json:"nodeId"`
	SessionID        string `json:"sessionId,omitempty"`
}

func (h HarnessDispatch) Prepare(_ context.Context, request sdk.EffectRequest) (sdk.PreparedEffect, error) {
	var binding harnessBinding
	if err := json.Unmarshal(request.Request, &binding); err != nil {
		return sdk.PreparedEffect{}, fmt.Errorf("decode harness dispatch request: %w", err)
	}
	if binding.WorkingDirectory == "" {
		binding.WorkingDirectory = request.ProjectRoot
	}
	projectRoot, err := filepath.EvalSymlinks(request.ProjectRoot)
	if err != nil {
		return sdk.PreparedEffect{}, err
	}
	workingDirectory, err := filepath.EvalSymlinks(binding.WorkingDirectory)
	if err != nil {
		return sdk.PreparedEffect{}, err
	}
	relative, err := filepath.Rel(projectRoot, workingDirectory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return sdk.PreparedEffect{}, fmt.Errorf("harness working directory must be inside the DAGrail project root")
	}
	if binding.RoleID == "" || binding.NodeID == "" {
		return sdk.PreparedEffect{}, fmt.Errorf("harness dispatch requires roleId and nodeId")
	}
	binding.WorkingDirectory = workingDirectory
	raw, _ := json.Marshal(binding)
	return sdk.PreparedEffect{AdapterID: h.Metadata().ID, Binding: raw}, nil
}

func (h HarnessDispatch) Dispatch(ctx context.Context, request sdk.EffectRequest, prepared sdk.PreparedEffect) (sdk.EffectReceipt, error) {
	var binding harnessBinding
	if prepared.AdapterID != h.Metadata().ID {
		return sdk.EffectReceipt{}, fmt.Errorf("prepared harness adapter mismatch")
	}
	if err := json.Unmarshal(prepared.Binding, &binding); err != nil {
		return sdk.EffectReceipt{}, err
	}
	forward := request
	forward.Request = prepared.Binding
	if binding.SessionID != "" {
		return h.Harness.Resume(ctx, binding.SessionID, forward)
	}
	return h.Harness.Dispatch(ctx, forward)
}

func (h HarnessDispatch) Reconcile(ctx context.Context, request sdk.EffectRequest, _ sdk.PreparedEffect, evidence json.RawMessage) (sdk.EffectReceipt, error) {
	if emptyObject(evidence) {
		var prior sdk.EffectReceipt
		if len(request.PriorReceipt) > 0 {
			if err := json.Unmarshal(request.PriorReceipt, &prior); err != nil {
				return sdk.EffectReceipt{}, fmt.Errorf("decode prior harness receipt: %w", err)
			}
		}
		if observer, ok := h.Harness.(sdk.HarnessObserver); ok && prior.ExternalID != "" {
			return observer.Observe(ctx, prior.ExternalID, request)
		}
	}
	var receipt sdk.EffectReceipt
	if err := json.Unmarshal(evidence, &receipt); err != nil {
		return receipt, fmt.Errorf("decode harness reconciliation receipt: %w", err)
	}
	if receipt.ExternalID != "" && (receipt.RecipientVisible || receipt.DeliveryStatus == "visible") {
		receipt.Status = "confirmed"
	} else {
		receipt.Status = "unknown"
		if receipt.DeliveryStatus == "" {
			receipt.DeliveryStatus = "unknown"
		}
	}
	return receipt, nil
}

func emptyObject(value json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(value))
	return trimmed == "" || trimmed == "{}" || trimmed == "null"
}
