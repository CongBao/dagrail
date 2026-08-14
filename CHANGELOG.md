# Changelog

All notable changes to DAGrail are documented here. The project follows Semantic Versioning while pre-1.0 APIs remain explicitly scoped by their stability labels.

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
