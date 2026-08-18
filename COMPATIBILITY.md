# Compatibility policy

DAGrail v0.10 starts a deliberately narrow beta compatibility line. Run
`dagrail contract` to inspect the exact surfaces, schema versions, MCP input-schema
digests, context budgets, and command inventory implemented by the current binary.

## Promised through the v0.x beta line

- Verified journal history is never rewritten. A release either reads a stored schema
  through an explicit upcaster or fails closed before reduction.
- Journal segment schema v3 adds canonical command-intent bindings. Existing v1 and v2
  segments remain readable; new callers must not reuse an idempotency key with changed
  actor, object, command kind, or request intent.
- The six MCP tool names remain stable. Documented input fields and CLI JSON fields are
  additive unless a new API version is selected.
- Stable provider contracts remain source-compatible. Additions use new optional
  interfaces or types; existing Go interfaces do not gain required methods.
- Explorer v1beta1 remains the retained flat response contract. Explorer v1beta2 adds
  hierarchical group summaries, lanes, collapsed-edge refs, and repeated group-state
  query keys under a new schema path/digest in `dagrail contract`. Both remain
  loopback-only and expose no mutation route.
- Graph Definition v1alpha1 remains importable. A future graph format uses another
  `apiVersion` rather than silently changing existing semantics. Additive `groups` and
  `node.groupId` declarations do not alter Node lifecycle or dependency authority.
- Graph capability discovery is bound to the exact Graph schema path and digest in the
  compatibility contract. Consumers must not infer missing capabilities from one
  adapter, harness, prompt, or example.
- LifecycleMigration v1alpha1 remains the single-command record contract.
  LifecycleMigration v1beta1 adds ordered per-record command bundles with independent
  proof closure. LifecycleProjection v1alpha1 remains additive. Import is still an
  operator-only pristine-target bootstrap and adds no source-specific converter. The
  compatibility contract and qualification gate publish and verify separate immutable
  schema digests for both the retained v1alpha1 surface and the v1beta1 bundle surface.
- DecisionRecord v1alpha1 is append-only authority. Fields are additive; the record
  continues to bind Project, Graph Revision, Node, Attempt, Role, input digest, closed
  outcome, and exact provider identity when the source is a provider.
- New Effect records bind adapter ID, version, schema hash, and canonical request.
  Existing records remain readable, but a pre-v0.23 nonterminal Effect without adapter
  metadata cannot be reconciled automatically; operators must close it with the exact
  prior runtime or keep its dependency cut conservatively blocked.
- SQLite is never portable authority and may be rebuilt from the verified journal.
- SecurityAudit and JournalVerification v1alpha1 fields are governed by the schema
  paths and exact digests in `dagrail contract`. They are additive, read-only reports;
  they do not upgrade the local-user boundary into an authorization system.
- PluginConformance and SupportReport v1alpha1 fields are governed by the schema paths
  and exact digests in `dagrail contract`. Conformance diagnostics are path-free;
  SupportReport remains aggregate and free of authority payloads and host output.
- RecoveryRehearsal v1alpha1 is a read-only, additive report bound to one immutable
  journal head. A passing rehearsal proves exact-prefix restore, reducer replay, current
  schema readability, projection rebuild, and logical projection equivalence.
- AuthorityRotationReceipt v1alpha1 binds a fresh Project identity to the immutable
  previous head and exact authenticated recovery prefix. Rotation never truncates or
  rewrites old bytes; it appends one terminal fence and performs no live cutover.
  Project v1alpha1 retains its v0.21 field set;
  local writer claims and per-generation recovery provenance are deliberately outside
  the strict locator and portable backup Project object.
- A v0.21 binary can parse the unchanged v0.22 locator shape but intentionally cannot
  read or mutate a v0.22-created journal: every new or replacement UUID starts with an
  `authority.established` schema-4 fence before its locator is published. A saved stale
  locator reaches the old journal's terminal `authority.retired` fence. Empty or
  non-empty pre-v0.22 stores require explicit exact-head adoption, which retires the old
  UUID and creates a fresh identity rather than minting a v0.22 claim for legacy state.
  Ordinary v0.22 writes remain schema 3. Both fences make a stale v0.21 writer fail
  during its locked journal reread even when it began admission before the cut.
  The per-user authority-anchor namespace is fixed from the OS account rather than
  process-selected home/config environment. Exact adoption intent derives its backup
  digest from the verified source prefix, so concurrent and crash-recovery retries
  converge instead of rebinding the reservation.
- ReleaseQualification v1alpha1 is an additive structural report. It never upgrades
  declared automation or source completeness into production-adoption evidence.
- ReleaseManifest v1beta1 fixes the six target names and complete archive/SBOM/checksum
  set. ReleaseVerification v1alpha1 is an additive, path-free offline report; neither
  surface substitutes internal consistency for publisher identity or adoption evidence.
- CommandCatalog, CLIError, and InstallationDiagnostic v1alpha1 are governed by the
  schema paths and digests in `dagrail contract`. Catalog fields and commands are
  additive in-version; the four broad error classes and their exit codes remain stable
  through the beta line. Completion is generated from the catalog, not an independent
  authority. Installation diagnostics remain path-free and omit raw host output.
- HistoricalBinaryMatrix v1alpha1 closes the v0.10.0–v0.25.1 input window by exact commit.
- LifecycleMigration v1alpha1/v1beta1 keep v0.22 Effect payloads readable. The additive
  `adapterVersion`/`adapterSchemaHash` fields are an optional pair on imported history;
  current writers emit both, while an unbound nonterminal Effect cannot be reconciled
  automatically.
- ReadinessDecision v1alpha1 may declare external-validation readiness but cannot set
  production validation or 1.0 readiness. Changing a pinned historical commit or
  weakening an adoption gap requires a new contract version and explicit rationale.
- Explorer requests accept only loopback/localhost Host values. An explicit Origin must
  match the exact HTTP Host including port; cross-port localhost callers receive no
  CORS access. These checks are additive to the loopback bind and read-only routes.

## Not promised

Human-readable wording, completion script formatting, command help layout, ordering of JSON object keys, experimental
provider contracts, native harness preview protocols, and the SQLite schema are not
stable interfaces. Pre-1.0 beta releases may remove a surface only with a versioned
replacement, migration instructions, and an explicit changelog entry.

The compatibility contract is descriptive rather than negotiable input: callers must
not edit it and expect the controller to change behavior. Automation should select on
API versions and schema hashes, not the DAGrail executable's display version.
