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
