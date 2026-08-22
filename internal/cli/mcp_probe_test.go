package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/mcpserver"
)

func TestMCPProbeCLIForwardsIndependentPhaseTimeoutsWithoutAnOuterDeadline(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	err := runMCPProbe(context.Background(), []string{
		"--root", "/generic/project",
		"--runtime", "/verified/dagrail",
		"--handshake-timeout", "7s",
		"--project-timeout", "45s",
	}, &stdout, &stderr, func(ctx context.Context, executable, root string, options mcpserver.ProbeOptions) (mcpserver.ProbeReport, error) {
		called = true
		if _, hasDeadline := ctx.Deadline(); hasDeadline {
			t.Fatal("CLI wrapped both probe phases in one outer deadline")
		}
		if executable != "/verified/dagrail" || root != "/generic/project" || options.HandshakeTimeout != 7*time.Second || options.ProjectRoundTripTimeout != 45*time.Second {
			t.Fatalf("probe arguments drifted: executable=%q root=%q options=%#v", executable, root, options)
		}
		return mcpserver.ProbeReport{APIVersion: "dagrail.io/mcp-probe/v1alpha1", Kind: "MCPProbe", ServerHandshakeReady: true, ProjectRoundTripReady: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || stderr.Len() != 0 {
		t.Fatalf("probe runner was not called cleanly: called=%v stderr=%q", called, stderr.String())
	}
	var report mcpserver.ProbeReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || !report.ServerHandshakeReady || !report.ProjectRoundTripReady {
		t.Fatalf("probe report was not emitted: report=%#v err=%v output=%s", report, err, stdout.String())
	}
}
