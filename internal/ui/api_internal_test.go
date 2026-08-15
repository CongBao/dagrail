package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/domain"
)

func TestExplorerResponseCapFailsClosedBeforeWriting(t *testing.T) {
	recorder := httptest.NewRecorder()
	err := writeAPIResult(recorder, map[string]string{"value": strings.Repeat("x", maxAPIResponse)}, nil)
	var bounded *explorerError
	if !asExplorerError(err, &bounded) || bounded.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized explorer response did not fail closed: %T %v", err, err)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("oversized explorer response wrote partial bytes: %d", recorder.Body.Len())
	}
}

func TestResourceViewOmitsClosureReceipt(t *testing.T) {
	view := resourceView(domain.ResourceLease{ID: "resource", Kind: "browser", Quantity: 1, NodeID: "node", AttemptID: "attempt", Status: "active", ClosureStatus: "unknown", ClosureReceipt: json.RawMessage(`{"private":"must-not-leak"}`)})
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-not-leak") || strings.Contains(string(raw), "closureReceipt") || !strings.Contains(string(raw), `"closureStatus":"unknown"`) {
		t.Fatalf("resource view leaked receipt or lost typed status: %s", raw)
	}
}

func TestNodeDetailCollectionsAreBounded(t *testing.T) {
	values := make([]int, maxNodeDetailItems+7)
	truncated := map[string]bool{}
	first := capFirst(values, maxNodeDetailItems, truncated, "first")
	last := capLast(values, maxNodeDetailItems, truncated, "last")
	if len(first) != maxNodeDetailItems || len(last) != maxNodeDetailItems || !truncated["first"] || !truncated["last"] {
		t.Fatalf("node detail cap failed: first=%d last=%d truncated=%v", len(first), len(last), truncated)
	}
}
