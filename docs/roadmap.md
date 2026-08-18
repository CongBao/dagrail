# Roadmap

DAGrail advances through evidence-backed milestones. A version is complete only after its implementation, tests, exact commit, push, and GitHub CI are all verified.

## v0.2.0 — Compatibility and recovery

- mixed-version journal verification and event upcasting;
- SQLite projection migrations and compatibility inspection;
- immutable legacy fixtures and future-version fail-closed tests.

## v0.3.0 — Evidence packages

- immutable execution package manifests and protected-input digests;
- artifact references, provenance, and evidence reuse decisions;
- policy reevaluation without unnecessary execution reruns.

## v0.4.0 — Provider runtime

- execute policy, predicate, importer, and projection providers;
- provider schema validation and conformance suites;
- explicit experimental/stable provider compatibility levels.

## v0.5.0 — Operational control

- explainable readiness and dependency cuts;
- incident deadlines, progress, attempt budgets, and circuit breakers;
- backup, restore, status, and bounded history commands;
- local browser-opened, strictly read-only DAG UI for topology, frontier,
  dependency cuts, attempts, leases, incidents, and history.

## v0.6.0 — Native Codex lifecycle

- capability-gated thread start and resume;
- recipient-visible acknowledgement and session observation;
- replacement-session recovery from durable checkpoints.

## v0.7.0 — Multi-harness conformance

- Claude Code and GitHub Copilot native adapters where supported;
- one shared dispatch/receipt conformance suite;
- explicit manual fallback for every unprovable capability.

## v0.8.0 — Distribution and security

- reproducible releases, install/upgrade/rollback verification, and SBOMs;
- permission and secret-boundary hardening;
- signed optional exports without making signatures a local runtime dependency.

## v0.9.0 — Reliability qualification

- crash, contention, disk, corruption, and long-run fault matrices;
- fuzzing for graph, journal, receipt, and patch inputs;
- scale fixtures and context-budget regression gates.

## v0.10.0 — Beta candidate

- end-to-end multi-session sample project qualification;
- stable CLI/MCP/provider compatibility promises;
- observe-only migration workflow for existing DAGs and beta operations guide.

## v0.11.0 — Operational DAG explorer

- a relatively complete, still strictly read-only UI with interactive topology,
  search and filters, node/blocker inspection, event timeline, and operational views
  for leases, resources, incidents, attempts, and effects;
- stable URL deep links, deterministic bounded APIs, and large-graph performance gates;
- a second isolated large-project shadow qualification after the UI lands, reading
  caller-selected governance artifacts into temporary DAGrail state without modifying
  the validation subject or its live workflow;
- an independent cold review of v0.11 implementation and qualification evidence before
  the milestone is declared complete.

## v0.12.0 — Security and trust hardening

- a versioned threat model, trust-boundary tests, permission audits, and dependency
  vulnerability/license gates;
- journal/export verification ergonomics, security-safe diagnostics, and hostile-input
  limits across every public file and protocol boundary;
- explicit local-user capability semantics without implying multi-tenant isolation.

## v0.13.0 — Installation and host operability

- digest-addressed runtime upgrade/rollback plus an offline marketplace linked into
  every release artifact across the six supported OS/architecture targets;
- fresh-host Codex, Claude Code, and Copilot plugin conformance fixtures with capability
  reports and actionable fallback diagnostics;
- support bundles that disclose their exact, secret-free contents before export.

## v0.14.0 — Lifecycle and disaster-recovery maturity

- stable project portability, compatibility rehearsal, and journal recovery runbooks;
- graph evolution and long-running lifecycle qualification across supported upgrade
  paths, including rollback boundaries and unsupported-future fail-closed behavior;
- operator-facing health and capacity evidence suitable for release qualification.

## v0.15.0 — Pre-1.0 release candidate

- public API/schema documentation, tutorials, examples, governance, support, and release
  policy complete enough for an external adopter;
- deterministic release-candidate gates for tests, race, fuzz smoke, static analysis,
  licenses, vulnerabilities, reproducibility, SBOM, provenance, installers, and docs;
- a documented evidence matrix separating implementation qualification from the real
  production adoption evidence that remains intentionally outstanding before 1.0.

## v0.16.0 — Closed release artifacts

- schema-bound manifest generation and offline verification for the exact six-platform
  binary/SBOM release set;
- checksum completeness, archive closure, decompression bounds, deterministic metadata,
  source identity, and SPDX inventory enforcement before tag publication;
- provenance attestation for both the sorted checksum set and verified release manifest.

## v0.17.0 — Stable operator and automation UX

- typed CLI error and exit-code contracts, machine-readable command discovery, shell
  completion, and compatibility tests for scripted use;
- bounded progress and cancellation behavior for slow local operations;
- installation and upgrade diagnostics that remain actionable without raw host output.

Delivered in v0.17.0. Error categories remain intentionally broad: domain-specific
policy and lifecycle details stay in their typed command reports rather than becoming
an unstable taxonomy of process exit codes.

## v0.18.0 — 1.0 readiness convergence

- historical-binary upgrade and rollback matrix across the beta compatibility window;
- one aggregate readiness report covering distribution, recovery, security, plugin,
  API, documentation, and release-policy evidence;
- a formal 1.0 readiness decision that continues to list missing real-world adoption
  evidence instead of synthesizing it from CI.

Delivered in v0.18.0 as an engineering-complete external-validation candidate. A 1.0
tag remains blocked on the four real-world evidence items in `docs/readiness.md`.

## v0.19.0 — Governance closure

- enforced Role capabilities at every public mutation boundary with an import-compatible
  migration path for older Graph definitions;
- typed task, review, decision, gate, and effect completion contracts;
- immutable provider-bound Decision records and unreachable-branch settlement;
- receipt-driven resource closure and closed incident recovery dispositions;
- repository-neutral qualification boundaries for real-project shadow validation.

Delivered in v0.19.0. External projects remain read-only validation subjects and do
not add project-specific types or adapters to the DAGrail kernel.

## v0.20.0 — System hardening and prompt audit

- bind idempotency keys to canonical command intent and reject changed retries;
- require live Role ownership at graph, incident, and effect-reconcile boundaries;
- propagate cancellation through policy/effect work and reduce repeated journal loads;
- re-audit MCP descriptions, hooks, launcher resolution, and all bundled skills for
  bounded context, closed actions, resource closure, evidence scope, and pre-wait;
- extend compatibility through the exact v0.19 source commit and repeat independent
  cold review plus repository-external shadow validation.

Delivered in v0.20.0 without adding validation-subject vocabulary or adapters to the
kernel. Cold-review and external-shadow evidence are release checks, not product state.

## v0.21.0 — Generic lifecycle bootstrap and integration repair

- validate and atomically import one complete external lifecycle prefix through closed
  native DAGrail events and a separately trusted source-authority digest;
- publish a deterministic redacted lifecycle projection and exact Graph capability
  discovery instead of relying on adapter inference;
- repair receipt-derived MCP installation diagnosis and automate upgrades across
  immutable local marketplace paths;
- extend the exact historical-binary window through v0.20.

The first real adoption remains an external validation subject. Its converter,
vocabulary, repository paths, and cutover policy are not shipped as DAGrail contracts.

## v0.22.0 — Lossless bootstrap and authority recovery

- add ordered source command bundles without weakening per-command proof closure;
- add authenticated, non-destructive Project authority rotation from an exact LKG
  backup prefix;
- make fresh-session MCP activation and CLI fallback explicit across plugin surfaces;
- extend the exact historical-binary window through v0.21;
- fence every new v0.22 authority before locator publication and rotate legacy adoption
  to a fresh Project UUID rather than promoting the historical identity.

## v0.22.1 — Established-authority relocation

- provide one public, crash-resumable continuation when a fresh replacement authority
  was durably established before the intended repository locator/runtime was selected;
- bind source anchor/lineage, exact head/backup, target root, destination runtime, and
  deterministic fresh UUID without reviving or copying an existing authority;
- keep relocation fence-only so Graph/history bootstrap and cutover parity remain
  explicit later steps.

## v0.22.2 — Byte-read-only operational inspection

- make public project queries derive operational state from the immutable journal
  without open-time automatic settlement or projection synchronization;
- make SQLite schema, integrity, and logical-fingerprint inspection avoid WAL/SHM
  creation on the protected runtime and fail closed on uncheckpointed sidecars;
- establish the replacement projection before relocation publishes its locator, with
  idempotent recovery from missing or corrupt derived cache state and no concurrent
  cursor regression;
- add a real subprocess preflight matrix that hashes the complete project locator and
  runtime before and after every supported read surface, including a stale cache.

## v0.23.0 — Field operations and handoff evidence

- turn `pre-wait` blockers into deterministic, owner-bound remediation proposals and
  expose project-wide signed actions plus a compact Role authorization envelope;
- close Attempt incidents through a typed repair-successor handoff without rewriting
  the failed Attempt or relying on chat to remember the new owner;
- distinguish unrelated journal progress from a prepared Effect's causal adapter ID,
  version, schema, and canonical request contract;
- publish read-only Git integration-scope and artifact-closure reports that separate
  producer changes from target history and verify consumer reachability before cleanup;
- keep all new evidence schemas digest-bound by the public compatibility contract and
  release qualification, while retaining the six-tool MCP boundary.

## v0.24.0 — Hierarchical project DAG

- add explicit nested Graph groups and two-phase group GraphPatch operations without
  changing Node lifecycle or dependency authority;
- derive deterministic lifecycle/health rollups, membership provenance, generic lanes,
  and exact collapsed-edge evidence from one GraphRevision and journal head;
- make the read-only Explorer open on a clear collapsed Project DAG while preserving the
  complete Execution Detail view and URL-restorable, accessible expansion.

## v0.25.0 — Entropy control and release hardening

- derive compatibility digests from the exact embedded public schemas instead of
  maintaining a second hand-copied digest table;
- keep the three bundled agent skills independently usable while enforcing one tested
  MCP/CLI, bounded-context, pre-wait, and exact unknown-result retry contract;
- separate maximum-size projection algorithm tests from journal/HTTP integration
  fixtures so cross-platform race qualification stays complete and predictably bounded;
- keep the README and public repository metadata focused on product purpose, supported
  harnesses, verified installation, and the shortest executable first workflow.

## Post-v0.23 field-validation backlog

These are repository-neutral observations from the first live writer cutover. They are
candidate priorities, not a committed release scope. Re-evaluate them after the first
three post-cutover lifecycle closures or 50 additional journal segments, whichever
happens first, and assign versions only after new evidence confirms priority.

The accepted v0.24 hierarchical-subgraph milestone is specified in
[`docs/v0.24-hierarchical-subgraphs.md`](v0.24-hierarchical-subgraphs.md). Product-side
evidence remains an external validation input; no product vocabulary or adapter becomes
part of the DAGrail kernel.

### Recovery and imported-state hygiene

- retain a verified current-authority backup in a durable, operator-selected location;
  a historical verification receipt or digest is not a recoverable backup artifact;
- add a closed import disposition for historical attempts that were running at the
  source cutoff, so they can remain auditable without permanently appearing live to
  frontier, capacity, and pre-wait calculations;
- provide an evidence-bound retirement/quarantine operation for stale execution
  ledgers and packages before a new attempt enters an expensive execution phase.

### Projection and historical evidence

- define a generic projection-publication provider contract for arbitrary current
  journal heads, stable external targets, exactly-once intent, zero-write identical
  retry, reconcile, and independent rebuild equivalence; project-specific views remain
  outside the kernel;
- expose byte-read-only point-in-time projection and pre-wait inspection by authenticated
  sequence or head, plus a content-addressed evidence bundle, without requiring callers
  to invoke internal reducers or reconstruct old query output from chat transcripts;
- bind continuity proofs to action, attempt, Effect, and causal identities instead of
  inferring saga position from the current head or fixed migration sequence numbers;
- separate stable semantic result fields from environment-specific telemetry in evidence
  schemas and comparator contracts, authenticating both while declaring which fields
  participate in cross-run or cross-environment equality;
- distinguish migration debt from later operational or adapter debt in closure reports,
  so a zero-debt cutover boundary does not hide known post-cutover work.
- bind v0.23 Git scope reports into execution/admission evidence when a workflow needs
  the three delta classes to become durable policy input rather than a read-only check.

### Controller and graph ergonomics

- generate previewable retry/supersede Graph patches for terminal or permanently skipped
  execution chains instead of requiring an orchestrator to clone a subgraph manually;
- issue an opaque, revision-bound assignment reference for cross-session work packages;
  the receiver must re-resolve or reject it when the Graph or journal head has advanced;
- move governance handoff, admission, and evidence transfer toward typed package refs so
  sessions do not carry large JSON envelopes, exact hashes, and full evidence indexes;
- require every Git candidate or prospective integration named by a package ref to remain
  consumer-reachable through a durable ref, annotated tag, or verified bundle; a commit
  digest alone is not a transferable artifact, and submit/complete must fail before
  journal mutation when the receiving repository cannot resolve the object;
- make disposable-root cleanup consume the v0.23 artifact-closure report as a
  lifecycle-bound receipt, so verification is acknowledged and durable before cleanup;
- support a typed metadata-only artifact rehydration successor when an already accepted
  review binds an exact tree digest but its transport object was lost; reconstruction
  must prove byte/tree identity and preserve the original semantic evidence without
  reopening semantic review;
- add a deterministic producer/consumer reachability preflight that verifies candidate,
  parents, prospective commit and tree in the receiving object store before expensive
  review or admission, while keeping conflict repair outside deterministic admission;
- generate a previewable repair successor and admission edge directly from closed return
  fields, carrying forward still-valid candidate and review evidence; the orchestrator
  should approve the proposed topology, not manually reproduce the returned failure in a
  fresh Graph patch;
- make the v0.23 Incident successor action and an approved repair Graph patch one
  crash-recoverable transition when both are created together;
- keep delivery acknowledgement, recipient acceptance and controller resource closure as
  distinct receipts, but surface the remaining owner slot and its fresh close action in
  the same bounded context so a delivered node cannot silently retain global capacity;
- consider authenticated checkpoint indexes or segment packs that accelerate large
  journal replay and export without rewriting append-only authority.

### Harness and bounded-context UX

- add a fresh-process MCP activation probe that proves the six high-level tools are
  callable, while continuing to emit exact CLI fallback commands when activation cannot
  be proven;
- encode context budget maxima and other closed limits directly in MCP input schemas,
  and return controller-generated inspect refs or bounded lookup results so callers do
  not guess stale or nonexistent object references;
- make worker/reviewer context and inspection expose the same fresh allowed-action set as
  the CLI, and provide an atomic select-and-apply path that forwards an opaque ref without
  manual transcription while still revalidating head, Role, session and input schema;
- surface lease-expiry warnings and a controller-generated renewal action before long
  reviews or external Effects cross their Role lease boundary.
