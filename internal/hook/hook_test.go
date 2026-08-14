package hook_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/hook"
	"github.com/CongBao/dagrail/internal/service"
)

func TestHookInjectsOnlyBoundedStatePointerAndNeverEchoesPrompt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	if _, err := service.Init(root, "hooks"); err != nil {
		t.Fatal(err)
	}
	prompt := "SECRET-PROMPT-CONTENT I think we are done and should wait"
	sessionPayload, err := json.Marshal(map[string]string{"cwd": root})
	if err != nil {
		t.Fatal(err)
	}
	output, active, err := hook.Run("codex", "session-start", root, bytes.NewReader(sessionPayload))
	if err != nil || !active {
		t.Fatalf("session hook: active=%v err=%v", active, err)
	}
	raw, _ := json.Marshal(output)
	if len(raw) > 2048 || strings.Contains(string(raw), prompt) || !strings.Contains(string(raw), "dag_context") {
		t.Fatalf("hook output is unbounded or unsafe: %d %s", len(raw), raw)
	}
	output, active, err = hook.Run("codex", "user-prompt-submit", root, bytes.NewBufferString(`{"prompt":"`+prompt+`"}`))
	if err != nil || !active {
		t.Fatalf("terminal prompt hook: active=%v err=%v", active, err)
	}
	raw, _ = json.Marshal(output)
	if strings.Contains(string(raw), "SECRET-PROMPT-CONTENT") || !strings.Contains(string(raw), "dag_pre_wait") {
		t.Fatalf("hook echoed prompt or omitted liveness guard: %s", raw)
	}
}
