# ADR 0025: Controller Role Transfer Preserves Session Audit

## Status

Accepted for v0.26.6.

## Context

A durable Attempt can outlive the harness session that owns its stable Role. When that
session is unavailable but its lease has not expired, ordinary takeover correctly
refuses to replace it: silently widening takeover would permit split-brain execution.
Waiting for expiry can nevertheless block an explicitly authorized successor, and
binding a controller under the worker Role would falsify audit identity.

## Decision

DAGrail defines the separate `role.control` capability and exact
`role.control-transfer` action. A currently leased, distinct controller Role may replace
one unexpired target lease only when the signed action and command name the target's
exact current session. The append-only `role.transferred` event retains the truthful
controller Role/session, complete previous and successor leases, reason, and timestamp.

Ordinary bind/takeover remains unchanged: a different session may take over only after
expiry. Controller transfer rejects self-transfer, stale actor or target sessions,
expired leases, changed idempotency intent, and missing capability. It changes only the
executor binding; the stable Role, active Attempt, checkpoint, evidence, resources, and
Node state are not rewritten.

## Consequences

Controllers receive a signed allowed action through bounded context and MCP `dag_apply`,
and operators have a typed `role transfer` CLI fallback. Lifecycle migration revalidates
the same compare-and-swap lease facts and capability at the transfer timestamp. Projects
must explicitly grant `role.control` through a reviewed Graph revision; no Role name is
privileged by the kernel.
