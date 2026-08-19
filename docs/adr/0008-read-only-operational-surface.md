# ADR 0008: Read-only local operational surface

- Status: accepted; foreground-process topology superseded by ADR 0022
- Date: 2026-08-14

## Context

Operators need to understand topology, readiness, dependency cuts, leases, attempts,
incidents, and recent history without loading that state into an LLM context. A browser
view is useful, but turning it into another control surface would widen the mutation and
authorization boundary.

## Decision

DAGrail exposes a loopback-only, foreground HTTP server through `dagrail ui`. The server
embeds its assets in the executable, accepts only `GET` and `HEAD`, and publishes a
bounded snapshot assembled from verified journal state. It exposes no allowed-action
references, controller tokens, event payloads, secrets, prompts, or artifact bodies.

The UI is a projection, never authority. Its topology and operational panels may be
recreated or replaced without affecting the journal. Mutations continue to use the CLI
or MCP application service, where revision, lease, idempotency, and policy checks apply.

## Consequences

- UI inspection does not add a second lifecycle writer.
- The server cannot bind a non-loopback address in v0.5.
- No login system, CDN, or remote dashboard is introduced. ADR 0022 later moved the
  loopback server behind the owner-local daemon without changing this read-only HTTP
  boundary.
- A future remote UI requires an explicit authentication, authorization, and transport
  decision rather than silently expanding this local boundary.
