<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/logo-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="./assets/logo.svg">
    <img alt="DAGrail logo" src="./assets/logo.svg" width="120">
  </picture>
</p>

<h1 align="center">DAGrail</h1>

<p align="center"><strong>LLM-led DAG governance with durable, machine-checked state.</strong></p>

[![CI](https://github.com/CongBao/dagrail/actions/workflows/ci.yml/badge.svg)](https://github.com/CongBao/dagrail/actions/workflows/ci.yml)
[![Apache-2.0 License](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](go.mod)

DAGrail is a lightweight control plane for long-running development DAGs. The LLM or
human chooses what to do; DAGrail keeps graph revisions, role leases, checkpoints,
allowed actions, effects, and recovery state outside chat.

It is not a workflow executor, requirement manager, container scheduler, or autonomous
agent scheduler. Codex, Claude Code, GitHub Copilot CLI, humans, and future harness
adapters remain the executors.

DAGrail is designed for agentic software-development workflows that outlive one LLM
session. It complements execution frameworks by owning the small but failure-prone
control-plane layer: who holds a Role, which Node is actually ready, what was committed,
and whether an ambiguous Git or harness effect is safe to reconcile.

## What it provides

- typed nodes, positive edge predicates, computed frontiers, and dependency cuts;
- replaceable sessions through stable roles, leases, attempts, and checkpoints;
- revision-bound allowed actions instead of hand-written lifecycle envelopes;
- bounded role-specific context through CLI or six high-level MCP tools;
- immutable execution packages and deterministic evidence-reuse decisions;
- bounded compile-in providers with schema and stability contracts;
- recoverable external effects with explicit `unknown` and `reconcile` states;
- an immutable RFC 8785 journal with disposable SQLite projections;
- explainable readiness, owned incidents, bounded history, verified backups, and a
  browser-opened read-only DAG UI.

## Status

`v0.7.0` is a technical alpha: local-first and single-user, with one native Go
executable, an immutable journal as authority, rebuildable SQLite projections, and
stdio MCP. Its operational surface adds explainable dependency blockers, incident
circuit breakers, digest-bound backup/restore, bounded history, and a loopback-only
read-only UI. Capability-gated native adapters support Codex thread lifecycle, Claude
Code headless completion turns, and GitHub Copilot ACP completion turns. One strict
receipt contract keeps transport, session, delivery, completion, and DAG acceptance
separate; every missing or unprovable capability retains an explicit manual fallback.
The Provider Runtime executes schema-bound extensions without giving them controller
storage handles.

## Build

Until the first GitHub release is published, build from source with Go 1.26 or newer:

```bash
CGO_ENABLED=0 go build -trimpath -o dagrail ./cmd/dagrail
./dagrail version
```

## Quick start

```bash
dagrail init --root . --name example
dagrail graph validate --file examples/development-dag.yaml
dagrail graph import --root . --file examples/development-dag.yaml \
  --idempotency-key import-v1
dagrail frontier --root . --format json
dagrail role bind --root . --role developer --harness codex \
  --session SESSION_ID --idempotency-key bind-SESSION_ID
dagrail context --root . --view worker --role developer --node implement
dagrail ui --root .
```

Use `dagrail action list` to obtain a signed, revision-bound action ref. Apply only that ref with a stable idempotency key. Run `dagrail pre-wait` before yielding or claiming the graph is idle.

## Execution evidence

An active worker publishes digest-only execution metadata through the
`evidence.publish` allowed action. Artifact bodies remain in Git, CI, or an artifact
store. Reviewers can then apply `evidence.assess-reuse` against a new policy binding and
current protected core. `reuse_execution` means the recorded execution may be fed to the
new policy; it never means that policy passed.

```bash
dagrail evidence list --root . --node implement
dagrail inspect --root . evidence-package:epkg_...
dagrail inspect --root . reuse-decision:reuse_...
```

## Agent integrations

| Harness | Integration |
| --- | --- |
| OpenAI Codex | Plugin surface plus native asynchronous start, resume, and observation when app-server capabilities pass |
| Claude Code | Plugin surface plus native synchronous headless start/resume when documented JSON flags pass |
| GitHub Copilot CLI | Plugin surface plus experimental synchronous ACP dispatch; cross-process resume remains manual |
| Other harnesses | CLI/MCP plus compile-in Go providers |

After building the binary, install its shared runtime and selected harness projections:

`dagrail plugin install` copies the running binary to a stable per-user location, verifies it from a fresh process, installs the plugin through each selected harness, and registers the MCP server with the absolute runtime path.

```bash
dagrail plugin install --harness codex,claude-code,copilot-cli
dagrail plugin status
```

Missing or unstable native dispatch capabilities fall back to an explicit launch/resume envelope. A transport response is never treated as recipient-visible delivery.

Codex native lifecycle uses its local app-server only when daemon/proxy capabilities are
present. Claude and Copilot native turns run outside journal transactions and store only
bounded receipt metadata and digests, never model output. See
[`docs/harnesses.md`](docs/harnesses.md) for dispatch, receipt, reconcile, and replacement
session semantics.

## Authority and recovery

Only `.dagrail/project.yaml` belongs in the governed repository. Runtime state lives in the user data directory:

- immutable, hash-chained journal segments are canonical;
- SQLite is a disposable query projection;
- large artifacts, secrets, PII, prompts, and chat transcripts are not journal content;
- a command commits at atomic journal rename;
- external effects use `prepared → dispatched → confirmed | failed | unknown → reconciling` and require reconciliation after ambiguity.

Useful recovery commands:

```bash
dagrail journal verify
dagrail journal compatibility
dagrail journal export --output journal.ndjson
dagrail journal replay
dagrail projection rebuild
dagrail doctor
```

Create and verify a portable journal backup with `dagrail backup create` and
`dagrail backup verify`. Restore is exact-prefix-only and rebuilds SQLite automatically.
See [`docs/operations.md`](docs/operations.md) for status, history, incident, backup, and
read-only UI workflows.

## Extensions and security boundary

The public `sdk` package defines compile-in providers. Providers return decisions,
proposals, or receipts and do not receive journal or SQLite handles. DAGrail does not load
`.so`, WASM, Rego, or CEL modules.

Callable providers run behind input-schema validation, a deadline, panic recovery, a
64 KiB output limit, and secret-field screening. `experimental` providers may evolve;
`stable` providers must bind the exact `InputSchema` hash. See
[`docs/providers.md`](docs/providers.md) for the SDK and conformance workflow.

DAGrail separates cooperative roles but does not claim malicious-user isolation. Journal hashing is tamper-evident, not signed. Remote stores, encryption, identity signatures, multi-tenancy, and cross-region availability are outside the alpha boundary.

See `CONTEXT.md` for the domain vocabulary, `docs/adr/` for architectural decisions,
[`docs/roadmap.md`](docs/roadmap.md) for planned milestones, and
[`CHANGELOG.md`](CHANGELOG.md) for released changes.

## License

Apache-2.0.
