package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/service"
)

func TestBetaProjectSurvivesSessionReplacementEffectRetryAndProjectionLoss(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	graphPath, err := filepath.Abs(filepath.Join("..", "..", "examples", "beta-project", "graph.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	current := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	svc, err := service.Init(root, "beta qualification")
	if err != nil {
		t.Fatal(err)
	}
	svc.Now = func() time.Time { return current }
	if _, err := svc.ImportGraph(graphPath, "beta/import", "orchestrator"); err != nil {
		t.Fatal(err)
	}
	assertContextBudget(t, svc, "orchestrator", "", "", 12*1024)

	bindStartFinish(t, svc, "architect", "codex", "architecture", "approved", "architecture-session")
	frontier, err := svc.Frontier()
	if err != nil || !sameStrings(frontier.Ready, []string{"cli", "core", "docs"}) {
		t.Fatalf("parallel frontier mismatch: %+v %v", frontier.Ready, err)
	}

	if _, err := svc.BindRole("codex-worker", "codex", "codex-old", time.Minute, false, "beta/bind/codex-old"); err != nil {
		t.Fatal(err)
	}
	applyKind(t, svc, "codex-worker", "core", "node.start", `{}`, "beta/start/core")
	applyKind(t, svc, "codex-worker", "core", "attempt.checkpoint", `{"summary":"candidate digest recorded; replacement may continue"}`, "beta/checkpoint/core")

	current = current.Add(2 * time.Minute)
	svc = reopenAt(t, root, &current)
	if _, err := svc.BindRole("codex-worker", "claude-code", "codex-successor", time.Hour, true, "beta/takeover/core"); err != nil {
		t.Fatal(err)
	}
	contextBytes, err := svc.Context("worker", "codex-worker", "core", 8192)
	if err != nil {
		t.Fatal(err)
	}
	contextText := string(contextBytes)
	if !strings.Contains(contextText, "codex-successor") || !strings.Contains(contextText, "candidate digest recorded") || strings.Contains(contextText, "codex-old") {
		t.Fatalf("successor context is not bounded to durable state: %s", contextText)
	}
	applyKind(t, svc, "codex-worker", "core", "attempt.submit", `{}`, "beta/submit/core")
	applyKind(t, svc, "codex-worker", "core", "task.complete", `{"outcome":"done"}`, "beta/finish/core")

	svc = reopenAt(t, root, &current)
	bindStartFinish(t, svc, "claude-worker", "claude-code", "cli", "done", "claude-session")
	svc = reopenAt(t, root, &current)
	bindStartFinish(t, svc, "copilot-worker", "copilot-cli", "docs", "done", "copilot-session")
	state, err := svc.State()
	if err != nil || state.Nodes["integrated"].Outcome != "joined" {
		t.Fatalf("join did not settle deterministically: %+v %v", state.Nodes["integrated"], err)
	}

	svc = reopenAt(t, root, &current)
	if _, err := svc.BindRole("reviewer", "codex", "review-session", time.Hour, false, "beta/bind/reviewer"); err != nil {
		t.Fatal(err)
	}
	applyKind(t, svc, "reviewer", "review", "node.start", `{}`, "beta/start/review")
	assertContextBudget(t, svc, "reviewer", "reviewer", "review", 12*1024)
	applyKind(t, svc, "reviewer", "review", "review.resolve", `{"outcome":"approve"}`, "beta/finish/review")

	svc = reopenAt(t, root, &current)
	if _, err := svc.BindRole("release", "codex", "release-session", time.Hour, false, "beta/bind/release"); err != nil {
		t.Fatal(err)
	}
	applyKind(t, svc, "release", "publish", "node.start", `{}`, "beta/start/publish")
	effectRef := actionRef(t, svc, "release", "publish", "effect.prepare")
	first, err := svc.ApplyAction(effectRef, json.RawMessage(`{}`), "beta/effect/publish")
	if err != nil || first.Status != "unknown" || first.ActionID == "" {
		t.Fatalf("manual effect should be durably ambiguous: %+v %v", first, err)
	}
	retry, err := svc.ApplyAction(effectRef, json.RawMessage(`{}`), "beta/effect/publish")
	if err != nil || retry.ActionID != first.ActionID {
		t.Fatalf("idempotent effect retry mismatch: %+v %v", retry, err)
	}

	svc = reopenAt(t, root, &current)
	effect, err := svc.ReconcileEffect(first.ActionID, json.RawMessage(`{"externalId":"publication-1","recipientVisible":true,"deliveryStatus":"visible"}`), "beta/reconcile/publish")
	if err != nil || effect.Status != "confirmed" {
		t.Fatalf("effect reconciliation failed: %+v %v", effect, err)
	}
	applyKind(t, svc, "release", "publish", "effect.complete", `{"outcome":"published"}`, "beta/finish/publish")
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	for nodeID, runtime := range state.Nodes {
		if runtime.Status != "terminal" {
			t.Fatalf("node %s is not terminal: %+v", nodeID, runtime)
		}
	}
	if len(state.Effects) != 1 || state.Nodes["complete"].Outcome != "complete" {
		t.Fatalf("effect or milestone mismatch: effects=%d milestone=%+v", len(state.Effects), state.Nodes["complete"])
	}
	if segments, err := svc.VerifyJournal(); err != nil || len(segments) < 1 {
		t.Fatalf("journal verification failed: segments=%d err=%v", len(segments), err)
	}
	audit, err := svc.PreWait()
	if err != nil || !audit.SafeToWait {
		t.Fatalf("completed project should be safe to wait: %+v %v", audit, err)
	}

	beforeRevision, beforeSequence := state.GraphRevision, state.HeadSequence
	if err := os.Remove(filepath.Join(svc.Project.DataDir, "projection.sqlite")); err != nil {
		t.Fatal(err)
	}
	svc = reopenAt(t, root, &current)
	rebuilt, err := svc.State()
	if err != nil || rebuilt.GraphRevision != beforeRevision || rebuilt.HeadSequence != beforeSequence || len(rebuilt.Effects) != 1 {
		t.Fatalf("projection rebuild changed authority: revision=%s sequence=%d effects=%d err=%v", rebuilt.GraphRevision, rebuilt.HeadSequence, len(rebuilt.Effects), err)
	}
}

func reopenAt(t *testing.T, root string, current *time.Time) *service.Service {
	t.Helper()
	svc, err := service.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	svc.Now = func() time.Time { return *current }
	return svc
}

func bindStartFinish(t *testing.T, svc *service.Service, roleID, harness, nodeID, outcome, session string) {
	t.Helper()
	if _, err := svc.BindRole(roleID, harness, session, time.Hour, false, "beta/bind/"+roleID); err != nil {
		t.Fatal(err)
	}
	applyKind(t, svc, roleID, nodeID, "node.start", `{}`, "beta/start/"+nodeID)
	view, budget := "worker", 8192
	if nodeID == "architecture" {
		view, budget = "orchestrator", 12*1024
	}
	assertContextBudget(t, svc, view, roleID, nodeID, budget)
	if nodeID == "architecture" {
		applyKind(t, svc, roleID, nodeID, "decision.record", `{"outcome":"`+outcome+`"}`, "beta/finish/"+nodeID)
		return
	}
	applyKind(t, svc, roleID, nodeID, "attempt.submit", `{}`, "beta/submit/"+nodeID)
	applyKind(t, svc, roleID, nodeID, "task.complete", `{"outcome":"`+outcome+`"}`, "beta/finish/"+nodeID)
}

func applyKind(t *testing.T, svc *service.Service, roleID, nodeID, kind, input, key string) service.ActionResult {
	t.Helper()
	result, err := svc.ApplyAction(actionRef(t, svc, roleID, nodeID, kind), json.RawMessage(input), key)
	if err != nil {
		t.Fatalf("apply %s to %s: %v", kind, nodeID, err)
	}
	return result
}

func assertContextBudget(t *testing.T, svc *service.Service, view, roleID, nodeID string, budget int) {
	t.Helper()
	value, err := svc.Context(view, roleID, nodeID, budget)
	if err != nil {
		t.Fatal(err)
	}
	if len(value) > budget {
		t.Fatalf("%s context is %d bytes, budget %d", view, len(value), budget)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
