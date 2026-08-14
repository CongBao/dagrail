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
