package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/CongBao/dagrail/sdk"
	"github.com/google/uuid"
)

const (
	claudeProtocol    = "claude-cli-json-v1"
	nativeTurnLimit   = 2 * time.Hour
	nativeOutputLimit = 4 * 1024 * 1024
)

type claudeBackend interface {
	Probe(context.Context, string) nativeCapability
	Start(context.Context, string, nativeStartRequest) (sdk.EffectReceipt, error)
}

type claudeCLIBackend struct{}

func (claudeCLIBackend) Probe(ctx context.Context, executable string) nativeCapability {
	result := nativeCapability{Mode: "manual", Stability: "stable", Execution: "synchronous-turn", Reason: "Claude Code headless JSON capability unavailable"}
	if executable == "" {
		result.Reason = "Claude Code executable not found"
		return result
	}
	output, err := boundedCombinedOutput(ctx, executable, "--help")
	if err != nil {
		result.Reason = boundedProtocolError(output, err)
		return result
	}
	help := string(output)
	for _, required := range []string{"--print", "--output-format", "--session-id", "--resume"} {
		if !strings.Contains(help, required) {
			result.Reason = "Claude Code help is missing required flag " + required
			return result
		}
	}
	result.Available, result.Dispatch, result.Resume = true, true, true
	result.Mode, result.Protocol, result.Reason = "native-headless-completion", claudeProtocol, ""
	result.ReceiptProof = []string{"session-created", "synchronous-result", "prompt-digest"}
	return result
}

func (claudeCLIBackend) Start(ctx context.Context, executable string, request nativeStartRequest) (sdk.EffectReceipt, error) {
	turnCtx, cancel := context.WithTimeout(ctx, nativeTurnLimit)
	defer cancel()
	sessionID := request.SessionID
	args := []string{"--print", request.Prompt, "--output-format", "json"}
	if sessionID == "" {
		sessionID = uuid.NewString()
		args = append(args, "--session-id", sessionID)
	} else {
		args = append(args, "--resume", sessionID)
	}
	command := exec.CommandContext(turnCtx, executable, args...)
	command.Dir = request.WorkingDirectory
	stdout := &limitedWriter{remaining: nativeOutputLimit}
	stderr := &limitedWriter{remaining: 64 * 1024}
	command.Stdout, command.Stderr = stdout, stderr
	runErr := command.Run()
	raw := strings.TrimSpace(stdout.String())
	receipt, parseErr := claudeResultReceipt(raw, request, sessionID, runErr)
	if parseErr != nil {
		return sdk.EffectReceipt{}, fmt.Errorf("Claude Code headless turn: %w (%s)", parseErr, boundedText(stderr.String(), 1024))
	}
	return receipt, nil
}

func claudeResultReceipt(raw string, request nativeStartRequest, sessionID string, runErr error) (sdk.EffectReceipt, error) {
	var result struct {
		Type      string          `json:"type"`
		Subtype   string          `json:"subtype"`
		IsError   bool            `json:"is_error"`
		SessionID string          `json:"session_id"`
		Result    json.RawMessage `json:"result"`
	}
	if json.Unmarshal([]byte(raw), &result) != nil || result.Type != "result" || result.SessionID == "" {
		if runErr == nil {
			runErr = fmt.Errorf("Claude Code returned an unsupported JSON result envelope")
		}
		return sdk.EffectReceipt{}, runErr
	}
	if result.SessionID != sessionID {
		return sdk.EffectReceipt{}, fmt.Errorf("Claude Code result belongs to unexpected session %s", result.SessionID)
	}
	detail := claudeReceiptDetail{
		Mode: "native-headless-completion", Protocol: claudeProtocol, ActionID: request.ActionID,
		SessionID: sessionID, PromptSHA256: digestText(request.Prompt), OutputSHA256: digestText(raw),
		OutputBytes: len(raw), Type: result.Type, Subtype: result.Subtype,
	}
	receipt := sdk.EffectReceipt{
		ExternalID: sessionID, TransportAccepted: true, TransportStatus: "accepted", SessionStatus: "created",
		DeliveryStatus: "visible", RecipientVisible: true, AcceptanceStatus: "pending",
	}
	if runErr != nil || result.IsError {
		receipt.Status, receipt.CompletionStatus = "failed", "failed"
		detail.Observation = "headless_result_failed"
	} else {
		receipt.Status, receipt.CompletionStatus = "confirmed", "completed"
		detail.Observation = "headless_result_completed"
	}
	receipt.Detail, _ = json.Marshal(detail)
	return receipt, nil
}

type claudeReceiptDetail struct {
	Mode         string `json:"mode"`
	Protocol     string `json:"protocol"`
	ActionID     string `json:"actionId"`
	SessionID    string `json:"sessionId"`
	PromptSHA256 string `json:"promptSha256"`
	OutputSHA256 string `json:"outputSha256"`
	OutputBytes  int    `json:"outputBytes"`
	Type         string `json:"type"`
	Subtype      string `json:"subtype,omitempty"`
	Observation  string `json:"observation"`
}

func digestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func boundedText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
