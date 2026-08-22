package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/effects"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/providers"
	"github.com/CongBao/dagrail/sdk"
	"github.com/google/uuid"
)

func TestPreWaitProposesTypedRepairAndIncidentSupersedeClosesOldAlert(t *testing.T) {
	svc, root := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"repair-plan"},"spec":{"roles":[{"id":"governor","capabilities":["node.run","incident.manage","graph.change"]}],"nodes":[{"id":"admission","kind":"task","role":"governor","title":"admission","outcomes":[{"id":"accepted","class":"success"},{"id":"returned","class":"failure"}]},{"id":"repair","kind":"task","role":"governor","title":"repair","outcomes":[{"id":"completed","class":"success"}]}],"edges":[{"id":"returned-to-repair","from":"admission","to":"repair","when":{"outcome":"returned"}}]}}`)
	if _, err := svc.BindRole("governor", "codex", "governor-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	started, err := svc.ApplyAction(findActionRef(t, svc, "governor", "admission", "node.start"), json.RawMessage(`{}`), "start")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "governor", "admission", "task.complete"), json.RawMessage(`{"outcome":"returned","classification":"infrastructure"}`), "return"); err != nil {
		t.Fatal(err)
	}
	incidentID := "attempt:" + started.AttemptID
	audit, err := svc.PreWait()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, remediation := range audit.Remediations {
		if remediation.Code == "supersede_incident_with_repair" && remediation.TargetRef == "incident:"+incidentID && remediation.Operation.Params["successorNodeId"] == "repair" {
			found = true
		}
	}
	if !found {
		t.Fatalf("repair remediation missing: %#v", audit.Remediations)
	}
	if _, err := svc.ResolveIncident(incidentID, "governor", incidentResolutionSupersededByRepair, "forged-resolve"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("ordinary resolve forged the typed repair resolution: %v", err)
	}
	state, _, err := svc.load()
	if err != nil {
		t.Fatal(err)
	}
	openIncident := state.Incidents[incidentID]
	var actionRef string
	for _, action := range svc.projectAllowedActions(state, "governor", 24) {
		if action.Kind == "incident.supersede" && action.TargetRef == "incident:"+incidentID {
			actionRef = action.Ref
			break
		}
	}
	if actionRef == "" {
		t.Fatal("signed incident supersede action was not exposed")
	}
	patchPath := root + "/repair-title-change.json"
	if err := os.WriteFile(patchPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"updateNode","node":{"id":"repair","kind":"task","role":"governor","title":"repair after concurrent graph change","outcomes":[{"id":"completed","class":"success"}]}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.PreviewGraphChange(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	var hookErr error
	svc.beforeIncidentSupersedeAppend = func() {
		svc.beforeIncidentSupersedeAppend = nil
		_, hookErr = svc.ApplyGraphChange(patchPath, preview.Token, "concurrent-graph-change", "governor")
	}
	if _, err := svc.ApplyAction(actionRef, json.RawMessage(`{"note":"stale action must not cross the new head"}`), "stale-supersede"); err == nil || (!strings.Contains(err.Error(), "changed") && !strings.Contains(err.Error(), "head")) {
		t.Fatalf("signed incident action crossed a concurrent head change: %v", err)
	}
	if hookErr != nil {
		t.Fatalf("concurrent graph change failed: %v", hookErr)
	}
	state, _, err = svc.load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Incidents[incidentID].Status != "open" || state.Incidents[incidentID].RemedyNodeID != "" {
		t.Fatalf("stale signed action mutated the incident: %#v", state.Incidents[incidentID])
	}
	actionRef = ""
	for _, action := range svc.projectAllowedActions(state, "governor", 24) {
		if action.Kind == "incident.supersede" && action.TargetRef == "incident:"+incidentID {
			actionRef = action.Ref
			break
		}
	}
	if actionRef == "" {
		t.Fatal("fresh signed incident supersede action was not exposed")
	}
	result, err := svc.ApplyAction(actionRef, json.RawMessage(`{"note":"repair node now owns the bounded remedy"}`), "supersede")
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "incident.supersede" || result.ObjectRef != "incident:"+incidentID || result.NodeID != "repair" {
		t.Fatalf("unexpected signed incident action result: %#v", result)
	}
	state, _, err = svc.load()
	if err != nil {
		t.Fatal(err)
	}
	incident := state.Incidents[incidentID]
	if incident.Status != "resolved" || incident.Resolution != "superseded_by_repair" || incident.RemedyNodeID != "repair" || incident.SupersededAt == "" {
		t.Fatalf("incident successor was not journaled: %#v", incident)
	}
	if !validExplicitIncidentUpdate(domain.State{}, openIncident, incident) {
		t.Fatal("migration transition rejected the complete typed repair binding")
	}
	if _, err := svc.ApplyAction(actionRef, json.RawMessage(`{"note":"different intent"}`), "supersede"); err == nil || !strings.Contains(err.Error(), "another command") {
		t.Fatalf("changed supersede intent reused an idempotency key: %v", err)
	}
	prior := openIncident
	forged := openIncident
	forged.Status = "resolved"
	forged.Resolution = incidentResolutionSupersededByRepair
	forged.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if !validExplicitIncidentUpdate(domain.State{}, prior, forged) {
		t.Fatal("migration transition rejected a v0.22 legacy ordinary resolution string")
	}
	partial := forged
	partial.RemedyNodeID = "repair"
	if validExplicitIncidentUpdate(domain.State{}, prior, partial) {
		t.Fatal("migration transition accepted a partial typed repair binding")
	}
}

func TestCompactIncidentSupersedeRetrySurvivesRoleSessionReplacement(t *testing.T) {
	repairID := "repair-" + strings.Repeat("x", 30_000)
	graph := fmt.Sprintf(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"compact-incident-retry"},"spec":{"roles":[{"id":"governor","capabilities":["node.run","incident.manage"]}],"nodes":[{"id":"admission","kind":"task","role":"governor","title":"admission","outcomes":[{"id":"returned","class":"failure"}]},{"id":%q,"kind":"task","role":"governor","title":"repair","outcomes":[{"id":"completed","class":"success"}]}],"edges":[{"id":"returned-to-repair","from":"admission","to":%q,"when":{"outcome":"returned"}}]}}`, repairID, repairID)
	svc, _ := governanceService(t, graph)
	if _, err := svc.BindRole("governor", "codex", "session-one", time.Hour, false, "bind-one"); err != nil {
		t.Fatal(err)
	}
	started, err := svc.ApplyAction(findActionRef(t, svc, "governor", "admission", "node.start"), json.RawMessage(`{}`), "start")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "governor", "admission", "task.complete"), json.RawMessage(`{"outcome":"returned","classification":"infrastructure"}`), "return"); err != nil {
		t.Fatal(err)
	}
	state, _, err := svc.load()
	if err != nil {
		t.Fatal(err)
	}
	incidentID := "attempt:" + started.AttemptID
	var action AllowedAction
	for _, candidate := range svc.projectAllowedActions(state, "governor", 24) {
		if candidate.Kind == "incident.supersede" && candidate.TargetRef == "incident:"+incidentID {
			action = candidate
			break
		}
	}
	if action.Ref == "" {
		t.Fatal("compact incident supersede action was not exposed")
	}
	secret, err := svc.actionSecret()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := verifyActionRef(action.Ref, secret)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.Compact || payload.SuccessorKey == "" || payload.SuccessorNodeID != "" {
		t.Fatalf("oversized incident action was not compact: %#v", payload)
	}
	input := json.RawMessage(`{"note":"bind the durable repair successor"}`)
	first, err := svc.ApplyAction(action.Ref, input, "supersede-long")
	if err != nil {
		t.Fatal(err)
	}
	if first.NodeID != "" || first.NodeRef == "" {
		t.Fatalf("oversized action result was not bounded: %#v", first)
	}
	if err := svc.ReleaseRole("governor", "session-one", "release-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("governor", "codex", "session-two", time.Hour, false, "bind-two"); err != nil {
		t.Fatal(err)
	}
	beforeRetry, _ := svc.State()
	retried, err := svc.ApplyAction(action.Ref, input, "supersede-long")
	if err != nil {
		t.Fatalf("exact compact retry after session replacement failed: %v", err)
	}
	afterRetry, _ := svc.State()
	if !reflect.DeepEqual(actionResultIdentity(retried), actionResultIdentity(first)) || afterRetry.HeadSequence != beforeRetry.HeadSequence {
		t.Fatalf("compact retry did not return the original result without appending: first=%#v retry=%#v before=%d after=%d", first, retried, beforeRetry.HeadSequence, afterRetry.HeadSequence)
	}
	canonicalRetryInput := json.RawMessage("{\n  \"note\" : \"bind the durable repair successor\"\n}")
	beforeCanonicalRetry, _ := svc.State()
	canonicalRetry, err := svc.ApplyAction(action.Ref, canonicalRetryInput, "supersede-long")
	afterCanonicalRetry, _ := svc.State()
	if err != nil || !reflect.DeepEqual(actionResultIdentity(canonicalRetry), actionResultIdentity(first)) || afterCanonicalRetry.HeadSequence != beforeCanonicalRetry.HeadSequence {
		t.Fatalf("canonical-equivalent cross-session retry was not idempotent: result=%#v err=%v before=%d after=%d", canonicalRetry, err, beforeCanonicalRetry.HeadSequence, afterCanonicalRetry.HeadSequence)
	}
	if _, err := svc.ApplyAction(action.Ref, json.RawMessage(`{"note":"changed intent"}`), "supersede-long"); err == nil || !strings.Contains(err.Error(), "another command") {
		t.Fatalf("compact retry accepted changed intent: %v", err)
	}
}

func TestLifecycleMigrationRetainsV022IncidentResolutionCompatibility(t *testing.T) {
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"legacy-incident-resolution"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","incident.manage"]}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"work","outcomes":[{"id":"failed","class":"failure"}]},{"id":"repair","kind":"task","role":"worker","title":"repair","outcomes":[{"id":"done","class":"success"}]}],"edges":[{"id":"repair-edge","from":"work","to":"repair","when":{"outcome":"failed"}}]}}`
	svc, initial := lifecycleWriterService(t, "legacy-incident-resolution", graph)
	_, _ = svc.BindRole("worker", "codex", "session", time.Hour, false, "bind")
	started, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "node.start"), json.RawMessage(`{}`), "start")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "task.complete"), json.RawMessage(`{"outcome":"failed"}`), "fail"); err != nil {
		t.Fatal(err)
	}
	incidentID := "attempt:" + started.AttemptID
	if _, err := svc.ResolveIncident(incidentID, "worker", "legacy ordinary resolution", "resolve"); err != nil {
		t.Fatal(err)
	}
	records := lifecycleRecordsFromWriter(t, svc, initial.HeadSequence)
	found := false
	for recordIndex := range records {
		for eventIndex := range records[recordIndex].Events {
			event := &records[recordIndex].Events[eventIndex]
			if event.Type != "incident.updated" {
				continue
			}
			var incident map[string]any
			_ = json.Unmarshal(event.Payload, &incident)
			if incident["id"] != incidentID || incident["status"] != "resolved" {
				continue
			}
			incident["resolution"] = incidentResolutionSupersededByRepair
			event.Payload = payloadJSON(incident)
			found = true
		}
	}
	if !found {
		t.Fatal("writer history did not contain the resolved Incident")
	}
	if err := validateLifecycleRecordsManifest(t, svc, initial, records); err != nil {
		t.Fatalf("v0.22 legacy Incident resolution was rejected: %v", err)
	}
	partial := cloneLifecycleRecords(t, records)
	for recordIndex := range partial {
		for eventIndex := range partial[recordIndex].Events {
			event := &partial[recordIndex].Events[eventIndex]
			if event.Type != "incident.updated" {
				continue
			}
			var incident map[string]any
			_ = json.Unmarshal(event.Payload, &incident)
			if incident["id"] == incidentID && incident["status"] == "resolved" {
				incident["remedyNodeId"] = "repair"
				event.Payload = payloadJSON(incident)
			}
		}
	}
	if err := validateLifecycleRecordsManifest(t, svc, initial, partial); err == nil || !strings.Contains(err.Error(), "repair successor") {
		t.Fatalf("partial typed repair binding was accepted: %v", err)
	}
}

func TestOrchestratorContextCarriesAuthorizationProjectActionsAndRemediations(t *testing.T) {
	svc, _ := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"bounded-ops"},"spec":{"roles":[{"id":"controller","capabilities":["node.run"]}],"nodes":[{"id":"one","kind":"task","role":"controller","title":"one","outcomes":[{"id":"done","class":"success"}]},{"id":"two","kind":"task","role":"controller","title":"two","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	if _, err := svc.BindRole("controller", "codex", "controller-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	raw, err := svc.Context("orchestrator", "controller", "", 12288)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, expected := range []string{`"authorization"`, `"leaseState":"active"`, `"projectAllowedActions"`, `"nodeId":"one"`, `"remediations"`, `"assign_ready_node"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("orchestrator context omitted %s: %s", expected, text)
		}
	}
	if len(raw) > 12288 {
		t.Fatalf("orchestrator context exceeded budget: %d", len(raw))
	}
}

func TestReadyNodeRemediationSeparatesRoleBindingFromNodeStart(t *testing.T) {
	svc, _ := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"ready-role-prerequisite"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"work","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)

	audit, err := svc.PreWait()
	if err != nil {
		t.Fatal(err)
	}
	var bind Remediation
	for _, remediation := range audit.Remediations {
		switch remediation.Code {
		case "bind_ready_node_role":
			bind = remediation
		case "assign_ready_node":
			t.Fatalf("missing Role lease exposed node.start assignment: %#v", remediation)
		}
	}
	if bind.Operation.Kind != "role.bind" || bind.TargetRef != "node:work" || bind.OwnerRole != "worker" || bind.NodeID != "work" {
		t.Fatalf("missing Role lease did not expose its exact bind prerequisite: %#v", bind)
	}
	for key, expected := range map[string]any{"roleId": "worker", "nodeId": "work", "leaseState": "missing", "nextOperation": "action.list", "nextActionKind": "node.start"} {
		if bind.Operation.Params[key] != expected {
			t.Fatalf("bind remediation parameter %s=%#v, want %#v: %#v", key, bind.Operation.Params[key], expected, bind)
		}
	}
	if _, err := svc.listActions(mustState(t, svc), "worker", "work"); err == nil || !strings.Contains(err.Error(), "no active lease") {
		t.Fatalf("missing Role unexpectedly exposed a directly applicable node.start action: %v", err)
	}

	if _, err := svc.BindRole("worker", "codex", "worker-session", 30*time.Minute, false, "bind/worker"); err != nil {
		t.Fatal(err)
	}
	audit, err = svc.PreWait()
	if err != nil {
		t.Fatal(err)
	}
	var assign Remediation
	for _, remediation := range audit.Remediations {
		switch remediation.Code {
		case "bind_ready_node_role":
			t.Fatalf("active Role lease retained a bind prerequisite: %#v", remediation)
		case "assign_ready_node":
			assign = remediation
		}
	}
	if assign.Operation.Kind != "action.list" || assign.Operation.Params["roleId"] != "worker" || assign.Operation.Params["nodeId"] != "work" || assign.Operation.Params["actionKind"] != "node.start" {
		t.Fatalf("active Role lease did not expose an executable node.start lookup: %#v", assign)
	}
	started, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "node.start"), json.RawMessage(`{}`), "start/work")
	if err != nil || started.Kind != "node.start" || started.AttemptID == "" {
		t.Fatalf("advertised node.start lookup was not executable: result=%#v err=%v", started, err)
	}
}

func TestReadyNodeControlTransferRemediationIsRoleScopedAndUnique(t *testing.T) {
	svc, _ := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"role-scoped-ready-transfer"},"spec":{"roles":[{"id":"controller","capabilities":["role.control"]},{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"one","kind":"task","role":"worker","title":"one","outcomes":[{"id":"done","class":"success"}]},{"id":"two","kind":"task","role":"worker","title":"two","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	for _, binding := range []struct{ role, session, key string }{{"controller", "controller-session", "bind/controller"}, {"worker", "worker-session", "bind/worker"}} {
		if _, err := svc.BindRole(binding.role, "codex", binding.session, time.Hour, false, binding.key); err != nil {
			t.Fatal(err)
		}
	}
	audit, err := svc.PreWait()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	assignCount, transferCount := 0, 0
	for _, remediation := range audit.Remediations {
		if ids[remediation.ID] {
			t.Fatalf("duplicate remediation ID %s: %#v", remediation.ID, audit.Remediations)
		}
		ids[remediation.ID] = true
		switch remediation.Code {
		case "assign_ready_node":
			assignCount++
		case "control_transfer_active_role":
			transferCount++
			if remediation.TargetRef != "role:worker" || remediation.OwnerRole != "controller" || remediation.NodeID != "" || remediation.Operation.Kind != "action.list" || remediation.Operation.Params["targetRoleId"] != "worker" {
				t.Fatalf("control transfer remediation is not Role-scoped: %#v", remediation)
			}
		}
	}
	if assignCount != 2 || transferCount != 1 {
		t.Fatalf("ready Role emitted duplicate/missing remediations: assign=%d transfer=%d all=%#v", assignCount, transferCount, audit.Remediations)
	}
}

func TestOrchestratorOperationsPlanIsDeterministicallyBounded(t *testing.T) {
	nodes := make([]map[string]any, 0, 20)
	for index := 0; index < 20; index++ {
		nodes = append(nodes, map[string]any{"id": fmt.Sprintf("node-%02d", index), "kind": "task", "role": "controller", "title": "bounded node", "outcomes": []map[string]any{{"id": "done", "class": "success"}}})
	}
	graph, _ := json.Marshal(map[string]any{"apiVersion": "dagrail.io/v1alpha1", "kind": "Graph", "metadata": map[string]any{"name": "bounded-operations-plan"}, "spec": map[string]any{"roles": []map[string]any{{"id": "controller", "capabilities": []string{"node.run"}}}, "nodes": nodes, "edges": []any{}}})
	svc, _ := governanceService(t, string(graph))
	if _, err := svc.BindRole("controller", "codex", "controller-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	raw, err := svc.Context("orchestrator", "controller", "", 12288)
	if err != nil {
		t.Fatal(err)
	}
	var envelope ContextEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	actions, ok := envelope.Data["projectAllowedActions"].([]any)
	if !ok || len(actions) != 6 || envelope.Data["projectAllowedActionsTruncated"] != true {
		t.Fatalf("project action summary was not deterministically bounded: %s", raw)
	}
	if len(raw) > 12288 {
		t.Fatalf("bounded operations plan exceeded context budget: %d", len(raw))
	}
}

func TestPreWaitBoundsTenThousandReadyNodesAndPaginatesDetail(t *testing.T) {
	nodes := make([]map[string]any, 0, 10000)
	for index := 0; index < 10000; index++ {
		nodes = append(nodes, map[string]any{"id": fmt.Sprintf("n-%05d", index), "kind": "task", "role": "worker", "title": "work", "outcomes": []map[string]any{{"id": "done", "class": "success"}}})
	}
	graph, _ := json.Marshal(map[string]any{"apiVersion": "dagrail.io/v1alpha1", "kind": "Graph", "metadata": map[string]any{"name": "large-pre-wait"}, "spec": map[string]any{"roles": []map[string]any{{"id": "worker", "capabilities": []string{"node.run"}}}, "nodes": nodes, "edges": []any{}}})
	svc, _ := governanceService(t, string(graph))
	started := time.Now()
	audit, err := svc.PreWaitContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if audit.Counts.ReadyNodes != 10000 || len(audit.ReadyNodes) > preWaitPreviewLimit || audit.RemediationCount != 10000 || len(audit.Remediations) > preWaitRemediationLimit || !audit.Truncated || !strings.HasPrefix(audit.InspectRef, "pre-wait-page:") {
		t.Fatalf("large pre-wait result is not count-bound and paginated: counts=%#v ready=%d remediations=%d/%d truncated=%v ref=%q", audit.Counts, len(audit.ReadyNodes), len(audit.Remediations), audit.RemediationCount, audit.Truncated, audit.InspectRef)
	}
	raw, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > preWaitMaxBytes {
		t.Fatalf("pre-wait response exceeded %d bytes: %d", preWaitMaxBytes, len(raw))
	}
	value, err := svc.Inspect(audit.InspectRef)
	if err != nil {
		t.Fatal(err)
	}
	page := value.(PreWaitPage)
	if page.Total != 10000 || len(page.Items) != preWaitPageLimit || !strings.HasPrefix(page.NextRef, "pre-wait-page:") || page.Items[0].Category != "readyNode" || page.JournalHead == "" || !strings.HasPrefix(page.InventoryDigest, "sha256:") {
		t.Fatalf("large pre-wait detail is not paginated: %#v", page)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second*time.Duration(raceTestMultiplier) {
		t.Fatalf("large pre-wait audit and first page took %s", elapsed)
	}
}

func TestOperationsIndexAndActionPagesAreBoundedAndSnapshotBound(t *testing.T) {
	roleID := strings.Repeat("r", 200)
	nodes := make([]map[string]any, 0, 80)
	for index := 0; index < 80; index++ {
		nodes = append(nodes, map[string]any{"id": fmt.Sprintf("node-%03d-%s", index, strings.Repeat("n", 180)), "kind": "task", "role": roleID, "title": "bounded action", "outcomes": []map[string]any{{"id": "done", "class": "success"}}})
	}
	graph, _ := json.Marshal(map[string]any{"apiVersion": "dagrail.io/v1alpha1", "kind": "Graph", "metadata": map[string]any{"name": "bounded-operations-pages"}, "spec": map[string]any{"roles": []map[string]any{{"id": roleID, "capabilities": []string{"node.run", "graph.change"}}}, "nodes": nodes, "edges": []any{}}})
	svc, root := governanceService(t, string(graph))
	if _, err := svc.BindRole(roleID, "codex", "controller-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	state, _, err := svc.load()
	if err != nil {
		t.Fatal(err)
	}
	value, err := svc.InspectContext(context.Background(), operationsRefForRole(state, roleID))
	if err != nil {
		t.Fatal(err)
	}
	index := value.(OperationsIndex)
	if raw, _ := json.Marshal(index); len(raw) > operationsMaxBytes {
		t.Fatalf("operations index exceeded %d bytes: %d", operationsMaxBytes, len(raw))
	}
	if index.ActionCount != 80 || len(index.ProjectAllowedActions) > operationsPreviewLimit || !index.ActionsTruncated || index.ActionsInspectRef == "" || !index.Truncated {
		t.Fatalf("operations index is not count-bound and recoverable: %#v", index)
	}
	oldPageRef := index.ActionsInspectRef
	oldRemediationRef := index.RemediationsInspectRef
	seen := 0
	for ref := index.ActionsInspectRef; ref != ""; {
		pageValue, pageErr := svc.InspectContext(context.Background(), ref)
		if pageErr != nil {
			t.Fatal(pageErr)
		}
		page := pageValue.(OperationsActionPage)
		if raw, _ := json.Marshal(page); len(raw) > operationsMaxBytes {
			t.Fatalf("operations action page exceeded %d bytes: %d", operationsMaxBytes, len(raw))
		}
		if len(page.Actions) == 0 || len(page.Actions) > operationsPageLimit || page.Total != 80 {
			t.Fatalf("invalid operations action page: %#v", page)
		}
		seen += len(page.Actions)
		ref = page.NextRef
	}
	if seen != 80 {
		t.Fatalf("operations action pagination recovered %d actions, want 80", seen)
	}
	remediationsSeen := 0
	for ref := index.RemediationsInspectRef; ref != ""; {
		pageValue, pageErr := svc.InspectContext(context.Background(), ref)
		if pageErr != nil {
			t.Fatal(pageErr)
		}
		page := pageValue.(RemediationPage)
		if raw, _ := json.Marshal(page); len(raw) > operationsMaxBytes {
			t.Fatalf("remediation page exceeded %d bytes: %d", operationsMaxBytes, len(raw))
		}
		if len(page.Remediations) == 0 || page.Total != 80 {
			t.Fatalf("invalid remediation page: %#v", page)
		}
		remediationsSeen += len(page.Remediations)
		ref = page.NextRef
	}
	if remediationsSeen != 80 {
		t.Fatalf("remediation pagination recovered %d remediations, want 80", remediationsSeen)
	}
	firstNode := nodes[0]
	firstNode["title"] = "changed after operations snapshot"
	patchRaw, _ := json.Marshal(map[string]any{"apiVersion": "dagrail.io/v1alpha1", "kind": "GraphPatch", "operations": []map[string]any{{"op": "updateNode", "node": firstNode}}})
	patchPath := root + "/operations-stale-patch.json"
	if err := os.WriteFile(patchPath, patchRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.PreviewGraphChange(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyGraphChange(patchPath, preview.Token, "operations-head-advance", roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.InspectContext(context.Background(), oldPageRef); err == nil || !strings.Contains(err.Error(), "stale operations") {
		t.Fatalf("operations page crossed a journal-head change: %v", err)
	}
	if _, err := svc.InspectContext(context.Background(), oldRemediationRef); err == nil || !strings.Contains(err.Error(), "stale remediation") {
		t.Fatalf("remediation page crossed a journal-head change: %v", err)
	}
}

func TestOperationsAndPreWaitRecoverArbitrarilyLongIdentifiersInBoundedChunks(t *testing.T) {
	longNodeID := "node-" + strings.Repeat("x", 30000)
	graph, _ := json.Marshal(map[string]any{"apiVersion": "dagrail.io/v1alpha1", "kind": "Graph", "metadata": map[string]any{"name": "long-identifiers"}, "spec": map[string]any{"roles": []map[string]any{{"id": "worker", "capabilities": []string{"node.run"}}}, "nodes": []map[string]any{{"id": longNodeID, "kind": "task", "role": "worker", "title": "long identifier", "outcomes": []map[string]any{{"id": "done", "class": "success"}}}}, "edges": []any{}}})
	svc, _ := governanceService(t, string(graph))
	if _, err := svc.BindRole("worker", "codex", "session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	statusValue, err := svc.BoundedStatusContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	statusSummary, ok := statusValue.(map[string]any)
	if !ok || statusSummary["truncated"] != true || statusSummary["detailRef"] == "" {
		t.Fatalf("large status was not bounded and recoverable: %#v", statusValue)
	}
	var recoveredStatus OperationalStatus
	if err := json.Unmarshal(readBoundedDetail(t, svc, statusSummary["detailRef"].(string)), &recoveredStatus); err != nil || len(recoveredStatus.Frontier.Ready) != 1 || recoveredStatus.Frontier.Ready[0] != longNodeID {
		t.Fatalf("status detail did not recover the exact frontier: %#v err=%v", recoveredStatus, err)
	}
	audit, err := svc.PreWaitContext(context.Background())
	if err != nil || audit.InspectRef == "" {
		t.Fatalf("long pre-wait identifier did not produce a bounded page: %#v %v", audit, err)
	}
	pageValue, err := svc.InspectContext(context.Background(), audit.InspectRef)
	if err != nil {
		t.Fatal(err)
	}
	page := pageValue.(PreWaitPage)
	if raw, _ := json.Marshal(page); len(raw) > preWaitMaxBytes {
		t.Fatalf("long-ID pre-wait page exceeded %d bytes: %d", preWaitMaxBytes, len(raw))
	}
	if len(page.Items) != 1 || !page.Items[0].IDTruncated || page.Items[0].InspectRef == "" || page.Items[0].EntityRef == "" || page.Items[0].ID != "" {
		t.Fatalf("long pre-wait ID was not replaced by a recoverable ref: %#v", page)
	}
	recoveredID := readBoundedDetail(t, svc, page.Items[0].InspectRef)
	if string(recoveredID) != longNodeID {
		t.Fatal("pre-wait item chunks did not recover the exact long Node ID")
	}
	if resolved, err := svc.ResolveEntityRef("node", page.Items[0].EntityRef); err != nil || resolved != longNodeID {
		t.Fatalf("pre-wait Node selector did not resolve the exact large identity: len=%d err=%v", len(resolved), err)
	}
	frontierValue, err := svc.InspectContext(context.Background(), "frontier")
	if err != nil {
		t.Fatal(err)
	}
	frontierSummary, ok := frontierValue.(map[string]any)
	if !ok || frontierSummary["truncated"] != true || frontierSummary["detailRef"] == "" {
		t.Fatalf("large frontier was not bounded and recoverable: %#v", frontierValue)
	}
	var recoveredFrontier domain.Frontier
	if err := json.Unmarshal(readBoundedDetail(t, svc, frontierSummary["detailRef"].(string)), &recoveredFrontier); err != nil || len(recoveredFrontier.Ready) != 1 || recoveredFrontier.Ready[0] != longNodeID {
		t.Fatalf("frontier detail did not recover the exact Node: %#v err=%v", recoveredFrontier, err)
	}
	if len(audit.Remediations) != 1 || !audit.Remediations[0].DetailsTruncated || audit.Remediations[0].DetailRef == "" || audit.RemediationsInspectRef == "" {
		t.Fatalf("oversized remediation was not bounded and recoverable: %#v", audit)
	}
	var remediation Remediation
	if err := json.Unmarshal(readBoundedDetail(t, svc, audit.Remediations[0].DetailRef), &remediation); err != nil || remediation.NodeID != longNodeID || remediation.TargetRef != "node:"+longNodeID {
		t.Fatalf("remediation detail did not recover the exact operation: %#v err=%v", remediation, err)
	}
	state, _, err := svc.load()
	if err != nil {
		t.Fatal(err)
	}
	value, err := svc.InspectContext(context.Background(), operationsRefForRole(state, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	index := value.(OperationsIndex)
	if raw, _ := json.Marshal(index); len(raw) > operationsMaxBytes {
		t.Fatalf("long-ID operations index exceeded %d bytes: %d", operationsMaxBytes, len(raw))
	}
	if index.ActionCount != 1 || len(index.ProjectAllowedActions) != 1 {
		t.Fatalf("long-ID action disappeared from operations: %#v", index)
	}
	action := index.ProjectAllowedActions[0]
	if !action.DetailsTruncated || action.DetailRef == "" || action.NodeID != "" || action.NodeRef == "" || len(action.Ref) > maxInlineActionRefPayloadBytes*2 {
		t.Fatalf("long-ID action was not compact and recoverable: %#v", action)
	}
	directActions, err := svc.ListActions("worker", longNodeID)
	if err != nil || len(directActions.Actions) != 1 || !directActions.Actions[0].DetailsTruncated || directActions.Actions[0].DetailRef == "" {
		t.Fatalf("CLI action list bypassed the bounded action protocol: %#v err=%v", directActions, err)
	}
	nodeValue, err := svc.InspectContext(context.Background(), action.NodeRef)
	if err != nil {
		t.Fatal(err)
	}
	nodeSummary := nodeValue.(map[string]any)
	nodeDetailRef, _ := nodeSummary["detailRef"].(string)
	if nodeSummary["truncated"] != true || nodeDetailRef == "" {
		t.Fatalf("long Node inspection was not bounded and recoverable: %#v", nodeSummary)
	}
	var nodeDetail map[string]any
	if err := json.Unmarshal(readBoundedDetail(t, svc, nodeDetailRef), &nodeDetail); err != nil {
		t.Fatal(err)
	}
	nodeDefinition, _ := nodeDetail["node"].(map[string]any)
	if nodeDefinition["id"] != longNodeID {
		t.Fatal("node detail chunks did not recover the exact Node definition")
	}
	detailRaw := readBoundedDetail(t, svc, action.DetailRef)
	var detail allowedActionDetail
	if err := json.Unmarshal(detailRaw, &detail); err != nil || detail.NodeID != longNodeID || detail.InputSchema == nil {
		t.Fatalf("action detail chunks did not recover exact semantics: detail=%#v err=%v", detail, err)
	}
	result, err := svc.ApplyActionContext(context.Background(), action.Ref, json.RawMessage(`{}`), "start-long-node")
	if err != nil {
		t.Fatal(err)
	}
	if result.NodeID != "" || result.NodeRef == "" {
		t.Fatalf("long-ID action result was not bounded: %#v", result)
	}
	state, _, err = svc.load()
	if err != nil || state.Nodes[longNodeID].Status != "active" {
		t.Fatalf("compact action ref did not resolve the exact Node: status=%#v err=%v", state.Nodes[longNodeID], err)
	}
	node, _ := state.NodeDefinition(longNodeID)
	attempt := state.Attempts[result.AttemptID]
	pack, err := svc.buildExecutionPackage(state, node, attempt, packageInputJSON(t, []domain.ProtectedInput{{Name: "fixture", Digest: testDigest("3")}}, nil), svc.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	packRaw, _ := json.Marshal(pack)
	expectedHead := state.HeadHash
	if _, _, err := svc.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "test.evidence", ActorRole: "worker", IdempotencyKey: "long-node-evidence", ObjectRef: "attempt:" + attempt.ID}, []journal.Event{{Type: "evidence.package-published", Payload: packRaw}}, svc.Now().UTC(), &expectedHead); err != nil {
		t.Fatal(err)
	}
	evidenceValue, err := svc.BoundedEvidenceContext(context.Background(), longNodeID, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	evidenceSummary, ok := evidenceValue.(map[string]any)
	if !ok || evidenceSummary["truncated"] != true || evidenceSummary["detailRef"] == "" || evidenceSummary["packageCount"] != 1 {
		t.Fatalf("large evidence index was not bounded and recoverable: %#v", evidenceValue)
	}
	var recoveredEvidence EvidenceIndex
	if err := json.Unmarshal(readBoundedDetail(t, svc, evidenceSummary["detailRef"].(string)), &recoveredEvidence); err != nil || len(recoveredEvidence.Packages) != 1 || recoveredEvidence.Packages[0].NodeID != longNodeID {
		t.Fatalf("evidence detail did not recover the exact package summary: %#v err=%v", recoveredEvidence, err)
	}
}

func TestOperationsRecoverArbitrarilyLongRoleAndSessionIdentity(t *testing.T) {
	roleID := "role-" + strings.Repeat("r", 30_000)
	sessionID := "session-" + strings.Repeat("s", 30_000)
	graph, _ := json.Marshal(map[string]any{"apiVersion": "dagrail.io/v1alpha1", "kind": "Graph", "metadata": map[string]any{"name": "long-operations-identity"}, "spec": map[string]any{"roles": []map[string]any{{"id": roleID, "capabilities": []string{"node.run"}}}, "nodes": []map[string]any{{"id": "task", "kind": "task", "role": roleID, "title": "task", "outcomes": []map[string]any{{"id": "done", "class": "success"}}}}, "edges": []any{}}})
	svc, _ := governanceService(t, string(graph))
	if _, err := svc.BindRole(roleID, "codex", sessionID, time.Hour, false, "bind-long-identity"); err != nil {
		t.Fatal(err)
	}
	historyValue, err := svc.BoundedHistoryContext(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	historySummary, ok := historyValue.(map[string]any)
	if !ok || historySummary["truncated"] != true || historySummary["detailRef"] == "" {
		t.Fatalf("large history was not bounded and recoverable: %#v", historyValue)
	}
	var recoveredHistory HistoryPage
	if err := json.Unmarshal(readBoundedDetail(t, svc, historySummary["detailRef"].(string)), &recoveredHistory); err != nil || len(recoveredHistory.Entries) == 0 || recoveredHistory.Entries[len(recoveredHistory.Entries)-1].ActorRole != roleID {
		t.Fatalf("history detail did not recover the exact actor Role: entries=%d err=%v", len(recoveredHistory.Entries), err)
	}
	contextRaw, err := svc.Context("worker", roleID, "task", 512)
	if err != nil || len(contextRaw) > 512 || bytes.Contains(contextRaw, []byte(roleID)) || bytes.Contains(contextRaw, []byte(sessionID)) {
		t.Fatalf("bounded context leaked the oversized authorization: bytes=%d err=%v", len(contextRaw), err)
	}
	state, _, err := svc.load()
	if err != nil {
		t.Fatal(err)
	}
	value, err := svc.InspectContext(context.Background(), operationsRefForRole(state, roleID))
	if err != nil {
		t.Fatal(err)
	}
	index := value.(OperationsIndex)
	if raw, _ := json.Marshal(index); len(raw) > operationsMaxBytes {
		t.Fatalf("long-identity operations index exceeded %d bytes: %d", operationsMaxBytes, len(raw))
	}
	if index.RoleID != "" || index.RoleRef == "" || !index.IdentityTruncated || index.IdentityDetailRef == "" || index.Authorization.RoleID != "" || index.Authorization.SessionID != "" || index.Authorization.RoleRef == "" || index.Authorization.SessionRef == "" {
		t.Fatalf("long operations identity was not compact and recoverable: %#v", index)
	}
	detailRaw := readBoundedDetail(t, svc, index.IdentityDetailRef)
	if len(detailRaw) != index.IdentityDetailBytes {
		t.Fatalf("identity detail byte count changed: got=%d want=%d", len(detailRaw), index.IdentityDetailBytes)
	}
	var detail struct {
		Role  domain.RoleDefinition `json:"role"`
		Lease domain.RoleLease      `json:"lease"`
	}
	if err := json.Unmarshal(detailRaw, &detail); err != nil || detail.Role.ID != roleID || detail.Lease.SessionID != sessionID {
		t.Fatalf("identity chunks did not recover exact authorization: role=%d session=%d err=%v", len(detail.Role.ID), len(detail.Lease.SessionID), err)
	}
	roleValue, err := svc.InspectContext(context.Background(), index.RoleRef)
	if err != nil {
		t.Fatal(err)
	}
	roleSummary := roleValue.(map[string]any)
	if roleSummary["truncated"] != true || roleSummary["detailRef"] == "" {
		t.Fatalf("role ref leaked or lost the oversized identity: %#v", roleSummary)
	}
}

func TestOversizedOutcomeCanCompleteThroughBoundOutcomeRef(t *testing.T) {
	outcomeID := "completed-" + strings.Repeat("o", 70_000)
	graph, _ := json.Marshal(map[string]any{"apiVersion": "dagrail.io/v1alpha1", "kind": "Graph", "metadata": map[string]any{"name": "long-outcome"}, "spec": map[string]any{"roles": []map[string]any{{"id": "worker", "capabilities": []string{"node.run"}}}, "nodes": []map[string]any{{"id": "task", "kind": "task", "role": "worker", "title": "task", "outcomes": []map[string]any{{"id": outcomeID, "class": "success"}}}}, "edges": []any{}}})
	svc, initial := lifecycleWriterService(t, "long-outcome", string(graph))
	if _, err := svc.BindRole("worker", "codex", "session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "node.start"), json.RawMessage(`{}`), "start"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "task", "attempt.submit"), json.RawMessage(`{}`), "submit"); err != nil {
		t.Fatal(err)
	}
	completeRef := findActionRef(t, svc, "worker", "task", "task.complete")
	actions, err := svc.ListActions("worker", "task")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	for _, action := range actions.Actions {
		if action.Kind == "task.complete" {
			schema = action.InputSchema
		}
	}
	schemaRaw, _ := json.Marshal(schema)
	if bytes.Contains(schemaRaw, []byte(outcomeID)) || !bytes.Contains(schemaRaw, []byte(outcomeBindingRef("task", outcomeID))) {
		t.Fatalf("terminal schema did not replace the oversized outcome with its bound ref: bytes=%d", len(schemaRaw))
	}
	options, _ := schema["x-dagrailOutcomeOptions"].([]map[string]any)
	if len(options) != 1 {
		t.Fatalf("terminal schema did not expose one bound outcome option: %#v", schema)
	}
	outcomeDetailRef, _ := options[0]["idDetailRef"].(string)
	if outcomeDetailRef == "" || string(readBoundedDetail(t, svc, outcomeDetailRef)) != outcomeID {
		t.Fatal("outcome option detail did not recover the exact declared outcome")
	}
	input, _ := json.Marshal(map[string]string{"outcomeRef": outcomeBindingRef("task", outcomeID)})
	if len(input) > 1024 {
		t.Fatalf("bound outcome input is unexpectedly large: %d", len(input))
	}
	if _, err := svc.ApplyAction(completeRef, input, "complete"); err != nil {
		t.Fatal(err)
	}
	state, _, err := svc.load()
	if err != nil || state.Nodes["task"].Status != "terminal" || state.Nodes["task"].Outcome != outcomeID {
		t.Fatalf("bound outcome did not preserve the exact terminal authority: runtime=%#v err=%v", state.Nodes["task"], err)
	}
	assertLifecycleWriterPrefixes(t, svc, initial)
}

func TestActionResultBoundsAndRecoversImportedOversizedAttemptID(t *testing.T) {
	svc, _ := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"long-attempt-result"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"task","kind":"task","role":"worker","title":"task","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	state, _, err := svc.load()
	if err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt-" + strings.Repeat("a", 30_000)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	attempt := domain.Attempt{ID: attemptID, NodeID: "task", RoleID: "worker", Number: 1, Status: "running", StartedAt: now, UpdatedAt: now}
	attemptRaw, _ := json.Marshal(attempt)
	statusRaw, _ := json.Marshal(map[string]string{"attemptId": attemptID, "status": "running", "updatedAt": now})
	expectedHead := state.HeadHash
	_, _, err = svc.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "migration.fixture", ActorRole: "migration", IdempotencyKey: "long-attempt-fixture", ObjectRef: "attempt:" + attemptID}, []journal.Event{{Type: "attempt.leased", Payload: attemptRaw}, {Type: "attempt.status-changed", Payload: statusRaw}}, time.Now().UTC(), &expectedHead)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "session-one", time.Hour, false, "bind-one"); err != nil {
		t.Fatal(err)
	}
	checkpointRef := findActionRef(t, svc, "worker", "task", "attempt.checkpoint")
	input := json.RawMessage(`{"summary":"preserve imported attempt identity"}`)
	result, err := svc.ApplyAction(checkpointRef, input, "checkpoint-long-attempt")
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(result); len(raw) > operationsMaxBytes {
		t.Fatalf("action result exceeded %d bytes: %d", operationsMaxBytes, len(raw))
	}
	if result.AttemptID != "" || result.AttemptRef == "" || !result.AttemptIDTruncated || result.AttemptIDBytes != len(attemptID) {
		t.Fatalf("oversized Attempt ID was not bounded: %#v", result)
	}
	if recovered := readBoundedDetail(t, svc, result.AttemptRef); string(recovered) != attemptID {
		t.Fatalf("attempt identity chunks did not recover the imported identity: bytes=%d", len(recovered))
	}
	attemptValue, err := svc.InspectContext(context.Background(), "attempt:"+attemptID)
	if err != nil {
		t.Fatal(err)
	}
	attemptSummary := attemptValue.(map[string]any)
	if attemptSummary["truncated"] != true || attemptSummary["detailRef"] == "" {
		t.Fatalf("direct Attempt inspection was not bounded: %#v", attemptSummary)
	}
	if err := svc.ReleaseRole("worker", "session-one", "release-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "session-two", time.Hour, false, "bind-two"); err != nil {
		t.Fatal(err)
	}
	beforeRetry, _ := svc.State()
	retried, err := svc.ApplyAction(checkpointRef, input, "checkpoint-long-attempt")
	afterRetry, _ := svc.State()
	if err != nil || !reflect.DeepEqual(actionResultIdentity(retried), actionResultIdentity(result)) {
		t.Fatalf("idempotent result reconstruction changed the bounded Attempt identity: first=%#v retry=%#v err=%v", result, retried, err)
	}
	if afterRetry.HeadSequence != beforeRetry.HeadSequence {
		t.Fatalf("cross-session exact retry appended another event: before=%d after=%d", beforeRetry.HeadSequence, afterRetry.HeadSequence)
	}
}

func readBoundedDetail(t *testing.T, svc *Service, firstRef string) []byte {
	t.Helper()
	var recovered bytes.Buffer
	for ref := firstRef; ref != ""; {
		value, err := svc.InspectContext(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		chunk := value.(BoundedDetailChunk)
		raw, err := base64.RawStdEncoding.DecodeString(chunk.Chunk)
		if err != nil {
			t.Fatal(err)
		}
		if encoded, _ := json.Marshal(chunk); len(encoded) > preWaitMaxBytes {
			t.Fatalf("detail chunk exceeded %d bytes: %d", preWaitMaxBytes, len(encoded))
		}
		recovered.Write(raw)
		ref = chunk.NextRef
	}
	return recovered.Bytes()
}

func actionResultIdentity(value ActionResult) ActionResult {
	value.HeadSequence = 0
	value.HeadHash = ""
	value.GraphRevision = ""
	value.Continuation = Continuation{}
	return value
}

func TestProjectAllowedActionsPreCancelledLargeIncidentGraphReturnsImmediately(t *testing.T) {
	state := domain.NewState("cancelled-operations")
	graph := domain.GraphDefinition{
		APIVersion: domain.GraphAPIVersion,
		Kind:       domain.GraphKind,
		Metadata:   domain.GraphMetadata{Name: "cancelled operations"},
		Spec:       domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{domain.CapabilityNodeRun, domain.CapabilityIncidentManage}}}},
	}
	for index := 0; index < 8000; index++ {
		nodeID := fmt.Sprintf("node-%05d", index)
		repairID := fmt.Sprintf("repair-%05d", index)
		graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: nodeID, Kind: "task", Role: "worker", Title: "work", Outcomes: []domain.Outcome{{ID: "failed", Class: "failure"}}}, domain.NodeDefinition{ID: repairID, Kind: "task", Role: "worker", Title: "repair", Supersedes: nodeID, Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}})
		state.Nodes[nodeID] = domain.NodeRuntime{Status: "terminal", Outcome: "failed", OutcomeClass: "failure"}
		state.Nodes[repairID] = domain.NodeRuntime{Status: "planned"}
		incidentID := fmt.Sprintf("incident-%05d", index)
		state.Incidents[incidentID] = domain.Incident{ID: incidentID, SourceType: "attempt", NodeID: nodeID, OwnerRole: "worker", Status: "open"}
	}
	state.Graph = &graph
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if _, err := (&Service{Now: time.Now}).projectAllowedActionsContext(ctx, state, "worker", 0, time.Now()); err != context.Canceled {
		t.Fatalf("pre-cancelled operations returned %v", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("pre-cancelled operations scanned the incident×graph product for %s", elapsed)
	}
	started = time.Now()
	actions, err := (&Service{Now: time.Now}).projectAllowedActionsContext(context.Background(), state, "worker", 0, time.Now())
	if err != nil || len(actions) != 0 {
		t.Fatalf("large unauthorised operations scan returned actions=%d err=%v", len(actions), err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("operations rebuilt the successor graph per incident: %s", elapsed)
	}
}

func TestProjectAllowedActionsIndexesAuthorizedNodesEffectsAndResourcesOnce(t *testing.T) {
	svc, _ := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"indexed-operations"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","resource.close"]}],"nodes":[{"id":"seed","kind":"task","role":"worker","title":"seed","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	if _, err := svc.BindRole("worker", "codex", "session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	state, _, err := svc.load()
	if err != nil {
		t.Fatal(err)
	}
	const size = 6000
	graph := domain.GraphDefinition{
		APIVersion: domain.GraphAPIVersion,
		Kind:       domain.GraphKind,
		Metadata:   domain.GraphMetadata{Name: "indexed operations"},
		Spec: domain.GraphSpec{
			Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{domain.CapabilityNodeRun, domain.CapabilityResourceClose}}},
		},
	}
	state.Graph = &graph
	state.Nodes = map[string]domain.NodeRuntime{}
	state.Attempts = map[string]domain.Attempt{}
	state.NodeAttempts = map[string][]string{}
	state.Resources = map[string]domain.ResourceLease{}
	for index := 0; index < size; index++ {
		nodeID := fmt.Sprintf("node-%05d", index)
		attemptID := fmt.Sprintf("attempt-%05d", index)
		resourceID := fmt.Sprintf("resource-%05d", index)
		graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: nodeID, Kind: "task", Role: "worker", Title: "active work", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}})
		state.Nodes[nodeID] = domain.NodeRuntime{Status: "active"}
		state.Attempts[attemptID] = domain.Attempt{ID: attemptID, NodeID: nodeID, RoleID: "worker", Number: 1, Status: "running"}
		state.NodeAttempts[nodeID] = []string{attemptID}
		state.Resources[resourceID] = domain.ResourceLease{ID: resourceID, NodeID: nodeID, AttemptID: attemptID, RoleID: "worker", Status: "active", ClosureStatus: "pending"}
	}
	started := time.Now()
	actions, err := svc.projectAllowedActionsContext(context.Background(), state, "worker", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != size*6 {
		t.Fatalf("indexed operations returned %d actions, want %d", len(actions), size*6)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second*time.Duration(raceTestMultiplier) {
		t.Fatalf("authorized action planning appears to rescan Nodes/Effects/Resources: %s", elapsed)
	}
}

func TestPreWaitPageRejectsHeadAndTimeDependentInventoryDrift(t *testing.T) {
	nodes := make([]map[string]any, 0, 10)
	for index := 0; index < 10; index++ {
		nodes = append(nodes, map[string]any{"id": fmt.Sprintf("node-%02d", index), "kind": "task", "role": "worker", "title": "work", "outcomes": []map[string]any{{"id": "done", "class": "success"}}})
	}
	graph, _ := json.Marshal(map[string]any{"apiVersion": "dagrail.io/v1alpha1", "kind": "Graph", "metadata": map[string]any{"name": "pre-wait-snapshot"}, "spec": map[string]any{"roles": []map[string]any{{"id": "worker", "capabilities": []string{"node.run"}}}, "nodes": nodes, "edges": []any{}}})
	svc, _ := governanceService(t, string(graph))
	now := time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return now }
	if _, err := svc.BindRole("worker", "codex", "session", time.Minute, false, "bind"); err != nil {
		t.Fatal(err)
	}
	audit, err := svc.PreWaitContext(context.Background())
	if err != nil || audit.InspectRef == "" {
		t.Fatalf("pre-wait did not issue a snapshot-bound page: %#v %v", audit, err)
	}
	if _, err := svc.BindRole("worker", "codex", "session", 2*time.Minute, false, "renew"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.InspectContext(context.Background(), audit.InspectRef); err == nil || !strings.Contains(err.Error(), "journal head changed") {
		t.Fatalf("pre-wait page crossed a journal-head change: %v", err)
	}
	audit, err = svc.PreWaitContext(context.Background())
	if err != nil || audit.InspectRef == "" {
		t.Fatalf("renewed pre-wait page unavailable: %#v %v", audit, err)
	}
	now = now.Add(3 * time.Minute)
	if _, err := svc.InspectContext(context.Background(), audit.InspectRef); err == nil || !strings.Contains(err.Error(), "liveness inventory changed") {
		t.Fatalf("pre-wait page crossed time-dependent inventory drift: %v", err)
	}
}

func TestPreWaitIgnoresExpiredRoleWithoutLiveResponsibility(t *testing.T) {
	now := time.Date(2026, 8, 22, 2, 0, 0, 0, time.UTC)
	graph := domain.GraphDefinition{
		APIVersion: domain.GraphAPIVersion,
		Kind:       domain.GraphKind,
		Metadata:   domain.GraphMetadata{Name: "passive terminal role"},
		Spec: domain.GraphSpec{
			Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{domain.CapabilityNodeRun}}},
			Nodes: []domain.NodeDefinition{{ID: "work", Kind: "task", Role: "worker", Title: "work", Outcomes: []domain.Outcome{{ID: "returned", Class: "failure"}}}},
		},
	}
	state := domain.State{
		ProjectID:     "project",
		GraphRevision: "revision",
		HeadSequence:  7,
		HeadHash:      strings.Repeat("a", 64),
		Graph:         &graph,
		Nodes:         map[string]domain.NodeRuntime{"work": {Status: "terminal", Outcome: "returned"}},
		Attempts:      map[string]domain.Attempt{"attempt": {ID: "attempt", NodeID: "work", RoleID: "worker", Status: "terminal", Outcome: "returned", UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339Nano)}},
		NodeAttempts:  map[string][]string{"work": {"attempt"}},
		Leases: map[string]domain.RoleLease{"worker": {
			RoleID: "worker", Harness: "codex", SessionID: "passive-session", Active: true,
			ExpiresAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
		}},
		Effects:   map[string]domain.EffectAction{},
		Resources: map[string]domain.ResourceLease{},
		Incidents: map[string]domain.Incident{},
	}
	svc := &Service{Now: func() time.Time { return now }}

	audit, err := svc.preWaitFromStateContext(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if !audit.SafeToWait || audit.Counts.ExpiredRoles != 0 || len(audit.Remediations) != 0 {
		t.Fatalf("passive terminal role still blocked pre-wait: %#v", audit)
	}
	status, err := operationalStatusFromStateContext(context.Background(), state, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(status.ExpiredRoleLeases, []string{"worker"}) {
		t.Fatalf("status lost diagnostic visibility of the expired lease: %#v", status.ExpiredRoleLeases)
	}
}

func TestPreWaitKeepsExpiredRoleWithLiveResponsibilityActionable(t *testing.T) {
	now := time.Date(2026, 8, 22, 2, 0, 0, 0, time.UTC)
	graph := domain.GraphDefinition{
		APIVersion: domain.GraphAPIVersion,
		Kind:       domain.GraphKind,
		Metadata:   domain.GraphMetadata{Name: "active role"},
		Spec: domain.GraphSpec{
			Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{domain.CapabilityNodeRun}}},
			Nodes: []domain.NodeDefinition{{ID: "work", Kind: "task", Role: "worker", Title: "work", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}}},
		},
	}
	state := domain.State{
		ProjectID:     "project",
		GraphRevision: "revision",
		HeadSequence:  8,
		HeadHash:      strings.Repeat("b", 64),
		Graph:         &graph,
		Nodes:         map[string]domain.NodeRuntime{"work": {Status: "active"}},
		Attempts:      map[string]domain.Attempt{"attempt": {ID: "attempt", NodeID: "work", RoleID: "worker", Status: "running", UpdatedAt: now.Format(time.RFC3339Nano)}},
		NodeAttempts:  map[string][]string{"work": {"attempt"}},
		Leases: map[string]domain.RoleLease{"worker": {
			RoleID: "worker", Harness: "codex", SessionID: "interrupted-session", Active: true,
			ExpiresAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
		}},
		Effects:   map[string]domain.EffectAction{},
		Resources: map[string]domain.ResourceLease{},
		Incidents: map[string]domain.Incident{},
	}
	svc := &Service{Now: func() time.Time { return now }}

	audit, err := svc.preWaitFromStateContext(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if audit.SafeToWait || audit.Counts.ExpiredRoles != 1 || !reflect.DeepEqual(audit.ExpiredRoles, []string{"worker"}) {
		t.Fatalf("live responsibility lost its expired-role blocker: %#v", audit)
	}
	found := false
	for _, remediation := range audit.Remediations {
		if remediation.Code == "renew_or_takeover_role" && remediation.OwnerRole == "worker" {
			found = true
		}
	}
	if !found {
		t.Fatalf("live responsibility omitted renewal/takeover remediation: %#v", audit.Remediations)
	}
}

func TestPreWaitExpiredRoleResponsibilityIndexCoversEveryLiveOwnerSurface(t *testing.T) {
	now := time.Date(2026, 8, 22, 2, 0, 0, 0, time.UTC)
	baseState := func() domain.State {
		graph := domain.GraphDefinition{
			APIVersion: domain.GraphAPIVersion,
			Kind:       domain.GraphKind,
			Metadata:   domain.GraphMetadata{Name: "role responsibility index"},
			Spec: domain.GraphSpec{
				Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{domain.CapabilityNodeRun}}},
				Nodes: []domain.NodeDefinition{{ID: "work", Kind: "task", Role: "worker", Title: "work", Outcomes: []domain.Outcome{{ID: "returned", Class: "failure"}}}},
			},
		}
		return domain.State{
			ProjectID: "project", GraphRevision: "revision", HeadSequence: 9, HeadHash: strings.Repeat("c", 64), Graph: &graph,
			Nodes:        map[string]domain.NodeRuntime{"work": {Status: "terminal", Outcome: "returned"}},
			Attempts:     map[string]domain.Attempt{"attempt": {ID: "attempt", NodeID: "work", RoleID: "worker", Status: "terminal", Outcome: "returned", UpdatedAt: now.Format(time.RFC3339Nano)}},
			NodeAttempts: map[string][]string{"work": {"attempt"}},
			Leases:       map[string]domain.RoleLease{"worker": {RoleID: "worker", Active: true, ExpiresAt: now.Add(-time.Minute).Format(time.RFC3339Nano)}},
			Effects:      map[string]domain.EffectAction{}, Resources: map[string]domain.ResourceLease{}, Incidents: map[string]domain.Incident{},
		}
	}
	tests := map[string]func(*domain.State){
		"ready node": func(state *domain.State) {
			state.Nodes["work"] = domain.NodeRuntime{Status: "planned"}
			state.Attempts = map[string]domain.Attempt{}
			state.NodeAttempts = map[string][]string{}
		},
		"active attempt": func(state *domain.State) {
			state.Nodes["work"] = domain.NodeRuntime{Status: "active"}
			state.Attempts["attempt"] = domain.Attempt{ID: "attempt", NodeID: "work", RoleID: "worker", Status: "running", UpdatedAt: now.Format(time.RFC3339Nano)}
		},
		"pending effect": func(state *domain.State) {
			state.Effects["effect"] = domain.EffectAction{ID: "effect", NodeID: "work", OwnerRole: "worker", Status: "unknown"}
		},
		"active resource": func(state *domain.State) {
			state.Resources["resource"] = domain.ResourceLease{ID: "resource", NodeID: "work", AttemptID: "attempt", RoleID: "worker", Status: "active"}
		},
		"open incident": func(state *domain.State) {
			state.Incidents["attempt:attempt"] = domain.Incident{ID: "attempt:attempt", SourceType: "attempt", SourceID: "attempt", OwnerRole: "worker", Status: "open", Deadline: now.Add(time.Hour).Format(time.RFC3339Nano)}
		},
	}
	svc := &Service{Now: func() time.Time { return now }}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			state := baseState()
			mutate(&state)
			inventory, err := svc.collectPreWait(context.Background(), state)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(inventory.expiredRoles, []string{"worker"}) {
				t.Fatalf("expired role with %s was not actionable: %#v", name, inventory)
			}
		})
	}
}

type cancelOnErrContext struct {
	context.Context
	calls       int
	cancelAfter int
}

func (ctx *cancelOnErrContext) Err() error {
	ctx.calls++
	if ctx.calls >= ctx.cancelAfter {
		return context.Canceled
	}
	return nil
}

func TestContextAndInspectPropagateCancellationIntoDeepQueries(t *testing.T) {
	nodes := make([]map[string]any, 0, 100)
	for index := 0; index < 100; index++ {
		nodes = append(nodes, map[string]any{"id": fmt.Sprintf("node-%03d", index), "kind": "task", "role": "worker", "title": "work", "outcomes": []map[string]any{{"id": "done", "class": "success"}}})
	}
	graph, _ := json.Marshal(map[string]any{"apiVersion": "dagrail.io/v1alpha1", "kind": "Graph", "metadata": map[string]any{"name": "query-cancellation"}, "spec": map[string]any{"roles": []map[string]any{{"id": "worker", "capabilities": []string{"node.run"}}}, "nodes": nodes, "edges": []any{}}})
	svc, _ := governanceService(t, string(graph))
	audit, err := svc.PreWaitContext(context.Background())
	if err != nil || audit.InspectRef == "" {
		t.Fatalf("pre-wait page unavailable: %#v %v", audit, err)
	}
	inspectCtx := &cancelOnErrContext{Context: context.Background(), cancelAfter: 3}
	if _, err := svc.InspectContext(inspectCtx, audit.InspectRef); err != context.Canceled {
		t.Fatalf("inspect page ignored cancellation after load: %v", err)
	}
	contextCtx := &cancelOnErrContext{Context: context.Background(), cancelAfter: 3}
	if _, err := svc.ContextSinceContext(contextCtx, "worker", "", "", 8192, 0); err != context.Canceled {
		t.Fatalf("bounded context ignored cancellation during frontier computation: %v", err)
	}
}

func TestPreWaitCachesLargeSharedDependencyCutsAndHonorsCancellation(t *testing.T) {
	state := domain.NewState("bounded-cuts")
	graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "bounded cuts"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{domain.CapabilityNodeRun}}}}}
	for index := 0; index <= 5000; index++ {
		id := fmt.Sprintf("node-%04d", index)
		graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: id, Kind: "task", Role: "worker", Title: "work", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}})
		state.Nodes[id] = domain.NodeRuntime{Status: "planned"}
		if index > 0 {
			graph.Spec.Edges = append(graph.Spec.Edges, domain.EdgeDefinition{ID: fmt.Sprintf("edge-%04d", index), From: fmt.Sprintf("node-%04d", index-1), To: id, When: domain.Predicate{Outcome: "done"}})
		}
	}
	state.Graph = &graph
	for index := 0; index < 64; index++ {
		id := fmt.Sprintf("resource-%02d", index)
		state.Resources[id] = domain.ResourceLease{ID: id, NodeID: "node-0000", AttemptID: "missing", RoleID: "worker", Status: "active"}
	}
	svc := &Service{Now: func() time.Time { return time.Now().UTC() }}
	started := time.Now()
	audit, err := svc.preWaitFromStateContext(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if audit.Counts.OrphanedResources != 64 || audit.RemediationCount < 64 || len(audit.Remediations) > preWaitRemediationLimit || !audit.RemediationsTruncated {
		t.Fatalf("shared-cut remediation summary is not bounded: %#v", audit)
	}
	if raw, _ := json.Marshal(audit); len(raw) > preWaitMaxBytes {
		t.Fatalf("shared-cut audit exceeded %d bytes: %d", preWaitMaxBytes, len(raw))
	}
	for _, remediation := range audit.Remediations {
		if remediation.Code == "close_or_reconcile_orphaned_resource" && (remediation.DependencyCutCount != 5000 || len(remediation.DependencyCut) > dependencyCutPreview || remediation.DependencyCutRef == "" || remediation.DependencyCutDigest == "") {
			t.Fatalf("dependency cut was copied instead of summarized: %#v", remediation)
		}
		if remediation.Code == "close_or_reconcile_orphaned_resource" {
			digestAndOffset := strings.TrimPrefix(remediation.DependencyCutRef, "dependency-cut:")
			digest, _, _ := strings.Cut(digestAndOffset, ":")
			page, err := dependencyCutPage(state, digest, 0)
			if err != nil || page.Total != 5000 || len(page.Items) != preWaitPageLimit || page.NextRef == "" || page.Digest != remediation.DependencyCutDigest {
				t.Fatalf("dependency cut detail is not bound and paginated: page=%#v err=%v", page, err)
			}
			break
		}
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("shared dependency-cut audit took %s", elapsed)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.preWaitFromStateContext(cancelled, state); err != context.Canceled {
		t.Fatalf("pre-wait ignored cancellation: %v", err)
	}
}

func TestPreWaitCachesOneSharedClosureAcrossDistinctResourceRoots(t *testing.T) {
	state := domain.NewState("shared-resource-roots")
	graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "shared resource roots"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{domain.CapabilityNodeRun}}}}}
	const rootCount, sharedCount = 48, 48
	for index := 0; index < sharedCount; index++ {
		id := fmt.Sprintf("shared-%02d", index)
		graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: id, Kind: "task", Role: "worker", Title: "shared", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}})
		state.Nodes[id] = domain.NodeRuntime{Status: "planned"}
		if index > 0 {
			graph.Spec.Edges = append(graph.Spec.Edges, domain.EdgeDefinition{ID: fmt.Sprintf("shared-edge-%02d", index), From: fmt.Sprintf("shared-%02d", index-1), To: id, When: domain.Predicate{Outcome: "done"}})
		}
	}
	orphaned := make([]string, 0, rootCount)
	for index := 0; index < rootCount; index++ {
		rootID := fmt.Sprintf("root-%02d", index)
		resourceID := fmt.Sprintf("resource-%02d", index)
		graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: rootID, Kind: "task", Role: "worker", Title: "root", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}})
		graph.Spec.Edges = append(graph.Spec.Edges, domain.EdgeDefinition{ID: fmt.Sprintf("root-edge-%02d", index), From: rootID, To: "shared-00", When: domain.Predicate{Outcome: "done"}})
		state.Nodes[rootID] = domain.NodeRuntime{Status: "planned"}
		state.Resources[resourceID] = domain.ResourceLease{ID: resourceID, NodeID: rootID, AttemptID: "missing", RoleID: "worker", Status: "active"}
		orphaned = append(orphaned, resourceID)
	}
	state.Graph = &graph
	ctx := &cancelOnErrContext{Context: context.Background(), cancelAfter: 600}
	remediations, err := (&Service{}).buildRemediationsContext(ctx, state, preWaitInventory{orphanedResources: orphaned})
	if err != nil {
		t.Fatalf("distinct roots repeated their shared dependency closure: calls=%d err=%v", ctx.calls, err)
	}
	if len(remediations) != rootCount {
		t.Fatalf("resource remediation count changed: got %d want %d", len(remediations), rootCount)
	}
	for _, remediation := range remediations {
		if remediation.DependencyCutCount != sharedCount {
			t.Fatalf("shared dependency cut changed: %#v", remediation)
		}
	}
}

func TestDependencyCutPagesChunkOversizedNodeIdentifiers(t *testing.T) {
	longNodeID := "node-" + strings.Repeat("z", 30000)
	state := domain.NewState("long-dependency-cut")
	state.Incidents["incident"] = domain.Incident{ID: "incident", DependencyCut: []string{longNodeID}}
	digest := dependencyCutDigest([]string{longNodeID})
	page, err := dependencyCutPageContext(context.Background(), state, digest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(page); len(raw) > preWaitMaxBytes {
		t.Fatalf("dependency-cut page exceeded %d bytes: %d", preWaitMaxBytes, len(raw))
	}
	if len(page.Items) != 1 || page.Items[0] != "" || len(page.OversizedItems) != 1 || page.OversizedItems[0].InspectRef == "" {
		t.Fatalf("oversized dependency-cut ID was not recoverable: %#v", page)
	}
	chunk, err := dependencyCutItemPage(context.Background(), state, digest, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(chunk.Chunk)
	if err != nil || !bytes.Equal(decoded, []byte(longNodeID)[:len(decoded)]) || chunk.NextRef == "" {
		t.Fatalf("dependency-cut detail chunk is invalid: %#v err=%v", chunk, err)
	}
}

func TestOversizedContextRetainsAuthorizationAndRecoverableOperations(t *testing.T) {
	svc, _ := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"bounded-large-context"},"spec":{"roles":[{"id":"controller","capabilities":["node.run","incident.manage"]}],"nodes":[{"id":"work","kind":"task","role":"controller","title":"work","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	if _, err := svc.BindRole("controller", "codex", "controller-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	started, err := svc.ApplyAction(findActionRef(t, svc, "controller", "work", "node.start"), json.RawMessage(`{}`), "start")
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := svc.load()
	if err != nil {
		t.Fatal(err)
	}
	now := svc.Now().UTC()
	events := make([]journal.Event, 0, 100)
	for index := 0; index < 100; index++ {
		incident := domain.Incident{ID: fmt.Sprintf("bulk-%03d", index), SourceType: "attempt", SourceID: started.AttemptID, NodeID: "work", OwnerRole: "controller", Status: "open", Classification: "unknown", Deadline: now.Add(time.Hour).Format(time.RFC3339Nano), AttemptBudget: 2, LastProgress: strings.Repeat("bounded-detail-", 256), DependencyCut: []string{"work"}, OpenedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
		raw, _ := json.Marshal(incident)
		events = append(events, journal.Event{Type: "incident.opened", Payload: raw})
	}
	expectedHead := state.HeadHash
	if _, _, err := svc.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "test.bulk-incidents", ActorRole: "controller", IdempotencyKey: "bulk-incidents", ObjectRef: "project:" + state.ProjectID}, events, now, &expectedHead); err != nil {
		t.Fatal(err)
	}
	raw, err := svc.Context("orchestrator", "controller", "work", 512)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 512 {
		t.Fatalf("minimal context exceeded its budget: %d", len(raw))
	}
	var envelope ContextEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	authorization, ok := envelope.Data["authorization"].(map[string]any)
	if !ok || !strings.HasPrefix(fmt.Sprint(authorization["roleRef"]), "role-key:") || authorization["leaseState"] != "active" {
		t.Fatalf("minimal context lost authorization: %s", raw)
	}
	operationsRef, ok := envelope.Data["operationsRef"].(string)
	if !ok || !strings.HasPrefix(operationsRef, "operations:") || len(operationsRef) > 96 {
		t.Fatalf("minimal context lost the recoverable operations ref: %s", raw)
	}
	if len(envelope.InspectRefs) > 8 || strings.Contains(string(raw), "bounded-detail-") {
		t.Fatalf("minimal context leaked unbounded incident detail or refs: %s", raw)
	}
	operationsValue, err := svc.Inspect(operationsRef)
	if err != nil {
		t.Fatal(err)
	}
	operations := operationsValue.(OperationsIndex)
	if operations.Authorization.RoleID != "controller" || len(operations.Remediations) == 0 {
		t.Fatalf("operations ref did not recover bounded actions/remediations: %#v", operations)
	}
	incidentsValue, err := svc.Inspect("incident-index:0")
	if err != nil {
		t.Fatal(err)
	}
	incidents := incidentsValue.(IncidentIndex)
	if incidents.Total != 100 || len(incidents.Items) != 50 || !strings.HasPrefix(incidents.NextRef, "incident-index:"+incidents.JournalHead+":") || incidents.InventoryDigest == "" {
		t.Fatalf("incident index is not bounded and paginated: %#v", incidents)
	}
}

func TestIncidentAndRemediationSurfacesBoundSchemaLegalLargeIdentity(t *testing.T) {
	svc, _ := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"large-incident"},"spec":{"roles":[{"id":"controller","capabilities":["node.run","incident.manage"]}],"nodes":[{"id":"work","kind":"task","role":"controller","title":"work","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	if _, err := svc.BindRole("controller", "codex", "session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	started, err := svc.ApplyAction(findActionRef(t, svc, "controller", "work", "node.start"), json.RawMessage(`{}`), "start")
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := svc.load()
	if err != nil {
		t.Fatal(err)
	}
	incidentID := "incident-" + strings.Repeat("i", 30_000)
	now := svc.Now().UTC()
	incident := domain.Incident{ID: incidentID, SourceType: "attempt", SourceID: started.AttemptID, NodeID: "work", OwnerRole: "controller", Status: "open", Classification: "unknown", Deadline: now.Add(time.Hour).Format(time.RFC3339Nano), AttemptBudget: 2, LastProgress: strings.Repeat("p", 30_000), OpenedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	raw, _ := json.Marshal(incident)
	expectedHead := state.HeadHash
	if _, _, err := svc.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "test.large-incident", ActorRole: "controller", IdempotencyKey: "large-incident", ObjectRef: "project:" + state.ProjectID}, []journal.Event{{Type: "incident.opened", Payload: raw}}, now, &expectedHead); err != nil {
		t.Fatal(err)
	}
	state, _, err = svc.load()
	if err != nil {
		t.Fatal(err)
	}
	index := incidentIndex(state, 0, 50)
	indexRaw, _ := json.Marshal(index)
	if len(indexRaw) > operationsMaxBytes || len(index.Items) != 1 || !index.Items[0].Truncated || index.Items[0].IncidentRef == "" || index.Items[0].DetailRef == "" || strings.Contains(string(indexRaw), incidentID) {
		t.Fatalf("large Incident escaped the bounded index: bytes=%d index=%#v", len(indexRaw), index)
	}
	var recovered domain.Incident
	if err := json.Unmarshal(readBoundedDetail(t, svc, index.Items[0].DetailRef), &recovered); err != nil || recovered.ID != incidentID || recovered.LastProgress != incident.LastProgress {
		t.Fatalf("Incident detail was not exactly recoverable: id=%d err=%v", len(recovered.ID), err)
	}
	audit, err := svc.PreWaitContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found Remediation
	for _, remediation := range audit.Remediations {
		if remediation.Code == "advance_incident" {
			found = remediation
			break
		}
	}
	if !found.DetailsTruncated || found.DetailRef == "" || audit.RemediationsInspectRef == "" {
		t.Fatalf("large Incident remediation was not bounded: %#v", audit)
	}
	var recoveredRemediation Remediation
	if err := json.Unmarshal(readBoundedDetail(t, svc, found.DetailRef), &recoveredRemediation); err != nil || recoveredRemediation.TargetRef != "incident:"+incidentID {
		t.Fatalf("remediation detail was not exactly recoverable: %#v err=%v", recoveredRemediation, err)
	}
	oldIndexRef := incidentIndexRef(state, strings.TrimPrefix(index.InventoryDigest, "sha256:"), 0)
	if _, err := svc.ProgressIncident(incidentID, "controller", "made progress", true, "progress"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.InspectContext(context.Background(), oldIndexRef); err == nil || !strings.Contains(err.Error(), "stale incident") {
		t.Fatalf("Incident index crossed a journal-head change: %v", err)
	}
}

func TestStoredAuthorityClassesUseOneBoundedRecoverableInspectionProtocol(t *testing.T) {
	state := domain.NewState("bounded-authority-classes")
	state.HeadHash = strings.Repeat("a", 64)
	state.Graph = &domain.GraphDefinition{}
	long := strings.Repeat("x", 30_000)
	evidenceDigest := "sha256:" + strings.Repeat("e", 64)
	state.Checkpoints["checkpoint"] = domain.Checkpoint{ID: "checkpoint", AttemptID: long, Summary: "summary", EvidenceRefs: []domain.EvidenceRef{{Digest: evidenceDigest, Type: "report", Size: 1}}}
	state.Decisions["decision"] = domain.DecisionRecord{ID: "decision", NodeID: long, AttemptID: long, RoleID: long, Outcome: long}
	state.Actions["action"] = domain.ActionRecord{ID: "action", NodeID: long, AttemptID: long, Input: json.RawMessage(`{"value":"` + long + `"}`)}
	state.Effects["effect"] = domain.EffectAction{ID: "effect", NodeID: long, AttemptID: long, OwnerRole: long, Request: json.RawMessage(`{"value":"` + long + `"}`)}
	state.Resources["resource"] = domain.ResourceLease{ID: "resource", NodeID: long, AttemptID: long, RoleID: long}
	state.EvidencePackages["evidence-package"] = domain.ExecutionPackage{ID: "evidence-package", NodeID: long, AttemptID: long}
	state.ReuseDecisions["reuse-decision"] = domain.ReuseDecision{ID: "reuse-decision", AssessedByAttempt: long, Reasons: []string{long}}

	for _, kind := range []string{"checkpoint", "decision", "action", "effect", "resource", "evidence-package", "reuse-decision"} {
		t.Run(kind, func(t *testing.T) {
			summary, err := boundedStoredObjectInspection(state, kind, kind)
			if err != nil {
				t.Fatal(err)
			}
			summaryRaw, _ := json.Marshal(summary)
			if len(summaryRaw) > operationsMaxBytes {
				t.Fatalf("%s summary exceeded %d bytes: %d", kind, operationsMaxBytes, len(summaryRaw))
			}
			fields, ok := summary.(map[string]any)
			if !ok || fields["truncated"] != true || fields["objectRef"] == "" || fields["detailRef"] == "" {
				t.Fatalf("%s did not use the common bounded envelope: %#v", kind, summary)
			}
			expected, err := storedObjectBytes(state, kind, kind)
			if err != nil {
				t.Fatal(err)
			}
			key := opaqueEntityKey(state, kind, kind)
			recovered := []byte{}
			for offset := 0; offset < len(expected); {
				chunk, err := storedObjectDetailPage(state, kind, state.HeadHash, key, offset)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := base64.RawStdEncoding.DecodeString(chunk.Chunk)
				if err != nil {
					t.Fatal(err)
				}
				recovered = append(recovered, decoded...)
				offset += len(decoded)
			}
			if !bytes.Equal(recovered, expected) {
				t.Fatalf("%s detail chunks changed authority bytes", kind)
			}
		})
	}
	evidenceValue, err := boundedEvidenceInspection(state, evidenceDigest)
	if err != nil {
		t.Fatal(err)
	}
	evidenceSummary, ok := evidenceValue.(map[string]any)
	if !ok || evidenceSummary["truncated"] != true || evidenceSummary["evidenceRef"] == "" || evidenceSummary["detailRef"] == "" {
		t.Fatalf("evidence detail did not use the bounded protocol: %#v", evidenceValue)
	}
	evidenceKey := opaqueEntityKey(state, "evidence", evidenceDigest)
	evidenceDetailDigest := strings.TrimPrefix(evidenceSummary["detailDigest"].(string), "sha256:")
	var recoveredEvidence []byte
	for offset := 0; offset < evidenceSummary["detailBytes"].(int); {
		chunk, err := evidenceDetailPage(state, state.HeadHash, evidenceKey, evidenceDetailDigest, offset)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.RawStdEncoding.DecodeString(chunk.Chunk)
		if err != nil {
			t.Fatal(err)
		}
		recoveredEvidence = append(recoveredEvidence, decoded...)
		offset += len(decoded)
	}
	if !bytes.Contains(recoveredEvidence, []byte(long)) {
		t.Fatal("evidence detail chunks did not recover the exact Attempt identity")
	}
	longResourceID := "resource-" + long
	state.Resources[longResourceID] = domain.ResourceLease{ID: longResourceID, NodeID: "node", AttemptID: "attempt"}
	result := boundedActionResult(state, ActionResult{ObjectRef: "resource:" + longResourceID})
	if !result.ObjectRefTruncated || result.ObjectInspectRef == "" || result.ObjectRefDetailRef == "" || result.ObjectRef != "" {
		t.Fatalf("large action-result object reference was not bounded and recoverable: %#v", result)
	}
	recoveredObjectRef := []byte{}
	refDigest := strings.TrimPrefix(result.ObjectRefDigest, "sha256:")
	key := opaqueEntityKey(state, "resource", longResourceID)
	for offset := 0; offset < result.ObjectRefBytes; {
		chunk, err := objectReferenceDetailPage(state, "resource", key, refDigest, offset)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.RawStdEncoding.DecodeString(chunk.Chunk)
		if err != nil {
			t.Fatal(err)
		}
		recoveredObjectRef = append(recoveredObjectRef, decoded...)
		offset += len(decoded)
	}
	if string(recoveredObjectRef) != "resource:"+longResourceID {
		t.Fatal("large action-result object reference changed during detail recovery")
	}
	longRoleID := "role-" + long
	state.Graph.Spec.Roles = append(state.Graph.Spec.Roles, domain.RoleDefinition{ID: longRoleID, Capabilities: []string{domain.CapabilityNodeRun}})
	roleResult := boundedActionResult(state, ActionResult{ObjectRef: "role:" + longRoleID})
	if !roleResult.ObjectRefTruncated || roleResult.ObjectInspectRef == "" || roleResult.ObjectRefDetailRef == "" || roleResult.ObjectRef != "" {
		t.Fatalf("large Role action-result object reference was not bounded and recoverable: %#v", roleResult)
	}
	recoveredRoleRef := []byte{}
	roleRefDigest := strings.TrimPrefix(roleResult.ObjectRefDigest, "sha256:")
	roleKey := opaqueEntityKey(state, "role", longRoleID)
	for offset := 0; offset < roleResult.ObjectRefBytes; {
		chunk, err := objectReferenceDetailPage(state, "role", roleKey, roleRefDigest, offset)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.RawStdEncoding.DecodeString(chunk.Chunk)
		if err != nil {
			t.Fatal(err)
		}
		recoveredRoleRef = append(recoveredRoleRef, decoded...)
		offset += len(decoded)
	}
	if string(recoveredRoleRef) != "role:"+longRoleID {
		t.Fatal("large Role action-result object reference changed during detail recovery")
	}
	longEffectID := "effect-" + long
	state.Effects[longEffectID] = domain.EffectAction{ID: longEffectID, NodeID: "missing", Status: "unknown"}
	svc := &Service{}
	continuityValue, err := svc.boundedEffectContinuityInspection(state, longEffectID)
	if err != nil {
		t.Fatal(err)
	}
	continuitySummary, ok := continuityValue.(map[string]any)
	if !ok || continuitySummary["truncated"] != true || continuitySummary["detailRef"] == "" {
		t.Fatalf("large Effect continuity was not bounded: %#v", continuityValue)
	}
	effectKey := opaqueEntityKey(state, "effect", longEffectID)
	digest := strings.TrimPrefix(continuitySummary["detailDigest"].(string), "sha256:")
	recoveredContinuity := []byte{}
	for offset := 0; offset < int(continuitySummary["detailBytes"].(int)); {
		chunk, err := svc.effectContinuityDetailPage(state, state.HeadHash, effectKey, digest, offset)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.RawStdEncoding.DecodeString(chunk.Chunk)
		if err != nil {
			t.Fatal(err)
		}
		recoveredContinuity = append(recoveredContinuity, decoded...)
		offset += len(decoded)
	}
	var continuity EffectContinuity
	if err := json.Unmarshal(recoveredContinuity, &continuity); err != nil || continuity.ActionID != longEffectID {
		t.Fatalf("Effect continuity detail did not recover the exact action: id=%d err=%v", len(continuity.ActionID), err)
	}
}

func TestGraphImpactUsesBoundedCountsWithoutLosingAdmissionToken(t *testing.T) {
	long := strings.Repeat("x", 30_000)
	impact := GraphImpact{
		CurrentRevision:  strings.Repeat("a", 64),
		ProposedRevision: strings.Repeat("b", 64),
		AddedNodes:       []string{long},
		UpdatedRoles:     []string{long},
		AddedGroups:      []string{long},
		UpdatedGroups:    []string{long},
		RemovedGroups:    []string{long},
		MovedNodes:       []string{long},
		DependencyCut:    []string{long},
		Token:            "signed-impact-token",
		ExpiresAt:        time.Now().UTC().Format(time.RFC3339Nano),
	}
	bounded := BoundedGraphImpact(impact)
	raw, _ := json.Marshal(bounded)
	if len(raw) > operationsMaxBytes || !bounded.Truncated || bounded.Token != impact.Token || bounded.AddedNodeCount != 1 || bounded.UpdatedRoleCount != 1 || bounded.AddedGroupCount != 1 || bounded.UpdatedGroupCount != 1 || bounded.RemovedGroupCount != 1 || bounded.MovedNodeCount != 1 || bounded.DependencyCutCount != 1 || bounded.ImpactDigest == "" || len(bounded.AddedNodes) != 0 || len(bounded.AddedGroups) != 0 {
		t.Fatalf("Graph impact was not bounded without losing admission authority: bytes=%d value=%#v", len(raw), bounded)
	}
}

func TestBoundedEffectActionUsesOneCurrentSnapshot(t *testing.T) {
	requestValue := strings.Repeat("payload-", 5000)
	graph, _ := json.Marshal(map[string]any{
		"apiVersion": domain.GraphAPIVersion,
		"kind":       domain.GraphKind,
		"metadata":   map[string]any{"name": "effect-snapshot"},
		"spec": map[string]any{
			"roles": []map[string]any{{"id": "operator", "capabilities": []string{"effect.apply", "effect.reconcile"}}},
			"nodes": []map[string]any{{"id": "publish", "kind": "effect", "role": "operator", "title": "publish", "inputs": map[string]any{"adapter": "manual", "request": map[string]any{"payload": requestValue}}, "outcomes": []map[string]any{{"id": "published", "class": "success"}}}},
			"edges": []any{},
		},
	})
	svc, _ := governanceService(t, string(graph))
	if _, err := svc.BindRole("operator", "codex", "operator-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "operator", "publish", "node.start"), json.RawMessage(`{}`), "start"); err != nil {
		t.Fatal(err)
	}
	prepared, err := svc.ApplyAction(findActionRef(t, svc, "operator", "publish", "effect.prepare"), json.RawMessage(`{}`), "prepare")
	if err != nil {
		t.Fatal(err)
	}
	staleState, _, err := svc.load()
	if err != nil {
		t.Fatal(err)
	}
	stale := staleState.Effects[prepared.ActionID]
	current, err := svc.ReconcileEffect(prepared.ActionID, json.RawMessage(`{"externalId":"published","recipientVisible":true,"deliveryStatus":"visible"}`), "reconcile")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BoundedEffectAction(stale); err == nil || !strings.Contains(err.Error(), "changed before") {
		t.Fatalf("mixed Effect snapshots were encoded as one result: %v", err)
	}
	bounded, err := svc.BoundedEffectAction(current)
	if err != nil {
		t.Fatal(err)
	}
	if !bounded.Truncated || bounded.Effect != nil || bounded.Status != current.Status || bounded.Sequence != current.Sequence || bounded.DetailRef == "" || bounded.DetailDigest == "" {
		t.Fatalf("current Effect snapshot was not bounded coherently: %#v", bounded)
	}
	recovered := readBoundedDetail(t, svc, bounded.DetailRef)
	expected, _ := json.Marshal(current)
	if !bytes.Equal(recovered, expected) {
		t.Fatal("bounded Effect detail and summary were derived from different snapshots")
	}
}

func TestMinimumContextUsesFixedLengthOpaqueRefsForLongRoleIDs(t *testing.T) {
	roleID := strings.Repeat("r", 252)
	roles := []map[string]any{{"id": roleID, "capabilities": []string{"node.run"}}}
	outcomes := []map[string]any{{"id": "done", "class": "success"}}
	nodes := []map[string]any{{"id": "task", "kind": "task", "role": roleID, "title": "task", "outcomes": outcomes}}
	graph, _ := json.Marshal(map[string]any{
		"apiVersion": "dagrail.io/v1alpha1",
		"kind":       "Graph",
		"metadata":   map[string]any{"name": "long-role-context"},
		"spec": map[string]any{
			"roles": roles,
			"nodes": nodes,
			"edges": []any{},
		},
	})
	svc, _ := governanceService(t, string(graph))
	if _, err := svc.BindRole(roleID, "codex", "session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	raw, err := svc.Context("worker", roleID, "task", 512)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 512 || strings.Contains(string(raw), roleID) {
		t.Fatalf("long Role ID escaped the fixed-length minimum context: bytes=%d body=%s", len(raw), raw)
	}
	var envelope ContextEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	operationsRef, ok := envelope.Data["operationsRef"].(string)
	if !ok || len(operationsRef) != len("operations:")+64 {
		t.Fatalf("operations ref is not fixed length: %q", operationsRef)
	}
	value, err := svc.Inspect(operationsRef)
	if err != nil {
		t.Fatal(err)
	}
	operations := value.(OperationsIndex)
	if operations.RoleID != roleID || operations.Authorization.RoleID != roleID {
		t.Fatal("opaque operations ref did not recover the full Role authorization")
	}
}

func TestEffectContinuitySeparatesHeadAdvanceFromCausalContractChange(t *testing.T) {
	svc, root := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect-continuity"},"spec":{"roles":[{"id":"operator","capabilities":["effect.apply","effect.reconcile"]}],"nodes":[{"id":"publish","kind":"effect","role":"operator","title":"publish","inputs":{"adapter":"manual","request":{"target":"artifact-store","intent":"publish-v1"}},"outcomes":[{"id":"published","class":"success"}]}],"edges":[]}}`)
	if _, err := svc.BindRole("operator", "codex", "operator-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "operator", "publish", "node.start"), json.RawMessage(`{}`), "start"); err != nil {
		t.Fatal(err)
	}
	prepared, err := svc.ApplyAction(findActionRef(t, svc, "operator", "publish", "effect.prepare"), json.RawMessage(`{}`), "prepare")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "operator", "publish", "attempt.checkpoint"), json.RawMessage(`{"summary":"external publication is awaiting a visible receipt"}`), "checkpoint"); err != nil {
		t.Fatal(err)
	}
	value, err := svc.Inspect("effect-continuity:" + prepared.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	report := value.(EffectContinuity)
	if !report.HeadAdvanced || !report.CausalContractUnchanged || !report.ReconcileAllowed || !contains(report.Reasons, "global_head_advanced") {
		t.Fatalf("head advance was confused with an effect contract change: %#v", report)
	}
	svc.Now = func() time.Time { return time.Now().UTC().Add(2 * time.Hour) }
	value, err = svc.Inspect("effect-continuity:" + prepared.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	report = value.(EffectContinuity)
	if report.ReconcileAllowed || !contains(report.ReconcileBlockers, "role_lease_invalid") {
		t.Fatalf("continuity report overstated reconciliation authorization: %#v", report)
	}
	patchPath := root + "/effect-change.json"
	if err := os.WriteFile(patchPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"updateNode","node":{"id":"publish","kind":"effect","role":"operator","title":"publish","inputs":{"adapter":"manual","request":{"target":"artifact-store","intent":"publish-v2"}},"outcomes":[{"id":"published","class":"success"}]}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PreviewGraphChange(patchPath); err == nil {
		t.Fatal("active effect contract was mutable")
	}
}

func TestEffectContinuityReconcileAndMigrationBindAdapterMetadata(t *testing.T) {
	svc, _ := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"effect-adapter-binding"},"spec":{"roles":[{"id":"operator","capabilities":["effect.apply","effect.reconcile"]}],"nodes":[{"id":"publish","kind":"effect","role":"operator","title":"publish","inputs":{"adapter":"manual","request":{"target":"artifact-store"}},"outcomes":[{"id":"published","class":"success"}]}],"edges":[]}}`)
	initial, _, err := svc.load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("operator", "codex", "operator-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "operator", "publish", "node.start"), json.RawMessage(`{}`), "start"); err != nil {
		t.Fatal(err)
	}
	prepared, err := svc.ApplyAction(findActionRef(t, svc, "operator", "publish", "effect.prepare"), json.RawMessage(`{}`), "prepare")
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := svc.load()
	if err != nil {
		t.Fatal(err)
	}
	effect := state.Effects[prepared.ActionID]
	if effect.AdapterVersion != "0.1.0" || effect.AdapterSchemaHash != "sha256:manual-effect-v1" {
		t.Fatalf("prepared effect did not persist adapter metadata: %#v", effect)
	}

	changed := providers.New()
	if err := changed.RegisterEffect(changedManualAdapter{}); err != nil {
		t.Fatal(err)
	}
	svc.Providers = changed
	value, err := svc.Inspect("effect-continuity:" + prepared.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	report := value.(EffectContinuity)
	if report.AdapterUnchanged || report.CausalContractUnchanged || report.ReconcileAllowed || !contains(report.ReconcileBlockers, "adapter_metadata_changed") {
		t.Fatalf("changed adapter metadata was treated as the prepared causal contract: %#v", report)
	}
	audit, err := svc.PreWait()
	if err != nil {
		t.Fatal(err)
	}
	assertEffectRemediationBlocked(t, audit, prepared.ActionID, "restore_prior_runtime_or_quarantine")
	beforeReconcile := state.HeadSequence
	if _, err := svc.ReconcileEffect(prepared.ActionID, json.RawMessage(`{}`), "reconcile-changed-adapter"); err == nil || !strings.Contains(err.Error(), "metadata changed") {
		t.Fatalf("reconcile crossed an adapter metadata change: %v", err)
	}
	after, _, _ := svc.load()
	if after.HeadSequence != beforeReconcile {
		t.Fatal("rejected adapter metadata change mutated the journal")
	}
	records := lifecycleRecordsFromWriter(t, svc, initial.HeadSequence)
	if err := validateLifecycleRecordsManifest(t, svc, initial, records); err == nil || !strings.Contains(err.Error(), "adapter metadata changed") {
		t.Fatalf("migration accepted an effect under different adapter metadata: %v", err)
	}
}

func TestLifecycleMigrationRetainsLegacyEffectPayloadCompatibility(t *testing.T) {
	const graph = `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"legacy-effect-migration"},"spec":{"roles":[{"id":"operator","capabilities":["effect.apply","effect.reconcile"]}],"nodes":[{"id":"publish","kind":"effect","role":"operator","title":"publish","inputs":{"adapter":"manual","request":{"target":"artifact-store"}},"outcomes":[{"id":"published","class":"success"}]}],"edges":[]}}`
	writer, writerInitial := lifecycleWriterService(t, "legacy-effect-writer", graph)
	if _, err := writer.BindRole("operator", "codex", "operator-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ApplyAction(findActionRef(t, writer, "operator", "publish", "node.start"), json.RawMessage(`{}`), "start"); err != nil {
		t.Fatal(err)
	}
	prepared, err := writer.ApplyAction(findActionRef(t, writer, "operator", "publish", "effect.prepare"), json.RawMessage(`{}`), "prepare")
	if err != nil {
		t.Fatal(err)
	}
	records := lifecycleRecordsFromWriter(t, writer, writerInitial.HeadSequence)
	found := false
	for recordIndex := range records {
		for eventIndex := range records[recordIndex].Events {
			event := &records[recordIndex].Events[eventIndex]
			if event.Type != "effect.prepared" {
				continue
			}
			var effect map[string]any
			if err := json.Unmarshal(event.Payload, &effect); err != nil {
				t.Fatal(err)
			}
			delete(effect, "adapterVersion")
			delete(effect, "adapterSchemaHash")
			event.Payload = payloadJSON(effect)
			found = true
		}
	}
	if !found {
		t.Fatal("writer history did not contain effect.prepared")
	}
	changed := providers.New()
	if err := changed.RegisterEffect(changedManualAdapter{}); err != nil {
		t.Fatal(err)
	}
	writer.Providers = changed
	if err := validateLifecycleRecordsManifest(t, writer, writerInitial, records); err != nil {
		t.Fatalf("v0.22-compatible Effect payload was rejected: %v", err)
	}

	partial := cloneLifecycleRecords(t, records)
	for recordIndex := range partial {
		for eventIndex := range partial[recordIndex].Events {
			event := &partial[recordIndex].Events[eventIndex]
			if event.Type != "effect.prepared" {
				continue
			}
			var effect map[string]any
			_ = json.Unmarshal(event.Payload, &effect)
			effect["adapterVersion"] = "0.1.0"
			event.Payload = payloadJSON(effect)
		}
	}
	if _, err := simulateLifecycleEventSequence(writerInitial, partial); err == nil || !strings.Contains(err.Error(), "graph declaration") {
		t.Fatalf("partial adapter metadata binding was accepted: %v", err)
	}

	importer, importInitial := lifecycleWriterService(t, "legacy-effect-importer", graph)
	manifest := LifecycleMigrationManifest{APIVersion: LifecycleMigrationAPIVersion, Kind: "LifecycleMigration", ProjectID: importInitial.ProjectID, GraphRevision: importInitial.GraphRevision, ExpectedJournalHead: importInitial.HeadHash, Source: LifecycleMigrationSource{System: "previous-dagrail", Project: "portable-history"}, Records: cloneLifecycleRecords(t, records)}
	sealLifecycleManifest(t, &manifest)
	validateLifecycleMigrationSchema(t, manifest)
	if _, err := importer.ImportLifecycleHistory(manifest, manifest.Source.AuthorityHash, "migration", "import-legacy-effect"); err != nil {
		t.Fatalf("legacy Effect history import failed: %v", err)
	}
	state, _, err := importer.load()
	if err != nil {
		t.Fatal(err)
	}
	effect := state.Effects[prepared.ActionID]
	if effect.ID == "" || effect.AdapterVersion != "" || effect.AdapterSchemaHash != "" {
		t.Fatalf("legacy Effect metadata was not preserved as unknown: %#v", effect)
	}
	continuityValue, err := importer.Inspect("effect-continuity:" + prepared.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	continuity := continuityValue.(EffectContinuity)
	if continuity.ReconcileAllowed || !contains(continuity.ReconcileBlockers, "prepared_adapter_metadata_missing") {
		t.Fatalf("legacy Effect was claimed safe to reconcile: %#v", continuity)
	}
	audit, err := importer.PreWait()
	if err != nil {
		t.Fatal(err)
	}
	assertEffectRemediationBlocked(t, audit, prepared.ActionID, "restore_prior_runtime_or_quarantine")
	before := state.HeadHash
	if _, err := importer.ReconcileEffect(prepared.ActionID, json.RawMessage(`{}`), "reconcile-legacy"); err == nil || !strings.Contains(err.Error(), "predates adapter metadata") {
		t.Fatalf("legacy Effect was reconciled automatically: %v", err)
	}
	after, _, _ := importer.load()
	if after.HeadHash != before {
		t.Fatal("rejected legacy Effect reconciliation mutated the journal")
	}

	terminalWriter, terminalInitial := lifecycleWriterService(t, "legacy-terminal-effect-writer", graph)
	_, _ = terminalWriter.BindRole("operator", "codex", "operator-session", time.Hour, false, "bind")
	_, _ = terminalWriter.ApplyAction(findActionRef(t, terminalWriter, "operator", "publish", "node.start"), json.RawMessage(`{}`), "start")
	terminalPrepared, err := terminalWriter.ApplyAction(findActionRef(t, terminalWriter, "operator", "publish", "effect.prepare"), json.RawMessage(`{}`), "prepare")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := terminalWriter.ReconcileEffect(terminalPrepared.ActionID, json.RawMessage(`{"externalId":"published","recipientVisible":true,"deliveryStatus":"visible"}`), "reconcile"); err != nil {
		t.Fatal(err)
	}
	terminalRecords := lifecycleRecordsFromWriter(t, terminalWriter, terminalInitial.HeadSequence)
	terminalWriter.Providers = changed
	if err := validateLifecycleRecordsManifest(t, terminalWriter, terminalInitial, cloneLifecycleRecords(t, terminalRecords)); err != nil {
		t.Fatalf("confirmed metadata-bound Effect depended on the current adapter: %v", err)
	}
	for recordIndex := range terminalRecords {
		for eventIndex := range terminalRecords[recordIndex].Events {
			event := &terminalRecords[recordIndex].Events[eventIndex]
			if event.Type != "effect.prepared" {
				continue
			}
			var effect map[string]any
			_ = json.Unmarshal(event.Payload, &effect)
			delete(effect, "adapterVersion")
			delete(effect, "adapterSchemaHash")
			event.Payload = payloadJSON(effect)
		}
	}
	if err := validateLifecycleRecordsManifest(t, terminalWriter, terminalInitial, terminalRecords); err != nil {
		t.Fatalf("terminal v0.22-compatible Effect payload was rejected: %v", err)
	}
}

func TestLifecycleMigrationRetainsReleasedOpaqueActionInputs(t *testing.T) {
	const graph = `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"legacy-action-inputs"},"spec":{"roles":[{"id":"operator","capabilities":["node.run","effect.apply","effect.reconcile"]}],"nodes":[{"id":"work","kind":"task","role":"operator","title":"work","outcomes":[{"id":"done","class":"success"}]},{"id":"publish","kind":"effect","role":"operator","title":"publish","inputs":{"adapter":"manual","request":{"target":"artifact-store"}},"outcomes":[{"id":"published","class":"success"}]}],"edges":[]}}`
	writer, initial := lifecycleWriterService(t, "legacy-action-input-writer", graph)
	if _, err := writer.BindRole("operator", "codex", "operator-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ApplyAction(findActionRef(t, writer, "operator", "work", "node.start"), json.RawMessage(`{}`), "work/start"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ApplyAction(findActionRef(t, writer, "operator", "work", "attempt.checkpoint"), json.RawMessage(`{"summary":"legacy checkpoint"}`), "work/checkpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ApplyAction(findActionRef(t, writer, "operator", "work", "attempt.submit"), json.RawMessage(`{}`), "work/submit"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ApplyAction(findActionRef(t, writer, "operator", "work", "task.complete"), json.RawMessage(`{"outcome":"done"}`), "work/complete"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ApplyAction(findActionRef(t, writer, "operator", "publish", "node.start"), json.RawMessage(`{}`), "publish/start"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ApplyAction(findActionRef(t, writer, "operator", "publish", "effect.prepare"), json.RawMessage(`{}`), "publish/prepare"); err != nil {
		t.Fatal(err)
	}

	records := lifecycleRecordsFromWriter(t, writer, initial.HeadSequence)
	mutatedKinds := map[string]bool{}
	for recordIndex := range records {
		for eventIndex := range records[recordIndex].Events {
			event := &records[recordIndex].Events[eventIndex]
			if event.Type != "action.applied" {
				continue
			}
			var action map[string]any
			if err := json.Unmarshal(event.Payload, &action); err != nil {
				t.Fatal(err)
			}
			kind, _ := action["kind"].(string)
			switch kind {
			case "node.start", "attempt.submit", "effect.prepare":
				action["input"] = true
				mutatedKinds[kind] = true
			case "attempt.checkpoint", "task.complete":
				input, _ := action["input"].(map[string]any)
				input["legacyIgnored"] = true
				mutatedKinds[kind] = true
			}
			event.Payload = payloadJSON(action)
		}
	}
	for _, kind := range []string{"node.start", "attempt.checkpoint", "attempt.submit", "task.complete", "effect.prepare"} {
		if !mutatedKinds[kind] {
			t.Fatalf("writer history did not contain %s", kind)
		}
	}
	if err := validateLifecycleRecordsManifest(t, writer, initial, records); err != nil {
		t.Fatalf("released opaque action input was rejected during migration: %v", err)
	}
}

func assertEffectRemediationBlocked(t *testing.T, audit PreWaitAudit, actionID, nextStep string) {
	t.Helper()
	found := false
	for _, remediation := range audit.Remediations {
		if remediation.TargetRef != "effect:"+actionID {
			continue
		}
		found = true
		if remediation.Code != "resolve_effect_reconcile_blockers" || remediation.Operation.Kind == "effect.reconcile" || remediation.Operation.Params["nextStep"] != nextStep {
			t.Fatalf("unsafe Effect remediation was advertised: %#v", remediation)
		}
	}
	if !found {
		t.Fatalf("blocked Effect remediation missing for %s: %#v", actionID, audit.Remediations)
	}
}

type changedManualAdapter struct{}

func (changedManualAdapter) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "manual", Version: "9.9.0", SchemaHash: "sha256:changed-manual-effect"}
}

func (changedManualAdapter) Prepare(ctx context.Context, request sdk.EffectRequest) (sdk.PreparedEffect, error) {
	return (effects.Manual{}).Prepare(ctx, request)
}

func (changedManualAdapter) Dispatch(ctx context.Context, request sdk.EffectRequest, prepared sdk.PreparedEffect) (sdk.EffectReceipt, error) {
	return (effects.Manual{}).Dispatch(ctx, request, prepared)
}

func (changedManualAdapter) Reconcile(ctx context.Context, request sdk.EffectRequest, prepared sdk.PreparedEffect, evidence json.RawMessage) (sdk.EffectReceipt, error) {
	return (effects.Manual{}).Reconcile(ctx, request, prepared, evidence)
}

func TestAllowedActionRuntimeEnforcesPublishedSchemasWithoutJournalMutation(t *testing.T) {
	svc, _ := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"action-schema-runtime"},"spec":{"resourceCapacities":[{"kind":"workspace","capacity":1}],"roles":[{"id":"worker","capabilities":["node.run","resource.close"]}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"work","resources":[{"kind":"workspace","quantity":1}],"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	if _, err := svc.BindRole("worker", "codex", "worker-session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	before, _, err := svc.load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "node.start"), json.RawMessage(`[]`), "invalid-start"); err == nil || !strings.Contains(err.Error(), "allowed action schema") {
		t.Fatalf("node.start accepted an array outside its published schema: %v", err)
	}
	after, _, _ := svc.load()
	if after.HeadSequence != before.HeadSequence {
		t.Fatal("rejected node.start mutated the journal")
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "node.start"), json.RawMessage(`{}`), "start"); err != nil {
		t.Fatal(err)
	}

	invalid := []struct {
		kind  string
		input json.RawMessage
	}{
		{"attempt.checkpoint", json.RawMessage(`{"summary":"bounded","unknown":true}`)},
		{"task.complete", json.RawMessage(`{"outcome":"done","unknown":true}`)},
		{"resource.close", json.RawMessage(`{"status":"confirmed","receipt":null}`)},
		{"evidence.publish", json.RawMessage(`{}`)},
	}
	for index, test := range invalid {
		stateBefore, _, _ := svc.load()
		if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", test.kind), test.input, fmt.Sprintf("invalid-%d", index)); err == nil || !strings.Contains(err.Error(), "allowed action schema") {
			t.Fatalf("%s accepted input outside its published schema: %v", test.kind, err)
		}
		stateAfter, _, _ := svc.load()
		if stateAfter.HeadSequence != stateBefore.HeadSequence {
			t.Fatalf("rejected %s input mutated the journal", test.kind)
		}
	}
	if _, err := svc.ApplyAction(findActionRef(t, svc, "worker", "work", "attempt.checkpoint"), json.RawMessage(`{"summary":"valid checkpoint"}`), "checkpoint"); err != nil {
		t.Fatalf("runtime rejected schema-valid checkpoint: %v", err)
	}
}

func TestEvidencePublishArtifactURISchemaMatchesRuntimePolicy(t *testing.T) {
	artifact := func(uri string, includeURI bool) map[string]any {
		value := map[string]any{
			"digest":     testDigest("a"),
			"type":       "candidate",
			"size":       1,
			"provenance": map[string]any{"producer": "test"},
		}
		if includeURI {
			value["uri"] = uri
		}
		return value
	}
	input := func(uri string, includeURI bool) json.RawMessage {
		encoded, err := json.Marshal(map[string]any{
			"candidate":          artifact(uri, includeURI),
			"prospectiveTree":    artifact("file:///tmp/prospective-tree", true),
			"commandGraphDigest": testDigest("b"),
			"protectedInputs":    []map[string]any{{"name": "source", "digest": testDigest("c")}},
			"observations": map[string]bool{
				"exact": true, "clean": true, "depthComplete": true,
				"sourceUnmodified": true, "resourcesClosed": true,
			},
			"artifacts": []map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	schema := actionInputSchema("evidence.publish", domain.NodeDefinition{})
	tests := []struct {
		name       string
		uri        string
		includeURI bool
		valid      bool
	}{
		{name: "omitted", valid: true},
		{name: "explicit empty", includeURI: true, valid: true},
		{name: "file", uri: "file:///tmp/candidate", includeURI: true, valid: true},
		{name: "http", uri: "http://example.test/candidate", includeURI: true, valid: true},
		{name: "https", uri: "https://example.test/candidate", includeURI: true, valid: true},
		{name: "s3", uri: "s3://bucket/candidate", includeURI: true, valid: true},
		{name: "gs", uri: "gs://bucket/candidate", includeURI: true, valid: true},
		{name: "az", uri: "az://container/candidate", includeURI: true, valid: true},
		{name: "git", uri: "git://example.test/candidate", includeURI: true, valid: true},
		{name: "oci", uri: "oci://registry.test/candidate", includeURI: true, valid: true},
		{name: "uppercase scheme", uri: "FILE:///tmp/candidate", includeURI: true, valid: true},
		{name: "at sign outside userinfo", uri: "file:///tmp/@cache/candidate", includeURI: true, valid: true},
		{name: "2048 ASCII characters", uri: "file:" + strings.Repeat("a", 2043), includeURI: true, valid: true},
		{name: "2048 Unicode characters", uri: "file:" + strings.Repeat("é", 2043), includeURI: true, valid: true},
		{name: "relative", uri: "artifacts/candidate.json", includeURI: true},
		{name: "unsupported scheme", uri: "ftp://example.test/candidate", includeURI: true},
		{name: "userinfo", uri: "https://user@example.test/candidate", includeURI: true},
		{name: "query", uri: "https://example.test/candidate?download=1", includeURI: true},
		{name: "bare query delimiter", uri: "file:///tmp/candidate?", includeURI: true},
		{name: "fragment", uri: "https://example.test/candidate#part", includeURI: true},
		{name: "bare fragment delimiter", uri: "file:///tmp/candidate#", includeURI: true},
		{name: "invalid escape", uri: "file:///tmp/%zz", includeURI: true},
		{name: "control character", uri: "file:///tmp/candidate\n", includeURI: true},
		{name: "2049 ASCII characters", uri: "file:" + strings.Repeat("a", 2044), includeURI: true},
		{name: "2049 Unicode characters", uri: "file:" + strings.Repeat("é", 2044), includeURI: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schemaErr := validateAllowedActionInput(schema, input(test.uri, test.includeURI))
			runtimeErr := validateArtifact(domain.ArtifactRef{
				Digest: testDigest("a"), Type: "candidate", Size: 1, URI: test.uri,
				Provenance: domain.ArtifactProvenance{Producer: "test"},
			})
			if (schemaErr == nil) != test.valid {
				t.Fatalf("schema validity = %t, want %t: %v", schemaErr == nil, test.valid, schemaErr)
			}
			if (runtimeErr == nil) != test.valid {
				t.Fatalf("runtime validity = %t, want %t: %v", runtimeErr == nil, test.valid, runtimeErr)
			}
		})
	}
}

func TestEvidenceReferenceURISchemaRetainsRelativeCompatibility(t *testing.T) {
	schema := actionInputSchema("attempt.checkpoint", domain.NodeDefinition{})
	input := json.RawMessage(`{"summary":"checkpoint","evidenceRefs":[{"digest":"` + testDigest("a") + `","type":"report","size":1,"uri":"artifacts/report.json"}]}`)
	if err := validateAllowedActionInput(schema, input); err != nil {
		t.Fatalf("checkpoint schema rejected a legacy relative EvidenceRef URI: %v", err)
	}
}
