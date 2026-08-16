package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/version"
)

func TestInstallUpgradesExistingBundledCodexMarketplaceWithoutManualUninstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fresh-host shell fixture")
	}
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	binRoot := filepath.Join(root, "bin")
	if err := os.MkdirAll(binRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAGRAIL_HOME", dataRoot)
	source := filepath.Join(root, "dagrail-source")
	runtimeScript := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = version ]; then echo '{\"version\":\"%s\"}'; exit 0; fi\nexit 1\n", version.Version)
	if err := os.WriteFile(source, []byte(runtimeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(binRoot, "dagrail")
	if _, err := installRuntimeFrom(context.Background(), source, runtimePath, dataRoot, runtimeVersion); err != nil {
		t.Fatal(err)
	}
	bundle, err := MaterializePluginBundle()
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "old-marketplace")
	logPath := filepath.Join(root, "host.log")
	if err := os.WriteFile(statePath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(binRoot, "codex")
	hostScript := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
if [ "$1 $2 $3" = "plugin marketplace add" ] && [ -f %q ]; then
  echo '{"alreadyAdded":true}'
  exit 1
fi
if [ "$1 $2 $3" = "plugin marketplace remove" ]; then
  rm -f %q
  echo '{}'
  exit 0
fi
case "$1 $2" in
  "plugin remove"|"plugin add"|"mcp remove"|"mcp add") echo '{}'; exit 0 ;;
esac
if [ "$1 $2 $3" = "plugin marketplace add" ]; then echo '{}'; exit 0; fi
exit 1
`, logPath, statePath, statePath)
	if err := os.WriteFile(codex, []byte(hostScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binRoot+string(os.PathListSeparator)+os.Getenv("PATH"))

	results, err := Install(context.Background(), Options{Harnesses: []string{"codex"}, RuntimePath: runtimePath, MarketplaceSource: bundle.Root})
	if err != nil || len(results) != 1 || results[0].Status != "installed_or_updated" || !results[0].MCPConfigured || !results[0].FreshSessionRequired || results[0].CurrentProcessVerified || results[0].Activation != "fresh-session-or-cli-fallback" {
		t.Fatalf("automated bundled upgrade failed: %+v, %v", results, err)
	}
	logRaw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logRaw)
	if strings.Contains(log, "marketplace upgrade") || strings.Count(log, "plugin marketplace add") != 2 || !strings.Contains(log, "plugin marketplace remove dagrail-bundled") || !strings.Contains(log, "plugin remove dagrail@dagrail-bundled") {
		t.Fatalf("host upgrade did not use the bounded local rotation: %s", log)
	}
}

func TestPluginStatusDoesNotClaimCurrentProcessActivation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	root := t.TempDir()
	binRoot := filepath.Join(root, "bin")
	if err := os.MkdirAll(binRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(binRoot, "codex")
	if err := os.WriteFile(codex, []byte(`#!/bin/sh
if [ "$1 $2" = "plugin list" ]; then echo '{"plugins":[{"id":"dagrail@dagrail"}]}'; exit 0; fi
if [ "$1 $2" = "mcp list" ]; then echo '{}'; exit 0; fi
exit 1
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binRoot+string(os.PathListSeparator)+os.Getenv("PATH"))
	results, err := Status(Options{Harnesses: []string{"codex"}, RuntimePath: filepath.Join(binRoot, "dagrail")})
	if err != nil || len(results) != 1 {
		t.Fatalf("status failed: %+v %v", results, err)
	}
	result := results[0]
	if result.Status != "installed" || !result.FreshSessionRequired || result.CurrentProcessVerified || result.Activation != "fresh-session-or-cli-fallback" {
		t.Fatalf("status overstated hot activation: %+v", result)
	}
}

func TestEnsureMarketplaceRotatesImmutableLocalBundle(t *testing.T) {
	plan := PlanResult{
		Harness:           "codex",
		MarketplaceLocal:  true,
		MarketplaceAdd:    []string{"plugin", "marketplace", "add", "/new/bundle", "--json"},
		MarketplaceUpdate: []string{"plugin", "marketplace", "upgrade", BundledMarketplaceName, "--json"},
		MarketplaceRemove: []string{"plugin", "marketplace", "remove", BundledMarketplaceName, "--json"},
		PluginRemove:      []string{"plugin", "remove", "dagrail@" + BundledMarketplaceName, "--json"},
	}
	var calls []string
	runner := func(_ context.Context, _ string, args ...string) (string, error) {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		switch len(calls) {
		case 1:
			return `{"alreadyAdded":true}`, errors.New("already registered")
		case 2, 3, 4:
			return `{}`, nil
		default:
			t.Fatalf("unexpected call: %s", call)
			return "", nil
		}
	}
	if err := ensureMarketplace(context.Background(), "/bin/codex", plan, runner); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"plugin marketplace add /new/bundle --json",
		"plugin remove dagrail@dagrail-bundled --json",
		"plugin marketplace remove dagrail-bundled --json",
		"plugin marketplace add /new/bundle --json",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("local marketplace rotation = %v, want %v", calls, want)
	}
	for _, call := range calls {
		if strings.Contains(call, "marketplace upgrade") {
			t.Fatalf("local immutable marketplace attempted an unsupported in-place upgrade: %v", calls)
		}
	}
}

func TestEnsureMarketplaceFailsClosedWhenLocalRotationCannotRegister(t *testing.T) {
	plan := PlanResult{MarketplaceLocal: true, MarketplaceAdd: []string{"add"}, MarketplaceRemove: []string{"remove"}, PluginRemove: []string{"uninstall"}}
	calls := 0
	runner := func(_ context.Context, _ string, _ ...string) (string, error) {
		calls++
		if calls == 1 {
			return "already exists", errors.New("exists")
		}
		if calls == 4 {
			return "permission denied", errors.New("denied")
		}
		return "", nil
	}
	if err := ensureMarketplace(context.Background(), "host", plan, runner); err == nil || !strings.Contains(err.Error(), "register rotated local marketplace") {
		t.Fatalf("local rotation did not surface its partial failure: %v", err)
	}
}

func TestEnsureMarketplaceFailsClosedWhenPluginCannotBeRemoved(t *testing.T) {
	plan := PlanResult{MarketplaceLocal: true, MarketplaceAdd: []string{"add"}, MarketplaceRemove: []string{"remove"}, PluginRemove: []string{"uninstall"}}
	calls := 0
	runner := func(_ context.Context, _ string, _ ...string) (string, error) {
		calls++
		if calls == 1 {
			return "already exists", errors.New("exists")
		}
		return "permission denied", errors.New("denied")
	}
	if err := ensureMarketplace(context.Background(), "host", plan, runner); err == nil || !strings.Contains(err.Error(), "remove plugin before") {
		t.Fatalf("local rotation ignored plugin removal failure: %v", err)
	}
	if calls != 2 {
		t.Fatalf("local rotation continued after plugin removal failure: %d calls", calls)
	}
}

func TestLocalBundleUpgradeConvergesAcrossHarnessesAndMutationFailures(t *testing.T) {
	root := t.TempDir()
	plans, err := Plan(Options{Harnesses: []string{"codex", "claude-code", "copilot-cli"}, RuntimePath: filepath.Join(root, "dagrail"), MarketplaceSource: filepath.Join(root, "bundle")})
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range plans {
		plan := plan
		for failAt := 1; failAt <= 6; failAt++ {
			failAt := failAt
			t.Run(fmt.Sprintf("%s/failure-%d", plan.Harness, failAt), func(t *testing.T) {
				marketplace := "old-bundle"
				pluginInstalled := true
				mcpLauncher := "old-runtime"
				mutations := 0
				failureInjected := false
				remoteUpdateCalled := false
				mutate := func() error {
					mutations++
					if mutations == failAt && !failureInjected {
						failureInjected = true
						return errors.New("injected post-side-effect failure")
					}
					return nil
				}
				runner := func(_ context.Context, _ string, args ...string) (string, error) {
					switch {
					case reflect.DeepEqual(args, plan.MarketplaceAdd):
						if marketplace != "" {
							return `{"alreadyAdded":true}`, errors.New("already exists")
						}
						marketplace = plan.MarketplaceAdd[3]
						return `{}`, mutate()
					case reflect.DeepEqual(args, plan.MarketplaceUpdate):
						remoteUpdateCalled = true
						return "", errors.New("local marketplace update must not run")
					case reflect.DeepEqual(args, plan.PluginRemove):
						if !pluginInstalled {
							return "not installed", errors.New("not installed")
						}
						pluginInstalled = false
						return `{}`, mutate()
					case reflect.DeepEqual(args, plan.MarketplaceRemove):
						if marketplace == "" {
							return "not found", errors.New("not found")
						}
						marketplace = ""
						return `{}`, mutate()
					case reflect.DeepEqual(args, plan.PluginInstall):
						pluginInstalled = true
						return `{}`, mutate()
					case reflect.DeepEqual(args, plan.MCPRemove):
						mcpLauncher = ""
						return `{}`, mutate()
					case reflect.DeepEqual(args, plan.MCPAdd):
						mcpLauncher = strings.Join(args, " ")
						return `{}`, mutate()
					default:
						return "", fmt.Errorf("unexpected host call: %v", args)
					}
				}

				firstErr := applyInstallPlan(context.Background(), "host", plan, runner)
				if firstErr == nil && !failureInjected {
					t.Fatalf("failure point %d was not exercised", failAt)
				}
				if err := applyInstallPlan(context.Background(), "host", plan, runner); err != nil {
					t.Fatalf("same install command did not converge after partial failure: %v", err)
				}
				if marketplace != filepath.Join(root, "bundle") || !pluginInstalled || !strings.Contains(mcpLauncher, filepath.Join(root, "dagrail")+" mcp --stdio") || remoteUpdateCalled {
					t.Fatalf("upgrade did not converge: marketplace=%q plugin=%t mcp=%q update=%t", marketplace, pluginInstalled, mcpLauncher, remoteUpdateCalled)
				}
			})
		}
	}
}
