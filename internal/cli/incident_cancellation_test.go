package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/cli"
	"github.com/CongBao/dagrail/internal/service"
	"github.com/gofrs/flock"
)

const incidentSignalHelperEnv = "DAGRAIL_TEST_INCIDENT_SIGNAL_ARGS"

type lockWaitReportingContext struct {
	context.Context
	once sync.Once
}

func (ctx *lockWaitReportingContext) Done() <-chan struct{} {
	ctx.once.Do(func() { _, _ = fmt.Fprintln(os.Stdout, "lock-wait-ready") })
	return ctx.Context.Done()
}

func TestIncidentCLISignalHelper(t *testing.T) {
	raw := os.Getenv(incidentSignalHelperEnv)
	if raw == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		os.Exit(125)
	}
	base, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	ctx := &lockWaitReportingContext{Context: base}
	err := cli.RunContext(ctx, args, strings.NewReader(""), os.Stdout, os.Stderr)
	cancel()
	if err == nil {
		os.Exit(0)
	}
	_ = cli.WriteErrorJSON(os.Stderr, err)
	os.Exit(cli.DescribeError(err).ExitCode)
}

func TestIncidentCLIInterruptCancelsCrossProcessEffectLockWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt delivery is not portable on Windows")
	}
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	svc, err := service.Init(root, "incident-cancellation")
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"incident-cancellation"},"spec":{"roles":[{"id":"worker","capabilities":["effect.apply","effect.reconcile","incident.manage"]}],"nodes":[{"id":"effect","kind":"effect","role":"worker","title":"effect","inputs":{"adapter":"manual","request":true},"outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "graph/import", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindRole("worker", "codex", "session", time.Hour, false, "bind"); err != nil {
		t.Fatal(err)
	}
	actionRef := func(kind string) string {
		actions, listErr := svc.ListActions("worker", "effect")
		if listErr != nil {
			t.Fatal(listErr)
		}
		for _, action := range actions.Actions {
			if action.Kind == kind {
				return action.Ref
			}
		}
		t.Fatalf("action %s is unavailable", kind)
		return ""
	}
	if _, err := svc.ApplyAction(actionRef("node.start"), json.RawMessage(`{}`), "start"); err != nil {
		t.Fatal(err)
	}
	prepared, err := svc.ApplyAction(actionRef("effect.prepare"), json.RawMessage(`{}`), "prepare")
	if err != nil || prepared.Status != "unknown" {
		t.Fatalf("manual Effect did not create an ambiguous Incident: %+v %v", prepared, err)
	}
	incidentID := "effect:" + prepared.ActionID
	before, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256([]byte(prepared.ActionID))
	lockPath := filepath.Join(svc.Project.DataDir, "effect-locks", hex.EncodeToString(sum[:])+".lock")
	externalLock := flock.New(lockPath)
	locked, err := externalLock.TryLock()
	if err != nil || !locked {
		t.Fatalf("hold cross-process Effect lock: locked=%v err=%v", locked, err)
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			_ = externalLock.Unlock()
		}
	}()

	args := []string{"--errors=json", "incident", "trip", "--root", root, "--incident", incidentID, "--actor-role", "worker", "--reason", "operator", "--idempotency-key", "trip-after-cancel"}
	helperArgs, _ := json.Marshal(args)
	command := exec.Command(os.Args[0], "-test.run=^TestIncidentCLISignalHelper$")
	command.Env = append(os.Environ(), incidentSignalHelperEnv+"="+string(helperArgs))
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	finished := false
	defer func() {
		if !finished {
			_ = command.Process.Kill()
			<-waited
		}
	}()
	ready := make(chan struct {
		line string
		err  error
	}, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		ready <- struct {
			line string
			err  error
		}{line: line, err: readErr}
	}()
	select {
	case result := <-ready:
		if result.err != nil || strings.TrimSpace(result.line) != "lock-wait-ready" {
			t.Fatalf("CLI did not report entry into the Effect lock wait: line=%q err=%v stderr=%s", result.line, result.err, stderr.String())
		}
	case waitErr := <-waited:
		finished = true
		t.Fatalf("CLI exited before reaching the held Effect lock: %v %s", waitErr, stderr.String())
	case <-time.After(5 * time.Second):
		t.Fatal("CLI did not reach the Effect lock wait")
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case waitErr := <-waited:
		finished = true
		exitErr, ok := waitErr.(*exec.ExitError)
		var report cli.ErrorReport
		decodeErr := json.Unmarshal(stderr.Bytes(), &report)
		if !ok || exitErr.ExitCode() != 130 || decodeErr != nil || report.Code != "interrupted" || report.ExitCode != 130 {
			t.Fatalf("CLI was killed by the signal instead of returning its interrupted result: err=%v stderr=%s", waitErr, stderr.String())
		}
	case <-time.After(3 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("CLI ignored interrupt while waiting for the Effect lock")
	}
	afterCancel, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if afterCancel.HeadHash != before.HeadHash || afterCancel.HeadSequence != before.HeadSequence {
		t.Fatalf("cancelled Incident command committed authority: before=%d/%s after=%d/%s", before.HeadSequence, before.HeadHash, afterCancel.HeadSequence, afterCancel.HeadHash)
	}
	if err := externalLock.Unlock(); err != nil {
		t.Fatal(err)
	}
	lockHeld = false

	var retryOutput bytes.Buffer
	if err := cli.Run(args, strings.NewReader(""), &retryOutput, &bytes.Buffer{}); err != nil || !strings.Contains(retryOutput.String(), `"status":"circuit-open"`) {
		t.Fatalf("same-key retry did not commit after unlock: %v %s", err, retryOutput.String())
	}
	afterRetry, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if afterRetry.HeadSequence != before.HeadSequence+1 || afterRetry.Incidents[incidentID].CircuitReason != "operator" {
		t.Fatalf("same-key retry did not commit exactly once: before=%d after=%d incident=%+v", before.HeadSequence, afterRetry.HeadSequence, afterRetry.Incidents[incidentID])
	}
	var idempotentOutput bytes.Buffer
	if err := cli.Run(args, strings.NewReader(""), &idempotentOutput, &bytes.Buffer{}); err != nil || !strings.Contains(idempotentOutput.String(), `"status":"circuit-open"`) {
		t.Fatalf("idempotent CLI retry failed: %v %s", err, idempotentOutput.String())
	}
	afterIdempotent, err := svc.State()
	if err != nil || afterIdempotent.HeadSequence != afterRetry.HeadSequence {
		t.Fatalf("idempotent retry appended another command: before=%d after=%d err=%v", afterRetry.HeadSequence, afterIdempotent.HeadSequence, err)
	}
}
