package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/harness"
	"github.com/CongBao/dagrail/internal/hook"
	"github.com/CongBao/dagrail/internal/install"
	"github.com/CongBao/dagrail/internal/mcpserver"
	internalproviders "github.com/CongBao/dagrail/internal/providers"
	"github.com/CongBao/dagrail/internal/service"
	"github.com/CongBao/dagrail/internal/version"
)

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail <command>")
	}
	switch args[0] {
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "graph":
		return runGraph(args[1:], stdout, stderr)
	case "frontier":
		return runFrontier(args[1:], stdout, stderr)
	case "role":
		return runRole(args[1:], stdout, stderr)
	case "action":
		return runAction(args[1:], stdout, stderr)
	case "context":
		return runContext(args[1:], stdout, stderr)
	case "inspect":
		return runInspect(args[1:], stdout, stderr)
	case "pre-wait":
		return runPreWait(args[1:], stdout, stderr)
	case "reconcile":
		return runReconcile(args[1:], stdout, stderr)
	case "mcp":
		return runMCP(args[1:], stderr)
	case "hook":
		return runHook(args[1:], stdin, stdout, stderr)
	case "harness":
		return runHarness(args[1:], stdout, stderr)
	case "plugin":
		return runPlugin(args[1:], stdout, stderr)
	case "provider":
		return runProvider(args[1:], stdout, stderr)
	case "journal":
		return runJournal(args[1:], stdout, stderr)
	case "evidence":
		return runEvidence(args[1:], stdout, stderr)
	case "projection":
		return runProjection(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "version":
		return writeJSON(stdout, map[string]string{"version": version.Version, "commit": version.Commit, "date": version.Date})
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runProvider(args []string, stdout, stderr io.Writer) error {
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
	s, err := service.Open(*root)
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
		if !json.Valid([]byte(*input)) {
			return fmt.Errorf("--input must be valid JSON")
		}
		result, err := s.InvokeProvider(context.Background(), internalproviders.Invocation{Kind: *kind, ProviderID: *id, Input: json.RawMessage(*input)})
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	default:
		return fmt.Errorf("unknown provider command %q", args[0])
	}
}

func runEvidence(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "list" {
		return fmt.Errorf("usage: dagrail evidence list [--node ID] [--attempt ID]")
	}
	flags := flag.NewFlagSet("evidence list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	nodeID := flags.String("node", "", "filter by node ID")
	attemptID := flags.String("attempt", "", "filter by attempt ID")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	s, err := service.Open(*root)
	if err != nil {
		return err
	}
	result, err := s.ListEvidence(*nodeID, *attemptID)
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func runPlugin(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail plugin <install|status|uninstall>")
	}
	flags := flag.NewFlagSet("plugin "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	harnesses := flags.String("harness", "codex,claude-code,copilot-cli", "comma-separated harnesses")
	source := flags.String("marketplace-source", install.DefaultMarketplaceSource, "local path or Git marketplace source")
	runtimePath := flags.String("runtime", "", "absolute DAGrail runtime path")
	dryRun := flags.Bool("dry-run", false, "show or validate without changing host configuration")
	if err := flags.Parse(args[1:]); err != nil {
		return err
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
	options := install.Options{Harnesses: []string{*harnesses}, RuntimePath: *runtimePath, MarketplaceSource: *source, DryRun: *dryRun}
	switch args[0] {
	case "install":
		results, err := install.Install(context.Background(), options)
		if writeErr := writeJSON(stdout, results); writeErr != nil {
			return writeErr
		}
		return err
	case "status":
		results, err := install.Status(options)
		if err != nil {
			return err
		}
		return writeJSON(stdout, results)
	case "uninstall":
		results, err := install.Uninstall(context.Background(), options)
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

func runMCP(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	stdio := flags.Bool("stdio", false, "serve MCP over stdio")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*stdio {
		return fmt.Errorf("this release supports only --stdio")
	}
	s, err := service.Open(*root)
	if err != nil {
		return err
	}
	return mcpserver.Run(context.Background(), s)
}

func runReconcile(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	action := flags.String("action", "", "effect action ID")
	receipt := flags.String("receipt", "{}", "adapter reconciliation evidence JSON")
	key := flags.String("idempotency-key", "", "idempotency key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !json.Valid([]byte(*receipt)) {
		return fmt.Errorf("--receipt must be valid JSON")
	}
	s, err := service.Open(*root)
	if err != nil {
		return err
	}
	value, err := s.ReconcileEffect(*action, json.RawMessage(*receipt), *key)
	if err != nil {
		return err
	}
	return writeJSON(stdout, value)
}

func runInspect(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("usage: dagrail inspect [--root path] kind:id")
	}
	s, err := service.Open(*root)
	if err != nil {
		return err
	}
	value, err := s.Inspect(flags.Arg(0))
	if err != nil {
		return err
	}
	return writeJSON(stdout, value)
}

func runPreWait(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("pre-wait", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	s, err := service.Open(*root)
	if err != nil {
		return err
	}
	value, err := s.PreWait()
	if err != nil {
		return err
	}
	return writeJSON(stdout, value)
}

func runRole(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail role <bind|takeover|release>")
	}
	flags := flag.NewFlagSet("role "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	role := flags.String("role", "", "stable role ID")
	harness := flags.String("harness", "", "harness ID")
	session := flags.String("session", "", "session audit ID")
	ttl := flags.Duration("ttl", 15*time.Minute, "lease TTL")
	key := flags.String("idempotency-key", "", "idempotency key")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	s, err := service.Open(*root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "bind", "takeover":
		lease, err := s.BindRole(*role, *harness, *session, *ttl, args[0] == "takeover", *key)
		if err != nil {
			return err
		}
		return writeJSON(stdout, lease)
	case "release":
		if err := s.ReleaseRole(*role, *session, *key); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"released": true, "roleId": *role})
	default:
		return fmt.Errorf("unknown role command %q", args[0])
	}
}

func runAction(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail action <list|apply>")
	}
	flags := flag.NewFlagSet("action "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	role := flags.String("role", "", "stable role ID")
	node := flags.String("node", "", "node ID")
	ref := flags.String("ref", "", "allowed action ref")
	input := flags.String("input", "{}", "JSON action input")
	key := flags.String("idempotency-key", "", "idempotency key")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	s, err := service.Open(*root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		value, err := s.ListActions(*role, *node)
		if err != nil {
			return err
		}
		return writeJSON(stdout, value)
	case "apply":
		if !json.Valid([]byte(*input)) {
			return fmt.Errorf("--input must be valid JSON")
		}
		value, err := s.ApplyAction(*ref, json.RawMessage(*input), *key)
		if err != nil {
			return err
		}
		return writeJSON(stdout, value)
	default:
		return fmt.Errorf("unknown action command %q", args[0])
	}
}

func runContext(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("context", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	view := flags.String("view", "orchestrator", "context view")
	role := flags.String("role", "", "role ID")
	node := flags.String("node", "", "node ID")
	budget := flags.Int("budget-bytes", 0, "maximum output bytes")
	cursor := flags.Uint64("cursor", 0, "journal cursor for a bounded delta")
	if err := flags.Parse(args); err != nil {
		return err
	}
	s, err := service.Open(*root)
	if err != nil {
		return err
	}
	value, err := s.ContextSince(*view, *role, *node, *budget, *cursor)
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

func runGraph(args []string, stdout, stderr io.Writer) error {
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
			if !json.Valid([]byte(*providerInput)) {
				return fmt.Errorf("--input must be valid JSON")
			}
			result, err = s.ImportGraphFromProvider(context.Background(), *providerID, json.RawMessage(*providerInput), *key, *role)
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
		s, err := service.Open(*root)
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
		s, err := service.Open(*root)
		if err != nil {
			return err
		}
		impact, err := s.PreviewGraphChange(*file)
		if err != nil {
			return err
		}
		return writeJSON(stdout, impact)
	case "apply-change":
		flags := flag.NewFlagSet("graph apply-change", flag.ContinueOnError)
		flags.SetOutput(stderr)
		root := flags.String("root", ".", "project root")
		file := flags.String("file", "", "graph patch file")
		token := flags.String("token", "", "impact token")
		key := flags.String("idempotency-key", "", "idempotency key")
		role := flags.String("actor-role", "", "actor role")
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
		impact, err := s.ApplyGraphChange(*file, *token, *key, *role)
		if err != nil {
			return err
		}
		return writeJSON(stdout, impact)
	default:
		return fmt.Errorf("unknown graph command %q", args[0])
	}
}

func runFrontier(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("frontier", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	format := flags.String("format", "human", "human or json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	s, err := service.Open(*root)
	if err != nil {
		return err
	}
	frontier, err := s.Frontier()
	if err != nil {
		return err
	}
	if *format == "json" {
		return writeJSON(stdout, frontier)
	}
	_, err = fmt.Fprintf(stdout, "Ready: %s\n", strings.Join(frontier.Ready, ", "))
	return err
}

func runJournal(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dagrail journal <verify|compatibility|export|replay>")
	}
	flags := flag.NewFlagSet("journal "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	output := flags.String("output", "", "export path; stdout when empty")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	s, err := service.Open(*root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "verify":
		segments, err := s.VerifyJournal()
		if err != nil {
			return err
		}
		compatibility, err := s.Compatibility()
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"valid": true, "segments": len(segments), "compatibility": compatibility})
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
			if err := os.WriteFile(*output, data, 0o600); err != nil {
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

func runDoctor(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	s, err := service.Open(*root)
	if err != nil {
		return err
	}
	report := s.Doctor()
	if err := writeJSON(stdout, report); err != nil {
		return err
	}
	if !report.Healthy {
		return fmt.Errorf("doctor found failed checks")
	}
	return nil
}

func runProjection(args []string, stdout, stderr io.Writer) error {
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
		result, err := s.RenderProjection(context.Background(), *providerID)
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
