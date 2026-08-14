package service_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/service"
)

func TestSupportReportIsBoundedToShareableDiagnostics(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "private-runtime")
	t.Setenv("DAGRAIL_HOME", dataRoot)
	svc, err := service.Init(root, "private-project-name")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "private-graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"private-graph-name"},"spec":{"nodes":[{"id":"private-node-id","kind":"join","title":"private-title","outcomes":[{"id":"done","class":"success"}]}]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "private-idempotency", "private-actor"); err != nil {
		t.Fatal(err)
	}
	data, report, err := svc.SupportBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !report.Shareable || report.Privacy.AbsolutePathsIncluded || report.Privacy.AuthorityPayloadsIncluded || report.ProjectRef == "" {
		t.Fatalf("support privacy contract is incorrect: %+v", report)
	}
	validatePublishedSchema(t, "../../schemas/support-report-v1alpha1.schema.json", report)
	for _, forbidden := range []string{root, dataRoot, "private-project-name", "private-graph-name", "private-node-id", "private-title", "private-idempotency", "private-actor"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("support report disclosed %q: %s", forbidden, data)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var roundTrip service.SupportReport
	if err := decoder.Decode(&roundTrip); err != nil || roundTrip.ProjectRef != report.ProjectRef {
		t.Fatalf("support report is not valid JSON: %+v %v", roundTrip, err)
	}
}
