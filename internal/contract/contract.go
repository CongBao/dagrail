package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	dagbundle "github.com/CongBao/dagrail"
	"github.com/CongBao/dagrail/internal/commandcatalog"
	"github.com/CongBao/dagrail/internal/compatibility"
	"github.com/CongBao/dagrail/internal/gitartifact"
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
	APIVersion                 string                   `json:"apiVersion"`
	Kind                       string                   `json:"kind"`
	Version                    string                   `json:"version"`
	Stability                  string                   `json:"stability"`
	Graph                      GraphSurface             `json:"graph"`
	GraphPatch                 DocumentedSurface        `json:"graphPatch"`
	CLI                        VersionedSurface         `json:"cli"`
	CommandCatalog             DocumentedSurface        `json:"commandCatalog"`
	CLIError                   DocumentedSurface        `json:"cliError"`
	DecisionRecord             DocumentedSurface        `json:"decisionRecord"`
	Installation               DocumentedSurface        `json:"installationDiagnostic"`
	HistoricalMatrix           DocumentedSurface        `json:"historicalBinaryMatrix"`
	Readiness                  DocumentedSurface        `json:"readinessDecision"`
	UI                         DocumentedSurface        `json:"ui"`
	Security                   DocumentedSurface        `json:"security"`
	JournalVerification        DocumentedSurface        `json:"journalVerification"`
	PluginConformance          DocumentedSurface        `json:"pluginConformance"`
	Support                    DocumentedSurface        `json:"support"`
	Recovery                   DocumentedSurface        `json:"recovery"`
	AuthorityAdoption          DocumentedSurface        `json:"authorityAdoption"`
	AuthorityRotation          DocumentedSurface        `json:"authorityRotation"`
	AuthorityRelocation        DocumentedSurface        `json:"authorityRelocation"`
	GitArtifactClosure         DocumentedSurface        `json:"gitArtifactClosure"`
	GitIntegrationScope        DocumentedSurface        `json:"gitIntegrationScope"`
	ReleaseQualification       DocumentedSurface        `json:"releaseQualification"`
	ReleaseManifest            DocumentedSurface        `json:"releaseManifest"`
	ReleaseVerification        DocumentedSurface        `json:"releaseVerification"`
	LifecycleMigrationV1Alpha1 DocumentedSurface        `json:"lifecycleMigrationV1Alpha1"`
	LifecycleMigration         DocumentedSurface        `json:"lifecycleMigration"`
	LifecycleProjection        DocumentedSurface        `json:"lifecycleProjection"`
	Provider                   VersionedSurface         `json:"providerSdk"`
	Journal                    JournalContract          `json:"journal"`
	Projection                 int                      `json:"projectionSchema"`
	MCP                        []mcpserver.ToolContract `json:"mcpTools"`
	Contexts                   []ContextBudget          `json:"contextBudgets"`
	Commands                   []string                 `json:"topLevelCommands"`
	Promises                   []string                 `json:"compatibilityPromises"`
}

func schemaDigest(path string) string {
	raw, err := dagbundle.SchemaFS.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("embedded public schema %s: %v", path, err))
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func documentedSurface(apiVersion, stability, schemaPath string) DocumentedSurface {
	return DocumentedSurface{
		APIVersion: apiVersion, Stability: stability, SchemaPath: schemaPath,
		SchemaSHA256: schemaDigest(schemaPath),
	}
}

func Current() Report {
	limits := service.ContextBudgetLimits()
	contextBudgets := make([]ContextBudget, 0, len(limits))
	for _, limit := range limits {
		contextBudgets = append(contextBudgets, ContextBudget{View: limit.View, Bytes: limit.Bytes})
	}
	return Report{
		APIVersion: "dagrail.io/v1beta1",
		Kind:       "CompatibilityContract",
		Version:    version.Version,
		Stability:  "beta",
		Graph: GraphSurface{
			APIVersion:   "dagrail.io/v1alpha1",
			Stability:    "additive",
			SchemaPath:   "schemas/graph-v1alpha1.schema.json",
			SchemaSHA256: schemaDigest("schemas/graph-v1alpha1.schema.json"),
			Capabilities: []string{"declared-lanes", "dynamic-graph", "hierarchical-subgraphs", "historical-lifecycle-import", "lifecycle-projection", "positive-predicate-ast", "resource-capacities", "resource-requests", "role-leases"},
		},
		GraphPatch:                 documentedSurface("dagrail.io/v1alpha1", "additive-two-phase-change", "schemas/graph-patch-v1alpha1.schema.json"),
		CLI:                        VersionedSurface{APIVersion: "dagrail.io/cli/v1beta1", Stability: "additive"},
		CommandCatalog:             documentedSurface(commandcatalog.APIVersion, "additive-machine-discovery", "schemas/command-catalog-v1alpha1.schema.json"),
		CLIError:                   documentedSurface("dagrail.io/cli-error/v1alpha1", "additive-opt-in-errors", "schemas/cli-error-v1alpha1.schema.json"),
		DecisionRecord:             documentedSurface("dagrail.io/decision-record/v1alpha1", "additive-journal-authority", "schemas/decision-record-v1alpha1.schema.json"),
		Installation:               documentedSurface(install.InstallationDiagnosticAPIVersion, "additive-local-diagnostic", "schemas/installation-diagnostic-v1alpha1.schema.json"),
		HistoricalMatrix:           documentedSurface(compatibility.APIVersion, "closed-beta-window", "schemas/historical-binary-matrix-v1alpha1.schema.json"),
		Readiness:                  documentedSurface("dagrail.io/readiness-decision/v1alpha1", "additive-structural-decision", "schemas/readiness-decision-v1alpha1.schema.json"),
		UI:                         documentedSurface("dagrail.io/ui/v1beta3", "additive-read-only", "schemas/ui-api-v1beta3.schema.json"),
		Security:                   documentedSurface("dagrail.io/security/v1alpha1", "additive-local-audit", "schemas/security-audit-v1alpha1.schema.json"),
		JournalVerification:        documentedSurface("dagrail.io/journal-verification/v1alpha1", "additive-read-only", "schemas/journal-verification-v1alpha1.schema.json"),
		PluginConformance:          documentedSurface(install.PluginConformanceAPIVersion, "additive-local-diagnostic", "schemas/plugin-conformance-v1alpha1.schema.json"),
		Support:                    documentedSurface(service.SupportAPIVersion, "additive-shareable-diagnostic", "schemas/support-report-v1alpha1.schema.json"),
		Recovery:                   documentedSurface(service.RecoveryAPIVersion, "additive-read-only-rehearsal", "schemas/recovery-rehearsal-v1alpha1.schema.json"),
		AuthorityAdoption:          documentedSurface(service.AuthorityAdoptionAPIVersion, "alpha-fresh-identity-migration", "schemas/authority-adoption-v1alpha1.schema.json"),
		AuthorityRotation:          documentedSurface(service.AuthorityRotationAPIVersion, "alpha-non-destructive-recovery", "schemas/authority-rotation-v1alpha1.schema.json"),
		AuthorityRelocation:        documentedSurface(service.AuthorityRelocationAPIVersion, "alpha-path-bound-recovery", "schemas/authority-relocation-v1alpha1.schema.json"),
		GitArtifactClosure:         documentedSurface(gitartifact.ClosureAPIVersion, "alpha-read-only-git-evidence", "schemas/git-artifact-closure-v1alpha1.schema.json"),
		GitIntegrationScope:        documentedSurface(gitartifact.ScopeAPIVersion, "alpha-read-only-git-evidence", "schemas/git-integration-scope-v1alpha1.schema.json"),
		ReleaseQualification:       documentedSurface("dagrail.io/release-qualification/v1alpha1", "additive-structural-candidate", "schemas/release-qualification-v1alpha1.schema.json"),
		ReleaseManifest:            documentedSurface(dagrelease.ManifestAPIVersion, "additive-distribution-contract", "schemas/release-manifest-v1beta1.schema.json"),
		ReleaseVerification:        documentedSurface(dagrelease.VerificationAPIVersion, "additive-offline-verification", "schemas/release-verification-v1alpha1.schema.json"),
		LifecycleMigrationV1Alpha1: documentedSurface(service.LifecycleMigrationAPIVersion, "alpha-operator-import-compatible", "schemas/lifecycle-migration-v1alpha1.schema.json"),
		LifecycleMigration:         documentedSurface(service.LifecycleMigrationBundleAPIVersion, "alpha-operator-import", "schemas/lifecycle-migration-v1beta1.schema.json"),
		LifecycleProjection:        documentedSurface(service.LifecycleProjectionAPIVersion, "alpha-rebuildable-projection", "schemas/lifecycle-projection-v1alpha1.schema.json"),
		Provider:                   VersionedSurface{APIVersion: sdk.APIVersion, Stability: "source-compatible"},
		Journal: JournalContract{
			ReadableSegmentSchemas: []int{journal.LegacySegmentSchemaVersion, journal.PreviousSegmentSchemaVersion, journal.CurrentSegmentSchemaVersion, journal.AuthorityFenceSchemaVersion},
			WriteSegmentSchema:     journal.CurrentSegmentSchemaVersion,
			WriteEventSchema:       journal.CurrentEventSchemaVersion,
		},
		Projection: projection.CurrentSchemaVersion,
		MCP:        mcpserver.ToolContracts(),
		Contexts:   contextBudgets,
		Commands:   commandcatalog.Names(),
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
			"the v0.10.0 through v0.26.4 beta binaries are immutable inputs to the current upgrade and rollback matrix",
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
			"lifecycle migration v1beta1 preserves one source record while independently closing every ordered command proof ledger",
			"authority rotation creates a fresh Project identity and never truncates or rewrites the previous journal",
			"authority rotation appends one terminal fence while preserving every previous segment byte",
			"authority establishment and retirement fences use readable segment schema 4 so v0.21 writers reject them during the locked append reread",
			"portable backup or runtime-directory copy cannot recreate the same writable Project UUID without a canonical-path-bound local authority claim",
			"ordinary project open never creates a writer claim; pre-v0.22 adoption is explicit and exact-head-bound, retires the legacy UUID, and establishes a fenced fresh identity before locator publication",
			"Project v1alpha1 remains readable by the v0.21 rollback binary after authority rotation",
			"plugin registration never claims an already-running harness loaded MCP without process-visible proof",
			"historical lifecycle preflight preserves current writer ready, capability, lease, causal time, decision, resource, incident, and effect-prefix invariants",
			"lifecycle projection omits action inputs and external evidence locators and digests effect and resource receipt bodies",
		},
	}
}
