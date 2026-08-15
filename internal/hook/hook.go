// Package hook provides bounded, read-only harness hooks. It never stores or
// echoes user prompts and never performs lifecycle transitions.
package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/CongBao/dagrail/internal/service"
)

const MaxInputBytes = 1 << 20

type Output struct {
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
	AdditionalContext  string              `json:"additionalContext,omitempty"`
	AdditionalContext2 string              `json:"additional_context,omitempty"`
}

type HookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

func Run(harness, event, root string, reader io.Reader) (Output, bool, error) {
	if !supportedHarness(harness) {
		return Output{}, false, fmt.Errorf("unsupported harness %s", harness)
	}
	if event != "session-start" && event != "user-prompt-submit" && event != "user-prompt-transformed" {
		return Output{}, false, fmt.Errorf("unsupported hook event %s", event)
	}
	payload, err := readObject(reader)
	if err != nil {
		return Output{}, false, nil
	}
	if root == "" {
		root = stringField(payload, "cwd", "workspace", "projectRoot")
	}
	if root == "" {
		root = "."
	}
	// Hooks are observational. In particular, opening a project here must not
	// settle automatic Nodes or repair/synchronize a projection as a hidden
	// lifecycle side effect.
	svc, err := service.OpenForRecovery(root)
	if err != nil {
		return Output{}, false, nil
	}
	state, err := svc.State()
	if err != nil {
		return Output{}, false, nil
	}
	guidance := ""
	eventName := "SessionStart"
	if event == "session-start" {
		frontier, _ := svc.Frontier()
		ready := boundedReadySummary(frontier.Ready, 8)
		revision := state.GraphRevision
		if len(revision) > 12 {
			revision = revision[:12]
		}
		guidance = fmt.Sprintf("DAGrail project %s is authoritative outside chat (cursor %d, graph %s, ready %s). Use $govern-dag only for an assigned control Role, $execute-dag-node for assigned work, or $review-dag-node for assigned review. Read dag_context and execute only returned allowed actions; never infer or edit lifecycle state from chat.", svc.Project.Config.Name, state.HeadSequence, revision, ready)
	} else {
		prompt := stringField(payload, "prompt", "user_prompt", "userPrompt", "message", "text", "transformedPrompt")
		if !looksTerminal(prompt) {
			return Output{}, false, nil
		}
		guidance = "Before yielding, waiting, claiming completion, or declaring blocked, call dag_pre_wait and resolve every reported ready, submitted, expired, or unreconciled item."
		if event == "user-prompt-transformed" {
			eventName = "UserPromptSubmit"
		} else {
			eventName = "UserPromptSubmit"
		}
	}
	guidance = truncateUTF8(guidance, 1900)
	switch harness {
	case "codex", "claude-code":
		return Output{HookSpecificOutput: &HookSpecificOutput{HookEventName: eventName, AdditionalContext: guidance}}, true, nil
	case "copilot-cli":
		return Output{AdditionalContext: guidance}, true, nil
	default:
		return Output{AdditionalContext2: guidance}, true, nil
	}
}

func boundedReadySummary(values []string, maximum int) string {
	if len(values) == 0 {
		return "none"
	}
	count := len(values)
	if count > maximum {
		values = values[:maximum]
	}
	result := strings.Join(values, ",")
	if count > len(values) {
		result += fmt.Sprintf(",…(+%d)", count-len(values))
	}
	return result
}

func truncateUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	end := 0
	for index := range value {
		if index > maximum {
			break
		}
		end = index
	}
	return value[:end]
}

func readObject(reader io.Reader) (map[string]any, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, MaxInputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxInputBytes {
		return nil, fmt.Errorf("hook input exceeds %d bytes", MaxInputBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		if err == io.EOF {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("hook input has trailing content")
	}
	return value, nil
}

func stringField(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok && text != "" {
			return text
		}
	}
	for _, nestedKey := range []string{"event", "payload", "request"} {
		if nested, ok := value[nestedKey].(map[string]any); ok {
			if result := stringField(nested, keys...); result != "" {
				return result
			}
		}
	}
	return ""
}

func looksTerminal(prompt string) bool {
	value := strings.ToLower(prompt)
	for _, signal := range []string{"wait", "yield", "done", "complete", "completed", "blocked", "stop", "finish", "等待", "完成", "结束", "阻塞", "暂停"} {
		if strings.Contains(value, signal) {
			return true
		}
	}
	return false
}

func supportedHarness(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "codex", "claude-code", "copilot-cli", "generic":
		return true
	default:
		return false
	}
}
