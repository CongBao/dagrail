# ADR 0018: Idempotency binds command intent

## Status

Accepted in v0.20.0.

## Context

Matching only an idempotency key can suppress a duplicate write while still hiding a
caller error: the same key may be reused for another actor, object, action, or request
body. Returning the first result in that case is not a safe retry.

## Decision

Journal segment schema v3 commands may carry a canonical request digest and object
reference. Public mutations bind
the key to command kind, actor, target object, and the RFC 8785-normalized request.
Retries return the original result only when every available binding agrees. Changed
intent fails closed. Historical commands without the additive digest remain readable;
their older, narrower idempotency contract is not retroactively rewritten.

The current request is decoded and validated before the replay decision. Graph import
binds the canonical Graph Revision and provenance; provider import additionally binds
the provider ID and canonical input; GraphPatch apply binds the exact preview digest.
Schema v1/v2 segments remain byte-compatible but may not carry the v3 command fields.

Long provider, harness, and external-effect work remains outside the journal lock. The
digest proves local command intent, not remote exactly-once execution; ambiguous effects
still require receipt-based reconciliation.

## Consequences

The journal remains the portable authority and SQLite remains disposable. New command
segments are more precise audit records, while old segments keep their original bytes
and hash chain. Callers must use a fresh key when they intentionally change a request.
