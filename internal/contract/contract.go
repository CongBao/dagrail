package contract

import (
	"github.com/CongBao/dagrail/internal/install"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/mcpserver"
	"github.com/CongBao/dagrail/internal/projection"
	"github.com/CongBao/dagrail/internal/service"
	"github.com/CongBao/dagrail/internal/version"
	"github.com/CongBao/dagrail/sdk"
)

type VersionedSurface struct {
	APIVersion string `json:"apiVersion"`
	Stability  string `json:"stability"`
}

type DocumentedSurface struct {
	APIVersion   string `json:"apiVersion"`
	Stability    string `json:"stability"`
	SchemaPath   string `json:"schemaPath"`
	SchemaSHA256 string `json:"schemaSha256"`
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
	APIVersion           string                   `json:"apiVersion"`
	Kind                 string                   `json:"kind"`
	Version              string                   `json:"version"`
	Stability            string                   `json:"stability"`
	Graph                VersionedSurface         `json:"graph"`
	CLI                  VersionedSurface         `json:"cli"`
	UI                   DocumentedSurface        `json:"ui"`
	Security             DocumentedSurface        `json:"security"`
	JournalVerification  DocumentedSurface        `json:"journalVerification"`
	PluginConformance    DocumentedSurface        `json:"pluginConformance"`
	Support              DocumentedSurface        `json:"support"`
	Recovery             DocumentedSurface        `json:"recovery"`
	ReleaseQualification DocumentedSurface        `json:"releaseQualification"`
	Provider             VersionedSurface         `json:"providerSdk"`
	Journal              JournalContract          `json:"journal"`
	Projection           int                      `json:"projectionSchema"`
	MCP                  []mcpserver.ToolContract `json:"mcpTools"`
	Contexts             []ContextBudget          `json:"contextBudgets"`
	Commands             []string                 `json:"topLevelCommands"`
	Promises             []string                 `json:"compatibilityPromises"`
}

func Current() Report {
	return Report{
		APIVersion: "dagrail.io/v1beta1",
		Kind:       "CompatibilityContract",
		Version:    version.Version,
		Stability:  "beta",
		Graph:      VersionedSurface{APIVersion: "dagrail.io/v1alpha1", Stability: "additive"},
		CLI:        VersionedSurface{APIVersion: "dagrail.io/cli/v1beta1", Stability: "additive"},
		UI: DocumentedSurface{
			APIVersion:   "dagrail.io/ui/v1beta1",
			Stability:    "additive-read-only",
			SchemaPath:   "schemas/ui-api-v1beta1.schema.json",
			SchemaSHA256: "sha256:8831e13abdd73698f75e0f97b406f0cfba96c055a31223494272b6d69f0dd5d4",
		},
		Security: DocumentedSurface{
			APIVersion:   "dagrail.io/security/v1alpha1",
			Stability:    "additive-local-audit",
			SchemaPath:   "schemas/security-audit-v1alpha1.schema.json",
			SchemaSHA256: "sha256:134f80395106519ded01e9bb1c7ac518e3bc36fd415c106610d453e8a6b8597a",
		},
		JournalVerification: DocumentedSurface{
			APIVersion:   "dagrail.io/journal-verification/v1alpha1",
			Stability:    "additive-read-only",
			SchemaPath:   "schemas/journal-verification-v1alpha1.schema.json",
			SchemaSHA256: "sha256:2a05bc82ce706e9744745fc2aee3a32f52498dc47971dc4131a416983c0782c4",
		},
		PluginConformance: DocumentedSurface{
			APIVersion:   install.PluginConformanceAPIVersion,
			Stability:    "additive-local-diagnostic",
			SchemaPath:   "schemas/plugin-conformance-v1alpha1.schema.json",
			SchemaSHA256: "sha256:244b31c6daf83cd4451d15089c7a8db4027fd761f58aefc6c114ff9379ad829b",
		},
		Support: DocumentedSurface{
			APIVersion:   service.SupportAPIVersion,
			Stability:    "additive-shareable-diagnostic",
			SchemaPath:   "schemas/support-report-v1alpha1.schema.json",
			SchemaSHA256: "sha256:d8cbae42d6387e8e0d63eea11c7b2980d9518498a03845ff8f8a05afc4dc9806",
		},
		Recovery: DocumentedSurface{
			APIVersion:   service.RecoveryAPIVersion,
			Stability:    "additive-read-only-rehearsal",
			SchemaPath:   "schemas/recovery-rehearsal-v1alpha1.schema.json",
			SchemaSHA256: "sha256:8a465d234f701ee98247117021f92657f603161aeb16f00a3ae3c1537e58b514",
		},
		ReleaseQualification: DocumentedSurface{
			APIVersion:   "dagrail.io/release-qualification/v1alpha1",
			Stability:    "additive-structural-candidate",
			SchemaPath:   "schemas/release-qualification-v1alpha1.schema.json",
			SchemaSHA256: "sha256:a01d53232ed16fa69f7da2d08156bb0113ae0679a9c396428bb931be03d738f3",
		},
		Provider: VersionedSurface{APIVersion: sdk.APIVersion, Stability: "source-compatible"},
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
			"action", "backup", "context", "contract", "doctor", "evidence", "frontier", "graph", "harness", "history", "hook", "incident", "init", "inspect", "journal", "mcp", "observe", "plugin", "pre-wait", "projection", "provider", "qualify", "reconcile", "recovery", "role", "security", "signature", "status", "support", "ui", "version",
		},
		Promises: []string{
			"journal history is never rewritten by an upgrade",
			"the six MCP tool names remain stable through the v0.x beta line",
			"stable provider interfaces receive only source-compatible additions through the v0.x beta line",
			"documented JSON fields are additive unless a new API version is selected",
			"the v1beta1 explorer remains loopback-only and exposes no lifecycle mutation route",
			"security audit diagnostics omit authority payloads and absolute project paths",
			"the local security boundary does not claim malicious same-user or multi-tenant isolation",
			"support reports contain no authority payloads, absolute paths, prompts, artifact bodies, or harness output",
			"recovery rehearsal writes only to disposable storage and binds replay and projection evidence to one immutable journal head",
			"release qualification distinguishes structural candidate evidence from outstanding production adoption evidence",
			"SQLite remains disposable and rebuildable from the verified journal",
		},
	}
}
