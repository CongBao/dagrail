# Public API and schema guide

DAGrail has four public integration surfaces. Everything under `internal/` is an
implementation detail and carries no source-compatibility promise.

## Compatibility discovery

`dagrail contract` is the machine-readable compatibility index. It reports the binary
version, top-level CLI commands, exactly six MCP tools, context budgets, readable/write
journal versions, projection schema, provider SDK version, and the path plus SHA-256
digest of every governed JSON report schema.

The Graph entry also publishes its exact schema path/digest and a sorted closed
capability list. Consumers must use that entry to discover resource capacities,
resource requests, dynamic graph changes, and lifecycle migration support; an adapter
omission or prompt example is not evidence that the capability is absent.

The beta line is additive unless a surface selects a new `apiVersion`. Automation
should select API versions and schema digests, never human wording or JSON key order.

## CLI

CLI mutation commands require a stable idempotency key and, where applicable, an
expected revision or controller-issued action ref. JSON output is intended for scripts;
human frontier output is the exception and can be replaced with `--format json`.
Role capabilities are authorization inputs, not labels: graph import validates their
closed declaration shape, and every action apply rechecks the current lease and the
NodeKind-specific capability. Older Graph definitions remain importable but cannot
apply newly protected mutations until a revision grants the corresponding capability.
Terminal actions are `task.complete`, `review.resolve`, `decision.record`,
`gate.evaluate`, and `effect.complete`; custom NodeKinds retain `attempt.finish`.
New mutation commands also bind the idempotency key to actor, target, command kind, and
an RFC 8785 request digest. Reusing a key with changed intent fails closed. Historical
commands without this additive journal field retain their original bytes and narrower
retry contract.

`dagrail commands` returns the detailed CommandCatalog v1alpha1 used by dispatcher
validation and shell completion. `dagrail completion bash|zsh|fish|powershell` emits a
bounded script derived from that catalog. `dagrail contract` binds the catalog schema
and digest into the broader compatibility surface.

By default, command errors remain concise text on stderr. Automation can place
`--errors=json` before the command or set `DAGRAIL_ERROR_FORMAT=json` to receive a
CLIError v1alpha1 envelope. The stable broad exits are `1` operation failure, `2` usage,
`7` failed diagnostic, and `130` interruption. Domain-specific lifecycle outcomes stay
in command reports rather than being inferred from process codes. Error messages are
bounded to 2 KiB and the complete envelope to 4 KiB.

Exit status is nonzero when a typed report is emitted but its gate fails, including
`doctor`, `doctor install`, `recovery rehearse`, and `qualify release`.
`release manifest|verify` is the maintainer-facing, offline distribution contract; it never
publishes or downloads an artifact. Process cancellation is propagated into MCP, UI,
provider, projection, and host-plugin work; harness-management subprocesses also have a
two-minute ceiling and a 64-KiB combined-output cap.

`recovery adopt-legacy-authority`, `rotate-authority`, and `relocate-authority` are
explicit operator mutations rather than normal orchestration actions. Relocation accepts
an authenticated backup of an already-established replacement plus the expected source
head and target locator ancestor, and returns AuthorityRelocationReceipt v1alpha1. Its
new authority is fence-only; Graph/history bootstrap remains a separate operation.

`dagrail doctor install` is a local, path-free diagnostic for the linked runtime,
plugin bundle, selected harness registrations, and MCP launchers. It reports closed
status codes without including executable paths or raw host output. It cannot attest
that the caller's already-running harness hot-loaded a registration, so it reports
`configurationReady: true` when persisted registration is complete, but keeps
activation `ready: false`, `currentProcessVerified: false`, and
`fresh-session-or-cli-fallback` until a fresh process exposes the tools.

`lifecycle validate-history|import-history` is a separate operator surface. v1beta1
records contain ordered `commands[]`, each validated as an independent closed writer
command; v1alpha1 remains readable with exactly one command per record. Both require
an external manifest and an independently supplied source-authority digest; import also
requires an actor Role label and stable idempotency key. It is deliberately absent from
MCP so an agent cannot turn source-specific conversion or cutover into an ordinary
allowed action. `lifecycle projection` is read-only and deterministic.

`dagrail readiness` returns ReadinessDecision v1alpha1. It combines source
qualification with the closed beta compatibility window and optional project or local
installation checks. A successful process exit means `externalValidationReady`, not
production validation or 1.0 readiness. The v1alpha1 schema deliberately fixes both of
those stronger booleans to false and always carries the outstanding adoption gaps.

## MCP

The stdio server exposes only these high-level tools:

| Tool | Purpose | Mutation |
| --- | --- | --- |
| `dag_context` | bounded role/view work package and cursor delta | no |
| `dag_inspect` | follow opaque refs into selected detail | no |
| `dag_apply` | apply a controller-issued allowed-action ref | yes |
| `dag_graph_change` | preview or apply a revision-bound graph patch | apply only |
| `dag_reconcile` | observe an ambiguous external effect | yes |
| `dag_pre_wait` | prove liveness before yielding | no |

Tool input-schema digests are part of `dagrail contract`. Callers cannot construct raw
lifecycle transitions; allowed-action refs bind project, head, Graph Revision, Role
lease, Node/Attempt, provider set, and expiry.
`dag_context` accepts only `orchestrator`, `worker`, or `reviewer`; a caller may lower
the byte budget to at least 512 bytes but cannot raise the fixed 12/8/12-KiB maximum.
Graph apply and effect reconciliation recheck the active owner lease. Tool cancellation
is propagated into policy providers and effect adapters; cancellation never commits a
semantic Decision that was not returned.

Decision and Gate Nodes produce DecisionRecord v1alpha1 authority. A record binds the
closed outcome and digest-only evidence to one Graph Revision and Attempt. Provider
decisions additionally bind the exact provider ID, version, and input-schema hash;
generic provider invocation output alone never advances a Node.

## JSON schemas

Published schemas live in `schemas/`. Current governed reports include lifecycle
migration/projection, the Explorer UI
API, security audit, journal verification, plugin conformance, support report, recovery
rehearsal, explicit legacy-authority adoption, authority rotation/relocation, release qualification,
and the compatibility contract itself. Reports use
closed objects so misspelled or silently added fields fail validation. ReleaseManifest
v1beta1 and ReleaseVerification v1alpha1 bind the complete distribution set separately
from source qualification.

## Go provider SDK

The public packages are:

- `github.com/CongBao/dagrail/sdk` for provider interfaces and shared types;
- `github.com/CongBao/dagrail/providers` for compile-time registration.

Providers return decisions, proposals, or receipts. They never receive journal or
SQLite handles. v0.x supports compile-in Go providers only; `.so`, WASM, Rego, CEL, and
runtime-downloaded code are outside the supported boundary. Stable providers bind a
SemVer and input-schema hash; experimental providers may change before 1.0.

## Error and effect semantics

External effects are sagas, not database transactions. `unknown` means the controller
cannot prove success or failure and must reconcile by stable action ID before retrying.
Transport response, session creation, recipient-visible delivery, acceptance, and
completion are distinct receipts. A failed Node freezes only its transitive dependency
cut; unrelated ready work remains available.
Resource closure follows the same proof rule: completion cannot release active capacity.
The Role applies a controller-issued `resource.close` action and, when the receipt is
unknown or failed, a later `resource.reconcile`. Only `confirmed` releases capacity.
