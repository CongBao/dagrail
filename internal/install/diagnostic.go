package install

import (
	"context"
	"regexp"

	"github.com/CongBao/dagrail/internal/version"
)

const InstallationDiagnosticAPIVersion = "dagrail.io/installation-diagnostic/v1alpha1"

var diagnosticVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

type DiagnosticRuntime struct {
	Verified          bool   `json:"verified"`
	Version           string `json:"version,omitempty"`
	RollbackAvailable bool   `json:"rollbackAvailable"`
}

type DiagnosticBundle struct {
	Verified bool   `json:"verified"`
	Version  string `json:"version,omitempty"`
	Files    int    `json:"files,omitempty"`
}

type DiagnosticHarness struct {
	ID                     string `json:"id"`
	Detected               bool   `json:"detected"`
	PluginStatus           string `json:"pluginStatus"`
	MCPConfigured          bool   `json:"mcpConfigured"`
	ConfigurationReady     bool   `json:"configurationReady"`
	Ready                  bool   `json:"ready"`
	CurrentProcessVerified bool   `json:"currentProcessVerified"`
	Activation             string `json:"activation"`
}

type DiagnosticCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Code   string `json:"code"`
}

type InstallationDiagnostic struct {
	APIVersion string              `json:"apiVersion"`
	Kind       string              `json:"kind"`
	Version    string              `json:"version"`
	Healthy    bool                `json:"healthy"`
	Runtime    DiagnosticRuntime   `json:"runtime"`
	Bundle     DiagnosticBundle    `json:"bundle"`
	Harnesses  []DiagnosticHarness `json:"harnesses"`
	Checks     []DiagnosticCheck   `json:"checks"`
}

func Diagnose(ctx context.Context, options Options) (InstallationDiagnostic, error) {
	report := InstallationDiagnostic{
		APIVersion: InstallationDiagnosticAPIVersion,
		Kind:       "InstallationDiagnostic",
		Version:    version.Version,
		Healthy:    true,
		Harnesses:  []DiagnosticHarness{},
		Checks:     []DiagnosticCheck{},
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	add := func(id string, passed bool, passCode, failCode string) {
		status, code := "pass", passCode
		if !passed {
			status, code, report.Healthy = "fail", failCode, false
		}
		report.Checks = append(report.Checks, DiagnosticCheck{ID: id, Status: status, Code: code})
	}

	runtimeStatus, runtimeErr := RuntimeStatus()
	runtimeVersionSafe := runtimeErr == nil && diagnosticVersionPattern.MatchString(runtimeStatus.Version) && len(runtimeStatus.Version) <= 128
	report.Runtime = DiagnosticRuntime{Verified: runtimeVersionSafe, RollbackAvailable: runtimeVersionSafe && runtimeStatus.RollbackAvailable}
	if runtimeVersionSafe {
		report.Runtime.Version = runtimeStatus.Version
	}
	runtimeReady := report.Runtime.Verified && report.Runtime.Version == version.Version
	runtimeFailCode := "runtime_unavailable_or_unverified"
	if report.Runtime.Verified && !runtimeReady {
		runtimeFailCode = "runtime_version_mismatch"
	}
	add("runtime", runtimeReady, "runtime_verified", runtimeFailCode)

	bundleStatus, bundleErr := PluginBundleStatus()
	bundleVersionSafe := bundleErr == nil && diagnosticVersionPattern.MatchString(bundleStatus.Version) && len(bundleStatus.Version) <= 128
	report.Bundle = DiagnosticBundle{Verified: bundleVersionSafe && bundleStatus.Status == "verified"}
	if bundleVersionSafe {
		report.Bundle.Version, report.Bundle.Files = bundleStatus.Version, bundleStatus.Files
	}
	bundleReady := report.Bundle.Verified && report.Bundle.Version == version.Version
	bundleFailCode := "bundle_unavailable_or_unverified"
	if report.Bundle.Verified && !bundleReady {
		bundleFailCode = "bundle_version_mismatch"
	}
	add("bundle", bundleReady, "bundle_verified", bundleFailCode)

	statusOptions := options
	if statusOptions.RuntimePath == "" && runtimeReady {
		// Bind the harness probe to the exact runtime artifact already verified
		// from the signed local receipt. Doctor callers should not have to repeat
		// a path that the controller can authenticate itself.
		statusOptions.RuntimePath = runtimeStatus.RuntimePath
	}
	states, err := StatusContext(ctx, statusOptions)
	if err != nil {
		return report, err
	}
	for _, state := range states {
		detected := state.Status != "not_detected"
		item := DiagnosticHarness{ID: state.Harness, Detected: detected, PluginStatus: state.Status, MCPConfigured: state.MCPConfigured, CurrentProcessVerified: false, Activation: "fresh-session-or-cli-fallback"}
		item.ConfigurationReady = detected && state.Status == "installed" && state.MCPConfigured
		// v1alpha1 Ready is activation readiness, not persisted registration
		// health. No supported probe proves the caller's already-running harness
		// loaded the new MCP registration, so it remains false.
		item.Ready = item.ConfigurationReady && item.CurrentProcessVerified
		report.Harnesses = append(report.Harnesses, item)
		add("harness."+state.Harness+".detected", detected, "harness_detected", "harness_not_detected")
		add("harness."+state.Harness+".plugin", state.Status == "installed", "plugin_installed", "plugin_not_installed")
		add("harness."+state.Harness+".mcp", state.MCPConfigured, "mcp_configured", "mcp_not_configured")
	}
	return report, nil
}
