package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type delayedProbeExecutor struct {
	delay time.Duration
}

func (executor delayedProbeExecutor) Execute(ctx context.Context, _ []string, _ []byte) ([]byte, []byte, error) {
	timer := time.NewTimer(executor.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-timer.C:
	}
	raw, err := json.Marshal(service.PreWaitAudit{SafeToWait: true})
	return raw, nil, err
}

func TestProbeUsesIndependentHandshakeAndProjectRoundTripDeadlines(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := NewRemote(delayedProbeExecutor{delay: 200 * time.Millisecond}, "/generic/project")
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "probe-deadline-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}

	report, err := probeWithConnector(ctx, "/generic/project", ProbeOptions{
		HandshakeTimeout:        100 * time.Millisecond,
		ProjectRoundTripTimeout: time.Second,
	}, func(context.Context) (*mcp.ClientSession, error) {
		return session, nil
	})
	if err != nil {
		t.Fatalf("project round trip inherited the shorter handshake deadline: %v", err)
	}
	if !report.ServerHandshakeReady || !report.ProjectRoundTripReady || report.ProjectRoundTripDurationMillis < 150 {
		t.Fatalf("probe report did not preserve both phases: %#v", report)
	}
	cancel()
	<-serverDone
}

func TestProbeProjectRoundTripRetainsItsOwnBoundedTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := NewRemote(delayedProbeExecutor{delay: 200 * time.Millisecond}, "/generic/project")
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "probe-timeout-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = probeWithConnector(ctx, "/generic/project", ProbeOptions{
		HandshakeTimeout:        time.Second,
		ProjectRoundTripTimeout: 20 * time.Millisecond,
	}, func(context.Context) (*mcp.ClientSession, error) {
		return session, nil
	})
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("project timeout was not enforced: %v", err)
	}
	cancel()
	<-serverDone
}
