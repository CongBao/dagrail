# Public API and schema guide

DAGrail has four public integration surfaces. Everything under `internal/` is an
implementation detail and carries no source-compatibility promise.

## Compatibility discovery

`dagrail contract` is the machine-readable compatibility index. It reports the binary
version, top-level CLI commands, exactly six MCP tools, context budgets, readable/write
journal versions, projection schema, provider SDK version, and the path plus SHA-256
digest of every governed JSON report schema.

The beta line is additive unless a surface selects a new `apiVersion`. Automation
should select API versions and schema digests, never human wording or JSON key order.

## CLI

CLI mutation commands require a stable idempotency key and, where applicable, an
expected revision or controller-issued action ref. JSON output is intended for scripts;
human frontier output is the exception and can be replaced with `--format json`.

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

`dagrail doctor install` is a local, path-free diagnostic for the linked runtime,
plugin bundle, selected harness registrations, and MCP launchers. It reports closed
status codes without including executable paths or raw host output.

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

## JSON schemas

Published schemas live in `schemas/`. Current governed reports include the Explorer UI
API, security audit, journal verification, plugin conformance, support report, recovery
rehearsal, release qualification, and the compatibility contract itself. Reports use
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
