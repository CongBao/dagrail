package contract

import (
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/mcpserver"
	"github.com/CongBao/dagrail/internal/projection"
	"github.com/CongBao/dagrail/internal/version"
	"github.com/CongBao/dagrail/sdk"
)

type VersionedSurface struct {
	APIVersion string `json:"apiVersion"`
	Stability  string `json:"stability"`
}

type JournalContract struct {
	ReadableSegmentSchemas []int `json:"readableSegmentSchemas"`
	WriteSegmentSchema     int   `json:"writeSegmentSchema"`
	WriteEventSchema       int   `json:"writeEventSchema"`
}

type ContextBudget struct {
	View  string `json:"view"`
	Bytes int    `json:"bytes"`
}

type Report struct {
	APIVersion string                   `json:"apiVersion"`
	Kind       string                   `json:"kind"`
	Version    string                   `json:"version"`
	Stability  string                   `json:"stability"`
	Graph      VersionedSurface         `json:"graph"`
	CLI        VersionedSurface         `json:"cli"`
	Provider   VersionedSurface         `json:"providerSdk"`
	Journal    JournalContract          `json:"journal"`
	Projection int                      `json:"projectionSchema"`
	MCP        []mcpserver.ToolContract `json:"mcpTools"`
	Contexts   []ContextBudget          `json:"contextBudgets"`
	Commands   []string                 `json:"topLevelCommands"`
	Promises   []string                 `json:"compatibilityPromises"`
}

func Current() Report {
	return Report{
		APIVersion: "dagrail.io/v1beta1",
		Kind:       "CompatibilityContract",
		Version:    version.Version,
		Stability:  "beta",
		Graph:      VersionedSurface{APIVersion: "dagrail.io/v1alpha1", Stability: "additive"},
		CLI:        VersionedSurface{APIVersion: "dagrail.io/cli/v1beta1", Stability: "additive"},
		Provider:   VersionedSurface{APIVersion: sdk.APIVersion, Stability: "source-compatible"},
		Journal: JournalContract{
			ReadableSegmentSchemas: []int{journal.LegacySegmentSchemaVersion, journal.CurrentSegmentSchemaVersion},
			WriteSegmentSchema:     journal.CurrentSegmentSchemaVersion,
			WriteEventSchema:       journal.CurrentEventSchemaVersion,
		},
		Projection: projection.CurrentSchemaVersion,
		MCP:        mcpserver.ToolContracts(),
		Contexts: []ContextBudget{
			{View: "orchestrator", Bytes: 12288},
			{View: "reviewer", Bytes: 12288},
			{View: "worker", Bytes: 8192},
		},
		Commands: []string{
			"action", "backup", "context", "contract", "doctor", "evidence", "frontier", "graph", "harness", "history", "hook", "incident", "init", "inspect", "journal", "mcp", "observe", "plugin", "pre-wait", "projection", "provider", "reconcile", "role", "signature", "status", "ui", "version",
		},
		Promises: []string{
			"journal history is never rewritten by an upgrade",
			"the six MCP tool names remain stable through the v0.x beta line",
			"stable provider interfaces receive only source-compatible additions through the v0.x beta line",
			"documented JSON fields are additive unless a new API version is selected",
			"SQLite remains disposable and rebuildable from the verified journal",
		},
	}
}
