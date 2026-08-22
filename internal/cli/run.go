package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/CongBao/dagrail/internal/commandcatalog"
	"github.com/CongBao/dagrail/internal/contract"
	"github.com/CongBao/dagrail/internal/controller"
	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/gitartifact"
	"github.com/CongBao/dagrail/internal/harness"
	"github.com/CongBao/dagrail/internal/hook"
	"github.com/CongBao/dagrail/internal/install"
	"github.com/CongBao/dagrail/internal/mcpserver"
	"github.com/CongBao/dagrail/internal/observe"
	internalproviders "github.com/CongBao/dagrail/internal/providers"
	"github.com/CongBao/dagrail/internal/qualification"
	"github.com/CongBao/dagrail/internal/readiness"
	dagrelease "github.com/CongBao/dagrail/internal/release"
	"github.com/CongBao/dagrail/internal/service"
	"github.com/CongBao/dagrail/internal/signing"
	dagrailui "github.com/CongBao/dagrail/internal/ui"
	"github.com/CongBao/dagrail/internal/version"
)

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return RunContext(context.Background(), args, stdin, stdout, stderr)
}

func RunContext(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (runErr error) {
	defer func() { runErr = normalizeDispatchError(runErr) }()
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-version") {
		return writeJSON(stdout, map[string]string{"version": version.Version, "commit": version.Commit, "date": version.Date})
	}
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		_, err := io.WriteString(stdout, topLevelHelp())
		return err
	}
	if WantsJSONErrors(args) {
		stderr = io.Discard
	}
	var err error
	args, err = consumeGlobalArgs(args)
	if err != nil {
		return err
	}
	args, offline := stripOffline(args)
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(args) == 0 {
		return usagef("a command is required")
	}
	if !commandcatalog.Contains(args[0]) {
		return usagef("unknown command %q", args[0])
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h" || args[1] == "help") {
		_, err := fmt.Fprintf(stdout, "Usage: dagrail %s [flags]\n\nRun 'dagrail commands' for the closed machine-readable command and subcommand catalog.\n", args[0])
		return err
	}
	if offline {
		if !offlineRecoveryCommand(args) {
			return usagef("offline mode is restricted to explicit recovery, restore, replay, rebuild, and authority verification commands")
		}
		client, clientErr := controller.NewClient()
		if clientErr != nil {
			return clientErr
		}
		probeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		_, statusErr := client.Status(probeCtx)
		cancel()
		if statusErr == nil {
			return fmt.Errorf("offline mode requires the DAGrail daemon to be stopped; run 'dagrail daemon stop' and retry")
		}
	}
	if !offline && shouldProxyToController(args) {
		client, clientErr := controller.NewClient()
		if clientErr != nil {
			return clientErr
		}
		remoteOut, remoteErr, executeErr := client.Execute(ctx, args, nil)
		if len(remoteOut) > 0 {
			_, _ = stdout.Write(remoteOut)
		}
		if len(remoteErr) > 0 {
			_, _ = stderr.Write(remoteErr)
		}
		return executeErr
	}
	return runDirectContext(ctx, args, stdin, stdout, stderr)
}

func runDirectContext(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	switch args[0] {
	case "artifact":
		return runArtifact(ctx, args[1:], stdout, stderr)
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "graph":
		return runGraph(ctx, args[1:], stdout, stderr)
	case "frontier":
		return runFrontier(ctx, args[1:], stdout, stderr)
	case "status":
		return runStatus(ctx, args[1:], stdout, stderr)
	case "history":
		return runHistory(ctx, args[1:], stdout, stderr)
	case "backup":
		return runBackup(args[1:], stdout, stderr)
	case "role":
		return runRole(args[1:], stdout, stderr)
	case "action":
		return runAction(ctx, args[1:], stdout, stderr)
	case "context":
		return runContext(ctx, args[1:], stdout, stderr)
	case "commands":
		if len(args) != 1 {
			return usagef("usage: dagrail commands")
		}
		return writeJSON(stdout, commandcatalog.Current(version.Version))
	case "completion":
		if len(args) != 2 {
			return usagef("usage: dagrail completion <bash|zsh|fish|powershell>")
		}
		result, completionErr := commandcatalog.Completion(args[1])
		if completionErr != nil {
			return usagef("%v", completionErr)
		}
		_, completionErr = io.WriteString(stdout, result)
		return completionErr
	case "contract":
		if len(args) != 1 {
			return usagef("usage: dagrail contract")
		}
		return writeJSON(stdout, contract.Current())
	case "inspect":
		return runInspect(ctx, args[1:], stdout, stderr)
	case "pre-wait":
		return runPreWait(ctx, args[1:], stdout, stderr)
	case "reconcile":
		return runReconcile(ctx, args[1:], stdout, stderr)
	case "recovery":
		return runRecovery(args[1:], stdout, stderr)
	case "mcp":
		return runMCP(ctx, args[1:], stdout, stderr)
	case "observe":
		return runObserve(args[1:], stdout, stderr)
	case "ui":
		return runUI(ctx, args[1:], stdout, stderr)
	case "hook":
		return runHook(args[1:], stdin, stdout, stderr)
	case "harness":
		return runHarness(args[1:], stdout, stderr)
	case "plugin":
		return runPlugin(ctx, args[1:], stdout, stderr)
	case "provider":
		return runProvider(ctx, args[1:], stdout, stderr)
	case "signature":
		return runSignature(args[1:], stdout, stderr)
	case "journal":
		return runJournal(ctx, args[1:], stdout, stderr)
	case "lifecycle":
		return runLifecycle(args[1:], stdout, stderr)
	case "evidence":
		return runEvidence(ctx, args[1:], stdout, stderr)
	case "incident":
		return runIncident(ctx, args[1:], stdout, stderr)
	case "projection":
		return runProjection(ctx, args[1:], stdout, stderr)
	case "qualify":
		return runQualify(args[1:], stdout, stderr)
	case "readiness":
		return runReadiness(ctx, args[1:], stdout, stderr)
	case "release":
		return runRelease(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(ctx, args[1:], stdout, stderr)
	case "daemon":
		return runDaemon(ctx, args[1:], stdout, stderr)
	case "security":
		return runSecurity(args[1:], stdout, stderr)
	case "support":
		return runSupport(args[1:], stdout, stderr)
	case "version":
		if len(args) != 1 {
			return usagef("usage: dagrail version")
		}
		return writeJSON(stdout, map[string]string{"version": version.Version, "commit": version.Commit, "date": version.Date})
	default:
		return usagef("unknown command %q", args[0])
	}
}

func topLevelHelp() string {
	return "DAGrail — LLM-led local DAG governance\n\nUsage:\n  dagrail [--errors=json] [--offline] <command> [flags]\n\nCore commands:\n  init, graph, frontier, role, action, context, inspect, pre-wait\n  reconcile, journal, projection, backup, daemon, mcp, ui, plugin\n\nRun 'dagrail commands' for the machine-readable catalog or '<command> --help' for flags.\n"
}

func stripOffline(args []string) ([]string, bool) {
	result := make([]string, 0, len(args))
	offline := false
	for _, arg := range args {
		if arg == "--offline" {
			offline = true
			continue
		}
		result = append(result, arg)
	}
	return result, offline
}

func offlineRecoveryCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	subcommand := ""
	if len(args) > 1 {
		subcommand = args[1]
	}
	switch args[0] {
	case "recovery":
		return true
	case "backup":
		return subcommand == "restore" || subcommand == "verify"
	case "journal":
		return subcommand == "verify" || subcommand == "replay"
	case "projection":
		return subcommand == "rebuild"
	case "doctor", "security":
		return true
	default:
		return false
	}
}

func shouldProxyToController(args []string) bool {
	if len(args) == 0 || os.Getenv("DAGRAIL_DAEMON_DISABLE") == "1" || os.Getenv("DAGRAIL_DAEMON_CHILD") == "1" {
		return false
	}
	for _, arg := range args[1:] {
		if arg == "--help" || arg == "-h" || arg == "help" {
			return false
		}
	}
	switch args[0] {
	case "daemon", "mcp", "ui", "plugin", "commands", "completion", "contract", "version", "artifact", "signature", "release", "qualify", "readiness", "observe", "hook", "harness":
		return false
	default:
		return true
	}
}

func runDaemon(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usagef("usage: dagrail daemon <start|status|stop|restart|warm|logs>")
	}
	client, err := controller.NewClient()
	if err != nil {
		return err
	}
	switch args[0] {
	case "serve":
		if os.Getenv("DAGRAIL_DAEMON_CHILD") != "1" {
			return fmt.Errorf("daemon serve is an internal entry point")
		}
		endpoint, err := controller.Endpoint()
		if err != nil {
			return err
		}
		service.EnableProcessSnapshotCache()
		server := controller.NewServer(func(callCtx context.Context, callArgs []string, input io.Reader, output, errors io.Writer) error {
			if len(callArgs) > 0 && callArgs[0] == "__daemon_warm" {
				flags := flag.NewFlagSet("daemon warm internal", flag.ContinueOnError)
				flags.SetOutput(errors)
				root := flags.String("root", "", "project root")
				if err := flags.Parse(callArgs[1:]); err != nil || *root == "" {
					return fmt.Errorf("daemon warm requires an absolute project root")
				}
				svc, err := service.Open(*root)
				if err != nil {
					return err
				}
				state, err := svc.State()
				if err != nil {
					return err
				}
				return writeJSON(output, map[string]any{"warmed": true, "projectId": state.ProjectID, "headSequence": state.HeadSequence, "headHash": state.HeadHash, "graphRevision": state.GraphRevision})
			}
			return runDirectContext(callCtx, callArgs, input, output, errors)
		}, endpoint)
		uiManager := &managedUI{}
		server.SetUIManager(uiManager.Start, uiManager.Status, uiManager.Stop)
		return server.Serve(ctx)
	case "start":
		status, err := client.Ensure(ctx)
		if err != nil {
			return err
		}
		return writeJSON(stdout, status)
	case "status":
		statusCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		status, err := client.Status(statusCtx)
		if err != nil {
			return writeJSON(stdout, map[string]any{"apiVersion": controller.APIVersion, "kind": "ControllerStatus", "running": false})
		}
		return writeJSON(stdout, status)
	case "stop":
		statusCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		_, statusErr := client.Status(statusCtx)
		cancel()
		if statusErr != nil {
			return writeJSON(stdout, map[string]any{"stopped": true, "alreadyStopped": true})
		}
		if err := client.Stop(ctx); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"stopped": true, "draining": true})
	case "restart":
		statusCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		_, statusErr := client.Status(statusCtx)
		cancel()
		if statusErr == nil {
			if err := client.Restart(ctx); err != nil {
				return err
			}
			time.Sleep(100 * time.Millisecond)
		}
		status, err := client.Ensure(ctx)
		if err != nil {
			return err
		}
		return writeJSON(stdout, status)
	case "warm":
		flags := flag.NewFlagSet("daemon warm", flag.ContinueOnError)
		flags.SetOutput(stderr)
		root := flags.String("root", ".", "project root")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		absoluteRoot, err := filepath.Abs(*root)
		if err != nil {
			return err
		}
		out, remoteErr, err := client.Execute(ctx, []string{"__daemon_warm", "--root", absoluteRoot}, nil)
		if len(remoteErr) > 0 {
			_, _ = stderr.Write(remoteErr)
		}
		if err != nil {
			return err
		}
		_, err = stdout.Write(out)
		return err
	case "logs":
		logs, err := client.Logs()
		if err != nil {
			return err
		}
		_, err = stdout.Write(logs)
		return err
	default:
		return usagef("unknown daemon command %q", args[0])
	}
}

func runArtifact(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usagef("usage: dagrail artifact <verify-git-closure|inspect-scope>")
	}
	flags := flag.NewFlagSet("artifact "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	repository := flags.String("repo", ".", "consumer Git repository")
	manifestPath := flags.String("file", "", "Git artifact closure manifest")
	base := flags.String("base", "", "exact producer base commit")
	candidate := flags.String("candidate", "", "exact producer candidate commit")
	target := flags.String("target", "", "exact accepted target commit")
	prospective := flags.String("prospective", "", "exact prospective integration commit")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usagef("usage: dagrail artifact %s [flags]", args[0])
	}
	switch args[0] {
	case "verify-git-closure":
		if *manifestPath == "" {
			return usagef("--file is required")
		}
		manifest, err := gitartifact.DecodeManifestContext(ctx, *manifestPath)
		if err != nil {
			return err
		}
		report, err := gitartifact.VerifyContext(ctx, *repository, manifest)
		if err != nil {
			return err
		}
		if err := writeJSON(stdout, report); err != nil {
			return err
		}
		if !report.Valid {
			return diagnosticError(fmt.Errorf("git artifact closure is not valid"))
		}
		return nil
	case "inspect-scope":
		if *base == "" || *candidate == "" || *target == "" || *prospective == "" {
			return usagef("--base, --candidate, --target and --prospective are required")
		}
		report, err := gitartifact.InspectScopeContext(ctx, *repository, *base, *candidate, *target, *prospective)
		if err != nil {
			return err
		}
		if err := writeJSON(stdout, report); err != nil {
			return err
		}
		if !report.Closed {
			return diagnosticError(fmt.Errorf("git integration scope contains unexplained prospective deltas"))
		}
		return nil
	default:
		return usagef("unknown artifact command %q", args[0])
	}
}

func runLifecycle(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usagef("usage: dagrail lifecycle <validate-history|import-history|projection>")
	}
	flags := flag.NewFlagSet("lifecycle "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	filePath := flags.String("file", "", "lifecycle migration manifest")
	trustedAuthority := flags.String("source-authority-hash", "", "trusted out-of-band SHA-256 digest of the source authority")
	actorRole := flags.String("actor-role", "", "migration actor role")
	idempotencyKey := flags.String("idempotency-key", "", "stable migration idempotency key")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usagef("usage: dagrail lifecycle %s [flags]", args[0])
	}
	switch args[0] {
	case "projection":
		s, err := service.OpenForInspection(*root)
		if err != nil {
			return err
		}
		report, err := s.LifecycleProjection()
		if err != nil {
			return err
		}
		return writeJSON(stdout, report)
	case "validate-history", "import-history":
		if *filePath == "" || *trustedAuthority == "" {
			return usagef("--file and --source-authority-hash are required")
		}
		manifest, err := service.DecodeLifecycleMigrationFile(*filePath)
		if err != nil {
			return err
		}
		if args[0] == "validate-history" {
			s, err := service.OpenForInspection(*root)
			if err != nil {
				return err
			}
			report, err := s.ValidateLifecycleMigration(manifest, *trustedAuthority)
			if err != nil {
				return err
			}
			return writeJSON(stdout, report)
		}
		s, err := service.OpenForMigration(*root)
		if err != nil {
			return err
		}
		receipt, err := s.ImportLifecycleHistory(manifest, *trustedAuthority, *actorRole, *idempotencyKey)
		if err != nil {
			return err
		}
		return writeJSON(stdout, receipt)
	default:
		return usagef("unknown lifecycle command %q", args[0])
	}
}

type repeatedFlag []string

func (value *repeatedFlag) String() string { return strings.Join(*value, ",") }
func (value *repeatedFlag) Set(item string) error {
	if strings.TrimSpace(item) == "" {
		return fmt.Errorf("flag value cannot be empty")
	}
	*value = append(*value, item)
	return nil
}

func runObserve(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail observe <assess|create-shadow|verify-shadow>")
	}
	flags := flag.NewFlagSet("observe "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourceRoot := flags.String("source-root", "", "existing source project root (read-only)")
	graphPath := flags.String("graph", "", "converted DAGrail Graph Definition")
	shadowRoot := flags.String("shadow-root", "", "separate DAGrail shadow project root")
	var authorities repeatedFlag
	flags.Var(&authorities, "authority", "source-relative authority file (repeatable)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "assess":
		report, err := observe.Assess(*sourceRoot, *graphPath, authorities)
		if err != nil {
			return err
		}
		return writeJSON(stdout, report)
	case "create-shadow":
		report, err := observe.CreateShadow(*sourceRoot, *graphPath, *shadowRoot, authorities)
		if err != nil {
			return err
		}
		return writeJSON(stdout, report)
	case "verify-shadow":
		report, err := observe.VerifyShadow(*shadowRoot)
		if err != nil {
			return err
		}
		return writeJSON(stdout, report)
	default:
		return fmt.Errorf("unknown observe command %q", args[0])
	}
}

func runSignature(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail signature <keygen|sign|verify>")
	}
	flags := flag.NewFlagSet("signature "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	filePath := flags.String("file", "", "payload file")
	privateKey := flags.String("private-key", "", "PKCS#8 Ed25519 private-key PEM")
	publicKey := flags.String("public-key", "", "PKIX Ed25519 public-key PEM")
	output := flags.String("output", "", "new detached signature file")
	signaturePath := flags.String("signature", "", "detached signature file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	var report signing.Report
	var err error
	switch args[0] {
	case "keygen":
		report, err = signing.GenerateKeyPair(*privateKey, *publicKey)
	case "sign":
		report, err = signing.SignFile(*filePath, *privateKey, *output)
	case "verify":
		report, err = signing.VerifyFile(*filePath, *signaturePath, *publicKey)
	default:
		return fmt.Errorf("unknown signature command %q", args[0])
	}
	if err != nil {
		return err
	}
	return writeJSON(stdout, report)
}

func runUI(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	operation := "start"
	if len(args) > 0 && (args[0] == "status" || args[0] == "stop") {
		operation, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet("ui", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	listen := flags.String("listen", "127.0.0.1:0", "loopback listen address")
	open := flags.Bool("open", true, "open the system browser")
	if err := flags.Parse(args); err != nil {
		return err
	}
	client, err := controller.NewClient()
	if err != nil {
		return err
	}
	switch operation {
	case "status":
		status, err := client.UIStatus(ctx)
		if err != nil {
			return err
		}
		return writeJSON(stdout, status)
	case "stop":
		status, err := client.StopUI(ctx)
		if err != nil {
			return err
		}
		return writeJSON(stdout, status)
	default:
		status, err := client.StartUI(ctx, controller.UIRequest{Root: *root, Listen: *listen, Open: *open})
		if err != nil {
			return err
		}
		return writeJSON(stdout, status)
	}
}

type managedUI struct {
	mu        sync.Mutex
	instance  *dagrailui.Instance
	root      string
	startedAt string
}

func (manager *managedUI) Start(_ context.Context, request controller.UIRequest) (controller.UIStatus, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	svc, err := service.OpenForInspection(request.Root)
	if err != nil {
		return controller.UIStatus{}, err
	}
	if manager.instance != nil {
		if manager.root == svc.Project.Root {
			return manager.statusLocked(), nil
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := manager.instance.Stop(stopCtx); err != nil {
			cancel()
			return controller.UIStatus{}, err
		}
		cancel()
		manager.instance = nil
	}
	instance, err := dagrailui.Start(svc, request.Listen, request.Open)
	if err != nil {
		return controller.UIStatus{}, err
	}
	manager.instance, manager.root = instance, svc.Project.Root
	manager.startedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return manager.statusLocked(), nil
}

func (manager *managedUI) Status() controller.UIStatus {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.statusLocked()
}

func (manager *managedUI) statusLocked() controller.UIStatus {
	status := controller.UIStatus{APIVersion: controller.APIVersion, Kind: "UIStatus", Running: manager.instance != nil}
	if manager.instance != nil {
		status.Root, status.URL, status.StartedAt = manager.root, manager.instance.URL(), manager.startedAt
	}
	return status
}

func (manager *managedUI) Stop(ctx context.Context) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.instance == nil {
		return nil
	}
	if err := manager.instance.Stop(ctx); err != nil {
		return err
	}
	manager.instance, manager.root, manager.startedAt = nil, "", ""
	return nil
}

func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	s, err := service.OpenForInspection(*root)
	if err != nil {
		return err
	}
	status, err := s.BoundedStatusContext(ctx)
	if err != nil {
		return err
	}
	return writeJSON(stdout, status)
}

func runHistory(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("history", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	after := flags.Uint64("after", 0, "exclusive journal cursor")
	limit := flags.Int("limit", 25, "entries, 1..100")
	if err := flags.Parse(args); err != nil {
		return err
	}
	s, err := service.OpenForInspection(*root)
	if err != nil {
		return err
	}
	history, err := s.BoundedHistoryContext(ctx, *after, *limit)
	if err != nil {
		return err
	}
	return writeJSON(stdout, history)
}

func runBackup(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail backup <create|verify|restore>")
	}
	flags := flag.NewFlagSet("backup "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	filePath := flags.String("file", "", "backup file")
	output := flags.String("output", "", "new backup file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	open := service.OpenForInspection
	if args[0] == "restore" {
		open = service.Open
	}
	s, err := open(*root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "create":
		if *output == "" {
			return fmt.Errorf("--output is required")
		}
		data, report, err := s.CreateBackup()
		if err != nil {
			return err
		}
		file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"created": true, "output": *output, "report": report})
	case "verify", "restore":
		if *filePath == "" {
			return fmt.Errorf("--file is required")
		}
		info, err := os.Stat(*filePath)
		if err != nil {
			return err
		}
		if info.Size() > 256*1024*1024 {
			return fmt.Errorf("backup exceeds 256 MiB limit")
		}
		data, err := os.ReadFile(*filePath)
		if err != nil {
			return err
		}
		if args[0] == "verify" {
			report, err := s.VerifyBackup(data)
			if err != nil {
				return err
			}
			return writeJSON(stdout, report)
		}
		report, err := s.RestoreBackup(data)
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"restored": true, "report": report})
	default:
		return fmt.Errorf("unknown backup command %q", args[0])
	}
}

func runIncident(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail incident <progress|trip|disposition|resolve|control-resolve|supersede>")
	}
	flags := flag.NewFlagSet("incident "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	id := flags.String("incident", "", "incident ID")
	incidentRef := flags.String("incident-ref", "", "opaque Incident ref")
	role := flags.String("actor-role", "", "acting Role")
	roleRef := flags.String("actor-role-ref", "", "opaque acting Role ref")
	key := flags.String("idempotency-key", "", "idempotency key")
	note := flags.String("note", "", "bounded progress note")
	madeProgress := flags.Bool("made-progress", false, "reset consecutive no-progress attempts")
	reason := flags.String("reason", "", "circuit-breaker reason")
	resolution := flags.String("resolution", "", "resolution summary")
	disposition := flags.String("disposition", "", "retry, rollback, lkg, quarantine, off-critical-path, or escalate")
	successorNode := flags.String("successor-node", "", "declared repair successor node")
	successorNodeRef := flags.String("successor-node-ref", "", "opaque repair successor Node ref")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	s, err := service.Open(*root)
	if err != nil {
		return err
	}
	if *incidentRef != "" {
		if *id != "" {
			return fmt.Errorf("provide --incident or --incident-ref, not both")
		}
		*id, err = s.ResolveEntityRef("incident", *incidentRef)
		if err != nil {
			return err
		}
	}
	if *roleRef != "" {
		if *role != "" {
			return fmt.Errorf("provide --actor-role or --actor-role-ref, not both")
		}
		*role, err = s.ResolveEntityRef("role", *roleRef)
		if err != nil {
			return err
		}
	}
	if *successorNodeRef != "" {
		if *successorNode != "" {
			return fmt.Errorf("provide --successor-node or --successor-node-ref, not both")
		}
		*successorNode, err = s.ResolveEntityRef("node", *successorNodeRef)
		if err != nil {
			return err
		}
	}
	var incident any
	switch args[0] {
	case "progress":
		incident, err = s.ProgressIncidentContext(ctx, *id, *role, *note, *madeProgress, *key)
	case "trip":
		incident, err = s.TripIncidentContext(ctx, *id, *role, *reason, *key)
	case "resolve":
		incident, err = s.ResolveIncidentContext(ctx, *id, *role, *resolution, *key)
	case "control-resolve":
		incident, err = s.ControlResolveIncidentContext(ctx, *id, *role, *disposition, *resolution, *note, *key)
	case "disposition":
		incident, err = s.SetIncidentDispositionContext(ctx, *id, *role, *disposition, *note, *key)
	case "supersede":
		incident, err = s.SupersedeIncidentContext(ctx, *id, *successorNode, *role, *note, *key)
	default:
		return fmt.Errorf("unknown incident command %q", args[0])
	}
	if err != nil {
		return err
	}
	if raw, _ := json.Marshal(incident); len(raw) > 24*1024 {
		ref, refErr := s.EntityRef("incident", *id)
		if refErr != nil {
			return refErr
		}
		value, inspectErr := s.Inspect(ref)
		if inspectErr != nil {
			return inspectErr
		}
		return writeJSON(stdout, value)
	}
	return writeJSON(stdout, incident)
}

func runProvider(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail provider <list|check|invoke>")
	}
	flags := flag.NewFlagSet("provider "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	kind := flags.String("kind", "", "predicate, policy, graph-importer, or projection")
	id := flags.String("id", "", "provider ID")
	input := flags.String("input", "{}", "provider invocation JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	open := service.OpenForInspection
	if args[0] == "invoke" {
		open = service.Open
	}
	s, err := open(*root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		return writeJSON(stdout, map[string]any{"providers": s.ProviderInventory()})
	case "check":
		report := s.CheckProviders()
		if err := writeJSON(stdout, report); err != nil {
			return err
		}
		if !report.Healthy {
			return fmt.Errorf("provider conformance checks failed")
		}
		return nil
	case "invoke":
		if *kind == "" || *id == "" {
			return fmt.Errorf("--kind and --id are required")
		}
		if !validBoundedJSON(*input, 64*1024) {
			return fmt.Errorf("--input must be valid JSON no larger than 64 KiB")
		}
		result, err := s.InvokeProvider(ctx, internalproviders.Invocation{Kind: *kind, ProviderID: *id, Input: json.RawMessage(*input)})
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	default:
		return fmt.Errorf("unknown provider command %q", args[0])
	}
}

func runEvidence(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "list" {
		return fmt.Errorf("usage: dagrail evidence list [--node ID] [--attempt ID]")
	}
	flags := flag.NewFlagSet("evidence list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	nodeID := flags.String("node", "", "filter by node ID")
	nodeRef := flags.String("node-ref", "", "opaque Node ref")
	attemptID := flags.String("attempt", "", "filter by attempt ID")
	attemptRef := flags.String("attempt-ref", "", "opaque Attempt ref")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	s, err := service.OpenForInspection(*root)
	if err != nil {
		return err
	}
	if *nodeRef != "" {
		if *nodeID != "" {
			return fmt.Errorf("provide --node or --node-ref, not both")
		}
		*nodeID, err = s.ResolveEntityRef("node", *nodeRef)
		if err != nil {
			return err
		}
	}
	if *attemptRef != "" {
		if *attemptID != "" {
			return fmt.Errorf("provide --attempt or --attempt-ref, not both")
		}
		*attemptID, err = s.ResolveEntityRef("attempt", *attemptRef)
		if err != nil {
			return err
		}
	}
	result, err := s.BoundedEvidenceContext(ctx, *nodeID, *attemptID)
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func runPlugin(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail plugin <install|update|status|conformance|materialize|bundle-status|runtime-status|rollback|uninstall>")
	}
	flags := flag.NewFlagSet("plugin "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	harnesses := flags.String("harness", "codex,claude-code,copilot-cli", "comma-separated harnesses")
	source := flags.String("marketplace-source", "", "local path or Git marketplace source (default: linked offline bundle)")
	runtimePath := flags.String("runtime", "", "absolute DAGrail runtime path")
	dryRun := flags.Bool("dry-run", false, "show or validate without changing host configuration")
	updateCheck := flags.Bool("check", false, "check whether the installed runtime and bundle match this release")
	updateVersion := flags.String("version", "", "exact release version to install (this binary's version)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "materialize" {
		result, err := install.MaterializePluginBundle()
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	if args[0] == "bundle-status" {
		result, err := install.PluginBundleStatus()
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	if args[0] == "update" {
		if *updateCheck {
			runtimeStatus, runtimeErr := install.RuntimeStatus()
			bundleStatus, bundleErr := install.PluginBundleStatus()
			ready := runtimeErr == nil && bundleErr == nil && runtimeStatus.Version == version.Version && bundleStatus.Version == version.Version && bundleStatus.Status == "verified"
			return writeJSON(stdout, map[string]any{"apiVersion": "dagrail.io/plugin-update/v1alpha1", "currentVersion": version.Version, "installedVersion": runtimeStatus.Version, "updateRequired": !ready, "runtimeReady": runtimeErr == nil, "bundleReady": bundleErr == nil && bundleStatus.Status == "verified"})
		}
		requested := strings.TrimPrefix(*updateVersion, "v")
		if requested == "" {
			requested = version.Version
		}
		if requested != version.Version {
			return fmt.Errorf("this runtime can install only its linked version %s; run the requested release binary first", version.Version)
		}
		var runtimeResult install.RuntimeResult
		var bundle install.BundleResult
		var err error
		if *dryRun {
			runtimeResult, err = install.PlanRuntime()
			if err == nil {
				bundle, err = install.PlanPluginBundle(runtimeResult.RuntimePath)
			}
		} else {
			runtimeResult, err = install.InstallRuntime()
			if err == nil {
				bundle, err = install.MaterializePluginBundle()
			}
		}
		if err != nil {
			return err
		}
		options := install.Options{Harnesses: []string{*harnesses}, RuntimePath: runtimeResult.RuntimePath, MarketplaceSource: bundle.Root, DryRun: *dryRun}
		if *dryRun {
			plans, planErr := install.Plan(options)
			if writeErr := writeJSON(stdout, map[string]any{"runtime": runtimeResult, "bundle": bundle, "harnesses": plans}); writeErr != nil {
				return writeErr
			}
			return planErr
		}
		results, installErr := install.Install(ctx, options)
		if writeErr := writeJSON(stdout, map[string]any{"runtime": runtimeResult, "bundle": bundle, "harnesses": results}); writeErr != nil {
			return writeErr
		}
		return installErr
	}
	if *runtimePath == "" {
		if args[0] == "install" && !*dryRun {
			runtimeResult, err := install.InstallRuntime()
			if err != nil {
				return err
			}
			*runtimePath = runtimeResult.RuntimePath
		} else {
			resolved, err := install.DefaultRuntimePath()
			if err != nil {
				return err
			}
			*runtimePath = resolved
		}
	}
	if *source == "" {
		if args[0] == "install" || args[0] == "uninstall" {
			var bundle install.BundleResult
			var err error
			if *dryRun || args[0] == "uninstall" {
				bundle, err = install.PluginBundleStatus()
			} else {
				bundle, err = install.MaterializePluginBundle()
			}
			if err != nil {
				return err
			}
			*source = bundle.Root
		} else {
			*source = install.DefaultMarketplaceSource
		}
	}
	options := install.Options{Harnesses: []string{*harnesses}, RuntimePath: *runtimePath, MarketplaceSource: *source, DryRun: *dryRun}
	switch args[0] {
	case "install":
		results, err := install.Install(ctx, options)
		if writeErr := writeJSON(stdout, results); writeErr != nil {
			return writeErr
		}
		return err
	case "status":
		results, err := install.StatusContext(ctx, options)
		if err != nil {
			return err
		}
		return writeJSON(stdout, results)
	case "conformance":
		result, err := install.ConformanceContext(ctx, options)
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	case "runtime-status":
		result, err := install.RuntimeStatus()
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	case "rollback":
		result, err := install.RollbackRuntime()
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	case "uninstall":
		results, err := install.Uninstall(ctx, options)
		if writeErr := writeJSON(stdout, results); writeErr != nil {
			return writeErr
		}
		return err
	default:
		return fmt.Errorf("unknown plugin command %q", args[0])
	}
}

func runHarness(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail harness <probe|envelope>")
	}
	flags := flag.NewFlagSet("harness "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	id := flags.String("harness", "", "harness ID")
	root := flags.String("root", ".", "project root")
	role := flags.String("role", "", "role ID")
	node := flags.String("node", "", "node ID")
	session := flags.String("session", "", "session ID")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	adapter, err := harness.New(*id)
	if err != nil {
		return err
	}
	switch args[0] {
	case "probe":
		return writeJSON(stdout, adapter.ProbeResult())
	case "envelope":
		return writeJSON(stdout, adapter.Envelope(*root, *role, *node, *session))
	default:
		return fmt.Errorf("unknown harness command %q", args[0])
	}
}

func runHook(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("hook", flag.ContinueOnError)
	flags.SetOutput(stderr)
	harness := flags.String("harness", "", "codex, claude-code, or copilot-cli")
	event := flags.String("event", "", "hook event")
	root := flags.String("root", "", "project root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	output, active, err := hook.Run(*harness, *event, *root, stdin)
	if err != nil {
		return err
	}
	if !active {
		return nil
	}
	return writeJSON(stdout, output)
}

func runMCP(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "probe" {
		return runMCPProbe(ctx, args[1:], stdout, stderr, mcpserver.ProbeWithOptions)
	}
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "default project root; tools may override it")
	stdio := flags.Bool("stdio", false, "serve MCP over stdio")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*stdio {
		return fmt.Errorf("this release supports only --stdio")
	}
	client, err := controller.NewClient()
	if err != nil {
		return err
	}
	return mcpserver.RunRemote(ctx, client, *root)
}

type mcpProbeRunner func(context.Context, string, string, mcpserver.ProbeOptions) (mcpserver.ProbeReport, error)

func runMCPProbe(ctx context.Context, args []string, stdout, stderr io.Writer, probe mcpProbeRunner) error {
	flags := flag.NewFlagSet("mcp probe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "optional project root for a real dag_pre_wait MCP round trip")
	runtimePath := flags.String("runtime", "", "absolute DAGrail runtime; defaults to this executable")
	handshakeTimeout := flags.Duration("handshake-timeout", mcpserver.DefaultProbeHandshakeTimeout, "fresh-process initialize and tools/list timeout")
	projectTimeout := flags.Duration("project-timeout", mcpserver.DefaultProbeProjectRoundTripTimeout, "project dag_pre_wait round-trip timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *runtimePath == "" {
		resolved, err := os.Executable()
		if err != nil {
			return err
		}
		*runtimePath = resolved
	}
	report, err := probe(ctx, *runtimePath, *root, mcpserver.ProbeOptions{
		HandshakeTimeout:        *handshakeTimeout,
		ProjectRoundTripTimeout: *projectTimeout,
	})
	if err != nil {
		return err
	}
	return writeJSON(stdout, report)
}

func runReconcile(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	action := flags.String("action", "", "effect action ID")
	effectRef := flags.String("effect-ref", "", "opaque Effect ref")
	receipt := flags.String("receipt", "{}", "optional adapter evidence JSON; omit for native observation")
	key := flags.String("idempotency-key", "", "idempotency key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !validBoundedJSON(*receipt, 64*1024) {
		return fmt.Errorf("--receipt must be valid JSON no larger than 64 KiB")
	}
	s, err := service.Open(*root)
	if err != nil {
		return err
	}
	if *effectRef != "" {
		if *action != "" {
			return fmt.Errorf("provide --action or --effect-ref, not both")
		}
		*action, err = s.ResolveEntityRef("effect", *effectRef)
		if err != nil {
			return err
		}
	}
	value, err := s.ReconcileEffectContext(ctx, *action, json.RawMessage(*receipt), *key)
	if err != nil {
		return err
	}
	bounded, err := s.BoundedEffectAction(value)
	if err != nil {
		return err
	}
	return writeJSON(stdout, bounded)
}

func runInspect(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	ref := flags.String("ref", "", "opaque object reference")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 || (*ref != "" && flags.NArg() != 0) || (*ref == "" && flags.NArg() != 1) {
		return fmt.Errorf("usage: dagrail inspect [--root path] [--ref opaque-ref | kind:id]")
	}
	if *ref == "" {
		*ref = flags.Arg(0)
	}
	s, err := service.OpenForInspection(*root)
	if err != nil {
		return err
	}
	value, err := s.InspectContext(ctx, *ref)
	if err != nil {
		return err
	}
	return writeJSON(stdout, value)
}

func runPreWait(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("pre-wait", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	s, err := service.OpenForInspection(*root)
	if err != nil {
		return err
	}
	value, err := s.PreWaitContext(ctx)
	if err != nil {
		return err
	}
	return writeJSON(stdout, value)
}

func runRole(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail role <bind|renew|takeover|transfer|release|status>")
	}
	flags := flag.NewFlagSet("role "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	role := flags.String("role", "", "stable role ID")
	roleRef := flags.String("role-ref", "", "opaque Role ref")
	harness := flags.String("harness", "", "harness ID")
	session := flags.String("session", "", "session audit ID")
	actorRole := flags.String("actor-role", "", "truthful controller Role with role.control")
	actorRoleRef := flags.String("actor-role-ref", "", "opaque truthful controller Role ref")
	actorSession := flags.String("actor-session", "", "controller Role session audit ID")
	expectedSession := flags.String("expected-session", "", "current target Role session used as a compare-and-swap guard")
	reason := flags.String("reason", "", "bounded audit reason for controller transfer")
	ttl := flags.Duration("ttl", 15*time.Minute, "lease TTL")
	key := flags.String("idempotency-key", "", "idempotency key")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	open := service.Open
	if args[0] == "status" {
		open = service.OpenForInspection
	}
	s, err := open(*root)
	if err != nil {
		return err
	}
	if *roleRef != "" {
		if *role != "" {
			return fmt.Errorf("provide --role or --role-ref, not both")
		}
		*role, err = s.ResolveEntityRef("role", *roleRef)
		if err != nil {
			return err
		}
	}
	if *actorRoleRef != "" {
		if *actorRole != "" {
			return fmt.Errorf("provide --actor-role or --actor-role-ref, not both")
		}
		*actorRole, err = s.ResolveEntityRef("role", *actorRoleRef)
		if err != nil {
			return err
		}
	}
	switch args[0] {
	case "status":
		value, err := s.RoleStatusContext(context.Background(), *role)
		if err != nil {
			return err
		}
		return writeJSON(stdout, value)
	case "bind", "takeover":
		lease, err := s.BindRole(*role, *harness, *session, *ttl, args[0] == "takeover", *key)
		if err != nil {
			return err
		}
		if raw, _ := json.Marshal(lease); len(raw) > 24*1024 {
			value, inspectErr := s.BoundedRoleLeaseReceipt(*key)
			if inspectErr != nil {
				return inspectErr
			}
			return writeJSON(stdout, value)
		}
		return writeJSON(stdout, lease)
	case "renew":
		lease, err := s.RenewRole(*role, *harness, *session, *ttl, *key)
		if err != nil {
			return err
		}
		if raw, _ := json.Marshal(lease); len(raw) > 24*1024 {
			value, inspectErr := s.BoundedRoleLeaseReceipt(*key)
			if inspectErr != nil {
				return inspectErr
			}
			return writeJSON(stdout, value)
		}
		return writeJSON(stdout, lease)
	case "transfer":
		transfer, err := s.TransferRole(*actorRole, *actorSession, *role, *expectedSession, *harness, *session, *ttl, *reason, *key)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(transfer)
		if len(raw) <= 24*1024 {
			return writeJSON(stdout, transfer)
		}
		value, receiptErr := s.BoundedRoleTransferReceipt(*key)
		if receiptErr != nil {
			return receiptErr
		}
		return writeJSON(stdout, value)
	case "release":
		if err := s.ReleaseRole(*role, *session, *key); err != nil {
			return err
		}
		if len([]byte(*role)) > 2048 {
			ref := *roleRef
			if ref == "" {
				ref, err = s.EntityRef("role", *role)
				if err != nil {
					return err
				}
			}
			return writeJSON(stdout, map[string]any{"released": true, "roleRef": ref})
		}
		return writeJSON(stdout, map[string]any{"released": true, "roleId": *role})
	default:
		return fmt.Errorf("unknown role command %q", args[0])
	}
}

func runAction(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail action <list|apply>")
	}
	flags := flag.NewFlagSet("action "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	role := flags.String("role", "", "stable role ID")
	roleRef := flags.String("role-ref", "", "opaque Role ref")
	node := flags.String("node", "", "node ID")
	nodeRef := flags.String("node-ref", "", "opaque Node ref")
	ref := flags.String("ref", "", "allowed action ref")
	kind := flags.String("kind", "", "select one current action by kind")
	input := flags.String("input", "{}", "JSON action input")
	key := flags.String("idempotency-key", "", "idempotency key")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	open := service.OpenForInspection
	if args[0] == "apply" {
		open = service.Open
	}
	s, err := open(*root)
	if err != nil {
		return err
	}
	if *roleRef != "" {
		if *role != "" {
			return fmt.Errorf("provide --role or --role-ref, not both")
		}
		*role, err = s.ResolveEntityRef("role", *roleRef)
		if err != nil {
			return err
		}
	}
	if *nodeRef != "" {
		if *node != "" {
			return fmt.Errorf("provide --node or --node-ref, not both")
		}
		*node, err = s.ResolveEntityRef("node", *nodeRef)
		if err != nil {
			return err
		}
	}
	switch args[0] {
	case "list":
		value, err := s.BoundedActionListContext(ctx, *role, *node)
		if err != nil {
			return err
		}
		return writeJSON(stdout, value)
	case "apply":
		if !validBoundedJSON(*input, 64*1024) {
			return fmt.Errorf("--input must be valid JSON no larger than 64 KiB")
		}
		if (*ref == "") == (*kind == "") {
			return fmt.Errorf("provide exactly one of --ref or --kind")
		}
		var value service.ActionResult
		if *kind != "" {
			value, err = s.ApplySelectedActionContext(ctx, *kind, *role, *node, json.RawMessage(*input), *key)
		} else {
			value, err = s.ApplyActionContext(ctx, *ref, json.RawMessage(*input), *key)
		}
		if err != nil {
			return err
		}
		return writeJSON(stdout, value)
	default:
		return fmt.Errorf("unknown action command %q", args[0])
	}
}

func runContext(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("context", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	view := flags.String("view", "orchestrator", "context view")
	role := flags.String("role", "", "role ID")
	roleRef := flags.String("role-ref", "", "opaque Role ref")
	node := flags.String("node", "", "node ID")
	nodeRef := flags.String("node-ref", "", "opaque Node ref")
	budget := flags.Int("budget-bytes", 0, "maximum output bytes")
	cursor := flags.Uint64("cursor", 0, "journal cursor for a bounded delta")
	if err := flags.Parse(args); err != nil {
		return err
	}
	s, err := service.OpenForInspection(*root)
	if err != nil {
		return err
	}
	if *roleRef != "" {
		if *role != "" {
			return fmt.Errorf("provide --role or --role-ref, not both")
		}
		*role, err = s.ResolveEntityRef("role", *roleRef)
		if err != nil {
			return err
		}
	}
	if *nodeRef != "" {
		if *node != "" {
			return fmt.Errorf("provide --node or --node-ref, not both")
		}
		*node, err = s.ResolveEntityRef("node", *nodeRef)
		if err != nil {
			return err
		}
	}
	value, err := s.ContextSinceContext(ctx, *view, *role, *node, *budget, *cursor)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(value, '\n'))
	return err
}

func runInit(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	name := flags.String("name", "", "project name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	s, err := service.Init(*root, *name)
	if err != nil {
		return err
	}
	return writeJSON(stdout, map[string]any{"projectId": s.Project.Config.ProjectID, "root": s.Project.Root})
}

func runGraph(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail graph <import|export|validate|preview-change|apply-change>")
	}
	switch args[0] {
	case "import":
		flags := flag.NewFlagSet("graph import", flag.ContinueOnError)
		flags.SetOutput(stderr)
		root := flags.String("root", ".", "project root")
		file := flags.String("file", "", "graph file")
		providerID := flags.String("provider", "", "compiled graph importer provider ID")
		providerInput := flags.String("input", "{}", "graph importer provider input JSON")
		key := flags.String("idempotency-key", "", "idempotency key")
		role := flags.String("actor-role", "", "actor role")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if (*file == "") == (*providerID == "") {
			return fmt.Errorf("exactly one of --file or --provider is required")
		}
		s, err := service.Open(*root)
		if err != nil {
			return err
		}
		var result any
		if *providerID != "" {
			if !validBoundedJSON(*providerInput, 64*1024) {
				return fmt.Errorf("--input must be valid JSON no larger than 64 KiB")
			}
			result, err = s.ImportGraphFromProvider(ctx, *providerID, json.RawMessage(*providerInput), *key, *role)
		} else {
			result, err = s.ImportGraph(*file, *key, *role)
		}
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	case "export":
		flags := flag.NewFlagSet("graph export", flag.ContinueOnError)
		flags.SetOutput(stderr)
		root := flags.String("root", ".", "project root")
		format := flags.String("format", "yaml", "yaml or json")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		s, err := service.OpenForInspection(*root)
		if err != nil {
			return err
		}
		value, err := s.ExportGraph(*format)
		if err != nil {
			return err
		}
		_, err = stdout.Write(append(value, '\n'))
		return err
	case "validate":
		flags := flag.NewFlagSet("graph validate", flag.ContinueOnError)
		flags.SetOutput(stderr)
		file := flags.String("file", "", "graph file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *file == "" {
			return fmt.Errorf("--file is required")
		}
		graph, err := service.ValidateGraphFile(*file)
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"valid": true, "name": graph.Metadata.Name, "nodes": len(graph.Spec.Nodes), "edges": len(graph.Spec.Edges)})
	case "preview-change":
		flags := flag.NewFlagSet("graph preview-change", flag.ContinueOnError)
		flags.SetOutput(stderr)
		root := flags.String("root", ".", "project root")
		file := flags.String("file", "", "graph patch file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *file == "" {
			return fmt.Errorf("--file is required")
		}
		s, err := service.OpenForInspection(*root)
		if err != nil {
			return err
		}
		impact, err := s.PreviewGraphChange(*file)
		if err != nil {
			return err
		}
		return writeJSON(stdout, service.BoundedGraphImpact(impact))
	case "apply-change":
		flags := flag.NewFlagSet("graph apply-change", flag.ContinueOnError)
		flags.SetOutput(stderr)
		root := flags.String("root", ".", "project root")
		file := flags.String("file", "", "graph patch file")
		token := flags.String("token", "", "impact token")
		key := flags.String("idempotency-key", "", "idempotency key")
		role := flags.String("actor-role", "", "actor role")
		roleRef := flags.String("actor-role-ref", "", "opaque actor Role ref")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *file == "" || *token == "" {
			return fmt.Errorf("--file and --token are required")
		}
		s, err := service.Open(*root)
		if err != nil {
			return err
		}
		if *roleRef != "" {
			if *role != "" {
				return fmt.Errorf("provide --actor-role or --actor-role-ref, not both")
			}
			*role, err = s.ResolveEntityRef("role", *roleRef)
			if err != nil {
				return err
			}
		}
		impact, err := s.ApplyGraphChange(*file, *token, *key, *role)
		if err != nil {
			return err
		}
		return writeJSON(stdout, service.BoundedGraphImpact(impact))
	default:
		return fmt.Errorf("unknown graph command %q", args[0])
	}
}

func runFrontier(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("frontier", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	format := flags.String("format", "human", "human or json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	s, err := service.OpenForInspection(*root)
	if err != nil {
		return err
	}
	if *format != "human" && *format != "json" {
		return fmt.Errorf("--format must be human or json")
	}
	frontier, err := s.InspectContext(ctx, "frontier")
	if err != nil {
		return err
	}
	if *format == "json" {
		return writeJSON(stdout, frontier)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var output string
	switch value := frontier.(type) {
	case domain.Frontier:
		ready := value.Ready
		if len(ready) > 8 {
			ready = ready[:8]
		}
		output = fmt.Sprintf("Ready (%d): %s\n", len(value.Ready), strings.Join(ready, ", "))
	case map[string]any:
		output = fmt.Sprintf("Ready (%v); blocked: %v; unreachable: %v; resource-blocked: %v; inspect: %v\n",
			value["readyCount"], value["blockedCount"], value["unreachableCount"], value["resourceBlockedCount"], value["detailRef"])
	default:
		return fmt.Errorf("unexpected frontier response %T", frontier)
	}
	if len([]byte(output)) > 24*1024 {
		output = "Frontier exceeded the human output budget; use --format json and follow its detailRef.\n"
	}
	_, err = io.WriteString(stdout, output)
	return err
}

func runJournal(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail journal <verify|compatibility|export|replay>")
	}
	flags := flag.NewFlagSet("journal "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	output := flags.String("output", "", "export path; stdout when empty")
	showProgress := flags.Bool("progress", false, "emit bounded verification progress to stderr")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	open := service.OpenForInspection
	if args[0] == "verify" {
		open = service.OpenForAuthorityVerification
	}
	if args[0] == "replay" {
		open = service.Open
	}
	s, err := open(*root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "verify":
		lastProgress := -1
		progress := func(completed, total int) {
			if !*showProgress {
				return
			}
			step := total / 100
			if step < 1 {
				step = 1
			}
			if completed != 0 && completed != total && completed-lastProgress < step {
				return
			}
			lastProgress = completed
			_, _ = fmt.Fprintf(stderr, "{\"operation\":\"journal.verify\",\"completed\":%d,\"total\":%d}\n", completed, total)
		}
		report, err := s.VerifyJournalReportContext(ctx, progress)
		if err != nil {
			return err
		}
		return writeJSON(stdout, report)
	case "compatibility":
		compatibility, err := s.Compatibility()
		if err != nil {
			return err
		}
		return writeJSON(stdout, compatibility)
	case "export":
		data, err := s.ExportJournal()
		if err != nil {
			return err
		}
		if *output != "" {
			if err := writeExclusive(*output, data, 0o600); err != nil {
				return err
			}
			return writeJSON(stdout, map[string]any{"exported": true, "output": *output, "bytes": len(data)})
		}
		_, err = stdout.Write(data)
		return err
	case "replay":
		if err := s.RebuildProjection(); err != nil {
			return err
		}
		state, err := s.State()
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"replayed": true, "headSequence": state.HeadSequence, "graphRevision": state.GraphRevision})
	default:
		return fmt.Errorf("unknown journal command %q", args[0])
	}
}

func runSecurity(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "audit" {
		return fmt.Errorf("usage: dagrail security audit [--root path]")
	}
	flags := flag.NewFlagSet("security audit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	s, err := service.OpenForInspection(*root)
	if err != nil {
		return err
	}
	report := s.SecurityAudit()
	if err := writeJSON(stdout, report); err != nil {
		return err
	}
	if !report.Secure {
		return fmt.Errorf("security audit found failed checks")
	}
	return nil
}

func runSupport(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "preview" && args[0] != "export") {
		return fmt.Errorf("usage: dagrail support <preview|export> [--root path] [--output file]")
	}
	flags := flag.NewFlagSet("support "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	output := flags.String("output", "", "new support-report output file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	s, err := service.OpenForInspection(*root)
	if err != nil {
		return err
	}
	data, report, err := s.SupportBytes()
	if err != nil {
		return err
	}
	if args[0] == "export" {
		if *output == "" {
			return fmt.Errorf("support export requires --output")
		}
		if err := writeExclusive(*output, data, 0o600); err != nil {
			return err
		}
	}
	return writeJSON(stdout, report)
}

func runRecovery(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail recovery <rehearse|rotate-authority|adopt-legacy-authority|relocate-authority>")
	}
	flags := flag.NewFlagSet("recovery "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	backupPath := flags.String("backup", "", "authenticated recovery backup")
	expectedHead := flags.String("expected-current-head", "", "exact current journal head")
	reason := flags.String("reason", "", "bounded authority replacement reason")
	idempotencyKey := flags.String("idempotency-key", "", "stable authority recovery key")
	expectedProjectID := flags.String("expected-project-id", "", "exact legacy or target-locator Project UUID")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected recovery arguments")
	}
	if args[0] == "relocate-authority" {
		if *backupPath == "" || *expectedProjectID == "" || *expectedHead == "" || *reason == "" || *idempotencyKey == "" {
			return fmt.Errorf("--backup, --expected-project-id, --expected-current-head, --reason, and --idempotency-key are required")
		}
		info, err := os.Lstat(*backupPath)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 256*1024*1024 {
			return fmt.Errorf("recovery backup must be a regular non-symlink file no larger than 256 MiB")
		}
		data, err := os.ReadFile(*backupPath)
		if err != nil {
			return err
		}
		receipt, err := service.RelocateAuthority(*root, data, *expectedProjectID, *expectedHead, *reason, *idempotencyKey)
		if err != nil {
			return err
		}
		return writeJSON(stdout, receipt)
	}
	s, err := service.OpenForRecovery(*root)
	if err != nil {
		return err
	}
	if args[0] == "rotate-authority" {
		if *backupPath == "" || *expectedHead == "" || *reason == "" || *idempotencyKey == "" {
			return fmt.Errorf("--backup, --expected-current-head, --reason, and --idempotency-key are required")
		}
		info, err := os.Lstat(*backupPath)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 256*1024*1024 {
			return fmt.Errorf("recovery backup must be a regular non-symlink file no larger than 256 MiB")
		}
		data, err := os.ReadFile(*backupPath)
		if err != nil {
			return err
		}
		receipt, err := s.RotateAuthority(data, *expectedHead, *reason, *idempotencyKey)
		if err != nil {
			return err
		}
		return writeJSON(stdout, receipt)
	}
	if args[0] == "adopt-legacy-authority" {
		if *expectedProjectID == "" || *expectedHead == "" || *reason == "" || *idempotencyKey == "" {
			return fmt.Errorf("--expected-project-id, --expected-current-head (or empty), --reason, and --idempotency-key are required")
		}
		head := *expectedHead
		if head == "empty" {
			head = ""
		}
		receipt, err := s.AdoptLegacyAuthority(*expectedProjectID, head, *reason, *idempotencyKey)
		if err != nil {
			return err
		}
		return writeJSON(stdout, receipt)
	}
	if args[0] != "rehearse" {
		return fmt.Errorf("unknown recovery command %q", args[0])
	}
	report, err := s.RehearseRecovery()
	if err != nil {
		return err
	}
	if err := writeJSON(stdout, report); err != nil {
		return err
	}
	if !report.Ready {
		return fmt.Errorf("recovery rehearsal did not pass")
	}
	return nil
}

func runQualify(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "release" {
		return fmt.Errorf("usage: dagrail qualify release [--source path] [--project path]")
	}
	flags := flag.NewFlagSet("qualify release", flag.ContinueOnError)
	flags.SetOutput(stderr)
	source := flags.String("source", ".", "DAGrail source checkout")
	project := flags.String("project", "", "optional DAGrail project for security and recovery evidence")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("usage: dagrail qualify release [--source path] [--project path]")
	}
	report, err := qualification.Run(*source, *project)
	if err != nil {
		return err
	}
	if err := writeJSON(stdout, report); err != nil {
		return err
	}
	if !report.StructuralCandidate {
		return fmt.Errorf("release qualification did not pass")
	}
	return nil
}

func runReadiness(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("readiness", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourceRoot := flags.String("source", ".", "DAGrail source checkout")
	projectRoot := flags.String("project", "", "optional DAGrail project for security and recovery evidence")
	installation := flags.Bool("installation", false, "include local runtime and harness installation evidence")
	harnesses := flags.String("harness", "codex,claude-code,copilot-cli", "comma-separated harnesses for installation evidence")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usagef("usage: dagrail readiness [--source path] [--project path] [--installation] [--harness list]")
	}
	report, err := readiness.Evaluate(ctx, readiness.Options{
		SourceRoot: *sourceRoot, ProjectRoot: *projectRoot, Installation: *installation,
		InstallationOptions: install.Options{Harnesses: []string{*harnesses}},
	})
	if err != nil {
		return err
	}
	if err := writeJSON(stdout, report); err != nil {
		return err
	}
	if !report.ExternalValidationReady {
		return diagnosticError(fmt.Errorf("readiness checks did not reach external-validation readiness"))
	}
	return nil
}

func runRelease(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail release <manifest|verify>")
	}
	switch args[0] {
	case "manifest":
		flags := flag.NewFlagSet("release manifest", flag.ContinueOnError)
		flags.SetOutput(stderr)
		directory := flags.String("directory", "dist", "closed release artifact directory")
		releaseVersion := flags.String("version", version.Version, "release version")
		tag := flags.String("tag", "", "v-prefixed release tag")
		commit := flags.String("commit", "", "full release commit SHA")
		sourceDateEpoch := flags.Int64("source-date-epoch", 0, "release commit timestamp")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("usage: dagrail release manifest [--directory dist] --commit SHA --source-date-epoch EPOCH")
		}
		if *releaseVersion != version.Version {
			return fmt.Errorf("--version must match this DAGrail source version")
		}
		if *tag == "" {
			*tag = "v" + *releaseVersion
		}
		manifest, err := dagrelease.Generate(*directory, *releaseVersion, *tag, *commit, *sourceDateEpoch)
		if err != nil {
			return err
		}
		raw, err := dagrelease.MarshalManifest(manifest)
		if err != nil {
			return err
		}
		if err := dagrelease.WriteManifest(filepath.Join(*directory, dagrelease.ManifestFileName), raw); err != nil {
			return err
		}
		return writeJSON(stdout, manifest)
	case "verify":
		flags := flag.NewFlagSet("release verify", flag.ContinueOnError)
		flags.SetOutput(stderr)
		directory := flags.String("directory", "dist", "closed release artifact directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("usage: dagrail release verify [--directory dist]")
		}
		report := dagrelease.Verify(*directory)
		if err := writeJSON(stdout, report); err != nil {
			return err
		}
		if !report.Verified {
			return fmt.Errorf("release verification did not pass")
		}
		return nil
	default:
		return fmt.Errorf("usage: dagrail release <manifest|verify>")
	}
}

func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "install" {
		flags := flag.NewFlagSet("doctor install", flag.ContinueOnError)
		flags.SetOutput(stderr)
		harnesses := flags.String("harness", "codex,claude-code,copilot-cli", "comma-separated harnesses")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return usagef("usage: dagrail doctor install [--harness list]")
		}
		report, err := install.Diagnose(ctx, install.Options{Harnesses: []string{*harnesses}})
		if err != nil {
			return err
		}
		if err := writeJSON(stdout, report); err != nil {
			return err
		}
		if !report.Healthy {
			return diagnosticError(fmt.Errorf("installation diagnostic found failed checks"))
		}
		return nil
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return usagef("usage: dagrail doctor [--root path] or dagrail doctor install [--harness list]")
	}
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usagef("usage: dagrail doctor [--root path]")
	}
	s, err := service.OpenForInspection(*root)
	if err != nil {
		return err
	}
	report := s.Doctor()
	if err := writeJSON(stdout, report); err != nil {
		return err
	}
	if !report.Healthy {
		return diagnosticError(fmt.Errorf("doctor found failed checks"))
	}
	return nil
}

func runProjection(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail projection <rebuild|render>")
	}
	flags := flag.NewFlagSet("projection "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	providerID := flags.String("provider", "", "compiled projection provider ID")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	s, err := service.Open(*root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "rebuild":
		if err := s.RebuildProjection(); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"rebuilt": true})
	case "render":
		if *providerID == "" {
			return fmt.Errorf("--provider is required")
		}
		result, err := s.RenderProjection(ctx, *providerID)
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	default:
		return fmt.Errorf("unknown projection command %q", args[0])
	}
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func validBoundedJSON(value string, limit int) bool {
	return len([]byte(value)) <= limit && json.Valid([]byte(value))
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}
