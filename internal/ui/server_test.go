package ui_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
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
	if !strings.Contains(string(indexBody), "Read only") || !strings.Contains(string(indexBody), `role="dialog" aria-modal="true"`) || strings.Contains(string(indexBody), "https://") {
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
}

func TestUIServerRejectsNonLoopbackBinding(t *testing.T) {
	if err := ui.Serve(context.Background(), nil, "0.0.0.0:0", false, io.Discard); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback binding accepted: %v", err)
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
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"explorer"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"worker","title":"alpha <script>","inputs":{"operation":"compile"},"metadata":{"privateNote":"ui-metadata-must-not-leak"},"externalRefs":[{"system":"tracker","type":"issue","id":"A","url":"https://example.invalid/?signal=ui-ref-must-not-leak"}],"outcomes":[{"id":"done","class":"success"}]},{"id":"B","kind":"review","role":"worker","title":"beta review","outcomes":[{"id":"approve","class":"success"}]},{"id":"C","kind":"task","role":"worker","title":"gamma","outcomes":[{"id":"done","class":"success"}]}],"edges":[{"id":"A-B","from":"A","to":"B","when":{"outcome":"done"}},{"id":"B-C","from":"B","to":"C","when":{"outcome":"approve"}}]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()

	overview := getJSON(t, server.URL+"/api/v1/overview", http.StatusOK)
	if overview["apiVersion"] != "dagrail.io/ui/v1beta1" || overview["readOnly"] != true {
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
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("large explorer API gate exceeded 5s: %v", elapsed)
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
	middle := decode("/api/v1/history?before=59&limit=50")
	oldest := decode("/api/v1/history?before=9&limit=50")
	if newest.Page.Entries[0].Sequence != 59 || newest.Page.Entries[len(newest.Page.Entries)-1].Sequence != 108 || newest.OlderBefore == nil || *newest.OlderBefore != 59 || newest.NewerBefore != nil {
		t.Fatalf("newest page cursors mismatch: %+v", newest)
	}
	if middle.Page.Entries[0].Sequence != 9 || middle.Page.Entries[len(middle.Page.Entries)-1].Sequence != 58 || middle.OlderBefore == nil || *middle.OlderBefore != 9 || middle.NewerBefore == nil || *middle.NewerBefore != 109 {
		t.Fatalf("middle page cursors mismatch: %+v", middle)
	}
	if oldest.Page.Entries[0].Sequence != 1 || oldest.Page.Entries[len(oldest.Page.Entries)-1].Sequence != 8 || oldest.OlderBefore != nil || oldest.NewerBefore == nil || *oldest.NewerBefore != 59 {
		t.Fatalf("oldest page cursors mismatch: %+v", oldest)
	}
}

func TestExplorerResponsesMatchPublishedSchema(t *testing.T) {
	schemaRaw, err := os.ReadFile("../../schemas/ui-api-v1beta1.schema.json")
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
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"schema"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"worker","title":"A","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph", "governor"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler(svc))
	defer server.Close()
	responses := []struct {
		path   string
		status int
	}{
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
