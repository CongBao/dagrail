package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGraphPatchMovesActiveNodeBetweenGroupsWithoutChangingExecutionContract(t *testing.T) {
	svc, root := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"group patch"},"spec":{"roles":[{"id":"governor","capabilities":["graph.change"]},{"id":"worker","capabilities":["node.run"]}],"groups":[{"id":"one","title":"One","kind":"work-unit","collapsedByDefault":true},{"id":"two","title":"Two","kind":"work-unit","collapsedByDefault":true}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"Work","groupId":"one","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`)
	if _, err := svc.BindRole("worker", "codex", "worker-session", time.Hour, false, "bind-worker"); err != nil {
		t.Fatal(err)
	}
	startRef := findActionRef(t, svc, "worker", "work", "node.start")
	if _, err := svc.ApplyAction(startRef, json.RawMessage(`{}`), "start-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("governor", "codex", "governor-session", time.Hour, false, "bind-governor"); err != nil {
		t.Fatal(err)
	}

	patchPath := filepath.Join(root, "move-group.json")
	if err := os.WriteFile(patchPath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"moveNodeToGroup","nodeId":"work","groupId":"two"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.PreviewGraphChange(patchPath)
	if err != nil {
		t.Fatalf("active node view move was rejected: %v", err)
	}
	if len(preview.MovedNodes) != 1 || preview.MovedNodes[0] != "work" || len(preview.DependencyCut) != 0 {
		t.Fatalf("group-only impact changed execution topology: %+v", preview)
	}
	if _, err := svc.ApplyGraphChange(patchPath, preview.Token, "move-work", "governor"); err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	node, ok := state.NodeDefinition("work")
	if !ok || node.GroupID != "two" || state.Nodes["work"].Status != "active" {
		t.Fatalf("group move changed or lost active execution: node=%+v runtime=%+v", node, state.Nodes["work"])
	}
}

func TestGraphPatchAddsUpdatesAndRemovesGroupsWithClosedImpact(t *testing.T) {
	svc, root := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"group patch"},"spec":{"roles":[{"id":"governor","capabilities":["graph.change"]}],"nodes":[{"id":"done","kind":"milestone","title":"Done","outcomes":[{"id":"reached","class":"success"}]}],"edges":[]}}`)
	if _, err := svc.BindRole("governor", "codex", "governor-session", time.Hour, false, "bind-governor"); err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(root, "groups.json")
	patch := `{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"addGroup","group":{"id":"phase","title":"Phase","kind":"custom"}},{"op":"addGroup","group":{"id":"work","title":"Work","kind":"work-unit","parentGroupId":"phase","summaryNodeId":"done","collapsedByDefault":true}},{"op":"moveNodeToGroup","nodeId":"done","groupId":"work"},{"op":"updateGroup","group":{"id":"work","title":"Work unit","kind":"work-unit","parentGroupId":"phase","summaryNodeId":"done","collapsedByDefault":true}}]}`
	if err := os.WriteFile(patchPath, []byte(patch), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.PreviewGraphChange(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.AddedGroups) != 2 || len(preview.UpdatedGroups) != 1 || len(preview.MovedNodes) != 1 {
		t.Fatalf("group impact is incomplete: %+v", preview)
	}
	if _, err := svc.ApplyGraphChange(patchPath, preview.Token, "groups", "governor"); err != nil {
		t.Fatal(err)
	}

	removePath := filepath.Join(root, "remove-group.json")
	if err := os.WriteFile(removePath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"removeGroup","groupId":"phase"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PreviewGraphChange(removePath); err == nil {
		t.Fatal("group with a child group was removed")
	}

	clearPath := filepath.Join(root, "clear-groups.json")
	clear := `{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"updateGroup","group":{"id":"work","title":"Work unit","kind":"work-unit","parentGroupId":"phase","collapsedByDefault":true}},{"op":"moveNodeToGroup","nodeId":"done"},{"op":"removeGroup","groupId":"work"},{"op":"removeGroup","groupId":"phase"}]}`
	if err := os.WriteFile(clearPath, []byte(clear), 0o600); err != nil {
		t.Fatal(err)
	}
	clearPreview, err := svc.PreviewGraphChange(clearPath)
	if err != nil {
		t.Fatalf("explicit ungroup and removal was rejected: %v", err)
	}
	if _, err := svc.ApplyGraphChange(clearPath, clearPreview.Token, "clear-groups", "governor"); err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil || state.Graph == nil || len(state.Graph.Spec.Groups) != 0 {
		t.Fatalf("groups were not removed cleanly: groups=%v err=%v", state.Graph.Spec.Groups, err)
	}
}

func TestGraphPatchAddsUpdatesAndSafelyRemovesLanes(t *testing.T) {
	svc, root := governanceService(t, `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"lane patch"},"spec":{"roles":[{"id":"governor","capabilities":["graph.change"]}],"groups":[{"id":"work","title":"Work","kind":"work-unit"}],"nodes":[{"id":"done","kind":"milestone","title":"Done","groupId":"work","outcomes":[{"id":"reached","class":"success"}]}],"edges":[]}}`)
	if _, err := svc.BindRole("governor", "codex", "governor-session", time.Hour, false, "bind-governor"); err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(root, "lanes.json")
	patch := `{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"addLane","lane":{"id":"delivery","title":"Delivery","order":25}},{"op":"updateGroup","group":{"id":"work","title":"Work","kind":"work-unit","laneId":"delivery"}},{"op":"updateLane","lane":{"id":"delivery","title":"Product delivery","order":20}}]}`
	if err := os.WriteFile(patchPath, []byte(patch), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.PreviewGraphChange(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.AddedLanes) != 1 || len(preview.UpdatedLanes) != 1 || len(preview.UpdatedGroups) != 1 || len(preview.DependencyCut) != 0 {
		t.Fatalf("lane-only impact changed execution topology: %+v", preview)
	}
	if _, err := svc.ApplyGraphChange(patchPath, preview.Token, "lanes", "governor"); err != nil {
		t.Fatal(err)
	}
	removePath := filepath.Join(root, "remove-lane.json")
	if err := os.WriteFile(removePath, []byte(`{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"removeLane","laneId":"delivery"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PreviewGraphChange(removePath); err == nil {
		t.Fatal("referenced lane was removed")
	}
	clearPath := filepath.Join(root, "clear-lane.json")
	clear := `{"apiVersion":"dagrail.io/v1alpha1","kind":"GraphPatch","operations":[{"op":"updateGroup","group":{"id":"work","title":"Work","kind":"work-unit"}},{"op":"removeLane","laneId":"delivery"}]}`
	if err := os.WriteFile(clearPath, []byte(clear), 0o600); err != nil {
		t.Fatal(err)
	}
	clearPreview, err := svc.PreviewGraphChange(clearPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(clearPreview.RemovedLanes) != 1 {
		t.Fatalf("lane removal impact missing: %+v", clearPreview)
	}
	if _, err := svc.ApplyGraphChange(clearPath, clearPreview.Token, "clear-lane", "governor"); err != nil {
		t.Fatal(err)
	}
}
