# Changelog

All notable changes to DAGrail are documented here. The project follows Semantic Versioning while pre-1.0 APIs remain explicitly scoped by their stability labels.

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
