# ADR 0016: The v0.x security boundary is one cooperative OS user

## Status

Accepted for v0.12.

## Decision

DAGrail treats the host OS account and its filesystem policy as the outer isolation
boundary. Roles, leases, and capabilities prevent accidental control-plane collisions;
they are not authorization against a malicious peer process running as that user.

Authority and protocol inputs use closed, bounded decoding. Runtime data is owner-only
where portable mode bits can be verified. Windows reports structural checks and the
need for host ACL inspection instead of fabricating a portable ACL guarantee. Security
diagnostics return typed categories, counts, hashes, and modes without authority
payloads or absolute paths.

## Consequences

The single static binary stays local-first and does not acquire accounts, tokens, a
daemon, or a policy server. Multi-user or remote operation will require a new trust
model, authenticated transport, key lifecycle, authorization policy, storage tenancy,
and migration plan rather than silently extending this contract.
