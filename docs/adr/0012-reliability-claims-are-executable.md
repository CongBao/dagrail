# ADR 0012: Reliability claims are executable contracts

Status: accepted for v0.9.

## Context

An append-only journal, idempotency key, or bounded context is only an architectural
claim until failure windows and scale limits are exercised. Real power loss and disk
failure cannot be reproduced portably in ordinary CI, and exposing a production fault
injection API would create a new control-plane attack surface.

## Decision

The journal has an unexported, nil-by-default fault callback at named commit phases.
Tests use it to produce deterministic I/O errors and hard subprocess exits before and
after atomic rename. The same suite launches independent OS processes to verify the
file lock and idempotency contract, corrupts exact authority bytes, and destroys SQLite
projection bytes to prove reconstruction.

Go fuzz targets cover Graph Definitions, GraphPatch parsing and application, portable
journal validation, and native receipt conformance. Production graph and patch inputs
are capped at 8 MiB, while predicate AST recursion is capped at 64 levels. A 2,048-node
fixture guards full frontier inspection and the three fixed context budgets.

## Consequences

CI can continuously falsify the most important recovery claims without invoking real
harnesses or effects. A failure before rename is definitely uncommitted; a failure
after rename is treated as ambiguous and converges through journal replay plus the same
idempotency key. Large frontiers remain inspectable without entering LLM context in
full.

The suite is evidence, not a proof of filesystem behavior under every kernel, device,
or power-loss mode. The fault callback is not exported and is nil in production. Longer
fuzz campaigns and platform-specific durability testing remain release-engineering
activities.
