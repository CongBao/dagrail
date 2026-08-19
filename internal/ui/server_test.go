package ui_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/service"
	"github.com/CongBao/dagrail/internal/ui"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestReadOnlyUIExposesBoundedSnapshotAndNoWriteRoute(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "ui-test")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"ui"},"spec":{"roles":[],"nodes":[{"id":"A","kind":"milestone","title":"A","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()
	response, err := http.Get(server.URL + "/api/v1/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" || !strings.Contains(response.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("unsafe response: %d %+v", response.StatusCode, response.Header)
	}
	var snapshot ui.Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.ReadOnly || len(snapshot.Nodes) != 1 || snapshot.Nodes[0].Status != "terminal" || snapshot.Incidents == nil || snapshot.Attempts == nil || snapshot.Leases == nil {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if strings.Contains(string(body), "allowedActions") || strings.Contains(string(body), "action-secret") {
		t.Fatalf("UI exposed control data: %s", body)
	}

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/snapshot", strings.NewReader(`{}`))
	writeResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = writeResponse.Body.Close()
	if writeResponse.StatusCode != http.StatusMethodNotAllowed || writeResponse.Header.Get("Allow") != "GET, HEAD" {
		t.Fatalf("write route was not rejected: %d", writeResponse.StatusCode)
	}

	index, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	indexBody, _ := io.ReadAll(index.Body)
	_ = index.Body.Close()
	if !strings.Contains(string(indexBody), "Read only") || !strings.Contains(string(indexBody), `role="dialog" aria-modal="true"`) || !strings.Contains(string(indexBody), `data-view="summary"`) || !strings.Contains(string(indexBody), `data-view="detail"`) || !strings.Contains(string(indexBody), `aria-hidden="true" tabindex="-1" inert`) || strings.Contains(string(indexBody), "https://") {
		t.Fatalf("unexpected UI shell: %s", indexBody)
	}
	assetResponse, err := http.Get(server.URL + "/assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	_ = assetResponse.Body.Close()
	if assetResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("unexpected embedded asset was served: %d", assetResponse.StatusCode)
	}
	stylesheet, err := http.Get(server.URL + "/assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	stylesheetBody, _ := io.ReadAll(stylesheet.Body)
	_ = stylesheet.Body.Close()
	if stylesheet.StatusCode != http.StatusOK || !strings.Contains(string(stylesheetBody), "[hidden] { display:none !important; }") {
		t.Fatalf("hidden UI controls are not guaranteed to collapse: %d %s", stylesheet.StatusCode, stylesheetBody)
	}
	script, err := http.Get(server.URL + "/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	scriptBody, _ := io.ReadAll(script.Body)
	_ = script.Body.Close()
	for _, contract := range []string{"aria-expanded", "ArrowRight", "ArrowLeft", "expanded", "collapsed", "renderBreadcrumb", "renderMinimap", "centerSelected"} {
		if !strings.Contains(string(scriptBody), contract) {
			t.Fatalf("UI interaction contract %q is missing", contract)
		}
	}
	for _, contract := range []string{"view:'auto'", "groupState", "state.view==='auto'?'':state.view"} {
		if !strings.Contains(string(scriptBody), contract) {
			t.Fatalf("UI automatic legacy/detail or compact group-state contract %q is missing", contract)
		}
	}
	for _, contract := range []string{"AbortController", "/api/v1/head", "completeAggregateEdges", "showNodeSkeleton", "dagrail.autoRefresh", "snapshotAt", "syncBackgroundInert"} {
		if !strings.Contains(string(scriptBody), contract) {
			t.Fatalf("UI large-graph request contract %q is missing", contract)
		}
	}
	for _, contract := range []string{"else{clearError();setConnection('connected','Connected')", "tooltip.textContent=`${node.title||node.id} (${node.id})`"} {
		if !strings.Contains(string(scriptBody), contract) {
			t.Fatalf("UI recovery or full-title contract %q is missing", contract)
		}
	}
	if strings.Contains(string(scriptBody), "setInterval") || strings.Contains(string(scriptBody), "15000") {
		t.Fatal("UI restored the overlapping fixed 15-second refresh loop")
	}
}

func TestUIServerRejectsNonLoopbackBinding(t *testing.T) {
	if err := ui.Serve(context.Background(), nil, "0.0.0.0:0", false, io.Discard); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback binding accepted: %v", err)
	}
}

func TestExplorerHeadPollObservesChangeWithoutMaterializingAView(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "head poll")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"head"},"spec":{"roles":[{"id":"controller","capabilities":[]}],"nodes":[{"id":"work","kind":"task","role":"controller","title":"Work","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "head/graph", "controller"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()
	var head ui.ExplorerHead
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/head", http.StatusOK), &head); err != nil {
		t.Fatal(err)
	}
	if head.SnapshotAvailable || !head.Changed || head.HeadSequence == 0 {
		t.Fatalf("cold head poll claimed a materialized snapshot: %+v", head)
	}
	_ = getBody(t, server.URL+"/api/v1/overview", http.StatusOK)
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/head", http.StatusOK), &head); err != nil {
		t.Fatal(err)
	}
	if !head.SnapshotAvailable || head.Changed || head.SnapshotSequence != head.HeadSequence {
		t.Fatalf("warm head poll lost snapshot identity: %+v", head)
	}
	if _, err := svc.BindRole("controller", "test", "session", time.Minute, false, "head/bind"); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/head", http.StatusOK), &head); err != nil {
		t.Fatal(err)
	}
	if !head.Changed || head.HeadSequence <= head.SnapshotSequence {
		t.Fatalf("head append was not observed cheaply: %+v", head)
	}
}

func TestExplorerRejectsDNSRebindingAndCrossPortOrigins(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "origin-boundary")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()

	maliciousHost, _ := http.NewRequest(http.MethodGet, server.URL+"/", nil)
	maliciousHost.Host = "rebound.example.invalid"
	response, err := http.DefaultClient.Do(maliciousHost)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("DNS-rebinding Host was accepted: %d", response.StatusCode)
	}

	crossPort, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/overview", nil)
	crossPort.Header.Set("Origin", "http://127.0.0.1:6553")
	response, err = http.DefaultClient.Do(crossPort)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden || response.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("cross-port Origin was accepted or CORS-enabled: %d %+v", response.StatusCode, response.Header)
	}

	sameOrigin, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/overview", nil)
	sameOrigin.Header.Set("Origin", server.URL)
	response, err = http.DefaultClient.Do(sameOrigin)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cross-Origin-Resource-Policy") != "same-origin" || response.Header.Get("Cross-Origin-Opener-Policy") != "same-origin" {
		t.Fatalf("same-origin response lacks isolation headers: %d %+v", response.StatusCode, response.Header)
	}
}

func TestExplorerAPIsAreFilteredBoundedAndPayloadFree(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "explorer")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"explorer"},"spec":{"roles":[{"id":"worker","capabilities":["node.run","node.review"]}],"nodes":[{"id":"A","kind":"task","role":"worker","title":"alpha <script>","inputs":{"operation":"compile"},"metadata":{"privateNote":"ui-metadata-must-not-leak"},"externalRefs":[{"system":"tracker","type":"issue","id":"A","url":"https://example.invalid/?signal=ui-ref-must-not-leak"}],"outcomes":[{"id":"done","class":"success"}]},{"id":"B","kind":"review","role":"worker","title":"beta review","outcomes":[{"id":"approve","class":"success"}]},{"id":"C","kind":"task","role":"worker","title":"gamma","outcomes":[{"id":"done","class":"success"}]}],"edges":[{"id":"A-B","from":"A","to":"B","when":{"outcome":"done"}},{"id":"B-C","from":"B","to":"C","when":{"outcome":"approve"}}]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()

	overview := getJSON(t, server.URL+"/api/v1/overview", http.StatusOK)
	if overview["apiVersion"] != "dagrail.io/ui/v1beta2" || overview["readOnly"] != true {
		t.Fatalf("unexpected overview: %#v", overview)
	}
	nodes := getJSON(t, server.URL+"/api/v1/nodes?q=beta&status=blocked&limit=20", http.StatusOK)
	if page := nodes["page"].(map[string]any); page["total"] != float64(1) {
		t.Fatalf("filtered node page mismatch: %#v", nodes)
	}
	topology := getJSON(t, server.URL+"/api/v1/topology?focus=B&depth=1&limit=20", http.StatusOK)
	if page := topology["page"].(map[string]any); page["total"] != float64(3) {
		t.Fatalf("focused topology mismatch: %#v", topology)
	}
	detailBody := getBody(t, server.URL+"/api/v1/node?id=A", http.StatusOK)
	if strings.Contains(string(detailBody), `"operation":"compile"`) || strings.Contains(string(detailBody), "allowedActions") || strings.Contains(string(detailBody), "ui-metadata-must-not-leak") || strings.Contains(string(detailBody), "ui-ref-must-not-leak") || !strings.Contains(string(detailBody), `"inputSha256":"sha256:`) || !strings.Contains(string(detailBody), `\u003cscript\u003e`) {
		t.Fatalf("node detail exposed payload or omitted digest/escaping: %s", detailBody)
	}
	getJSON(t, server.URL+"/api/v1/history?limit=10", http.StatusOK)
	operations := getJSON(t, server.URL+"/api/v1/operations?limit=10", http.StatusOK)
	if _, ok := operations["effects"]; !ok {
		t.Fatalf("operations omitted effects: %#v", operations)
	}
	getJSON(t, server.URL+"/api/v1/topology?limit=501", http.StatusBadRequest)
	getJSON(t, server.URL+"/api/v1/nodes?focus=A", http.StatusBadRequest)
	getJSON(t, server.URL+"/api/v1/nodes?q=alpha&q=beta", http.StatusBadRequest)
	getJSON(t, server.URL+"/api/v1/nodes?q="+strings.Repeat("x", 257), http.StatusBadRequest)
	getJSON(t, server.URL+"/api/v1/topology?focus="+strings.Repeat("x", 4097), http.StatusBadRequest)
	getJSON(t, server.URL+"/api/v1/node?id=missing", http.StatusNotFound)
	request, _ := http.NewRequest(http.MethodHead, server.URL+"/api/v1/nodes?unknown=1", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("HEAD bypassed query validation: %d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodHead, server.URL+"/api/v1/node?id=missing", nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("HEAD bypassed resource validation: %d", response.StatusCode)
	}

	request, _ = http.NewRequest(http.MethodHead, server.URL+"/api/v1/overview", nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("X-DAGrail-Read-Only") != "true" || !strings.Contains(response.Header.Get("Permissions-Policy"), "camera=()") {
		t.Fatalf("HEAD/security contract mismatch: %d %#v", response.StatusCode, response.Header)
	}
}

func TestExplorerLargeGraphUsesDeterministicPagesAndTopologyCaps(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "large explorer")
	if err != nil {
		t.Fatal(err)
	}
	graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "large"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run"}}}, Nodes: []domain.NodeDefinition{}, Edges: []domain.EdgeDefinition{}}}
	for index := range 2048 {
		id := fmt.Sprintf("node-%04d", index)
		graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: id, Kind: "task", Role: "worker", Title: "bounded node " + id, Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}})
		if index > 0 {
			previous := fmt.Sprintf("node-%04d", index-1)
			graph.Spec.Edges = append(graph.Spec.Edges, domain.EdgeDefinition{ID: previous + "-" + id, From: previous, To: id, When: domain.Predicate{Outcome: "done"}})
		}
	}
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "large.json")
	if err := os.WriteFile(graphPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "large", "governor"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()
	started := time.Now()
	first := getBody(t, server.URL+"/api/v1/nodes?limit=200", http.StatusOK)
	second := getBody(t, server.URL+"/api/v1/nodes?limit=200", http.StatusOK)
	if string(first) != string(second) || len(first) >= 2*1024*1024 {
		t.Fatalf("large node page is not deterministic/bounded: equal=%v bytes=%d", string(first) == string(second), len(first))
	}
	var page ui.NodePage
	if err := json.Unmarshal(first, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Nodes) != 200 || page.Page.Total != 2048 || !page.Page.Truncated || page.Page.NextCursor == nil || *page.Page.NextCursor != 200 {
		t.Fatalf("unexpected large node page: %+v nodes=%d", page.Page, len(page.Nodes))
	}
	var topology ui.TopologyPage
	topologyRaw := getBody(t, server.URL+"/api/v1/topology?limit=200", http.StatusOK)
	if err := json.Unmarshal(topologyRaw, &topology); err != nil {
		t.Fatal(err)
	}
	if len(topology.Nodes) != 200 || topology.Page.Total != 2048 || !topology.Page.Truncated || len(topology.Edges) != 199 {
		t.Fatalf("unexpected bounded topology: nodes=%d edges=%d page=%+v", len(topology.Nodes), len(topology.Edges), topology.Page)
	}
	var focused ui.TopologyPage
	focusedRaw := getBody(t, server.URL+"/api/v1/topology?focus=node-1024&depth=2&limit=200", http.StatusOK)
	if err := json.Unmarshal(focusedRaw, &focused); err != nil {
		t.Fatal(err)
	}
	if len(focused.Nodes) != 5 || len(focused.Edges) != 4 || focused.Page.Truncated {
		t.Fatalf("focused neighborhood mismatch: nodes=%d edges=%d page=%+v", len(focused.Nodes), len(focused.Edges), focused.Page)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second*time.Duration(raceTestMultiplier) {
		t.Fatalf("large explorer API gate exceeded 5s: %v", elapsed)
	}
}

func TestExplorerSummaryTopologyKeepsAllGroupsBeyondInternalNodeCap(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "hierarchical explorer")
	if err != nil {
		t.Fatal(err)
	}
	graph := domain.GraphDefinition{
		APIVersion: domain.GraphAPIVersion,
		Kind:       domain.GraphKind,
		Metadata:   domain.GraphMetadata{Name: "large grouped graph"},
		Spec: domain.GraphSpec{
			Roles:  []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run"}}},
			Groups: []domain.GroupDefinition{},
			Nodes:  []domain.NodeDefinition{},
			Edges:  []domain.EdgeDefinition{},
		},
	}
	for groupIndex := 0; groupIndex < 58; groupIndex++ {
		groupID := fmt.Sprintf("group-%02d", groupIndex)
		summaryNodeID := fmt.Sprintf("%s-node-20", groupID)
		graph.Spec.Groups = append(graph.Spec.Groups, domain.GroupDefinition{ID: groupID, Title: fmt.Sprintf("Work unit %02d", groupIndex), Kind: "work-unit", SummaryNodeID: summaryNodeID, CollapsedByDefault: true})
		for nodeIndex := 0; nodeIndex < 21; nodeIndex++ {
			nodeID := fmt.Sprintf("%s-node-%02d", groupID, nodeIndex)
			graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: nodeID, Kind: "task", Role: "worker", Title: nodeID, GroupID: groupID, Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}})
		}
		if groupIndex > 0 {
			graph.Spec.Edges = append(graph.Spec.Edges, domain.EdgeDefinition{ID: fmt.Sprintf("edge-%02d", groupIndex), From: fmt.Sprintf("group-%02d-node-20", groupIndex-1), To: summaryNodeID, When: domain.Predicate{Outcome: "done"}})
		}
	}
	graphRaw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "grouped.json")
	if err := os.WriteFile(graphPath, graphRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()

	var summary ui.TopologyPage
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/topology?limit=1", http.StatusOK), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.APIVersion != "dagrail.io/ui/v1beta2" || summary.Mode != "summary" || len(summary.Groups) != 58 || len(summary.Nodes) != 0 || summary.Page.Truncated {
		t.Fatalf("summary render cap hid top-level structure: %#v", summary)
	}
	if summary.ProjectionDigest == "" || summary.MembershipDigest == "" {
		t.Fatalf("summary projection provenance is missing: %#v", summary)
	}
	validateExplorerContract(t, summary)

	var matched ui.TopologyPage
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/topology?mode=summary&q=group-19-node-07&limit=1", http.StatusOK), &matched); err != nil {
		t.Fatal(err)
	}
	if len(matched.Groups) != 1 || matched.Groups[0].ID != "group-19" || matched.Groups[0].InternalMatchCount != 1 {
		t.Fatalf("internal match lost its group context: %#v", matched.Groups)
	}

	var expanded ui.TopologyPage
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/topology?mode=summary&expanded=group-19&limit=200", http.StatusOK), &expanded); err != nil {
		t.Fatal(err)
	}
	if len(expanded.Groups) != 58 || len(expanded.Nodes) != 21 || len(expanded.ExpandedGroupIDs) != 1 || expanded.ExpandedGroupIDs[0] != "group-19" {
		t.Fatalf("single-group expansion changed unrelated groups: groups=%d nodes=%d expanded=%v", len(expanded.Groups), len(expanded.Nodes), expanded.ExpandedGroupIDs)
	}
	var collapsedFocus ui.TopologyPage
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/topology?mode=summary&focus=group-19&collapsed=group-19&limit=200", http.StatusOK), &collapsedFocus); err != nil {
		t.Fatal(err)
	}
	if len(collapsedFocus.Nodes) != 0 || len(collapsedFocus.ExpandedGroupIDs) != 0 {
		t.Fatalf("group focus overrode explicit collapse: nodes=%d expanded=%v", len(collapsedFocus.Nodes), collapsedFocus.ExpandedGroupIDs)
	}

	var detail ui.TopologyPage
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/topology?mode=detail&limit=200", http.StatusOK), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Mode != "detail" || len(detail.Nodes) != 200 || !detail.Page.Truncated {
		t.Fatalf("execution detail no longer preserves the bounded flat graph: %#v", detail.Page)
	}
	validateExplorerContract(t, detail)
}

func TestExplorerProductScaleSummaryIsCompleteAndBounded(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "product-scale explorer")
	if err != nil {
		t.Fatal(err)
	}
	graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "product scale"}, Spec: domain.GraphSpec{
		Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{}}, {ID: "global", Capabilities: []string{}}}, Groups: []domain.GroupDefinition{}, Nodes: []domain.NodeDefinition{}, Edges: []domain.EdgeDefinition{},
	}}
	groupNode := func(group, node int) string { return fmt.Sprintf("group-%02d-node-%02d", group, node) }
	for groupIndex := range 58 {
		nodeCount := 15
		if groupIndex < 21 {
			nodeCount++
		}
		groupID := fmt.Sprintf("group-%02d", groupIndex)
		graph.Spec.Groups = append(graph.Spec.Groups, domain.GroupDefinition{ID: groupID, Title: fmt.Sprintf("Work unit %02d", groupIndex), Kind: "work-unit", SummaryNodeID: groupNode(groupIndex, nodeCount-1), CollapsedByDefault: true})
		for nodeIndex := range nodeCount {
			graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: groupNode(groupIndex, nodeIndex), Kind: "task", Role: "worker", Title: fmt.Sprintf("Internal %02d/%02d", groupIndex, nodeIndex), GroupID: groupID, Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}})
		}
	}
	for index := range 29 {
		graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: fmt.Sprintf("global-%02d", index), Kind: "gate", Role: "global", Title: fmt.Sprintf("Global gate %02d", index), Outcomes: []domain.Outcome{{ID: "passed", Class: "success"}}})
	}
	edgeCount := 0
	for from := 0; from < 58 && edgeCount < 190; from++ {
		for to := from + 1; to < 58 && edgeCount < 190; to++ {
			graph.Spec.Edges = append(graph.Spec.Edges, domain.EdgeDefinition{ID: fmt.Sprintf("aggregate-%03d", edgeCount), From: groupNode(from, 0), To: groupNode(to, 0), When: domain.Predicate{Outcome: "done"}})
			edgeCount++
		}
	}
	if len(graph.Spec.Nodes) != 920 || len(graph.Spec.Edges) != 190 {
		t.Fatalf("invalid product-scale fixture: nodes=%d edges=%d", len(graph.Spec.Nodes), len(graph.Spec.Edges))
	}
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "product-scale.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(path, "product-scale", "governor"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()
	started := time.Now()
	var topology ui.TopologyPage
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/topology?mode=summary&limit=200", http.StatusOK), &topology); err != nil {
		t.Fatal(err)
	}
	if len(topology.Groups) != 58 || len(topology.Nodes) != 29 || topology.AggregateEdgePage == nil || topology.AggregateEdgePage.Total != 190 || len(topology.AggregateEdges) != 100 || !topology.AggregateEdgePage.Truncated {
		t.Fatalf("product-scale summary is incomplete: groups=%d nodes=%d aggregate=%d page=%+v", len(topology.Groups), len(topology.Nodes), len(topology.AggregateEdges), topology.AggregateEdgePage)
	}
	ref := url.QueryEscape(topology.AggregateEdgeIndexRef)
	var tail ui.AggregateEdgePage
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/aggregate-edges?ref="+ref+"&cursor=100&limit=100", http.StatusOK), &tail); err != nil {
		t.Fatal(err)
	}
	if len(tail.AggregateEdges) != 90 || tail.Page.Truncated || tail.Page.Total != 190 {
		t.Fatalf("product-scale aggregate edge continuation is incomplete: %+v", tail.Page)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second*time.Duration(raceTestMultiplier) {
		t.Fatalf("product-scale summary gate exceeded 5s: %v", elapsed)
	}
	validateExplorerContract(t, topology, tail)
}

func TestExplorerAggregateEdgesAreBoundedAndRecoverable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "aggregate edge explorer")
	if err != nil {
		t.Fatal(err)
	}
	graph := domain.GraphDefinition{
		APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "aggregate edges"},
		Spec: domain.GraphSpec{
			Groups: []domain.GroupDefinition{
				{ID: "source-group", Title: "Source", Kind: "work-unit", SummaryNodeID: "source", CollapsedByDefault: true},
				{ID: "target-group", Title: "Target", Kind: "work-unit", SummaryNodeID: "target", CollapsedByDefault: true},
			},
			Nodes: []domain.NodeDefinition{
				{ID: "source", Kind: "milestone", Title: "Source", GroupID: "source-group", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}},
				{ID: "target", Kind: "milestone", Title: "Target", GroupID: "target-group", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}},
			},
		},
	}
	for index := 0; index < 70; index++ {
		graph.Spec.Edges = append(graph.Spec.Edges, domain.EdgeDefinition{ID: fmt.Sprintf("parallel-%02d", index), From: "source", To: "target", When: domain.Predicate{Outcome: "done"}})
	}
	raw, _ := json.Marshal(graph)
	graphPath := filepath.Join(root, "aggregate.json")
	if err := os.WriteFile(graphPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()

	var topology ui.TopologyPage
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/topology?mode=summary", http.StatusOK), &topology); err != nil {
		t.Fatal(err)
	}
	if len(topology.AggregateEdges) != 1 || topology.AggregateEdges[0].Count != 70 || topology.AggregateEdges[0].InspectRef == "" {
		t.Fatalf("aggregate edge summary is not bounded and recoverable: %#v", topology.AggregateEdges)
	}
	ref := url.QueryEscape(topology.AggregateEdges[0].InspectRef)
	var page ui.GroupEdgePage
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/group-edges?ref="+ref+"&limit=50", http.StatusOK), &page); err != nil {
		t.Fatal(err)
	}
	if page.Page.Total != 70 || len(page.EdgeIDs) != 50 || page.Page.NextCursor == nil || *page.Page.NextCursor != 50 {
		t.Fatalf("aggregate edge detail is not recoverable: %#v", page)
	}
	var tail ui.GroupEdgePage
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/group-edges?ref="+ref+"&cursor=50&limit=50", http.StatusOK), &tail); err != nil {
		t.Fatal(err)
	}
	if len(tail.EdgeIDs) != 20 || tail.Page.Truncated {
		t.Fatalf("aggregate edge tail is incomplete: %#v", tail)
	}
	validateExplorerContract(t, topology, page, tail)
}

func TestExplorerDenseAggregateEdgeIndexIsBoundedAndRecoverable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "dense aggregate edge explorer")
	if err != nil {
		t.Fatal(err)
	}
	graph := domain.GraphDefinition{
		APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind,
		Metadata: domain.GraphMetadata{Name: "dense aggregate edges"},
	}
	// Keep the public HTTP/pagination contract integrated without rebuilding a
	// 9k-edge journal under every race-detector run. The maximum-size algorithm
	// boundary is exercised directly in api_internal_test.go.
	const groupCount = 20
	for index := 0; index < groupCount; index++ {
		groupID := fmt.Sprintf("group-%03d", index)
		nodeID := fmt.Sprintf("node-%03d", index)
		graph.Spec.Groups = append(graph.Spec.Groups, domain.GroupDefinition{ID: groupID, Title: groupID, Kind: "work-unit", SummaryNodeID: nodeID, CollapsedByDefault: true})
		graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: nodeID, Kind: "milestone", Title: nodeID, GroupID: groupID, Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}})
	}
	for from := 0; from < groupCount; from++ {
		for to := from + 1; to < groupCount; to++ {
			graph.Spec.Edges = append(graph.Spec.Edges, domain.EdgeDefinition{ID: fmt.Sprintf("edge-%03d-%03d", from, to), From: fmt.Sprintf("node-%03d", from), To: fmt.Sprintf("node-%03d", to), When: domain.Predicate{Outcome: "done"}})
		}
	}
	raw, _ := json.Marshal(graph)
	graphPath := filepath.Join(root, "dense.json")
	if err := os.WriteFile(graphPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()

	var topology ui.TopologyPage
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/topology", http.StatusOK), &topology); err != nil {
		t.Fatal(err)
	}
	wantEdges := groupCount * (groupCount - 1) / 2
	if topology.AggregateEdgePage == nil || topology.AggregateEdgePage.Total != wantEdges || !topology.AggregateEdgePage.Truncated || topology.AggregateEdgeIndexRef == "" || len(topology.AggregateEdges) > 100 {
		t.Fatalf("dense aggregate edge index is not bounded and recoverable: page=%+v ref=%q inline=%d", topology.AggregateEdgePage, topology.AggregateEdgeIndexRef, len(topology.AggregateEdges))
	}
	var page ui.AggregateEdgePage
	endpoint := server.URL + "/api/v1/aggregate-edges?ref=" + url.QueryEscape(topology.AggregateEdgeIndexRef) + "&cursor=100&limit=100"
	if err := json.Unmarshal(getBody(t, endpoint, http.StatusOK), &page); err != nil {
		t.Fatal(err)
	}
	wantTail := wantEdges - 100
	if page.Page.Total != wantEdges || page.Page.Cursor != 100 || len(page.AggregateEdges) != wantTail || page.Page.NextCursor != nil || page.Page.Truncated {
		t.Fatalf("dense aggregate edge continuation is incomplete: %#v", page)
	}
	_ = getBody(t, endpoint+"&groupState=expanded", http.StatusNotFound)
	validateExplorerContract(t, topology, page)
}

func TestExplorerGroupStateIsOpaqueAndReadSurfacesAreByteNonmutating(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "opaque group state")
	if err != nil {
		t.Fatal(err)
	}
	graph := domain.GraphDefinition{
		APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "opaque groups"},
		Spec: domain.GraphSpec{
			Groups: []domain.GroupDefinition{
				{ID: "first,group", Title: "First", Kind: "work-unit", SummaryNodeID: "first", CollapsedByDefault: true},
				{ID: "second", Title: "Second", Kind: "work-unit", SummaryNodeID: "second", CollapsedByDefault: true},
			},
			Nodes: []domain.NodeDefinition{
				{ID: "first", Kind: "milestone", Title: "First", GroupID: "first,group", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}},
				{ID: "second", Kind: "milestone", Title: "Second", GroupID: "second", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}},
			},
			Edges: []domain.EdgeDefinition{{ID: "first-second", From: "first", To: "second", When: domain.Predicate{Outcome: "done"}}},
		},
	}
	raw, _ := json.Marshal(graph)
	graphPath := filepath.Join(root, "opaque.json")
	if err := os.WriteFile(graphPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	before := snapshotTreeBytes(t, root)
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()

	var expanded ui.TopologyPage
	endpoint := server.URL + "/api/v1/topology?mode=summary&expanded=first%2Cgroup&expanded=second"
	if err := json.Unmarshal(getBody(t, endpoint, http.StatusOK), &expanded); err != nil {
		t.Fatal(err)
	}
	if len(expanded.ExpandedGroupIDs) != 2 || len(expanded.Nodes) != 2 || len(expanded.Lanes) != 1 || expanded.Lanes[0].ID != "work-units" {
		t.Fatalf("opaque repeated group state or lanes were lost: %#v", expanded)
	}
	var collapsed ui.TopologyPage
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/topology?mode=summary", http.StatusOK), &collapsed); err != nil {
		t.Fatal(err)
	}
	if len(collapsed.AggregateEdges) != 1 {
		t.Fatalf("expected one recoverable aggregate edge: %#v", collapsed.AggregateEdges)
	}
	ref := url.QueryEscape(collapsed.AggregateEdges[0].InspectRef)
	_ = getBody(t, server.URL+"/api/v1/group-edges?ref="+ref, http.StatusOK)
	for _, path := range []string{"/api/v1/head", "/api/v1/overview", "/api/v1/node?id=first", "/api/v1/history", "/api/v1/operations", "/api/v1/snapshot"} {
		_ = getBody(t, server.URL+path, http.StatusOK)
	}
	after := snapshotTreeBytes(t, root)
	if mismatch := firstSnapshotMismatch(before, after); mismatch != "" {
		t.Fatalf("read-only group surfaces changed protected bytes: %s", mismatch)
	}
}

func TestFocusedTopologyKeepsFocusBeforeHighFanoutNeighbors(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "focused topology")
	if err != nil {
		t.Fatal(err)
	}
	graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "star"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run"}}}}}
	graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: "z-focus", Kind: "task", Role: "worker", Title: "focus", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}})
	for index := range 600 {
		id := fmt.Sprintf("a-neighbor-%03d", index)
		graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: id, Kind: "task", Role: "worker", Title: id, Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}})
		graph.Spec.Edges = append(graph.Spec.Edges, domain.EdgeDefinition{ID: "focus-" + id, From: "z-focus", To: id, When: domain.Predicate{Outcome: "done"}})
	}
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "star.json")
	if err := os.WriteFile(graphPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "star", "governor"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()
	var topology ui.TopologyPage
	if err := json.Unmarshal(getBody(t, server.URL+"/api/v1/topology?focus=z-focus&depth=1&limit=200", http.StatusOK), &topology); err != nil {
		t.Fatal(err)
	}
	if len(topology.Nodes) != 200 || topology.Nodes[0].ID != "z-focus" || topology.Page.Total != 601 || !topology.Page.Truncated || len(topology.Edges) != 199 {
		t.Fatalf("focused topology lost distance ordering: first=%q nodes=%d edges=%d page=%+v", topology.Nodes[0].ID, len(topology.Nodes), len(topology.Edges), topology.Page)
	}
}

func TestLegacySnapshotFailsAtomicallyAboveResponseLimit(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "bounded legacy snapshot")
	if err != nil {
		t.Fatal(err)
	}
	graph := domain.GraphDefinition{APIVersion: domain.GraphAPIVersion, Kind: domain.GraphKind, Metadata: domain.GraphMetadata{Name: "large snapshot"}, Spec: domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run"}}}, Nodes: []domain.NodeDefinition{
		{ID: "A", Kind: "task", Role: "worker", Title: "A", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}},
		{ID: "B", Kind: "task", Role: "worker", Title: "B", Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}},
	}}}
	for index := range 18000 {
		id := fmt.Sprintf("edge-%05d-%s", index, strings.Repeat("x", 50))
		graph.Spec.Edges = append(graph.Spec.Edges, domain.EdgeDefinition{ID: id, From: "A", To: "B", When: domain.Predicate{Outcome: "done"}})
	}
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "large-snapshot.json")
	if err := os.WriteFile(graphPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "large-snapshot", "governor"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()
	response, err := http.Get(server.URL + "/api/v1/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge || len(body) > 1024 || !strings.Contains(string(body), "bounded API limit") {
		t.Fatalf("legacy snapshot did not fail atomically: status=%d bytes=%d body=%s", response.StatusCode, len(body), body)
	}
	request, _ := http.NewRequest(http.MethodHead, server.URL+"/api/v1/snapshot", nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("legacy snapshot HEAD diverged from GET: %d", response.StatusCode)
	}
}

func TestHistoryBeforeCursorsRoundTripWithoutOverlap(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "history cursors")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"history"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"work","kind":"task","role":"worker","title":"work","outcomes":[{"id":"complete","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 53; index++ {
		sessionID := fmt.Sprintf("session-%03d", index)
		if _, err := svc.BindRole("worker", "test", sessionID, time.Hour, false, fmt.Sprintf("bind-%03d", index)); err != nil {
			t.Fatal(err)
		}
		if err := svc.ReleaseRole("worker", sessionID, fmt.Sprintf("release-%03d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.BindRole("worker", "test", "session-final", time.Hour, false, "bind-final"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()
	decode := func(path string) ui.HistoryResponse {
		var response ui.HistoryResponse
		if err := json.Unmarshal(getBody(t, server.URL+path, http.StatusOK), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	newest := decode("/api/v1/history?limit=50")
	middle := decode("/api/v1/history?before=60&limit=50")
	oldest := decode("/api/v1/history?before=10&limit=50")
	if newest.Page.Entries[0].Sequence != 60 || newest.Page.Entries[len(newest.Page.Entries)-1].Sequence != 109 || newest.OlderBefore == nil || *newest.OlderBefore != 60 || newest.NewerBefore != nil {
		t.Fatalf("newest page cursors mismatch: %+v", newest)
	}
	if middle.Page.Entries[0].Sequence != 10 || middle.Page.Entries[len(middle.Page.Entries)-1].Sequence != 59 || middle.OlderBefore == nil || *middle.OlderBefore != 10 || middle.NewerBefore == nil || *middle.NewerBefore != 110 {
		t.Fatalf("middle page cursors mismatch: %+v", middle)
	}
	if oldest.Page.Entries[0].Sequence != 1 || oldest.Page.Entries[len(oldest.Page.Entries)-1].Sequence != 9 || oldest.OlderBefore != nil || oldest.NewerBefore == nil || *oldest.NewerBefore != 60 {
		t.Fatalf("oldest page cursors mismatch: %+v", oldest)
	}
}

func TestExplorerResponsesMatchPublishedSchema(t *testing.T) {
	schemaRaw, err := os.ReadFile("../../schemas/ui-api-v1beta2.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(schemaRaw, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:ui-api", document); err != nil {
		t.Fatal(err)
	}
	published, err := compiler.Compile("urn:dagrail:ui-api")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "schema fixture")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"schema"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]},{"id":"governor","capabilities":["incident.manage"]}],"nodes":[{"id":"A","kind":"task","role":"worker","title":"A","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("governor", "codex", "schema-governor", time.Hour, false, "bind-schema-governor"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	incidentRaw, _ := json.Marshal(domain.Incident{ID: "schema-incident", SourceType: "node", SourceID: "A", NodeID: "A", OwnerRole: "governor", Status: "open", Classification: "product", AttemptBudget: 2, DependencyCut: []string{"A"}, OpenedAt: now, UpdatedAt: now})
	if _, err := svc.Journal.Append(journal.Command{ID: "schema-incident-open", Kind: "incident.open", ActorRole: "governor", IdempotencyKey: "schema-incident-open", ObjectRef: "incident:schema-incident"}, []journal.Event{{Type: "incident.opened", Payload: incidentRaw}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetIncidentDisposition("schema-incident", "governor", "quarantine", "bounded isolation", "schema-incident-disposition"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()
	responses := []struct {
		path   string
		status int
	}{
		{"/api/v1/head", http.StatusOK},
		{"/api/v1/overview", http.StatusOK},
		{"/api/v1/nodes", http.StatusOK},
		{"/api/v1/topology", http.StatusOK},
		{"/api/v1/node?id=A", http.StatusOK},
		{"/api/v1/history", http.StatusOK},
		{"/api/v1/operations", http.StatusOK},
		{"/api/v1/nodes?unknown=1", http.StatusBadRequest},
	}
	for _, response := range responses {
		var instance any
		body := getBody(t, server.URL+response.path, response.status)
		if err := json.Unmarshal(body, &instance); err != nil {
			t.Fatalf("decode %s: %v", response.path, err)
		}
		if err := published.Validate(instance); err != nil {
			t.Fatalf("%s does not match published UI schema: %v\n%s", response.path, err, body)
		}
	}
}

func getJSON(t *testing.T, url string, status int) map[string]any {
	t.Helper()
	body := getBody(t, url, status)
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode %s: %v body=%s", url, err, body)
	}
	return value
}

func getBody(t *testing.T, url string, status int) []byte {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("GET %s = %d, want %d: %s", url, response.StatusCode, status, body)
	}
	if len(body) >= 2*1024*1024+1024 {
		t.Fatalf("response exceeds bounded API envelope: %d", len(body))
	}
	return body
}

func snapshotTreeBytes(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	result := map[string][32]byte{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = sha256.Sum256(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func firstSnapshotMismatch(before, after map[string][32]byte) string {
	for path, digest := range before {
		if after[path] != digest {
			return path
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			return path
		}
	}
	return ""
}

func validateExplorerContract(t *testing.T, values ...any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "ui-api-v1beta2.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:ui-api-v1beta2", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:ui-api-v1beta2")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var instance any
		if err := json.Unmarshal(encoded, &instance); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Fatalf("grouped Explorer response violates v1beta2: %v\n%s", err, encoded)
		}
	}
}
