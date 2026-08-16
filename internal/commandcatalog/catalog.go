package commandcatalog

import (
	"fmt"
	"sort"
	"strings"
)

const (
	APIVersion = "dagrail.io/command-catalog/v1alpha1"

	ExitOperationFailed = 1
	ExitUsage           = 2
	ExitDiagnostic      = 7
	ExitInterrupted     = 130
)

type Command struct {
	Name        string   `json:"name"`
	Summary     string   `json:"summary"`
	Effect      string   `json:"effect"`
	Project     string   `json:"project"`
	Output      string   `json:"output"`
	Subcommands []string `json:"subcommands"`
}

type ExitCode struct {
	Code      string `json:"code"`
	ExitCode  int    `json:"exitCode"`
	Retryable bool   `json:"retryable"`
}

type ErrorContract struct {
	APIVersion string     `json:"apiVersion"`
	OptIn      string     `json:"optIn"`
	MaxBytes   int        `json:"maxBytes"`
	ExitCodes  []ExitCode `json:"exitCodes"`
}

type Report struct {
	APIVersion string        `json:"apiVersion"`
	Kind       string        `json:"kind"`
	Version    string        `json:"version"`
	Commands   []Command     `json:"commands"`
	Errors     ErrorContract `json:"errors"`
}

var commands = []Command{
	{Name: "action", Summary: "List or apply controller-issued actions", Effect: "mixed", Project: "required", Output: "json", Subcommands: []string{"apply", "list"}},
	{Name: "backup", Summary: "Create, verify, or restore portable journal backups", Effect: "mixed", Project: "required", Output: "json", Subcommands: []string{"create", "restore", "verify"}},
	{Name: "commands", Summary: "Describe the bounded machine-readable CLI surface", Effect: "read", Project: "none", Output: "json", Subcommands: []string{}},
	{Name: "completion", Summary: "Generate shell completion from the command catalog", Effect: "read", Project: "none", Output: "text", Subcommands: []string{"bash", "fish", "powershell", "zsh"}},
	{Name: "context", Summary: "Produce a role-bounded work context", Effect: "read", Project: "required", Output: "json", Subcommands: []string{}},
	{Name: "contract", Summary: "Print the compatibility contract", Effect: "read", Project: "none", Output: "json", Subcommands: []string{}},
	{Name: "doctor", Summary: "Diagnose project or installation health", Effect: "read", Project: "optional", Output: "json", Subcommands: []string{"install"}},
	{Name: "evidence", Summary: "Query immutable evidence indexes", Effect: "read", Project: "required", Output: "json", Subcommands: []string{"list"}},
	{Name: "frontier", Summary: "Compute the current ready frontier", Effect: "read", Project: "required", Output: "json", Subcommands: []string{}},
	{Name: "graph", Summary: "Import, inspect, and revise the runtime graph", Effect: "mixed", Project: "required", Output: "json", Subcommands: []string{"apply-change", "export", "import", "preview-change", "validate"}},
	{Name: "harness", Summary: "Probe harness capabilities or emit a manual envelope", Effect: "read", Project: "optional", Output: "json", Subcommands: []string{"envelope", "probe"}},
	{Name: "history", Summary: "Read bounded journal history", Effect: "read", Project: "required", Output: "json", Subcommands: []string{}},
	{Name: "hook", Summary: "Handle a bounded harness hook event", Effect: "read", Project: "optional", Output: "json", Subcommands: []string{}},
	{Name: "incident", Summary: "Classify, progress, disposition, trip, or resolve a governed incident", Effect: "write", Project: "required", Output: "json", Subcommands: []string{"disposition", "progress", "resolve", "trip"}},
	{Name: "init", Summary: "Initialize a DAGrail project", Effect: "write", Project: "optional", Output: "json", Subcommands: []string{}},
	{Name: "inspect", Summary: "Inspect an object by opaque reference", Effect: "read", Project: "required", Output: "json", Subcommands: []string{}},
	{Name: "journal", Summary: "Verify, export, replay, or inspect journal compatibility", Effect: "mixed", Project: "required", Output: "json", Subcommands: []string{"compatibility", "export", "replay", "verify"}},
	{Name: "lifecycle", Summary: "Validate or atomically import authenticated history and export its projection", Effect: "mixed", Project: "required", Output: "json", Subcommands: []string{"import-history", "projection", "validate-history"}},
	{Name: "mcp", Summary: "Serve the six high-level tools over stdio", Effect: "serve", Project: "required", Output: "transport", Subcommands: []string{}},
	{Name: "observe", Summary: "Assess or verify an isolated shadow project", Effect: "mixed", Project: "optional", Output: "json", Subcommands: []string{"assess", "create-shadow", "verify-shadow"}},
	{Name: "plugin", Summary: "Manage runtime and harness plugin projections", Effect: "mixed", Project: "none", Output: "json", Subcommands: []string{"bundle-status", "conformance", "install", "materialize", "rollback", "runtime-status", "status", "uninstall"}},
	{Name: "pre-wait", Summary: "Check machine liveness before yielding", Effect: "read", Project: "required", Output: "json", Subcommands: []string{}},
	{Name: "projection", Summary: "Rebuild or render derived projections", Effect: "mixed", Project: "required", Output: "json", Subcommands: []string{"rebuild", "render"}},
	{Name: "provider", Summary: "List, check, or invoke compiled providers", Effect: "mixed", Project: "required", Output: "json", Subcommands: []string{"check", "invoke", "list"}},
	{Name: "qualify", Summary: "Evaluate structural release qualification", Effect: "read", Project: "optional", Output: "json", Subcommands: []string{"release"}},
	{Name: "readiness", Summary: "Aggregate pre-1.0 evidence without inferring adoption", Effect: "read", Project: "optional", Output: "json", Subcommands: []string{}},
	{Name: "reconcile", Summary: "Reconcile an uncertain external effect", Effect: "write", Project: "required", Output: "json", Subcommands: []string{}},
	{Name: "recovery", Summary: "Rehearse recovery or replace, adopt, and relocate authority identities", Effect: "mixed", Project: "required", Output: "json", Subcommands: []string{"adopt-legacy-authority", "rehearse", "relocate-authority", "rotate-authority"}},
	{Name: "release", Summary: "Create or verify a closed release manifest", Effect: "mixed", Project: "none", Output: "json", Subcommands: []string{"manifest", "verify"}},
	{Name: "role", Summary: "Bind, take over, or release a stable role", Effect: "write", Project: "required", Output: "json", Subcommands: []string{"bind", "release", "takeover"}},
	{Name: "security", Summary: "Audit the local project security posture", Effect: "read", Project: "required", Output: "json", Subcommands: []string{"audit"}},
	{Name: "signature", Summary: "Generate keys or sign and verify detached payloads", Effect: "mixed", Project: "none", Output: "json", Subcommands: []string{"keygen", "sign", "verify"}},
	{Name: "status", Summary: "Read compact operational status", Effect: "read", Project: "required", Output: "json", Subcommands: []string{}},
	{Name: "support", Summary: "Preview or export a redacted support report", Effect: "mixed", Project: "required", Output: "json", Subcommands: []string{"export", "preview"}},
	{Name: "ui", Summary: "Serve the loopback-only read-only explorer", Effect: "serve", Project: "required", Output: "text", Subcommands: []string{}},
	{Name: "version", Summary: "Print linked build identity", Effect: "read", Project: "none", Output: "json", Subcommands: []string{}},
}

func Current(version string) Report {
	items := make([]Command, len(commands))
	for index, command := range commands {
		items[index] = command
		items[index].Subcommands = append([]string{}, command.Subcommands...)
	}
	return Report{
		APIVersion: APIVersion,
		Kind:       "CommandCatalog",
		Version:    version,
		Commands:   items,
		Errors: ErrorContract{
			APIVersion: "dagrail.io/cli-error/v1alpha1",
			OptIn:      "--errors=json or DAGRAIL_ERROR_FORMAT=json",
			MaxBytes:   4096,
			ExitCodes: []ExitCode{
				{Code: "operation_failed", ExitCode: ExitOperationFailed, Retryable: false},
				{Code: "usage", ExitCode: ExitUsage, Retryable: false},
				{Code: "diagnostic_failed", ExitCode: ExitDiagnostic, Retryable: false},
				{Code: "interrupted", ExitCode: ExitInterrupted, Retryable: true},
			},
		},
	}
}

func Names() []string {
	result := make([]string, 0, len(commands))
	for _, command := range commands {
		result = append(result, command.Name)
	}
	return result
}

func Contains(name string) bool {
	index := sort.Search(len(commands), func(index int) bool { return commands[index].Name >= name })
	return index < len(commands) && commands[index].Name == name
}

func Completion(shell string) (string, error) {
	shell = strings.ToLower(strings.TrimSpace(shell))
	switch shell {
	case "bash":
		return bashCompletion(), nil
	case "zsh":
		return zshCompletion(), nil
	case "fish":
		return fishCompletion(), nil
	case "powershell":
		return powershellCompletion(), nil
	default:
		return "", fmt.Errorf("unsupported shell %q", shell)
	}
}

func bashCompletion() string {
	var body strings.Builder
	body.WriteString("# generated by dagrail completion bash\n_dagrail_complete() {\n  local cur=\"${COMP_WORDS[COMP_CWORD]}\"\n  local values=\"")
	body.WriteString(strings.Join(Names(), " "))
	body.WriteString(" --errors=json\"\n  case \"${COMP_WORDS[1]}\" in\n")
	for _, command := range commands {
		if len(command.Subcommands) > 0 {
			fmt.Fprintf(&body, "    %s) values=\"%s\" ;;\n", command.Name, strings.Join(command.Subcommands, " "))
		}
	}
	body.WriteString("  esac\n  COMPREPLY=( $(compgen -W \"$values\" -- \"$cur\") )\n}\ncomplete -F _dagrail_complete dagrail\n")
	return body.String()
}

func zshCompletion() string {
	var body strings.Builder
	body.WriteString("#compdef dagrail\n# generated by dagrail completion zsh\nlocal -a values\nvalues=(")
	for _, command := range commands {
		fmt.Fprintf(&body, " '%s:%s'", command.Name, strings.ReplaceAll(command.Summary, "'", ""))
	}
	body.WriteString(" )\nif (( CURRENT == 2 )); then\n  _describe 'command' values\n  return\nfi\ncase $words[2] in\n")
	for _, command := range commands {
		if len(command.Subcommands) > 0 {
			fmt.Fprintf(&body, "  %s) compadd -- %s ;;\n", command.Name, strings.Join(command.Subcommands, " "))
		}
	}
	body.WriteString("esac\n")
	return body.String()
}

func fishCompletion() string {
	var body strings.Builder
	body.WriteString("# generated by dagrail completion fish\ncomplete -c dagrail -f\n")
	for _, command := range commands {
		fmt.Fprintf(&body, "complete -c dagrail -n '__fish_use_subcommand' -a %s -d '%s'\n", command.Name, strings.ReplaceAll(command.Summary, "'", ""))
		for _, subcommand := range command.Subcommands {
			fmt.Fprintf(&body, "complete -c dagrail -n '__fish_seen_subcommand_from %s' -a %s\n", command.Name, subcommand)
		}
	}
	return body.String()
}

func powershellCompletion() string {
	var body strings.Builder
	body.WriteString("# generated by dagrail completion powershell\n$DagrailCommands = @{")
	for _, command := range commands {
		if len(command.Subcommands) == 0 {
			fmt.Fprintf(&body, "\n  '%s' = @()", command.Name)
			continue
		}
		fmt.Fprintf(&body, "\n  '%s' = @('%s')", command.Name, strings.Join(command.Subcommands, "','"))
	}
	body.WriteString("\n}\nRegister-ArgumentCompleter -Native -CommandName dagrail -ScriptBlock {\n  param($wordToComplete, $commandAst, $cursorPosition)\n  $parts = $commandAst.CommandElements | ForEach-Object { $_.Extent.Text }\n  $values = if ($parts.Count -le 2) { $DagrailCommands.Keys } else { $DagrailCommands[$parts[1]] }\n  $values | Where-Object { $_ -like \"$wordToComplete*\" } | ForEach-Object { [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_) }\n}\n")
	return body.String()
}
