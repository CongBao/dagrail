package harness_test

import (
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/harness"
)

func TestAllFirstPartyHarnessesProduceExplicitNonConfirmingFallbackEnvelopes(t *testing.T) {
	for _, id := range []string{"codex", "claude-code", "copilot-cli"} {
		adapter, err := harness.New(id)
		if err != nil {
			t.Fatal(err)
		}
		envelope := adapter.Envelope("/project", "developer", "node-A", "")
		if envelope.RecipientVisible || envelope.Command == "" || !strings.Contains(envelope.Prompt, "node-A") || !strings.Contains(envelope.Prompt, "$execute-dag-node") {
			t.Fatalf("unsafe or incomplete %s envelope: %#v", id, envelope)
		}
	}
}
