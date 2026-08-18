# Changelog

All notable changes to DAGrail are documented here. The project follows Semantic Versioning while pre-1.0 APIs remain explicitly scoped by their stability labels.

## 0.25.0 — 2026-08-18

### Changed

- public contract schema digests are now derived from the exact schemas embedded in the
  release binary, removing the manual digest-copy step from every schema change;
- bundled governance, execution, and review skills share an executable safety contract
  for MCP activation checks, CLI fallback, bounded pre-wait, and the sole exact
  cross-session retry exception;
- the README now leads with the product problem, architecture, supported harnesses, and
  verified one-command install while keeping the source-independent quick start short;
- GitHub artifact upload and build-provenance actions use commit-pinned Node 24 releases.

### Fixed

- the missing action-secret read-surface test now establishes its writable precondition
  explicitly on every supported OS before testing fail-closed inspection;
- maximum grouped-topology and dense aggregate-edge algorithm checks use direct immutable
  State fixtures, while smaller HTTP integration tests retain endpoint/schema coverage;
  this preserves the 256-group and 9,045-edge evidence without timing out macOS race CI.

## 0.24.0 — 2026-08-18

### Added

- Graph v1alpha1 now supports explicit, nested `groups` and `node.groupId` declarations;
  GraphPatch can add, update, remove, and revision-safely move Nodes between groups;
- the deterministic grouping projection separates lifecycle from operational health,
  binds membership/projection digests, aggregates cross-group edges, and exposes exact
  source edges through bounded inspect refs;
- Explorer API v1beta2 and the default Project DAG view render collapsed groups in fixed
  generic lanes, preserve the complete Execution Detail view, and support group-aware
  search, breadcrumbs, minimap, fit/center, keyboard disclosure, and URL-restorable
  expansion state.

### Changed

- the summary render cap applies only to expanded internal Nodes; every top-level group
  remains visible on graphs with more than 1,000 internal Nodes;
- grouping authority is always explicit GraphRevision data. DAGrail ships no
  project-specific metadata key or naming heuristic; integrations may propose normal
  two-phase GraphPatch operations instead.

### Fixed

- group identifiers round-trip as repeated opaque query values instead of a comma-split
  text protocol;
- expand/collapse-all uses a compact URL state at the 256-group limit, while individual
  group exceptions remain opaque and restorable;
- dense aggregate-edge indexes are returned through a bounded, projection-bound cursor
  ref, so a valid grouped DAG cannot lose its entire summary to the Explorer byte cap;
- Graphs without declared groups continue to open directly in Execution Detail;
- group declarations have closed count, depth, and field-size limits shared by runtime
  validation and published schemas;
- summary, detail, and aggregate-edge inspection remain byte-nonmutating across the
  protected project/runtime tree.

## 0.23.1 — 2026-08-18

### Fixed

- the Git closure process-count fixture now builds its 64-commit history in one
  deterministic `fast-import` transaction instead of relying on repeated loose-object
  publication during a loaded CI run;
- large-graph wall-clock regression gates retain their ordinary-build thresholds while
  allowing a fixed race-detector multiplier, so instrumentation and shared-runner load
  do not misclassify linear algorithms as quadratic regressions.

## 0.23.0 — 2026-08-18

### Added

- orchestrator context now carries a compact authorization envelope, project-wide
  signed actions, and deterministic remediation proposals for ready work, stale
  Attempts, expired Roles, pending Effects, orphaned Resources, and Incidents;
- Attempt incidents can be closed through a signed `incident.supersede` action when a
  typed edge or `supersedes` declaration identifies the bounded repair successor;
- `effect-continuity:<action-id>` inspection separates unrelated journal-head progress
  from changes to the prepared adapter ID, version, schema, or canonical Effect request;
- `artifact inspect-scope` classifies exact base/candidate/target/prospective Git deltas,
  while `artifact verify-git-closure` verifies commits, ordered parents, trees, tags,
  peeled refs, and reachability before disposable handoff state is removed;
- both Git evidence reports have closed v1alpha1 schemas published and digest-bound by
  `dagrail contract` and release qualification.

### Changed

- bundled orchestrator, worker, and reviewer skills now consume the operations plan,
  use causal Effect continuity, and require typed Git scope/closure evidence at the
  appropriate boundary; they also preserve the one safe cross-session exception for
  an unknown-result retry using the original ref, the same RFC 8785 canonical JSON
  input value, and idempotency key;
- the README is now a short product introduction centered on the problem, architecture,
  supported harnesses, one-command installation, and essential usage; release history
  remains in this changelog.

### Fixed

- every `allowedActions[].inputSchema` is now enforced by the same runtime entry point
  before a journal mutation; lifecycle import separately validates the normalized
  writer proof shape instead of confusing it with caller input;
- Effect preparation persists the adapter version and schema hash, and reconciliation
  fails closed when a same-ID adapter was upgraded or legacy metadata is unavailable;
- retained lifecycle migration schemas still accept v0.22 Effect payloads without the
  additive adapter metadata pair; imported legacy Effects remain visible but cannot be
  reconciled automatically, and a partial metadata pair is rejected;
- v0.22 Incident histories that used the later-reserved repair phrase as ordinary
  resolution text remain importable when no repair binding is present; current writers
  still require the complete typed successor action for new repair semantics;
- generic Incident resolution cannot forge the reserved `superseded_by_repair`
  disposition without the typed successor action and its complete repair binding;
- signed Incident successor actions commit against the exact signed journal head and
  cannot cross a concurrent graph/head advance;
- Git scope evidence compares complete tree entries, including mode changes, both
  rename endpoints, and candidate changes discarded from the prospective tree;
  a prospective third value is never mislabeled conflict resolution, and artifact
  retention accepts only real durable refs, never revision expressions;
- oversized context keeps compact Role authorization plus an `operations:<key>`
  recovery ref, while open Incidents use a bounded, paginated summary index; minimum
  contexts use fixed-length opaque Role/Node refs even when declaration IDs are long;
- read-only context/action queries no longer lazily create the signing secret; writable
  project bootstrap establishes it durably and inspection fails closed if it is missing;
- operations inspection propagates missing, damaged, or unreadable action-secret errors
  instead of presenting an incomplete empty action plan;
- Git artifact inspection disables partial-clone lazy fetch, propagates cancellation,
  and reads each commit tree once rather than spawning a subprocess per changed path;
- Git evidence now removes ambient repository/object-store redirection, ignores replace
  refs and grafts, and verifies commit identity plus retention from raw object bytes;
  closure manifests are regular-file-only, bounded before allocation, and cancellable;
- Git closure verification now enumerates refs and object types in fixed process-count
  batches, caches each retaining ref's raw ancestor closure, and propagates cancellation
  as interruption instead of misreporting missing evidence;
- pre-wait and operations recovery now expose exact counts with 24-KiB-bounded previews
  and journal-head/inventory-bound pages; stale page offsets fail closed, dependency
  cuts use count/digest/ref summaries with one shared adjacency/cache per audit, and CLI
  plus MCP context/inspect cancellation reaches the underlying computation;
- Incident successors and resource availability are indexed once per query instead of
  multiplying Incidents or planned Nodes by the complete graph/resource set; identifiers
  too large for one page use compact signed action bindings and digest-bound detail chunks
  rather than making a returned continuation ref impossible to consume;
- authorized action planning now builds Role capability, Node, Effect-by-Attempt, and
  Resource-by-Attempt indexes once, avoiding residual Node-by-Node and Node-by-Resource
  scans on large active DAGs;
- oversized Role/session authorization and imported Attempt IDs use fixed-length opaque
  refs plus digest-bound detail chunks, keeping operations and ActionResult surfaces
  within their byte budgets;
- schema-legal outcomes larger than the action-input budget remain executable through a
  controller-bound `outcomeRef`, with an optional chunked ID mapping for audit detail;
- status, history, frontier, evidence, Incident, remediation, Effect continuity, and
  stored-authority inspection now share one 24-KiB summary and digest-bound detail
  protocol; CLI and MCP accept opaque Role, Node, Attempt, Incident, Resource, and
  Effect selectors instead of requiring schema-legal large IDs to fit smaller inputs;
- lifecycle migration preserves released pre-v0.23 opaque inputs for actions whose
  persisted meaning is completely proven by companion native events, while current
  action calls still enforce their closed input schemas;
- confirmed Effect history remains portable after its adapter is upgraded or removed;
  nonterminal Effects retain strict adapter-continuity admission;
- zero-delta Git scope reports encode `entries` as an empty array and therefore match
  their published schema;
- the README quick start now binds the declared example Role and its commands are
  exercised from an empty project with only README-provided bytes by an executable
  regression test.

### Boundaries

- remediation entries are deterministic proposals, not an autonomous scheduler;
  mutations still require a current signed action, active Role lease, capability, and
  stable idempotency key;
- Git artifact inspection is read-only and repository-neutral. It does not merge,
  retain, delete, push, or infer semantic approval.
- pre-v0.23 nonterminal Effects remain readable but lack a provable adapter version and
  schema binding; reconcile them with the exact prior runtime before upgrade or leave
  their dependency cut conservatively blocked.

## 0.22.2 — 2026-08-17

### Fixed

- project query commands no longer run automatic lifecycle settlement or synchronize
  `projection.sqlite` as a hidden open-time side effect;
- read-only projection diagnostics inspect only a stable checkpointed SQLite image and
  fail closed when WAL/SHM state is present, without creating or checkpointing sidecars;
- authority relocation now creates and verifies the fence-only projection before the
  replacement locator becomes visible, matching adoption and rotation ordering;
- replacement projection rebuilds consume a writer-locked journal snapshot, and normal
  projection synchronization never lets an older cursor overwrite a newer one;
- inspection keeps ordinary claim, establishment-fence, and lineage validation and
  fails on a missing runtime without materializing directories; only explicit recovery
  operations retain the relaxed evidence-open capability;
- `status`, `journal verify`, `lifecycle projection`, `pre-wait`, and portable backup
  create/verify now have an executable byte-nonmutation contract, including when the
  disposable projection cache is stale.

### Boundaries

- backup/support/journal exports may create only their explicitly requested output
  artifact outside the protected project; a red `pre-wait` report remains a semantic
  liveness result and does not itself repair imported attempts, leases, or resources.

## 0.22.1 — 2026-08-16

### Added

- public `recovery relocate-authority` for the bounded case where an adoption/rotation
  replacement was already established under an unsuitable local runtime before the
  intended repository locator was published;
- a digest-pinned v1alpha1 relocation receipt binding the claimed source UUID/head,
  authenticated backup, locator ancestor, canonical target and destination-runtime
  roots, deterministic fresh UUID, reason, and idempotency key.

### Fixed

- an already-established replacement now has a crash-resumable public continuation:
  DAGrail retires the anchored source under its writer lock, establishes the fresh
  destination fence before locator publication, rejects unrelated lineage and changed
  roots, and returns one receipt for identical cross-process retries.

### Boundaries

- relocation never revives/rebinds an existing UUID and does not copy Graph or lifecycle
  state; explicit authenticated bootstrap and parity remain required before cutover.

## 0.22.0 — 2026-08-16

### Added

- lifecycle migration v1beta1 source command bundles: one immutable external record may
  carry several ordered writer-equivalent commands, each with an independent closed
  proof ledger, while v1alpha1 retains its single-command semantics;
- non-destructive `recovery rotate-authority`, which authenticates an exact backup/LKG
  prefix, creates a fresh Project UUID and fence-only journal, records local recovery
  provenance, preserves every prior segment byte, and appends one terminal retirement
  fence to the old journal;
- explicit `recovery adopt-legacy-authority` for exact-head, locally audited migration
  of a pre-v0.22 runtime store; it retires the legacy UUID, creates a fresh identity,
  and never mints v0.22 writer authority for legacy state;
- explicit fresh-session activation and typed CLI fallback guidance across installer
  results, installation diagnostics, hooks, harness docs, and all bundled skills.

### Fixed

- external histories no longer need to discard lifecycle transitions or falsify source
  causality when one source record maps to multiple native DAGrail actions;
- recovery from a valid-but-contaminated authority no longer depends on manual locator
  edits or journal deletion; live leases and unclosed work fail the rotation closed;
- repeated authority generations and exact concurrent rotation retries are idempotent;
  every writer requires a canonical-data-root-bound local authority claim, so copying a
  locator, backup, or complete project data directory cannot create a second writer;
- the per-user authority anchor can no longer be redirected by production process-home
  environment, legacy-adoption backup identity is prefix-deterministic, and concurrent
  or reservation-before-fence crash retries return the same receipt;
- visible locator, claim, lineage, and anchor files are revalidated and directory-synced
  on fresh retries; copied or partially initialized locators can no longer report a
  successful initialization without their schema-4 establishment fence;
- journal-directory creation now flushes its writable parent chain on Unix and Windows,
  and fresh retries replay every unproven parent boundary instead of treating a visible
  directory as durable; normal open, mutation, and `init` fail closed when a claimed
  initial or replacement authority has lost its establishment fence;
- legacy-adoption backup derivation and retirement admission now converge even when a
  same-intent loser crosses the initial retirement check before the winner commits;
- delayed rotation and adoption retries traverse cycle-checked lineage beyond 128
  generations, subject only to the documented 10,000-store defensive local bound;
- retirement is an append-only journal fence, and every newly initialized or replacement
  journal begins with an establishment fence before locator publication; the exact
  v0.21 reducer can parse Project v1alpha1 but cannot read or mutate those authorities;
- establishment and retirement barriers use dedicated segment schema 4; an already-open
  v0.21 writer therefore rejects the journal again while holding the append lock, even
  if it validated state before the barrier committed;
- Project v1alpha1 remains byte-shape parseable by v0.21 after rotation; recovery
  provenance is kept outside the strict repository locator and backup Project object;
- missing or corrupt rotated lineage blocks all ordinary writes; an exact rotation retry
  may restore only bytes already authenticated by the surviving claim and unique local
  predecessor retirement fence;
- recovery inspection handles reject generic append and restore at the journal layer;
  only dedicated legacy retirement and rotation transactions can cross that boundary;
- long-running harnesses are no longer treated as MCP-ready merely because upgraded
  skills or registrations are visible outside that process.
- retained LifecycleMigration v1alpha1 and bundled v1beta1 schemas are independently
  digest-pinned by the public contract and structural qualification gate; runtime
  decoding now rejects unknown record, command, and native-event fields at the same
  closed boundaries declared by both schemas.

### Boundaries

- source converters and validation-subject vocabulary remain outside DAGrail;
- authority rotation is an operator recovery primitive, not automatic live cutover.

## 0.21.0 — 2026-08-15

### Added

- `lifecycle validate-history|import-history|projection` for one complete, bounded
  external lifecycle prefix mapped to generic DAGrail native events;
- an out-of-band authority-statement trust anchor binding the target, normalized source
  chain, and native mapping; strict per-event transition/global-invariant preflight;
  atomic journal commit; and deterministic redacted lifecycle projection;
- current-writer prefix equivalence for ready/capability admission, 24-hour leases,
  same-session lease renewal, causal event time, checkpoint/evidence/Decision/action
  binding, resource closure, Incident management, and Effect dispatch/reconcile crash
  recovery; the executable matrix covers task, review, resource, Incident, evidence,
  and Effect writer paths and validates every emitted prefix against the public schema;
- schema-bound Graph capability discovery, including dynamic graph, positive predicate,
  resource capacity/request, Role lease, lifecycle import, and lifecycle projection support.

### Fixed

- lifecycle preflight now binds each `action.applied` to the concrete native events in
  the same normalized source record, so a later final map cannot hide substituted
  checkpoint text, package/Decision IDs, completion outcomes, or resource/effect targets;
- source-command proofs are now single-use and record-closing: duplicate action use,
  orphan Resource/Effect observations, missing release/Incident companions, and
  unrelated native events fail before import;
- Incident replay now matches the closed writer state machine and exact automatic
  companions, including circuit-open behavior and observation-bound resolution time;
- Resource and Effect Incidents can no longer be manually resolved ahead of their
  underlying ambiguity; only the matching confirmed observation resolves them, while
  an explicit `retry` disposition resets a circuit for another bounded reconcile and
  its operator, time, deadline, and progress audit survive later automatic observations;
- Effect dispatch, reconcile, and Effect-sourced Incident mutations now share one
  per-action, cross-process observation lock from the prepared commit through receipt
  persistence; Incident mutations reload authorization after waiting, so a late or
  stale observation cannot downgrade a confirmed Effect or erase an operator circuit;
- all four Incident mutation APIs now have context-aware variants and the CLI passes
  its signal context through lock acquisition; interruption leaves the journal head
  unchanged and an exact same-key retry commits once after the lock becomes available;
- lifecycle preflight now treats each dispatch/reconcile admission as a one-shot proof:
  an observation consumes it, a `reconciling` receipt does not mint another, and a
  circuit-open/resolved Incident rejects new reconcile admission until explicit retry;
- imported Effect preparation now binds the Graph-declared adapter and canonical
  request, prepared adapter identity, a closed typed receipt whose inner/outer status
  agrees, and completion failure classification where the action retains that input;
- the public migration schema and generated Resource allowed actions now accept the
  same arbitrary non-null JSON receipt shapes and reject null Resource receipts and
  Effect requests;
- slow policy and Effect preparation recheck head, action expiry, session, and Role
  lease at the authoritative persistence time, while ordinary actions use one timestamp
  for native events and their journal segment;
- same-session Role renewal, active-lease Incident updates, arbitrary valid JSON action
  input, Effect request, Resource receipt, and closed action/Incident projection
  vocabularies now match the current writer and published schemas;
- `doctor install` now checks MCP configuration against the exact runtime path already
  verified by the local runtime receipt instead of reporting a false missing launcher;
- upgrades from an existing immutable local bundled marketplace now perform a bounded
  remove/register/install rotation and no longer call a remote-only in-place update.

### Boundaries

- lifecycle import is a high-risk operator CLI bootstrap, not a seventh MCP tool;
- source-specific converters, vocabulary, authority discovery, and cutover policy remain
  outside the kernel. Validation subjects do not define DAGrail product contracts.
- projection schemas prohibit evidence URIs as well as redacting them in the reference
  implementation.
- the validation-subject boundary remains in its original ADR 0017, extended for the
  v0.21 migration surface instead of duplicating the same decision under a new number.

## 0.20.0 — 2026-08-15

### Added

- canonical command-intent digests that bind new idempotency keys to actor, object,
  command kind, and bounded request data without rewriting historical journal bytes;
- adversarial tests for signed-action capability forgery, changed-request idempotency,
  unleased graph mutation, cancellation during policy evaluation, invalid MCP graph
  modes, and textual hook-launcher path substitution;
- payload-free Decision summaries and resource-closure status in the read-only Explorer.

### Changed

- provider, effect, CLI, and MCP cancellation now reaches the actual invocation rather
  than stopping only at the outer command boundary;
- graph changes, incident updates, and effect reconciliation require an active owner
  Role lease in addition to their declared capability;
- graph import, provider import, and graph-change replay now parse and bind the current
  Graph, provider ID, provenance, and patch before returning an idempotent result;
- hook and Explorer startup use a journal-derived read-only open and cannot settle
  automatic Nodes or repair projections as a hidden lifecycle action;
- effect reconciliation is serialized per stable action across goroutines and local
  processes without holding the journal writer lock during adapter I/O; subprocess
  competition and lock-holder crash recovery are executable regression tests;
- context construction reuses one loaded state, hooks emit a bounded eight-Node ready
  summary, and every hook instruction names the exact governing, execution, or review
  skill without guessing Role identity;
- plugin installation and conformance require the textual `dagrail` hook launcher to
  resolve in a fresh process to the same receipt- and digest-verified runtime used by
  a structurally inspected absolute MCP `command` with exact `mcp --stdio` arguments;
  Codex/Copilot use closed top-level JSON shapes while Claude Code uses its name-specific
  `mcp get dagrail` fields because its public CLI has no JSON listing contract;
- all three bundled skills were rewritten around current allowed-action refs, closed
  NodeKind outcomes, resource/effect reconciliation, evidence boundaries, and mandatory
  pre-wait liveness checks.

### Compatibility

- the closed historical-binary window now covers v0.10–v0.19; journal segment schema
  v3 adds command intent bindings while schemas v1 and v2 remain readable and reject
  hybrid v3 command fields.

## 0.19.0 — 2026-08-15

### Added

- enforced Role capabilities at task, review, decision, gate, effect, graph-change,
  incident, reconcile, and resource-closure mutation boundaries while retaining
  import compatibility for older Graph definitions;
- NodeKind-specific terminal actions and immutable Decision records binding human,
  LLM, or provider judgments to Project, Graph Revision, Attempt, evidence, and exact
  provider identity;
- explicit resource close/reconcile receipts that retain capacity and open a scoped
  incident while closure is failed or unknown;
- closed incident classifications and recovery dispositions, plus deterministic
  automatic skipping for branches made unreachable by positive predicates;
- a published DecisionRecord v1alpha1 schema and projection schema v4.

### Changed

- successful tasks must be submitted before completion, while review, decision, gate,
  and effect Nodes expose their own closed lifecycle verbs;
- graph apply, effect reconcile, and incident mutations enforce the matching declared
  Role capability;
- the compatibility window now includes the exact v0.18 source commit.

### Architecture

- external repositories are formal validation subjects only: their conversion and
  comparison drivers stay outside DAGrail and cannot define kernel vocabulary or
  product contracts.

## 0.18.0 — 2026-08-14

### Added

- a closed HistoricalBinaryMatrix v1alpha1 manifest pinning every v0.10–v0.17 beta
  release commit as immutable compatibility input;
- a dedicated CI and tag-release matrix that builds all pinned binaries plus the
  candidate, exercises every adjacent runtime upgrade/rollback/re-forward pair, and
  recovers a v0.10-created journal with the candidate;
- `readiness`, a schema-bound aggregate decision over source qualification,
  distribution, API/docs, historical compatibility, browser origin isolation, and
  optional project/installation evidence;
- a public 1.0 external-validation runbook that keeps four real adoption requirements
  separate from structural CI evidence.

### Security

- the Explorer now rejects non-loopback Host values, DNS-rebinding hostnames, and
  cross-port Origin values, emits same-origin opener/resource policy headers, and never
  enables CORS;
- qualification includes explicit localhost origin-boundary evidence rather than
  treating a loopback bind as sufficient browser isolation.

### Changed

- the readiness decision can reach `ready_for_external_validation`, but its v1alpha1
  schema fixes `oneDotZeroReady` and `productionValidated` to false until independent
  adoption, a long-running live DAG, real-host receipts, and an operator restore drill
  are observed.

## 0.17.0 — 2026-08-14

### Added

- `commands`, a bounded schema-valid catalog for command effect, project requirement,
  output mode, subcommands, error opt-in, and stable broad exit classes;
- catalog-generated Bash, Zsh, Fish, and PowerShell completion through
  `completion <shell>`;
- an opt-in CLIError v1alpha1 JSON envelope with bounded messages and explicit
  `operation_failed`, `usage`, `diagnostic_failed`, and `interrupted` classes;
- `doctor install`, a path-free InstallationDiagnostic v1alpha1 report over runtime,
  linked plugin bundle, harness detection, plugin registration, and MCP configuration.

### Changed

- process interruption now propagates through provider calls, plugin install/status,
  graph import providers, projection providers, MCP stdio, and the read-only UI;
- external harness management commands retain the 64-KiB output cap and now also have
  a two-minute upper bound while honoring an earlier caller cancellation;
- the compatibility contract now derives its top-level command list from the same
  catalog used by dispatch and shell completion.

## 0.16.0 — 2026-08-14

### Added

- `release manifest|verify`, a bounded offline contract for exactly six platform
  archives, six SPDX inventories, one sorted checksum set, and their source tag,
  commit, and reproducible timestamp;
- published ReleaseManifest v1beta1 and ReleaseVerification v1alpha1 schemas with
  exact digests in `dagrail contract`;
- adversarial archive, checksum, SPDX, manifest-key, file-set, mutation, and identity
  tests across tar/gzip and ZIP release formats.

### Changed

- tag publication now generates and verifies `release-manifest.json` before creating a
  GitHub release, then includes the manifest and checksums in provenance attestation;
- SBOM generation uses the action's file-specific input and disables its implicit
  artifact/release uploads so publication remains owned by the closed release job;
- archive verification requires a closed three-file payload, deterministic timestamps,
  root ownership metadata for tar files, an executable Unix binary, bounded expansion,
  and readable ZIP content.
- every push now assembles all six archives, generates six real SPDX inventories, and
  verifies the aggregated manifest without publishing release assets.

## 0.15.0 — 2026-08-14

### Added

- `qualify release`, a schema-bound structural-candidate report that validates public
  repository completeness, the compatibility contract, published schema digests,
  plugin metadata, the linked bundle, workflow gates, and commit-pinned actions;
- optional inspection-only project evidence for security audit and recovery rehearsal,
  kept separate from source-release qualification;
- public API, first-DAG tutorial, release, governance, support, and code-of-conduct
  documentation for an external adopter.

### Changed

- tag releases now repeat tests, race detection, vet, and all bounded fuzz targets before
  publication; publish also depends on qualification, security, six builds,
  reproducibility, SBOM, checksum, and provenance jobs;
- the release report explicitly keeps `productionValidated` false and lists independent
  adoption, long-running live use, real-host receipt proof, and operator restore drills
  as outstanding evidence rather than inferring them from tests.

## 0.14.0 — 2026-08-14

### Added

- `recovery rehearse`, a schema-bound read-only disaster-recovery proof that captures a
  verified journal head, restores the exact prefix into disposable storage, replays the
  reducer, and rebuilds an independent SQLite projection;
- stable logical projection fingerprints over every materialized table, independent of
  SQLite pages, WAL layout, data-root path, or filesystem metadata;
- recovery qualification for legacy event upcasting, stale projection detection,
  post-rebuild equivalence, and proof that rehearsal does not mutate the live project.

### Changed

- journal compatibility can now be evaluated against a captured immutable segment set,
  avoiding a second read that could drift from the recovery snapshot;
- the compatibility contract now publishes the RecoveryRehearsal v1alpha1 schema and
  guarantees that rehearsal writes only to disposable storage.

## 0.13.0 — 2026-08-14

### Added

- a digest-addressed offline marketplace linked into the static executable, with exact
  Codex, Claude Code, and Copilot manifests, skills, hooks, and assets materialized
  under a stable local root before host installation;
- `plugin materialize`, `plugin bundle-status`, and a schema-bound `plugin conformance`
  report that separates runtime, bundle, host detection, plugin registration, MCP,
  native capability, receipt proof, and explicit manual fallback;
- schema-bound `support preview|export` diagnostics containing only pseudonymous
  project identity, build metadata, typed security/journal evidence, status-only doctor
  checks, and aggregate lifecycle counts.

### Changed

- default host installation uses the immutable bundled marketplace rather than a moving
  Git branch; an explicit `--marketplace-source` remains available for development;
- default uninstall removes the matching MCP, plugin, and bundled marketplace
  registrations while retaining verified runtime and bundle bytes for rollback;
- harness command output used by installation is capped at 64 KiB, and conformance
  omits executable paths, raw host reasons, and unsafe or unbounded version output;
- support export creates an owner-only new file and refuses to overwrite existing
  evidence.

## 0.12.0 — 2026-08-14

### Added

- a versioned threat model and ADR that make the host OS account the explicit outer
  trust boundary without claiming malicious same-user or multi-tenant isolation;
- `dagrail security audit`, a schema-bound, path-redacted report over runtime-data,
  journal, projection, locator, action-secret, and observation-locator structure and
  permissions;
- a richer schema-bound `journal verify` report with head identity, schema window,
  canonical export byte count, and SHA-256 digest;
- per-push known-vulnerability, module-integrity, and forbidden/unknown dependency
  license gates using pinned security tools.
- a `go1.26.6` toolchain floor so source and release builds include the standard-library
  fixes required by the vulnerability gate.

### Security

- authority JSON now rejects duplicate keys, excessive nesting/value/key/string
  counts, unknown closed-envelope fields, trailing documents, and unsafe numeric forms;
- journal loading rejects symlinks, excessive segment/event counts, segments over
  16 MiB, and canonical unknown fields that are not covered by the typed hash envelope;
- project locators are bounded strict YAML; MCP stdio is capped per message and per
  tool input; CLI JSON flags, hooks, signatures, effect bindings, reconciliation
  evidence, and receipts have explicit limits;
- SQLite and the HMAC action secret are hardened to owner-only files on POSIX, and
  sensitive effect evidence is screened before it reaches immutable authority;
- Graph metadata, project locators, Role bindings, incident text, commands, and event
  payloads are screened at both entry-specific and final journal-write boundaries;
- applied dynamic-graph authorization tokens are discarded before the revision event
  is committed and never become journal authority.

## 0.11.0 — 2026-08-14

### Added

- a full loopback-only DAG Explorer with Topology, Nodes, Timeline, and Operations
  views, URL deep links, search, status/kind/Role filters, pagination, and a detailed
  payload-free Node inspector;
- deterministic bounded v1beta1 APIs for overview, Nodes, focused topology, Node
  details, non-overlapping history navigation, and operational objects, with a public
  response/error schema bound into `dagrail contract` by SHA-256;
- focused one-to-four-hop graph neighborhoods, keyboard navigation, zoom controls,
  modal focus preservation, responsive layouts, and automatic refresh without
  background control actions;
- a 2,048-Node Explorer performance fixture plus high-fanout focus, bidirectional
  history, query, response-size, escaping, endpoint, schema, accessibility shell, and
  legacy-snapshot compatibility tests.

### Security

- every Explorer collection and JSON response has a fixed upper bound; unknown,
  duplicate, oversized, and out-of-range query inputs fail closed;
- Node input and effect receipt bodies are reduced to safe digests or typed states;
  metadata and external-reference URLs are omitted; `HEAD` shares GET validation; the
  embedded asset allowlist is closed; and browser permissions are disabled.

## 0.10.0 — 2026-08-14

### Added

- a machine-readable beta compatibility contract covering Graph, CLI, provider SDK,
  journal, projection, MCP schema hashes, context budgets, and command inventory;
- an observe-only assessment and isolated-shadow workflow that binds source authority
  digests to a Graph Revision without writing to the source project;
- a public multi-harness sample and executable end-to-end qualification covering
  replacement sessions, manual effect ambiguity, idempotent retry, reconciliation,
  deterministic Nodes, bounded context, restart, and projection loss;
- public schemas, beta operations guidance, migration guidance, and compatibility ADRs.

### Security

- portable observation provenance excludes absolute workstation paths; private source
  locators use owner-only shadow storage;
- observation rejects symlink escape and bounds graph, authority-file, aggregate-byte,
  and file-count inputs.

## 0.9.0 — 2026-08-14

### Added

- deterministic journal fault points and subprocess crash tests on both sides of the
  atomic rename commit boundary;
- cross-process single-writer/idempotency contention, journal corruption, disposable
  projection recovery, and 256-segment longevity matrices;
- bounded fuzz targets for Graph Definitions, GraphPatch inputs, journal segments, and
  native harness receipts, with per-push GitHub smoke jobs;
- a 2,048-node scale fixture covering validation, projection materialization, full
  frontier inspection, and all role-specific context budgets;
- an executable reliability qualification guide and architecture decision record.

### Changed

- oversized ready frontiers now degrade to a count, a deterministic bounded prefix,
  and a valid `frontier` inspect ref instead of failing the context request;
- graph and patch files are restricted to regular inputs of at most 8 MiB, and closed
  predicate AST nesting is capped at 64 levels;
- POSIX journal directory synchronization errors are no longer silently discarded
  after segment rename.

## 0.8.0 — 2026-08-14

### Added

- reproducible six-target release builds with deterministic archives, sorted checksums,
  per-target SPDX SBOMs, and GitHub build-provenance attestations;
- digest-addressed runtime upgrade backups, fresh-process verification,
  `plugin runtime-status`, and reversible `plugin rollback`;
- optional Ed25519 detached signatures over exact export bytes, with a public
  v1alpha1 envelope schema and fail-closed key parsing;
- Dependabot coverage for Go modules and GitHub Actions.

### Changed

- GitHub Actions are pinned to exact commits, and CI independently compares two linked
  binaries and deterministic archives;
- release installers validate one exact checksum entry and a closed archive allowlist;
- secret-field screening additionally rejects common token material, bearer values,
  URI userinfo, and credential-like URI query parameters.

### Security

- runtime replacement now validates both candidate and installed bytes, restores the
  previous executable after an unsuccessful publish, and refuses corrupt rollback
  artifacts;
- private signing keys must be regular PKCS#8 Ed25519 files and, on POSIX, inaccessible
  to group and other users.

## 0.7.0 — 2026-08-14

### Added

- capability-gated Claude Code headless JSON start/resume with preselected session IDs,
  synchronous completion receipts, and digest-only result metadata;
- GitHub Copilot CLI ACP v1 stdio dispatch with exact JSON-RPC request binding,
  synchronous stop-reason receipts, bounded streams, and default-deny permission calls;
- a shared native-receipt conformance contract that prevents transport, session,
  visible delivery, harness completion, and DAG acceptance from collapsing;
- opt-in installed-Copilot ACP integration smoke coverage and protocol fixture tests.

### Changed

- first-party harness probes now report protocol stability, execution mode, and the
  exact receipt proofs they can provide;
- Copilot cross-process resume and observation remain explicit manual fallbacks: a
  real stdio smoke showed that an advertised ACP `loadSession` bit does not make an
  ephemeral ACP session durable across server processes;
- Copilot tool permission requests default to one-shot rejection; only an explicit
  graph request may select `allow-once`, and persistent automatic approval is rejected.

## 0.6.0 — 2026-08-14

### Added

- capability-gated Codex app-server daemon/proxy integration for native thread start
  and resume;
- stable action-to-user-message binding and recipient-visible delivery proof from an
  exact completed `userMessage` notification;
- read-only `thread/read` observation for evidence-free native reconciliation and turn
  status receipts;
- optional `HarnessObserver` SDK seam and prior-receipt binding for adapter-safe
  reconciliation;
- replacement-session qualification proving a new Role binding can continue an active
  Attempt from its durable checkpoint.

### Changed

- Codex harness provider metadata advances to its v2 envelope contract; unsupported or
  drifting native APIs continue to return manual or `unknown` receipts.

## 0.5.0 — 2026-08-14

### Added

- reason-coded readiness explanations, resource shortfalls, and visible dependency cuts;
- incident progress, deadline, attempt-budget, circuit-breaker, and resolution controls;
- payload-free bounded history, operational status, and digest-bound journal
  backup/verify/restore commands;
- a loopback-only, browser-opened, strictly read-only DAG UI with topology, frontier,
  incidents, attempts, leases, and recent history;
- overdue and circuit-open incident checks in the pre-wait liveness audit.

### Security

- the UI accepts only `GET` and `HEAD`, exposes no action references or event payloads,
  uses no remote assets, and refuses non-loopback binds.

## 0.4.0 — 2026-08-14

### Added

- bounded Provider Runtime for policy, predicate, graph-importer, and projection calls;
- self-contained JSON Schema input validation, call deadlines, panic recovery, output
  limits, authority JSON checks, and sensitive-field screening;
- explicit experimental/stable provider contracts with exact stable schema hashing;
- `dagrail provider list|check|invoke`, provider-backed graph import, and verified-event
  projection rendering;
- provider import provenance in the immutable journal and a public conformance guide.

## 0.3.0 — 2026-08-14

### Added

- immutable, content-derived Execution Packages with candidate, prospective-tree,
  command-graph, protected-input, observation, artifact, and provenance bindings;
- deterministic, policy-bound Reuse Decisions that distinguish policy-only changes
  from protected-core changes without claiming semantic approval;
- `evidence.publish` and `evidence.assess-reuse` allowed actions, bounded context refs,
  `dagrail evidence list`, and package/decision inspection;
- SQLite projection schema v3 with evidence and reuse indexes plus v1/v2 migrations;
- replay-time verification of package IDs, core digests, decision results, and reason codes.

## 0.2.0 — 2026-08-14

### Added

- backward-compatible mixed-version journal reading;
- explicit event schema versions and deterministic in-memory upcasting;
- journal compatibility reporting;
- serialized SQLite projection schema migrations, transient-lock safety, and
  segment-schema provenance;
- immutable compatibility fixtures and fail-closed future-version tests.

## 0.1.0 — 2026-08-14

### Added

- initial typed DAG control kernel, journal, projections, CLI, MCP, effects, plugins, skills, and hooks.
