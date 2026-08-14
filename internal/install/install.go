package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/version"
)

const (
	MarketplaceName          = "dagrail"
	PluginID                 = "dagrail@dagrail"
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
	PluginInstall     []string `json:"pluginInstall"`
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
	Status      string `json:"status"`
	Version     string `json:"version"`
	RuntimePath string `json:"runtimePath"`
	SHA256      string `json:"sha256"`
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
		switch harness {
		case "codex":
			plan.MarketplaceAdd = []string{"plugin", "marketplace", "add", options.MarketplaceSource, "--ref", "main", "--json"}
			plan.MarketplaceUpdate = []string{"plugin", "marketplace", "upgrade", MarketplaceName, "--json"}
			plan.PluginInstall = []string{"plugin", "add", PluginID, "--json"}
			plan.MCPRemove = []string{"mcp", "remove", MarketplaceName}
			plan.MCPAdd = []string{"mcp", "add", MarketplaceName, "--", options.RuntimePath, "mcp", "--stdio"}
		case "claude-code":
			plan.MarketplaceAdd = []string{"plugin", "marketplace", "add", options.MarketplaceSource}
			plan.MarketplaceUpdate = []string{"plugin", "marketplace", "update", MarketplaceName}
			plan.PluginInstall = []string{"plugin", "install", PluginID, "--scope", "user"}
			plan.MCPRemove = []string{"mcp", "remove", MarketplaceName}
			plan.MCPAdd = []string{"mcp", "add", "--transport", "stdio", "--scope", "user", MarketplaceName, "--", options.RuntimePath, "mcp", "--stdio"}
		case "copilot-cli":
			plan.MarketplaceAdd = []string{"plugin", "marketplace", "add", options.MarketplaceSource}
			plan.MarketplaceUpdate = []string{"plugin", "marketplace", "update", MarketplaceName}
			plan.PluginInstall = []string{"plugin", "install", PluginID}
			plan.MCPRemove = []string{"mcp", "remove", MarketplaceName}
			plan.MCPAdd = []string{"mcp", "add", MarketplaceName, "--timeout", "10000", "--", options.RuntimePath, "mcp", "--stdio"}
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
		if err := validateFreshRuntime(ctx, options.RuntimePath); err != nil {
			results = append(results, Result{Harness: plan.Harness, Status: "failed", Executable: executable, Message: err.Error()})
			failures = append(failures, err)
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
	harnesses, err := normalizeHarnesses(options.Harnesses)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(harnesses))
	for _, harness := range harnesses {
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
			pluginOutput, pluginErr := run(context.Background(), path, pluginArgs...)
			if pluginErr == nil && strings.Contains(strings.ToLower(pluginOutput), "dagrail") {
				status = "installed"
			} else if pluginErr != nil {
				message = "plugin status probe unavailable: " + pluginErr.Error()
			}
			mcpOutput, mcpErr := run(context.Background(), path, "mcp", "list")
			mcpConfigured = mcpErr == nil && strings.Contains(strings.ToLower(mcpOutput), "dagrail")
		}
		results = append(results, Result{Harness: harness, Status: status, Executable: path, MCPConfigured: mcpConfigured, Message: message})
	}
	return results, nil
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
		var args []string
		switch plan.Harness {
		case "codex":
			args = []string{"plugin", "remove", "dagrail"}
		case "claude-code":
			args = []string{"plugin", "uninstall", PluginID, "--scope", "user"}
		case "copilot-cli":
			args = []string{"plugin", "uninstall", "dagrail"}
		}
		if _, runErr := run(ctx, executable, args...); runErr != nil {
			failures = append(failures, runErr)
			results = append(results, Result{Harness: plan.Harness, Status: "failed", Executable: executable, Message: runErr.Error()})
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

func InstallRuntime() (RuntimeResult, error) {
	source, err := os.Executable()
	if err != nil {
		return RuntimeResult{}, err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return RuntimeResult{}, err
	}
	destination, err := DefaultRuntimePath()
	if err != nil {
		return RuntimeResult{}, err
	}
	sourceDigest, err := fileSHA256(source)
	if err != nil {
		return RuntimeResult{}, err
	}
	status := "installed"
	if destinationDigest, digestErr := fileSHA256(destination); digestErr == nil && destinationDigest == sourceDigest {
		status = "noop"
	} else {
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return RuntimeResult{}, err
		}
		temporary, err := os.CreateTemp(filepath.Dir(destination), ".dagrail-*.tmp")
		if err != nil {
			return RuntimeResult{}, err
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		sourceFile, err := os.Open(source)
		if err != nil {
			_ = temporary.Close()
			return RuntimeResult{}, err
		}
		_, copyErr := io.Copy(temporary, sourceFile)
		closeSourceErr := sourceFile.Close()
		syncErr := temporary.Sync()
		closeErr := temporary.Close()
		if err := errors.Join(copyErr, closeSourceErr, syncErr, closeErr); err != nil {
			return RuntimeResult{}, err
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(temporaryPath, 0o755); err != nil {
				return RuntimeResult{}, err
			}
		}
		if err := os.Rename(temporaryPath, destination); err != nil {
			if removeErr := os.Remove(destination); removeErr != nil && !os.IsNotExist(removeErr) {
				return RuntimeResult{}, fmt.Errorf("replace runtime: %w", err)
			}
			if err := os.Rename(temporaryPath, destination); err != nil {
				return RuntimeResult{}, fmt.Errorf("publish runtime: %w", err)
			}
		}
	}
	if err := validateFreshRuntime(context.Background(), destination); err != nil {
		return RuntimeResult{}, err
	}
	if err := writeRuntimeReceipt(destination, sourceDigest); err != nil {
		return RuntimeResult{}, err
	}
	return RuntimeResult{Status: status, Version: version.Version, RuntimePath: destination, SHA256: sourceDigest}, nil
}

func validateFreshRuntime(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("runtime path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("runtime %s is not a regular executable file", path)
	}
	command := exec.CommandContext(ctx, path, "version")
	command.Env = append(os.Environ(), "DAGRAIL_FRESH_PROCESS_PROBE=1")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("fresh runtime probe failed: %w", err)
	}
	var value struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(output, &value); err != nil || value.Version == "" {
		return fmt.Errorf("fresh runtime probe returned an invalid version envelope")
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeRuntimeReceipt(runtimePath, digest string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := os.Getenv("DAGRAIL_HOME")
	if root == "" {
		switch runtime.GOOS {
		case "darwin":
			root = filepath.Join(home, "Library", "Application Support", "dagrail")
		case "windows":
			root = os.Getenv("LOCALAPPDATA")
			if root == "" {
				root = filepath.Join(home, "AppData", "Local")
			}
			root = filepath.Join(root, "DAGrail")
		default:
			root = os.Getenv("XDG_DATA_HOME")
			if root == "" {
				root = filepath.Join(home, ".local", "share")
			}
			root = filepath.Join(root, "dagrail")
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	data, _ := json.Marshal(map[string]any{"schemaVersion": 1, "version": version.Version, "runtimePath": runtimePath, "sha256": digest, "installedAt": time.Now().UTC().Format(time.RFC3339Nano)})
	return os.WriteFile(filepath.Join(root, "install-receipt.json"), data, 0o600)
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
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
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
