package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/cli"
)

func TestUserCanInitializeImportGraphAndReadFrontier(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{
  "apiVersion":"dagrail.io/v1alpha1",
  "kind":"Graph",
  "metadata":{"name":"example"},
  "spec":{
    "roles":[{"id":"developer","capabilities":["node.run"]}],
    "nodes":[
      {"id":"A","kind":"task","role":"developer","title":"first","outcomes":[{"id":"success","class":"success"}]},
      {"id":"B","kind":"task","role":"developer","title":"second","outcomes":[{"id":"success","class":"success"}]}
    ],
    "edges":[{"id":"A-to-B","from":"A","to":"B","when":{"outcome":"success"}}]
  }
}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &stdout, &stderr)
		if err != nil && stderr.Len() > 0 {
			t.Log(stderr.String())
		}
		return stdout.String(), err
	}

	if _, err := run("init", "--root", root, "--name", "example"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "import-example"); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "import-example"); err != nil {
		t.Fatalf("idempotent import of the same current source failed: %v", err)
	}
	out, err := run("frontier", "--root", root, "--format", "json")
	if err != nil {
		t.Fatalf("frontier: %v", err)
	}
	if !strings.Contains(out, `"ready":["A"]`) {
		t.Fatalf("expected only A ready, got %s", out)
	}
	status, err := run("status", "--root", root)
	if err != nil || !strings.Contains(status, `"headSequence":1`) || !strings.Contains(status, `"blocked":["B"]`) {
		t.Fatalf("operational status unavailable: %v %s", err, status)
	}
	history, err := run("history", "--root", root, "--after", "0", "--limit", "1")
	if err != nil || !strings.Contains(history, `"commandKind":"graph.import"`) || strings.Contains(history, `"payload"`) {
		t.Fatalf("bounded history contract failed: %v %s", err, history)
	}
	backupPath := filepath.Join(root, "journal-backup.json")
	created, err := run("backup", "create", "--root", root, "--output", backupPath)
	if err != nil || !strings.Contains(created, `"valid":true`) {
		t.Fatalf("backup create failed: %v %s", err, created)
	}
	verified, err := run("backup", "verify", "--root", root, "--file", backupPath)
	if err != nil || !strings.Contains(verified, `"segments":1`) {
		t.Fatalf("backup verify failed: %v %s", err, verified)
	}
	compatibility, err := run("journal", "compatibility", "--root", root)
	if err != nil {
		t.Fatalf("journal compatibility: %v", err)
	}
	if !strings.Contains(compatibility, `"currentWriteSegmentSchema":3`) || !strings.Contains(compatibility, `"projectionSchemaVersion":4`) {
		t.Fatalf("compatibility report lacks current schemas: %s", compatibility)
	}
	verification, err := run("journal", "verify", "--root", root)
	if err != nil || !strings.Contains(verification, `"kind":"JournalVerification"`) || !strings.Contains(verification, `"canonicalExportSha256":"sha256:`) {
		t.Fatalf("journal verification lacks bounded integrity evidence: %v %s", err, verification)
	}
	securityAudit, err := run("security", "audit", "--root", root)
	if err != nil || !strings.Contains(securityAudit, `"secure":true`) || !strings.Contains(securityAudit, `"multiTenantIsolation":false`) || strings.Contains(securityAudit, root) {
		t.Fatalf("security audit is unhealthy or path-leaking: %v %s", err, securityAudit)
	}
	providers, err := run("provider", "list", "--root", root)
	if err != nil || !strings.Contains(providers, `"id":"manual"`) || !strings.Contains(providers, `"stability":"experimental"`) {
		t.Fatalf("provider inventory missing built-ins: %v %s", err, providers)
	}
	conformance, err := run("provider", "check", "--root", root)
	if err != nil || !strings.Contains(conformance, `"healthy":true`) {
		t.Fatalf("provider conformance failed: %v %s", err, conformance)
	}
}

func TestWorkerCanBindStartCheckpointFinishAndUnlockDependentNode(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{
  "apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"lifecycle"},
  "spec":{"roles":[{"id":"developer","capabilities":["node.run"]}],
    "nodes":[
      {"id":"A","kind":"task","role":"developer","title":"first","outcomes":[{"id":"success","class":"success"}]},
      {"id":"B","kind":"task","role":"developer","title":"second","outcomes":[{"id":"success","class":"success"}]}
    ],"edges":[{"id":"A-to-B","from":"A","to":"B","when":{"outcome":"success"}}]}}
`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &stdout, &stderr)
		if err != nil && stderr.Len() > 0 {
			t.Log(stderr.String())
		}
		return stdout.String(), err
	}
	if _, err := run("init", "--root", root, "--name", "lifecycle"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "developer", "--harness", "codex", "--session", "session-A", "--idempotency-key", "bind-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "developer", "--harness", "claude-code", "--session", "session-B", "--idempotency-key", "bind-B"); err == nil {
		t.Fatal("second live binding for the same stable role must fail")
	}

	startRef := allowedActionRef(t, run, root, "developer", "A", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", startRef, "--input", `{}`, "--idempotency-key", "start-A"); err != nil {
		t.Fatalf("start: %v", err)
	}
	checkpointRef := allowedActionRef(t, run, root, "developer", "A", "attempt.checkpoint")
	if _, err := run("action", "apply", "--root", root, "--ref", checkpointRef, "--input", `{"summary":"candidate prepared","evidenceRefs":[{"digest":"sha256:abc","type":"test-report","size":12}]}`, "--idempotency-key", "checkpoint-A"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	submitRef := allowedActionRef(t, run, root, "developer", "A", "attempt.submit")
	if _, err := run("action", "apply", "--root", root, "--ref", submitRef, "--input", `{}`, "--idempotency-key", "submit-A"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	finishRef := allowedActionRef(t, run, root, "developer", "A", "task.complete")
	if _, err := run("action", "apply", "--root", root, "--ref", finishRef, "--input", `{"outcome":"success"}`, "--idempotency-key", "finish-A"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	out, err := run("frontier", "--root", root, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"ready":["B"]`) {
		t.Fatalf("expected B ready after A succeeds, got %s", out)
	}
	context, err := run("context", "--root", root, "--view", "worker", "--role", "developer", "--node", "A", "--budget-bytes", "8192")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(context, `"summary":"candidate prepared"`) || !strings.Contains(context, `"outcome":"success"`) {
		t.Fatalf("successor context must contain checkpoint and terminal result, got %s", context)
	}
}

func TestExecutionEvidenceCanBePublishedInspectedAndReusedAcrossPolicyChanges(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"evidence"},"spec":{"roles":[{"id":"developer","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"developer","title":"A","objective":"build","outcomes":[{"id":"success","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	if _, err := run("init", "--root", root, "--name", "evidence"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "developer", "--harness", "codex", "--session", "evidence-session", "--idempotency-key", "bind-evidence"); err != nil {
		t.Fatal(err)
	}
	startRef := allowedActionRef(t, run, root, "developer", "A", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", startRef, "--idempotency-key", "start"); err != nil {
		t.Fatal(err)
	}
	protected := []map[string]string{{"name": "toolchain", "digest": cliDigest("d")}, {"name": "fixture", "digest": cliDigest("c")}}
	artifact := func(digest, artifactType string) map[string]any {
		return map[string]any{"digest": digest, "type": artifactType, "size": 12, "provenance": map[string]string{"producer": "codex", "revision": "1"}}
	}
	packageInput, _ := json.Marshal(map[string]any{
		"candidate": artifact(cliDigest("a"), "candidate"), "prospectiveTree": artifact(cliDigest("b"), "git-tree"),
		"commandGraphDigest": cliDigest("e"), "protectedInputs": protected,
		"observations": map[string]bool{"exact": true, "clean": true, "depthComplete": true, "sourceUnmodified": true, "resourcesClosed": true},
		"artifacts":    []map[string]any{artifact(cliDigest("f"), "test-report")},
	})
	publishRef := allowedActionRef(t, run, root, "developer", "A", "evidence.publish")
	published, err := run("action", "apply", "--root", root, "--ref", publishRef, "--input", string(packageInput), "--idempotency-key", "publish-package")
	if err != nil {
		t.Fatalf("publish evidence: %v", err)
	}
	var publishResult struct {
		ObjectRef string `json:"objectRef"`
	}
	if err := json.Unmarshal([]byte(published), &publishResult); err != nil || !strings.HasPrefix(publishResult.ObjectRef, "evidence-package:epkg_") {
		t.Fatalf("publish result lacks package ref: %v %s", err, published)
	}
	packageID := strings.TrimPrefix(publishResult.ObjectRef, "evidence-package:")
	listed, err := run("evidence", "list", "--root", root, "--node", "A")
	if err != nil || !strings.Contains(listed, packageID) {
		t.Fatalf("evidence index missing package: %v %s", err, listed)
	}
	inspectedPackage, err := run("inspect", "--root", root, publishResult.ObjectRef)
	if err != nil || !strings.Contains(inspectedPackage, cliDigest("a")) || strings.Contains(inspectedPackage, "prompt") {
		t.Fatalf("inspect package is incomplete or leaked forbidden content: %v %s", err, inspectedPackage)
	}

	reuseInput, _ := json.Marshal(map[string]any{
		"packageId": packageID, "policy": map[string]string{"id": "validator", "version": "2.0.0", "schemaHash": cliDigest("1")},
		"candidateDigest": cliDigest("a"), "prospectiveTreeDigest": cliDigest("b"), "commandGraphDigest": cliDigest("e"), "protectedInputs": protected,
	})
	reuseRef := allowedActionRef(t, run, root, "developer", "A", "evidence.assess-reuse")
	reused, err := run("action", "apply", "--root", root, "--ref", reuseRef, "--input", string(reuseInput), "--idempotency-key", "reuse-policy-v2")
	if err != nil {
		t.Fatalf("assess reuse: %v", err)
	}
	var reuseResult struct {
		ObjectRef string `json:"objectRef"`
	}
	if err := json.Unmarshal([]byte(reused), &reuseResult); err != nil || !strings.HasPrefix(reuseResult.ObjectRef, "reuse-decision:reuse_") {
		t.Fatalf("reuse result lacks decision ref: %v %s", err, reused)
	}
	decision, err := run("inspect", "--root", root, reuseResult.ObjectRef)
	if err != nil || !strings.Contains(decision, `"result":"reuse_execution"`) || !strings.Contains(decision, `"protected_core_unchanged"`) {
		t.Fatalf("policy-only change should reuse execution: %v %s", err, decision)
	}

	changedInput, _ := json.Marshal(map[string]any{
		"packageId": packageID, "policy": map[string]string{"id": "validator", "version": "3.0.0", "schemaHash": cliDigest("2")},
		"candidateDigest": cliDigest("9"), "prospectiveTreeDigest": cliDigest("b"), "commandGraphDigest": cliDigest("e"), "protectedInputs": protected,
	})
	changedRef := allowedActionRef(t, run, root, "developer", "A", "evidence.assess-reuse")
	changed, err := run("action", "apply", "--root", root, "--ref", changedRef, "--input", string(changedInput), "--idempotency-key", "reuse-policy-v3-changed-core")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(changed, `"objectRef":"reuse-decision:`) {
		t.Fatalf("changed-core decision ref missing: %s", changed)
	}
	context, err := run("context", "--root", root, "--view", "worker", "--role", "developer", "--node", "A", "--budget-bytes", "8192")
	if err != nil || !strings.Contains(context, `"result":"rerun_required"`) {
		t.Fatalf("worker context lacks latest reuse decision: %v %s", err, context)
	}
}

func allowedActionRef(t *testing.T, run func(...string) (string, error), root, role, node, kind string) string {
	t.Helper()
	out, err := run("action", "list", "--root", root, "--role", role, "--node", node)
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	var value struct {
		Actions []struct{ Kind, Ref string } `json:"actions"`
	}
	if err := json.Unmarshal([]byte(out), &value); err != nil {
		t.Fatalf("decode actions: %v", err)
	}
	for _, action := range value.Actions {
		if action.Kind == kind {
			return action.Ref
		}
	}
	t.Fatalf("action %s not found in %s", kind, out)
	return ""
}

func cliDigest(character string) string { return "sha256:" + strings.Repeat(character, 64) }

func TestGraphChangeRequiresImpactTokenAndProtectsActiveNodes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"changes"},"spec":{"roles":[{"id":"developer","capabilities":["node.run","graph.change"]}],"nodes":[{"id":"A","kind":"task","role":"developer","title":"A","outcomes":[{"id":"success","class":"success"}]},{"id":"B","kind":"task","role":"developer","title":"B","outcomes":[{"id":"success","class":"success"}]}],"edges":[{"id":"A-B","from":"A","to":"B","when":{"outcome":"success"}}]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(root, "patch.json")
	patch := `{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"updateRole","role":{"id":"developer","capabilities":["node.run","graph.change","incident.manage"]}},{"op":"addNode","node":{"id":"C","kind":"task","role":"developer","title":"C","outcomes":[{"id":"pass","class":"success"}]}},{"op":"addEdge","edge":{"id":"B-C","from":"B","to":"C","when":{"outcome":"success"}}}]}`
	if err := os.WriteFile(patchPath, []byte(patch), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	if _, err := run("init", "--root", root); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "developer", "--harness", "codex", "--session", "graph-session", "--idempotency-key", "bind-graph"); err != nil {
		t.Fatal(err)
	}
	preview, err := run("graph", "preview-change", "--root", root, "--file", patchPath)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	var impact struct{ Token, ProposedRevision string }
	if err := json.Unmarshal([]byte(preview), &impact); err != nil {
		t.Fatal(err)
	}
	if impact.Token == "" || impact.ProposedRevision == "" {
		t.Fatalf("preview must return a bound impact token: %s", preview)
	}
	if _, err := run("graph", "apply-change", "--root", root, "--file", patchPath, "--token", impact.Token, "--actor-role", "developer", "--idempotency-key", "patch-1"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	exported, err := run("graph", "export", "--root", root, "--format", "json")
	if err != nil || !strings.Contains(exported, `"id":"C"`) || !strings.Contains(exported, `"incident.manage"`) {
		t.Fatalf("exported graph lacks C: %v %s", err, exported)
	}
	if _, err := run("graph", "apply-change", "--root", root, "--file", patchPath, "--token", impact.Token, "--actor-role", "developer", "--idempotency-key", "patch-stale"); err == nil {
		t.Fatal("consumed/stale impact token must fail")
	}

	startRef := allowedActionRef(t, run, root, "developer", "A", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", startRef, "--idempotency-key", "start"); err != nil {
		t.Fatal(err)
	}
	updatePath := filepath.Join(root, "update-active.json")
	update := `{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"updateNode","node":{"id":"A","kind":"task","role":"developer","title":"changed","outcomes":[{"id":"success","class":"success"}]}}]}`
	if err := os.WriteFile(updatePath, []byte(update), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "preview-change", "--root", root, "--file", updatePath); err == nil {
		t.Fatal("active node contract must be frozen")
	}
}

func TestContextBudgetInspectAndPreWaitAreMachineDecidable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"context"},"spec":{"roles":[{"id":"developer","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"developer","title":"A","objective":"` + strings.Repeat("bounded-", 3000) + `","outcomes":[{"id":"success","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	if _, err := run("init", "--root", root); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph"); err != nil {
		t.Fatal(err)
	}
	audit, err := run("pre-wait", "--root", root)
	if err != nil || !strings.Contains(audit, `"safeToWait":false`) || !strings.Contains(audit, `"readyNodes":["A"]`) {
		t.Fatalf("ready work must reject passive wait: %v %s", err, audit)
	}
	context, err := run("context", "--root", root, "--view", "worker", "--node", "A", "--budget-bytes", "1024")
	if err != nil {
		t.Fatal(err)
	}
	if len(context) > 1025 || !strings.Contains(context, `"truncated":true`) {
		t.Fatalf("context must honor budget: %d %s", len(context), context)
	}
	inspected, err := run("inspect", "--root", root, "node:A")
	if err != nil || !strings.Contains(inspected, `"objective":"bounded-`) {
		t.Fatalf("inspect must provide opt-in detail: %v %s", err, inspected)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "developer", "--harness", "codex", "--session", "s", "--idempotency-key", "bind"); err != nil {
		t.Fatal(err)
	}
	startRef := allowedActionRef(t, run, root, "developer", "A", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", startRef, "--idempotency-key", "start"); err != nil {
		t.Fatal(err)
	}
	audit, err = run("pre-wait", "--root", root)
	if err != nil || !strings.Contains(audit, `"safeToWait":true`) {
		t.Fatalf("running attempt permits bounded yield: %v %s", err, audit)
	}
	submitRef := allowedActionRef(t, run, root, "developer", "A", "attempt.submit")
	if _, err := run("action", "apply", "--root", root, "--ref", submitRef, "--idempotency-key", "submit"); err != nil {
		t.Fatal(err)
	}
	audit, err = run("pre-wait", "--root", root)
	if err != nil || !strings.Contains(audit, `"safeToWait":false`) || !strings.Contains(audit, `"submittedAttempts"`) {
		t.Fatalf("submitted work must advance before wait: %v %s", err, audit)
	}
}

func TestManualEffectRemainsUnknownUntilRecipientVisibleReceiptIsReconciled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".test-data"))
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect"},"spec":{"roles":[{"id":"orchestrator","capabilities":["effect.apply","effect.reconcile"]}],"nodes":[{"id":"deliver","kind":"effect","role":"orchestrator","title":"deliver handoff","inputs":{"adapter":"manual","request":{"instruction":"Deliver work package to reviewer"}},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	if _, err := run("init", "--root", root); err != nil {
		t.Fatal(err)
	}
	if _, err := run("graph", "import", "--root", root, "--file", graphPath, "--idempotency-key", "graph"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("role", "bind", "--root", root, "--role", "orchestrator", "--harness", "codex", "--session", "central", "--idempotency-key", "bind"); err != nil {
		t.Fatal(err)
	}
	startRef := allowedActionRef(t, run, root, "orchestrator", "deliver", "node.start")
	if _, err := run("action", "apply", "--root", root, "--ref", startRef, "--idempotency-key", "start"); err != nil {
		t.Fatal(err)
	}
	prepareRef := allowedActionRef(t, run, root, "orchestrator", "deliver", "effect.prepare")
	prepared, err := run("action", "apply", "--root", root, "--ref", prepareRef, "--idempotency-key", "deliver-1")
	if err != nil {
		t.Fatalf("prepare effect: %v", err)
	}
	var result struct{ ActionID, Status string }
	if err := json.Unmarshal([]byte(prepared), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "unknown" || result.ActionID == "" {
		t.Fatalf("manual dispatch cannot self-confirm delivery: %s", prepared)
	}
	audit, err := run("pre-wait", "--root", root)
	if err != nil || !strings.Contains(audit, `"pendingEffects"`) || !strings.Contains(audit, `"safeToWait":false`) {
		t.Fatalf("unknown effect must block dependent control action: %v %s", err, audit)
	}
	if _, err := run("reconcile", "--root", root, "--action", result.ActionID, "--receipt", `{"externalId":"receipt-1","recipientVisible":true}`, "--idempotency-key", "reconcile-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	inspected, err := run("inspect", "--root", root, "effect:"+result.ActionID)
	if err != nil || !strings.Contains(inspected, `"status":"confirmed"`) {
		t.Fatalf("effect should be confirmed from visible receipt: %v %s", err, inspected)
	}
	if ref := allowedActionRef(t, run, root, "orchestrator", "deliver", "effect.complete"); ref == "" {
		t.Fatal("confirmed effect should allow explicit terminal outcome")
	}
}

func TestSignatureCLIProducesPortableDetachedVerification(t *testing.T) {
	root := t.TempDir()
	payload := filepath.Join(root, "journal.ndjson")
	privateKey := filepath.Join(root, "private.pem")
	publicKey := filepath.Join(root, "public.pem")
	signature := filepath.Join(root, "journal.ndjson.sig.json")
	if err := os.WriteFile(payload, []byte("portable journal export\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	if output, err := run("signature", "keygen", "--private-key", privateKey, "--public-key", publicKey); err != nil || !strings.Contains(output, `"valid":true`) {
		t.Fatalf("keygen: %v %s", err, output)
	}
	if output, err := run("signature", "sign", "--file", payload, "--private-key", privateKey, "--output", signature); err != nil || !strings.Contains(output, `"payloadSha256":"sha256:`) {
		t.Fatalf("sign: %v %s", err, output)
	}
	if output, err := run("signature", "verify", "--file", payload, "--signature", signature, "--public-key", publicKey); err != nil || !strings.Contains(output, `"valid":true`) {
		t.Fatalf("verify: %v %s", err, output)
	}
}

func TestContractCLIReportsTheClosedMCPBetaSurface(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := cli.Run([]string{"contract"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"apiVersion":"dagrail.io/v1beta1"`) || !strings.Contains(out.String(), `"name":"dag_pre_wait"`) || !strings.Contains(out.String(), `"providerSdk":{"apiVersion":"dagrail.io/provider/v1alpha1"`) {
		t.Fatalf("unexpected compatibility contract: %s", out.String())
	}
}

func TestPluginBundleCanBeMaterializedWithoutAHostOrNetwork(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime-data"))
	var out, errOut bytes.Buffer
	if err := cli.Run([]string{"plugin", "materialize"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"status":"materialized"`) || !strings.Contains(out.String(), `"digest":"sha256:`) {
		t.Fatalf("unexpected bundle receipt: %s", out.String())
	}
	out.Reset()
	if err := cli.Run([]string{"plugin", "bundle-status"}, strings.NewReader(""), &out, &errOut); err != nil || !strings.Contains(out.String(), `"status":"verified"`) {
		t.Fatalf("bundle status: %v %s", err, out.String())
	}
}

func TestSupportCLIExportsOnceWithoutPrivateAuthority(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "private-runtime"))
	var out, errOut bytes.Buffer
	if err := cli.Run([]string{"init", "--root", root, "--name", "private-project"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "support.json")
	out.Reset()
	args := []string{"support", "export", "--root", root, "--output", output}
	if err := cli.Run(args, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(raw), `"apiVersion":"dagrail.io/support/v1alpha1"`) || strings.Contains(string(raw), root) || strings.Contains(string(raw), "private-project") {
		t.Fatalf("unsafe support export: %v %s", err, raw)
	}
	if err := cli.Run(args, strings.NewReader(""), &out, &errOut); err == nil {
		t.Fatal("support export overwrote an existing report")
	}
}

func TestRecoveryCLIEmitsSchemaBoundReadOnlyRehearsal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime-data"))
	var out, errOut bytes.Buffer
	if err := cli.Run([]string{"init", "--root", root, "--name", "recovery"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := cli.Run([]string{"recovery", "rehearse", "--root", root}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"apiVersion":"dagrail.io/recovery-rehearsal/v1alpha1"`) || !strings.Contains(out.String(), `"ready":true`) || !strings.Contains(out.String(), `"projectionEquivalent":true`) {
		t.Fatalf("unexpected recovery rehearsal: %s", out.String())
	}
}

func TestQualifyReleaseCLIDistinguishesCandidateFromProductionEvidence(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := cli.Run([]string{"qualify", "release", "--source", root}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"apiVersion":"dagrail.io/release-qualification/v1alpha1"`) || !strings.Contains(out.String(), `"structuralCandidate":true`) || !strings.Contains(out.String(), `"productionValidated":false`) {
		t.Fatalf("unexpected release qualification: %s", out.String())
	}
}

func TestQualifyReleaseCLIRejectsTrailingArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	err := cli.Run([]string{"qualify", "release", "unexpected"}, strings.NewReader(""), &out, &errOut)
	if err == nil || out.Len() != 0 {
		t.Fatalf("trailing qualification argument was not rejected: err=%v output=%s", err, out.String())
	}
}

func TestReleaseVerificationCLIEmitsPathFreeFailureEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-release-root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	err := cli.Run([]string{"release", "verify", "--directory", root}, strings.NewReader(""), &out, &errOut)
	if err == nil || !strings.Contains(out.String(), `"apiVersion":"dagrail.io/release-verification/v1alpha1"`) || !strings.Contains(out.String(), `"verified":false`) || strings.Contains(out.String(), root) || strings.Contains(out.String(), "private-release-root") {
		t.Fatalf("unexpected release verification failure: err=%v output=%s", err, out.String())
	}
}

func TestReleaseManifestCLIRejectsVersionDriftAndTrailingArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	err := cli.Run([]string{"release", "manifest", "--version", "99.0.0", "--commit", "0123456789abcdef0123456789abcdef01234567", "--source-date-epoch", "1786665600"}, strings.NewReader(""), &out, &errOut)
	if err == nil || out.Len() != 0 {
		t.Fatalf("release version drift was accepted: err=%v output=%s", err, out.String())
	}
	err = cli.Run([]string{"release", "verify", "unexpected"}, strings.NewReader(""), &out, &errOut)
	if err == nil {
		t.Fatal("release CLI accepted a trailing argument")
	}
	err = cli.Run([]string{"release", "verify", "--commit", "0123456789abcdef0123456789abcdef01234567"}, strings.NewReader(""), &out, &errOut)
	if err == nil {
		t.Fatal("release verification accepted a manifest-only flag")
	}
}

func TestObserveCLIRecordsOnlyAnIsolatedShadow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime-data"))
	source := filepath.Join(root, "existing-project")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "requirements.md"), []byte("requirement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "converted.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"shadow"},"spec":{"roles":[{"id":"dev","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"dev","title":"A","outcomes":[{"id":"ok","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	shadow := filepath.Join(root, "shadow")
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	assessed, err := run("observe", "assess", "--source-root", source, "--graph", graphPath, "--authority", "requirements.md")
	if err != nil || !strings.Contains(assessed, `"kind":"ObservationSnapshot"`) {
		t.Fatalf("assess: %v %s", err, assessed)
	}
	created, err := run("observe", "create-shadow", "--source-root", source, "--graph", graphPath, "--shadow-root", shadow, "--authority", "requirements.md")
	if err != nil || !strings.Contains(created, `"status":"created"`) {
		t.Fatalf("create shadow: %v %s", err, created)
	}
	verified, err := run("observe", "verify-shadow", "--shadow-root", shadow)
	if err != nil || !strings.Contains(verified, `"valid":true`) {
		t.Fatalf("verify shadow: %v %s", err, verified)
	}
	if _, err := os.Stat(filepath.Join(source, ".dagrail")); !os.IsNotExist(err) {
		t.Fatalf("observe CLI wrote into source: %v", err)
	}
}
