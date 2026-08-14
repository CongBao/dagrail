# ADR 0007: Compile-in providers run behind a bounded application boundary

- Status: Accepted
- Date: 2026-08-14

## Context

Compile-time registration avoids an unstable binary plugin ABI, but registration alone
does not make extension code safe to call. A provider can receive malformed input,
panic, ignore cancellation, return an unbounded document, leak a secret-like field, or
silently change the schema promised by a stable release.

## Decision

Policy, predicate, graph-importer, and projection providers are invoked only by the
DAGrail Provider Runtime. Callable providers expose a self-contained JSON Schema.
Before invocation, DAGrail validates metadata, authority JSON, sensitive-field rules,
and the request schema. Calls have a five-second default deadline, recover panics, and
return at most 64 KiB of valid, secret-screened authority JSON. Graph importers must
return a valid Graph Definition before the application can commit it.

Provider metadata declares `experimental` or `stable`. Omitted stability remains
`experimental` for source compatibility. A stable provider's `schemaHash` must equal
`sdk.InputSchemaHash(InputSchema())`; experimental providers still require a valid
schema when called but may evolve it between custom builds.

The runtime never passes journal, SQLite, filesystem, or network handles to providers.
Generic `provider invoke` output is diagnostic and does not become DAG authority.
Application paths such as provider-backed graph import decide whether and how a result
is validated and journaled. Provider schemas cannot load remote references.

## Consequences

Custom distributions have one testable call boundary and can fail closed before
extension code runs. Timeouts cannot forcibly terminate Go code that ignores context,
so providers remain trusted compile-in code; the bounded goroutine has no controller
handles and cannot commit DAG state. Strong process isolation remains a future option.
