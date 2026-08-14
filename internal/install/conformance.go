package install

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/harness"
	"github.com/CongBao/dagrail/sdk"
)

const PluginConformanceAPIVersion = "dagrail.io/plugin-conformance/v1alpha1"

type ConformanceRuntime struct {
	Verified bool   `json:"verified"`
	Version  string `json:"version,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
}

type ConformanceBundle struct {
	Verified bool   `json:"verified"`
	Version  string `json:"version"`
	Digest   string `json:"digest"`
	Files    int    `json:"files"`
}

type HarnessConformance struct {
	Harness          string                  `json:"harness"`
	Ready            bool                    `json:"ready"`
	Detected         bool                    `json:"detected"`
	PluginStatus     string                  `json:"pluginStatus"`
	MCPConfigured    bool                    `json:"mcpConfigured"`
	Version          string                  `json:"version,omitempty"`
	Capabilities     sdk.HarnessCapabilities `json:"capabilities"`
	NativeMode       string                  `json:"nativeMode,omitempty"`
	Protocol         string                  `json:"protocol,omitempty"`
	Stability        string                  `json:"stability,omitempty"`
	Execution        string                  `json:"execution,omitempty"`
	ReceiptProof     []string                `json:"receiptProof,omitempty"`
	ManualFallback   bool                    `json:"manualFallback"`
	UnavailableCodes []string                `json:"unavailableCodes,omitempty"`
}

type ConformanceReport struct {
	APIVersion string               `json:"apiVersion"`
	Kind       string               `json:"kind"`
	Ready      bool                 `json:"ready"`
	Runtime    ConformanceRuntime   `json:"runtime"`
	Bundle     ConformanceBundle    `json:"bundle"`
	Harnesses  []HarnessConformance `json:"harnesses"`
}

func Conformance(options Options) (ConformanceReport, error) {
	harnessIDs, err := normalizeHarnesses(options.Harnesses)
	if err != nil {
		return ConformanceReport{}, err
	}
	report := ConformanceReport{APIVersion: PluginConformanceAPIVersion, Kind: "PluginConformance", Ready: true}
	if runtimeStatus, err := RuntimeStatus(); err == nil {
		report.Runtime = ConformanceRuntime{Verified: true, Version: runtimeStatus.Version, SHA256: runtimeStatus.SHA256}
	} else {
		report.Ready = false
	}
	bundleStatus, err := PluginBundleStatus()
	if err != nil {
		return ConformanceReport{}, err
	}
	report.Bundle = ConformanceBundle{Verified: bundleStatus.Status == "verified", Version: bundleStatus.Version, Digest: bundleStatus.Digest, Files: bundleStatus.Files}
	if !report.Bundle.Verified {
		report.Ready = false
	}
	states, err := Status(Options{Harnesses: harnessIDs, RuntimePath: options.RuntimePath, MarketplaceSource: options.MarketplaceSource})
	if err != nil {
		return ConformanceReport{}, err
	}
	stateByHarness := map[string]Result{}
	for _, state := range states {
		stateByHarness[state.Harness] = state
	}
	for _, harnessID := range harnessIDs {
		adapter, err := harness.New(harnessID)
		if err != nil {
			return ConformanceReport{}, err
		}
		probe := adapter.ProbeResult()
		state := stateByHarness[harnessID]
		item := HarnessConformance{
			Harness:        harnessID,
			Detected:       probe.Detected,
			PluginStatus:   state.Status,
			MCPConfigured:  state.MCPConfigured,
			Version:        safeProbeVersion(probe.Version),
			Capabilities:   probe.Capabilities,
			NativeMode:     probe.NativeMode,
			Protocol:       probe.Protocol,
			Stability:      probe.Stability,
			Execution:      probe.Execution,
			ReceiptProof:   probe.ReceiptProof,
			ManualFallback: strings.TrimSpace(probe.Fallback) != "",
		}
		if !report.Runtime.Verified {
			item.UnavailableCodes = append(item.UnavailableCodes, "runtime_unverified")
		}
		if !report.Bundle.Verified {
			item.UnavailableCodes = append(item.UnavailableCodes, "bundle_unverified")
		}
		if !item.Detected {
			item.UnavailableCodes = append(item.UnavailableCodes, "harness_not_detected")
		}
		if item.PluginStatus != "installed" {
			item.UnavailableCodes = append(item.UnavailableCodes, "plugin_not_installed")
		}
		if !item.MCPConfigured {
			item.UnavailableCodes = append(item.UnavailableCodes, "mcp_not_configured")
		}
		if !item.ManualFallback {
			item.UnavailableCodes = append(item.UnavailableCodes, "manual_fallback_missing")
		}
		item.Ready = len(item.UnavailableCodes) == 0
		report.Ready = report.Ready && item.Ready
		report.Harnesses = append(report.Harnesses, item)
	}
	return report, nil
}

func safeProbeVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len([]byte(value)) > 256 || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == ' ' {
			continue
		}
		switch character {
		case '.', '-', '_', '+', ':', '(', ')':
		default:
			return ""
		}
	}
	raw, err := json.Marshal(map[string]string{"value": value})
	if err != nil || domain.RejectSensitiveFields(raw) != nil {
		return ""
	}
	return value
}
