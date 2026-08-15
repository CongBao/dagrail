package contract

import (
	"github.com/CongBao/dagrail/internal/commandcatalog"
	"github.com/CongBao/dagrail/internal/compatibility"
	"github.com/CongBao/dagrail/internal/install"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/mcpserver"
	"github.com/CongBao/dagrail/internal/projection"
	dagrelease "github.com/CongBao/dagrail/internal/release"
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

// GraphSurface makes graph capabilities discoverable without requiring a
// consumer to infer support from a harness adapter, prompt, or example file.
type GraphSurface struct {
	APIVersion   string   `json:"apiVersion"`
	Stability    string   `json:"stability"`
	SchemaPath   string   `json:"schemaPath"`
	SchemaSHA256 string   `json:"schemaSha256"`
	Capabilities []string `json:"capabilities"`
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
	Graph                GraphSurface             `json:"graph"`
	CLI                  VersionedSurface         `json:"cli"`
	CommandCatalog       DocumentedSurface        `json:"commandCatalog"`
	CLIError             DocumentedSurface        `json:"cliError"`
	DecisionRecord       DocumentedSurface        `json:"decisionRecord"`
	Installation         DocumentedSurface        `json:"installationDiagnostic"`
	HistoricalMatrix     DocumentedSurface        `json:"historicalBinaryMatrix"`
	Readiness            DocumentedSurface        `json:"readinessDecision"`
	UI                   DocumentedSurface        `json:"ui"`
	Security             DocumentedSurface        `json:"security"`
	JournalVerification  DocumentedSurface        `json:"journalVerification"`
	PluginConformance    DocumentedSurface        `json:"pluginConformance"`
	Support              DocumentedSurface        `json:"support"`
	Recovery             DocumentedSurface        `json:"recovery"`
	ReleaseQualification DocumentedSurface        `json:"releaseQualification"`
	ReleaseManifest      DocumentedSurface        `json:"releaseManifest"`
	ReleaseVerification  DocumentedSurface        `json:"releaseVerification"`
	LifecycleMigration   DocumentedSurface        `json:"lifecycleMigration"`
	LifecycleProjection  DocumentedSurface        `json:"lifecycleProjection"`
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
		Graph: GraphSurface{
			APIVersion:   "dagrail.io/v1alpha1",
			Stability:    "additive",
			SchemaPath:   "schemas/graph-v1alpha1.schema.json",
			SchemaSHA256: "sha256:33282af1ce75d9cc0ab2bf570144ff0e80ff7ec52599327851a7dd0107793c9e",
			Capabilities: []string{"dynamic-graph", "historical-lifecycle-import", "lifecycle-projection", "positive-predicate-ast", "resource-capacities", "resource-requests", "role-leases"},
		},
		CLI: VersionedSurface{APIVersion: "dagrail.io/cli/v1beta1", Stability: "additive"},
		CommandCatalog: DocumentedSurface{
			APIVersion:   commandcatalog.APIVersion,
			Stability:    "additive-machine-discovery",
			SchemaPath:   "schemas/command-catalog-v1alpha1.schema.json",
			SchemaSHA256: "sha256:1d9b40770dca0a7bdd382de612f866609bc454f631f6fbc23aecbbf610bc792d",
		},
		CLIError: DocumentedSurface{
			APIVersion:   "dagrail.io/cli-error/v1alpha1",
			Stability:    "additive-opt-in-errors",
			SchemaPath:   "schemas/cli-error-v1alpha1.schema.json",
			SchemaSHA256: "sha256:ce7c541fa46c92182cbeef29a181efec5f6fd70081d9a0048e1839d2eb531ff1",
		},
		DecisionRecord: DocumentedSurface{
			APIVersion:   "dagrail.io/decision-record/v1alpha1",
			Stability:    "additive-journal-authority",
			SchemaPath:   "schemas/decision-record-v1alpha1.schema.json",
			SchemaSHA256: "sha256:976d23474541ed2ad4715bcebd4da7b01d2664d6d10282d1d835ab0c15fb8fb0",
		},
		Installation: DocumentedSurface{
			APIVersion:   install.InstallationDiagnosticAPIVersion,
			Stability:    "additive-local-diagnostic",
			SchemaPath:   "schemas/installation-diagnostic-v1alpha1.schema.json",
			SchemaSHA256: "sha256:575f4d580dacc1b81c8ab5787d0d4c2e1904927beb5449a850d3ce35e60a3942",
		},
		HistoricalMatrix: DocumentedSurface{
			APIVersion:   compatibility.APIVersion,
			Stability:    "closed-beta-window",
			SchemaPath:   "schemas/historical-binary-matrix-v1alpha1.schema.json",
			SchemaSHA256: "sha256:88f4cf5f44ef604698d58f315696291fd8d94ea00a26ba699e5b94be3b570f7c",
		},
		Readiness: DocumentedSurface{
			APIVersion:   "dagrail.io/readiness-decision/v1alpha1",
			Stability:    "additive-structural-decision",
			SchemaPath:   "schemas/readiness-decision-v1alpha1.schema.json",
			SchemaSHA256: "sha256:3accab79286031db41164b8dee5217ba8295ddd8af066101101e6e55fa500565",
		},
		UI: DocumentedSurface{
			APIVersion:   "dagrail.io/ui/v1beta1",
			Stability:    "additive-read-only",
			SchemaPath:   "schemas/ui-api-v1beta1.schema.json",
			SchemaSHA256: "sha256:4c2723b7600dcf95a05ce501ba110109df3aedfabe8c3d541d346d9e0a04dac0",
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
			SchemaSHA256: "sha256:ea770c501e2764bf266ce89f554c8816f57d39ffd38a4c968119eb1999020e47",
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
			SchemaSHA256: "sha256:6a7d06ede063f82112389f0f801a8a756088175fb4c93a7527bda55fce9c5ea9",
		},
		ReleaseManifest: DocumentedSurface{
			APIVersion:   dagrelease.ManifestAPIVersion,
			Stability:    "additive-distribution-contract",
			SchemaPath:   "schemas/release-manifest-v1beta1.schema.json",
			SchemaSHA256: "sha256:cb04a29967fbe1a150c0dbc1f9d780cc5d7f494680a5d1eae173728ea4c9980f",
		},
		ReleaseVerification: DocumentedSurface{
			APIVersion:   dagrelease.VerificationAPIVersion,
			Stability:    "additive-offline-verification",
			SchemaPath:   "schemas/release-verification-v1alpha1.schema.json",
			SchemaSHA256: "sha256:9a23a60cdef7b2444f5deb0ed802935b1f2052aea3156582eb3e0244989cb283",
		},
		LifecycleMigration: DocumentedSurface{
			APIVersion:   service.LifecycleMigrationAPIVersion,
			Stability:    "alpha-operator-import",
			SchemaPath:   "schemas/lifecycle-migration-v1alpha1.schema.json",
			SchemaSHA256: "sha256:ba1ba1b15c72624a9ffe3d15e6397655016c73665a390b5881d80fa2e47a827e",
		},
		LifecycleProjection: DocumentedSurface{
			APIVersion:   service.LifecycleProjectionAPIVersion,
			Stability:    "alpha-rebuildable-projection",
			SchemaPath:   "schemas/lifecycle-projection-v1alpha1.schema.json",
			SchemaSHA256: "sha256:a4bcbeca02e9649c18e9ebad13e1cc8d0b1ed7f11de6f1c92a2a23c899615514",
		},
		Provider: VersionedSurface{APIVersion: sdk.APIVersion, Stability: "source-compatible"},
		Journal: JournalContract{
			ReadableSegmentSchemas: []int{journal.LegacySegmentSchemaVersion, journal.PreviousSegmentSchemaVersion, journal.CurrentSegmentSchemaVersion},
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
		Commands: commandcatalog.Names(),
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
			"published release sets bind exactly six archives, six SPDX inventories, sorted checksums, source identity, and bounded offline verification",
			"command discovery and completion are generated from one bounded catalog",
			"opt-in CLI error envelopes keep stable broad exit classes and preserve interruption",
			"host plugin commands are output-bounded, time-bounded, and cancellation-aware",
			"the v0.10 through v0.20 beta binaries are immutable inputs to the current upgrade and rollback matrix",
			"readiness can declare external-validation readiness but cannot infer production validation or 1.0 readiness",
			"the loopback explorer rejects non-loopback Host values and cross-port Origin values without exposing CORS access",
			"SQLite remains disposable and rebuildable from the verified journal",
			"semantic and provider decisions are immutable revision-bound records rather than chat or opaque provider output",
			"role capabilities are enforced at every lifecycle mutation boundary while older Graph definitions remain importable",
			"resource capacity is released only after a confirmed closure receipt; ambiguous closure remains reconcilable",
			"new mutation idempotency keys bind command kind, actor, object, and canonical request intent; changed retries fail closed",
			"plugin conformance verifies that the textual hook launcher resolves to the same fresh-process runtime used for MCP",
			"the explorer exposes typed decision and resource closure summaries without decision facts, evidence URIs, or receipt bodies",
			"graph capability discovery is schema-bound and does not depend on harness adapter inference",
			"historical lifecycle import accepts only one authenticated complete source prefix into a pristine graph-only project",
			"historical lifecycle preflight preserves current writer ready, capability, lease, causal time, decision, resource, incident, and effect-prefix invariants",
			"lifecycle projection omits action inputs and external evidence locators and digests effect and resource receipt bodies",
		},
	}
}
