package mcpserver

import (
	"io"
	"strings"
	"testing"
)

func TestBoundedNDJSONReaderRejectsOversizedMessageBeforeTheNextFrame(t *testing.T) {
	reader := newBoundedNDJSONReader(strings.NewReader(strings.Repeat("x", 65)+"\n{}\n"), 64)
	if _, err := io.ReadAll(reader); err == nil || !strings.Contains(err.Error(), "exceeds 64 bytes") {
		t.Fatalf("oversized MCP frame was accepted: %v", err)
	}

	reader = newBoundedNDJSONReader(strings.NewReader("{}\n[]\n"), 64)
	raw, err := io.ReadAll(reader)
	if err != nil || string(raw) != "{}\n[]\n" {
		t.Fatalf("bounded MCP frames were corrupted: %q %v", raw, err)
	}
}
