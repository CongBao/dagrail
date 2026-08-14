package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
