package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	MarketplaceName          = "dagrail"
	PluginID                 = "dagrail@dagrail"
	BundledMarketplaceName   = "dagrail-bundled"
	MCPServerName            = "dagrail"
	DefaultMarketplaceSource = "CongBao/dagrail"
)

type Options struct {
	Harnesses         []string `json:"harnesses"`
	RuntimePath       string   `json:"runtimePath"`
	MarketplaceSource string   `json:"marketplaceSource"`
	DryRun            bool     `json:"dryRun"`
}

type PlanResult struct {
	Harness           string   `json:"harness"`
	Executable        string   `json:"executable,omitempty"`
	MarketplaceAdd    []string `json:"marketplaceAdd"`
	MarketplaceUpdate []string `json:"marketplaceUpdate"`
	MarketplaceRemove []string `json:"marketplaceRemove"`
	PluginInstall     []string `json:"pluginInstall"`
	PluginRemove      []string `json:"pluginRemove"`
	MCPRemove         []string `json:"mcpRemove"`
	MCPAdd            []string `json:"mcpAdd"`
}

type Result struct {
	Harness       string `json:"harness"`
	Status        string `json:"status"`
	Executable    string `json:"executable,omitempty"`
	MCPConfigured bool   `json:"mcpConfigured"`
	Message       string `json:"message,omitempty"`
}

type RuntimeResult struct {
	Status            string `json:"status"`
	Version           string `json:"version"`
	RuntimePath       string `json:"runtimePath"`
	SHA256            string `json:"sha256"`
	PreviousVersion   string `json:"previousVersion,omitempty"`
	RollbackAvailable bool   `json:"rollbackAvailable"`
}

func Plan(options Options) ([]PlanResult, error) {
	if !filepath.IsAbs(options.RuntimePath) {
		return nil, fmt.Errorf("runtime path must be absolute")
	}
	if options.MarketplaceSource == "" {
		options.MarketplaceSource = DefaultMarketplaceSource
	}
	harnesses, err := normalizeHarnesses(options.Harnesses)
	if err != nil {
		return nil, err
	}
	results := make([]PlanResult, 0, len(harnesses))
	for _, harness := range harnesses {
		plan := PlanResult{Harness: harness}
		marketplaceName, pluginID := MarketplaceName, PluginID
		localMarketplace := filepath.IsAbs(options.MarketplaceSource)
		if localMarketplace {
			marketplaceName, pluginID = BundledMarketplaceName, "dagrail@"+BundledMarketplaceName
		}
		switch harness {
		case "codex":
			plan.MarketplaceAdd = []string{"plugin", "marketplace", "add", options.MarketplaceSource}
			if !localMarketplace {
				plan.MarketplaceAdd = append(plan.MarketplaceAdd, "--ref", "main")
			}
			plan.MarketplaceAdd = append(plan.MarketplaceAdd, "--json")
			plan.MarketplaceUpdate = []string{"plugin", "marketplace", "upgrade", marketplaceName, "--json"}
			plan.MarketplaceRemove = []string{"plugin", "marketplace", "remove", marketplaceName, "--json"}
			plan.PluginInstall = []string{"plugin", "add", pluginID, "--json"}
			plan.PluginRemove = []string{"plugin", "remove", pluginID, "--json"}
			plan.MCPRemove = []string{"mcp", "remove", MCPServerName}
			plan.MCPAdd = []string{"mcp", "add", MCPServerName, "--", options.RuntimePath, "mcp", "--stdio"}
		case "claude-code":
			plan.MarketplaceAdd = []string{"plugin", "marketplace", "add", options.MarketplaceSource}
			plan.MarketplaceUpdate = []string{"plugin", "marketplace", "update", marketplaceName}
			plan.MarketplaceRemove = []string{"plugin", "marketplace", "remove", marketplaceName}
			plan.PluginInstall = []string{"plugin", "install", pluginID, "--scope", "user"}
			plan.PluginRemove = []string{"plugin", "uninstall", pluginID, "--scope", "user"}
			plan.MCPRemove = []string{"mcp", "remove", MCPServerName}
			plan.MCPAdd = []string{"mcp", "add", "--transport", "stdio", "--scope", "user", MCPServerName, "--", options.RuntimePath, "mcp", "--stdio"}
		case "copilot-cli":
			plan.MarketplaceAdd = []string{"plugin", "marketplace", "add", options.MarketplaceSource}
			plan.MarketplaceUpdate = []string{"plugin", "marketplace", "update", marketplaceName}
			plan.MarketplaceRemove = []string{"plugin", "marketplace", "remove", marketplaceName}
			plan.PluginInstall = []string{"plugin", "install", pluginID}
			plan.PluginRemove = []string{"plugin", "uninstall", "dagrail"}
			plan.MCPRemove = []string{"mcp", "remove", MCPServerName}
			plan.MCPAdd = []string{"mcp", "add", MCPServerName, "--timeout", "10000", "--", options.RuntimePath, "mcp", "--stdio"}
		}
		results = append(results, plan)
	}
	return results, nil
}

func Install(ctx context.Context, options Options) ([]Result, error) {
	plans, err := Plan(options)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(plans))
	var failures []error
	var runtimeValidationErr error
	runtimeValidated := false
	for _, plan := range plans {
		executable := findExecutable(plan.Harness)
		plan.Executable = executable
		if executable == "" {
			results = append(results, Result{Harness: plan.Harness, Status: "not_detected", Message: "harness executable not found"})
			failures = append(failures, fmt.Errorf("%s not detected", plan.Harness))
			continue
		}
		if options.DryRun {
			results = append(results, Result{Harness: plan.Harness, Status: "planned", Executable: executable, Message: strings.Join(plan.MCPAdd, " ")})
			continue
		}
		if !runtimeValidated {
			runtimeValidationErr = validateFreshRuntime(ctx, options.RuntimePath)
			if runtimeValidationErr == nil {
				runtimeValidationErr = validateHookLauncher(ctx, options.RuntimePath)
			}
			runtimeValidated = true
		}
		if runtimeValidationErr != nil {
			results = append(results, Result{Harness: plan.Harness, Status: "failed", Executable: executable, Message: runtimeValidationErr.Error()})
			failures = append(failures, runtimeValidationErr)
			continue
		}
		marketplaceOutput, marketplaceErr := run(ctx, executable, plan.MarketplaceAdd...)
		if marketplaceErr != nil && !alreadyRegistered(marketplaceOutput) {
			results = append(results, Result{Harness: plan.Harness, Status: "failed", Executable: executable, Message: marketplaceErr.Error()})
			failures = append(failures, marketplaceErr)
			continue
		}
		if marketplaceErr != nil || marketplaceAlreadyAdded(marketplaceOutput) {
			if _, updateErr := run(ctx, executable, plan.MarketplaceUpdate...); updateErr != nil {
				results = append(results, Result{Harness: plan.Harness, Status: "failed", Executable: executable, Message: updateErr.Error()})
				failures = append(failures, updateErr)
				continue
			}
		}
		if _, runErr := run(ctx, executable, plan.PluginInstall...); runErr != nil {
			results = append(results, Result{Harness: plan.Harness, Status: "failed", Executable: executable, Message: runErr.Error()})
			failures = append(failures, runErr)
			continue
		}
		_, _ = run(ctx, executable, plan.MCPRemove...)
		if _, runErr := run(ctx, executable, plan.MCPAdd...); runErr != nil {
			results = append(results, Result{Harness: plan.Harness, Status: "failed", Executable: executable, Message: runErr.Error()})
			failures = append(failures, runErr)
			continue
		}
		results = append(results, Result{Harness: plan.Harness, Status: "installed_or_updated", Executable: executable, MCPConfigured: true})
	}
	return results, errors.Join(failures...)
}

func Status(options Options) ([]Result, error) {
	return StatusContext(context.Background(), options)
}

func StatusContext(ctx context.Context, options Options) ([]Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	harnesses, err := normalizeHarnesses(options.Harnesses)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(harnesses))
	for _, harness := range harnesses {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		path := findExecutable(harness)
		status := "not_detected"
		mcpConfigured := false
		message := ""
		if path != "" {
			status = "detected_not_installed"
			pluginArgs := []string{"plugin", "list"}
			if harness == "codex" {
				pluginArgs = append(pluginArgs, "--json")
			}
			pluginOutput, pluginErr := run(ctx, path, pluginArgs...)
			if pluginErr == nil && pluginListingContains(pluginOutput) {
				status = "installed"
			} else if pluginErr != nil {
				message = "plugin status probe unavailable: " + pluginErr.Error()
			}
			mcpOutput, mcpErr := run(ctx, path, "mcp", "list")
			mcpConfigured = mcpErr == nil && mcpConfigurationMatches(mcpOutput, options.RuntimePath)
		}
		results = append(results, Result{Harness: harness, Status: status, Executable: path, MCPConfigured: mcpConfigured, Message: message})
	}
	return results, nil
}

func pluginListingContains(output string) bool {
	var value any
	if json.Unmarshal([]byte(output), &value) == nil {
		return pluginJSONContains(value)
	}
	for _, field := range strings.Fields(strings.ToLower(output)) {
		candidate := strings.Trim(field, "\"'`,:;[]{}()")
		if candidate == "dagrail" || strings.HasPrefix(candidate, "dagrail@") {
			return true
		}
	}
	return false
}

func pluginJSONContains(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for _, field := range []string{"name", "id", "plugin", "pluginId"} {
			if candidate, ok := typed[field].(string); ok && pluginIdentifier(candidate) {
				return true
			}
		}
		for key, child := range typed {
			if pluginIdentifier(key) {
				switch value := child.(type) {
				case map[string]any, []any:
					return true
				case bool:
					if value {
						return true
					}
				}
			}
			if pluginJSONContains(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if pluginJSONContains(child) {
				return true
			}
		}
	}
	return false
}

func pluginIdentifier(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "dagrail" || strings.HasPrefix(value, "dagrail@")
}

func mcpConfigurationMatches(output, runtimePath string) bool {
	if !filepath.IsAbs(runtimePath) {
		return false
	}
	var value any
	if json.Unmarshal([]byte(output), &value) == nil {
		return mcpJSONContains(value, runtimePath)
	}
	trimmed := strings.TrimSpace(output)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return false
	}
	for _, line := range strings.Split(output, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "dagrail") && strings.Contains(line, runtimePath) && strings.Contains(lower, "mcp") && strings.Contains(lower, "--stdio") {
			return true
		}
	}
	return false
}

func mcpJSONContains(value any, runtimePath string) bool {
	return mcpJSONContainsNamed(value, runtimePath, false)
}

func mcpJSONContainsNamed(value any, runtimePath string, inheritedName bool) bool {
	switch typed := value.(type) {
	case map[string]any:
		stringsInObject := collectDirectStrings(typed)
		hasName, hasRuntime, hasMCP, hasStdio := inheritedName, false, false, false
		for _, field := range []string{"name", "id", "server", "serverName"} {
			if candidate, ok := typed[field].(string); ok && strings.EqualFold(strings.TrimSpace(candidate), MCPServerName) {
				hasName = true
			}
		}
		for _, candidate := range stringsInObject {
			trimmed := strings.TrimSpace(candidate)
			lower := strings.ToLower(trimmed)
			hasRuntime = hasRuntime || trimmed == runtimePath
			hasMCP = hasMCP || lower == "mcp"
			hasStdio = hasStdio || lower == "--stdio"
		}
		if hasName && hasRuntime && hasMCP && hasStdio {
			return true
		}
		for key, child := range typed {
			childName := hasName || strings.EqualFold(strings.TrimSpace(key), MCPServerName)
			if mcpJSONContainsNamed(child, runtimePath, childName) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if mcpJSONContainsNamed(child, runtimePath, inheritedName) {
				return true
			}
		}
	}
	return false
}

func collectDirectStrings(value map[string]any) []string {
	result := make([]string, 0, len(value))
	for _, child := range value {
		switch typed := child.(type) {
		case string:
			result = append(result, typed)
		case []any:
			for _, item := range typed {
				if text, ok := item.(string); ok {
					result = append(result, text)
				}
			}
		}
	}
	return result
}

func Uninstall(ctx context.Context, options Options) ([]Result, error) {
	plans, err := Plan(options)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(plans))
	var failures []error
	for _, plan := range plans {
		executable := findExecutable(plan.Harness)
		if executable == "" {
			results = append(results, Result{Harness: plan.Harness, Status: "not_detected"})
			continue
		}
		if options.DryRun {
			results = append(results, Result{Harness: plan.Harness, Status: "planned", Executable: executable})
			continue
		}
		_, _ = run(ctx, executable, plan.MCPRemove...)
		pluginOutput, runErr := run(ctx, executable, plan.PluginRemove...)
		if runErr != nil && !alreadyAbsent(pluginOutput) {
			failures = append(failures, runErr)
			results = append(results, Result{Harness: plan.Harness, Status: "failed", Executable: executable, Message: runErr.Error()})
			continue
		}
		marketplaceOutput, removeErr := run(ctx, executable, plan.MarketplaceRemove...)
		if removeErr != nil && !alreadyAbsent(marketplaceOutput) {
			failures = append(failures, removeErr)
			results = append(results, Result{Harness: plan.Harness, Status: "uninstalled_marketplace_retained", Executable: executable, Message: removeErr.Error()})
			continue
		}
		results = append(results, Result{Harness: plan.Harness, Status: "uninstalled", Executable: executable})
	}
	return results, errors.Join(failures...)
}

func DefaultRuntimePath() (string, error) {
	if override := os.Getenv("DAGRAIL_RUNTIME_PATH"); override != "" {
		path, err := filepath.Abs(override)
		return filepath.Clean(path), err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		root := os.Getenv("LOCALAPPDATA")
		if root == "" {
			root = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(root, "DAGrail", "bin", "dagrail.exe"), nil
	}
	return filepath.Join(home, ".local", "bin", "dagrail"), nil
}

func validateHookLauncher(ctx context.Context, runtimePath string) error {
	resolved, err := exec.LookPath("dagrail")
	if err != nil {
		return fmt.Errorf("hook launcher dagrail is not available on PATH")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("resolve hook launcher: %w", err)
	}
	want, err := filepath.Abs(runtimePath)
	if err != nil {
		return fmt.Errorf("resolve runtime path: %w", err)
	}
	resolvedReal, resolvedErr := filepath.EvalSymlinks(resolved)
	wantReal, wantErr := filepath.EvalSymlinks(want)
	if resolvedErr != nil || wantErr != nil || filepath.Clean(resolvedReal) != filepath.Clean(wantReal) {
		return fmt.Errorf("hook launcher dagrail does not resolve to the verified runtime")
	}
	if err := validateFreshRuntime(ctx, resolvedReal); err != nil {
		return fmt.Errorf("hook launcher fresh-process probe: %w", err)
	}
	return nil
}

func normalizeHarnesses(values []string) ([]string, error) {
	if len(values) == 0 {
		values = []string{"codex", "claude-code", "copilot-cli"}
	}
	aliases := map[string]string{"codex": "codex", "claude": "claude-code", "claude-code": "claude-code", "copilot": "copilot-cli", "copilot-cli": "copilot-cli"}
	seen := map[string]bool{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			canonical, ok := aliases[strings.ToLower(strings.TrimSpace(part))]
			if !ok {
				return nil, fmt.Errorf("unsupported harness %s", part)
			}
			seen[canonical] = true
		}
	}
	result := []string{}
	for _, id := range []string{"codex", "claude-code", "copilot-cli"} {
		if seen[id] {
			result = append(result, id)
		}
	}
	return result, nil
}

func findExecutable(harness string) string {
	command := map[string]string{"codex": "codex", "claude-code": "claude", "copilot-cli": "copilot"}[harness]
	if path, err := exec.LookPath(command); err == nil {
		return path
	}
	if harness == "codex" && runtime.GOOS == "darwin" {
		for _, path := range []string{"/Applications/ChatGPT.app/Contents/Resources/codex", "/Applications/Codex.app/Contents/Resources/codex"} {
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				return path
			}
		}
	}
	return ""
}
func run(ctx context.Context, name string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(commandCtx, name, args...)
	output := &boundedBuffer{remaining: 64 * 1024}
	command.Stdout, command.Stderr = output, output
	err := command.Run()
	if err != nil {
		if ctx.Err() != nil {
			return output.String(), ctx.Err()
		}
		if commandCtx.Err() == context.DeadlineExceeded {
			return output.String(), fmt.Errorf("host command exceeded the two-minute limit")
		}
		return output.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}
func alreadyRegistered(output string) bool {
	value := strings.ToLower(output)
	return strings.Contains(value, "already registered") || strings.Contains(value, "already added") || strings.Contains(value, "already exists")
}

func marketplaceAlreadyAdded(output string) bool {
	if alreadyRegistered(output) {
		return true
	}
	var value struct {
		AlreadyAdded bool `json:"alreadyAdded"`
	}
	return json.Unmarshal([]byte(output), &value) == nil && value.AlreadyAdded
}

func alreadyAbsent(output string) bool {
	value := strings.ToLower(output)
	return strings.Contains(value, "not found") ||
		strings.Contains(value, "not installed") ||
		strings.Contains(value, "not registered") ||
		strings.Contains(value, "does not exist") ||
		strings.Contains(value, "no such")
}
