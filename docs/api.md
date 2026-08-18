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

`dagrail artifact inspect-scope` compares exact base, candidate, target, and prospective
Git commits and classifies complete tree entries (path, mode, and object ID) without
treating target history as producer scope. Rename endpoints and discarded candidate
changes remain explicit and unexplained prospective deltas fail closed.
`dagrail artifact verify-git-closure` checks exact commit/tree/tag identities, ordered
parents, peeled refs, and continued reachability through actual full refs; revision
expressions are not retention authority. Commit identity and retention are derived from
raw object bytes and the raw ordered-parent graph, not replace refs, grafts, or Git's
interpreted revision view. Both bind the caller-selected repository after removing
repository-redirection environment, disable Git lazy fetch, fail closed on missing
promisor objects, use bounded batch reads, and are byte-nonmutating read surfaces with
schemas and digests published by `dagrail contract`. Closure manifests must be regular,
non-symlink files of at most 1 MiB; their bounded reader honors caller cancellation.

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

Project query opens are byte-nonmutating. In particular, `status`, `history`,
`context`, `frontier`, `inspect`, `evidence list`, `pre-wait`, `doctor`, `security
audit`, `journal verify|compatibility|export`, `backup create|verify`, `graph export`,
`provider list|check`, `support preview|export`, `recovery rehearse`, and lifecycle
validation/projection do not settle automatic Nodes, synchronize the disposable
projection, or create SQLite WAL/SHM files. Commands that name an output path may write
only that separate artifact. Projection diagnostics inspect a stable checkpointed
SQLite image; any existing WAL/SHM state fails closed instead of being ignored or
checkpointed. Journal-derived query state remains authoritative. Inspection still
requires the ordinary local authority claim, establishment fence, and lineage and does
not create a missing runtime. A failing query report does not authorize or perform
repair; relaxed authority access exists only behind explicit recovery commands.
The signed-action secret is created durably during explicit initialization or another
writable open. An inspection handle never creates or repairs it; a damaged/legacy
runtime missing that file receives a closed diagnostic until an authorized writable
command performs the local bootstrap.

Authority adoption, rotation, and relocation publish a replacement locator only after
the schema-4 establishment fence and its rebuildable projection are both durable. An
exact retry may recreate a missing or corrupt projection from the replacement journal
without changing the committed recovery receipt. The journal snapshot remains writer
locked through that rebuild, and projection synchronization never moves its journal
cursor backward.

`dagrail readiness` returns ReadinessDecision v1alpha1. It combines source
qualification with the closed beta compatibility window and optional project or local
installation checks. A successful process exit means `externalValidationReady`, not
production validation or 1.0 readiness. The v1alpha1 schema deliberately fixes both of
those stronger booleans to false and always carries the outstanding adoption gaps.

## MCP

The stdio server exposes only these high-level tools:

| Tool | Purpose | Mutation |
| --- | --- | --- |
| `dag_context` | bounded role/view work package, authorization, remediations, actions, and cursor delta | no |
| `dag_inspect` | follow opaque refs, including `effect-continuity:<action-id>`, into selected detail | no |
| `dag_apply` | apply a controller-issued allowed-action ref | yes |
| `dag_graph_change` | preview or apply a revision-bound graph patch | apply only |
| `dag_reconcile` | observe an ambiguous external effect | yes |
| `dag_pre_wait` | prove liveness before yielding | no |

Tool input-schema digests are part of `dagrail contract`. Callers cannot construct raw
lifecycle transitions; allowed-action refs bind project, head, Graph Revision, Role
lease, Node/Attempt, provider set, and expiry. The returned `inputSchema` is enforced at
`dag_apply`/`action apply` before the journal commit; it is not advisory metadata.
The orchestrator view also exposes project-wide signed actions and deterministic
remediation proposals. An `incident.supersede` action can close an Attempt incident only
when a typed edge or `supersedes` declaration identifies an active/planned repair Node.
Effect continuity compares the prepared adapter ID, version, schema hash, and canonical
request digest separately from unrelated journal-head advancement. A missing legacy
binding or same-ID adapter upgrade fails reconciliation closed.
`dag_context` accepts only `orchestrator`, `worker`, or `reviewer`; a caller may lower
the byte budget to at least 512 bytes but cannot raise the fixed 12/8/12-KiB maximum.
When a schema-legal Role or Node ID is larger than the MCP selector limit, pass the
controller-issued `role_ref` or `node_ref` instead of copying the identifier. Graph
apply accepts `actor_role_ref`, and Effect reconcile accepts `effect_ref`; each is an
opaque selector for the exact current authority object, not a caller-defined alias.
When content must be truncated, compact authorization and a fixed-length opaque
`operations:<key>` ref survive;
following that opaque ref recovers bounded signed actions and remediation proposals.
The operations index and each action page are capped at 24 KiB. Exact action and
remediation counts remain visible; truncation returns journal-head and inventory-bound
`operations-actions:<opaque-role>:<head>:<digest>:<offset>` and pre-wait inspect refs.
Action refs switch to an equivalent compact, signed binding when declaration IDs would
otherwise dominate the response. Oversized action detail and liveness identifiers are
returned through digest-bound base64 chunks; callers concatenate decoded chunks and
verify the advertised digest instead of expanding one large value into model context.
The same bounded-identity rule covers Role/session authorization and imported Attempt
IDs. A terminal action whose declared outcome is too large for the 64-KiB action input
uses the controller-issued `outcomeRef`; its `x-dagrailOutcomeOptions` entry binds that
short ref to an optional digest-bound outcome-ID detail stream. The ref selects exactly
one outcome on the signed Node and cannot be reused on another Node.
`dag_pre_wait` is independently bounded: it returns exact per-category counts, small
deterministic previews, `truncated`, and
`pre-wait-page:<head>:<inventory-digest>:<offset>` inspect refs. A page fails stale if
either the journal head or time-dependent liveness inventory changes. A
remediation embeds only a small dependency-cut preview plus its exact count, SHA-256
digest, and byte-bounded `dependency-cut:<digest>:<offset>` pages; an individually large
cut member uses its own digest-bound chunk ref. CLI and MCP cancellation
interrupt context, inspect, operations, inventory, and cut computation instead of
returning a misleading partial diagnostic.
If an apply call crashes, times out, or loses its response, retry the original signed
ref with the same RFC 8785 canonical JSON input value and the same idempotency
key—even after a session replacement. Whitespace and object-key order may differ;
the semantic JSON value may not. This exception retrieves the committed result only;
old refs remain invalid for changed input, a new key, or new work.
Open Incidents use snapshot-bound incident-index pages rather than copying unbounded
detail into the work package. Status, history, frontier, evidence indexes, individual
authority objects, and Effect continuity also switch to 24-KiB summaries plus
digest-bound chunks when necessary. Direct CLI evidence filters accept `--node-ref`
and `--attempt-ref`; mutating CLI selectors use the corresponding opaque ref flags.
Graph apply and effect reconciliation recheck the active owner lease. Tool cancellation
is propagated into policy providers and effect adapters; cancellation never commits a
semantic Decision that was not returned.

Decision and Gate Nodes produce DecisionRecord v1alpha1 authority. A record binds the
closed outcome and digest-only evidence to one Graph Revision and Attempt. Provider
decisions additionally bind the exact provider ID, version, and input-schema hash;
generic provider invocation output alone never advances a Node.

## JSON schemas

Published schemas live in `schemas/`. Current governed reports include lifecycle
migration/projection, Git artifact closure/integration scope, the Explorer UI
API, security audit, journal verification, plugin conformance, support report, recovery
rehearsal, explicit legacy-authority adoption, authority rotation/relocation, release qualification,
and the compatibility contract itself. Reports use
closed objects so misspelled or silently added fields fail validation. ReleaseManifest
v1beta1 and ReleaseVerification v1alpha1 bind the complete distribution set separately
from source qualification.

Lifecycle migration retains the v0.22 Effect shape: `adapterVersion` and
`adapterSchemaHash` are an optional pair for imported history. Current writers always
emit both. Missing legacy metadata is preserved as unknown and blocks automatic Effect
reconciliation; supplying only one field or changing a bound value fails closed.

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
