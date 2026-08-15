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
- capability-enforced Roles and NodeKind-specific lifecycle actions;
- immutable human, LLM, and provider Decision records with closed outcomes;
- replaceable sessions through stable roles, leases, attempts, and checkpoints;
- revision-bound allowed actions instead of hand-written lifecycle envelopes;
- bounded role-specific context through CLI or six high-level MCP tools;
- one machine-readable command catalog, generated shell completion, bounded typed
  process errors, and path-free installation diagnostics;
- a machine-readable beta compatibility contract and digest-bound observe-only shadows;
- immutable execution packages and deterministic evidence-reuse decisions;
- bounded compile-in providers with schema and stability contracts;
- recoverable external effects and resource closure with explicit `unknown` and `reconcile` states;
- an immutable RFC 8785 journal with disposable SQLite projections;
- explainable readiness, owned incidents, bounded history, verified backups, and a
  browser-opened read-only DAG Explorer with focused topology and deep links;
- a path-redacted local security audit, strict hostile-input limits, and schema-bound
  journal verification evidence;
- executable crash/ambiguity, cross-process contention, corruption, fuzz, and
  large-graph qualification suites.

## Status

`v0.19.0` is a pre-1.0 release candidate: local-first and single-user, with one native Go
executable, an immutable journal as authority, rebuildable SQLite projections, and
stdio MCP. Its operational surface adds explainable dependency blockers, incident
circuit breakers, digest-bound backup/restore, bounded history, and a loopback-only
read-only UI. Capability-gated native adapters support Codex thread lifecycle, Claude
Code headless completion turns, and GitHub Copilot ACP completion turns. One strict
receipt contract keeps transport, session, delivery, completion, and DAG acceptance
separate; every missing or unprovable capability retains an explicit manual fallback.
The Provider Runtime executes schema-bound extensions without giving them controller
storage handles. Reproducible release inputs, per-target SPDX SBOMs and build
provenance, digest-verified runtime upgrade/rollback, and optional detached signatures
form the distribution-security baseline. `dagrail contract` exposes the exact beta
surfaces implemented by a binary, while `dagrail observe` can qualify an existing DAG
through a separate, digest-bound shadow without modifying the source project.

The v0.16 distribution contract adds an offline-verifiable manifest for the complete
six-platform release set: six closed binary archives, six SPDX inventories, sorted
checksums, and exact tag/commit/source-date identity. Publication verifies that manifest
before release and attests it together with the checksum set.

The v0.17 operator surface adds `dagrail commands`, catalog-generated completion for
Bash, Zsh, Fish, and PowerShell, opt-in bounded JSON errors with stable broad exit
classes, cancellation propagation into slow host/provider/UI/MCP work, and a path-free
`dagrail doctor install` report. Use `--errors=json` before the command, or set
`DAGRAIL_ERROR_FORMAT=json`, when an automation needs the error envelope.

The v0.18 readiness surface pins and builds the complete v0.10–v0.17 beta binary
window, tests adjacent runtime upgrades and reversible rollbacks, and proves the current
binary can verify and recover a journal created by v0.10. `dagrail readiness` aggregates
that structural evidence without overstating it: the current decision is
`ready_for_external_validation`, while `oneDotZeroReady` and `productionValidated`
remain false. See the [readiness and external-validation runbook](docs/readiness.md).

The v0.19 governance-closure surface turns Role capabilities into enforced mutation
authorization, replaces generic completion with NodeKind-specific actions, journals
human/LLM/provider judgments as revision-bound Decision records, requires confirmed
resource-closure receipts before releasing capacity, and automatically skips branches
made permanently unreachable by closed positive predicates. Existing projects remain
external validation subjects; repository-specific conversion stays outside DAGrail.

The structural release report validates public source and automated gate
declarations, but deliberately reports `productionValidated: false`. Independent
external adoption, a long-running live DAG, real-host delivery receipts, and an operator
backup/restore drill remain outstanding before a 1.0 claim.

The v1beta1 Explorer adds bounded Topology, Nodes, Timeline, and Operations views. It
supports search, filters, stable local deep links, focused graph neighborhoods, and
payload-free Node inspection while retaining a closed `GET`/`HEAD`-only HTTP surface.
Its response/error schema and exact digest are published through `dagrail contract`.

The v0.12 security surface publishes a versioned threat model and machine-readable
audit schema. It closes public authority envelopes, bounds MCP frames and tool inputs,
verifies owner-only POSIX runtime state, and makes vulnerability and license checks CI
gates. The boundary remains cooperative: another malicious process running as the same
OS user is outside the guarantee.

The executable also contains an exact, digest-addressed local marketplace. A
normal plugin install materializes that immutable bundle and installs all three hosts
without fetching plugin content from a moving branch. `plugin conformance` reports the
runtime, bundle, host, MCP, native receipt, and manual-fallback layers independently.
`support preview|export` produces a schema-bound diagnostic with no authority payloads,
absolute paths, prompts, artifact bodies, raw harness output, or private graph IDs.

The reliability suite kills writer subprocesses on both sides of the journal rename
commit point, contends independent writers, corrupts disposable and authoritative
stores, fuzzes graph/patch/journal/receipt inputs, and holds context output to fixed
budgets on a 2,048-node frontier. See
[`docs/qualification.md`](docs/qualification.md) for the executable matrix and limits.

## Build

Until the first GitHub release is published, build from source with Go 1.26.6 or newer.
The module toolchain directive prevents builds with a known-vulnerable earlier 1.26
standard library:

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
dagrail contract
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
dagrail plugin runtime-status
dagrail plugin bundle-status
dagrail plugin conformance
```

`dagrail plugin uninstall` removes the MCP registration, plugin, and its matching
bundled marketplace registration. The verified runtime and immutable bundle are kept
so rollback and support inspection remain possible.

Runtime upgrades preserve one immutable, digest-addressed rollback candidate. Use
`dagrail plugin rollback` only after inspecting `runtime-status`; rollback switches the
shared executable, not host-specific manifest content.

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
dagrail security audit
dagrail support preview
dagrail recovery rehearse
dagrail qualify release --source .
dagrail release verify --directory dist
```

Create and verify a portable journal backup with `dagrail backup create` and
`dagrail backup verify`. Restore is exact-prefix-only and rebuilds SQLite automatically.
`recovery rehearse` restores the captured journal into disposable storage, replays the
reducer, rebuilds SQLite, and compares logical projection fingerprints without mutating
the live project. See [`docs/recovery.md`](docs/recovery.md) for the incident runbook and
[`docs/operations.md`](docs/operations.md) for status, history, incident, backup, and
read-only UI workflows, and [`docs/ui.md`](docs/ui.md) for Explorer bounds and deep
links.

For a larger executable walkthrough, see
[`examples/beta-project`](examples/beta-project) and the
[`beta operations guide`](docs/beta-operations.md). Existing DAGs can be assessed with
the strictly separate [`observe-only workflow`](docs/observe.md); it validates byte
identity and Graph structure, not semantic migration.

Portable exports can optionally be signed and verified over their exact bytes with
`dagrail signature keygen|sign|verify`. Signatures are detached, never required for
local runtime operation, and establish trust only when the public key is distributed
through a separately trusted channel.

Public integration contracts are summarized in [`docs/api.md`](docs/api.md), and new
users can start with [`docs/tutorial.md`](docs/tutorial.md). Release evidence and policy
live in [`docs/release.md`](docs/release.md), with community expectations in
[`GOVERNANCE.md`](GOVERNANCE.md), [`SUPPORT.md`](SUPPORT.md), and
[`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## Extensions and security boundary

The public `sdk` package defines compile-in providers. Providers return decisions,
proposals, or receipts and do not receive journal or SQLite handles. DAGrail does not load
`.so`, WASM, Rego, or CEL modules.

Callable providers run behind input-schema validation, a deadline, panic recovery, a
64 KiB output limit, and secret-field screening. `experimental` providers may evolve;
`stable` providers must bind the exact `InputSchema` hash. See
[`docs/providers.md`](docs/providers.md) for the SDK and conformance workflow.

DAGrail separates cooperative roles but does not claim malicious same-user isolation.
Journal hashing is tamper-evident; optional export signatures do not add journal actor
identity. Remote stores, encryption, identity signatures, multi-tenancy, and
cross-region availability are outside the alpha boundary.

See `CONTEXT.md` for the domain vocabulary, `docs/adr/` for architectural decisions,
[`docs/roadmap.md`](docs/roadmap.md) for planned milestones, and
[`CHANGELOG.md`](CHANGELOG.md) for released changes. Public beta guarantees and explicit
non-guarantees are in [`COMPATIBILITY.md`](COMPATIBILITY.md).
The versioned security analysis is in
[`docs/security/threat-model-v1.md`](docs/security/threat-model-v1.md).

## License

Apache-2.0.
