package ui_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/service"
	"github.com/CongBao/dagrail/internal/ui"
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
	if !strings.Contains(string(indexBody), "Read only") || strings.Contains(string(indexBody), "https://") {
		t.Fatalf("unexpected UI shell: %s", indexBody)
	}
}

func TestUIServerRejectsNonLoopbackBinding(t *testing.T) {
	if err := ui.Serve(context.Background(), nil, "0.0.0.0:0", false, io.Discard); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback binding accepted: %v", err)
	}
}
