# ADR 0011: Distribution is reproducible; signatures remain optional

Status: accepted for v0.8.

## Context

DAGrail is a control-plane executable installed into several agent hosts. A partially
published upgrade, mutable rollback copy, ambiguous release archive, or signature that
silently canonicalizes the exported payload would undermine the recovery guarantees of
the runtime it distributes. At the same time, requiring a signing service or local PKI
would make an early local-first controller materially heavier.

## Decision

Release inputs use the source commit timestamp, trimmed paths, disabled VCS embedding,
an empty Go build ID, deterministic archive metadata, sorted checksums, per-target SPDX
SBOMs, and GitHub build-provenance attestations. Every third-party workflow action is
pinned to a full commit.

Runtime upgrade is a local saga. The running candidate is validated in a fresh process,
the installed executable is preserved at a SHA-256-addressed path, publication uses an
atomic replacement where supported, and the final path is checked for both digest and
reported version. A versioned receipt binds the current and optional rollback artifact.
Rollback performs the same checks and preserves the displaced runtime, so it can be
reversed.

Portable files may be signed with detached Ed25519 envelopes. DAGrail signs a
domain-separated SHA-256 digest of the exact file bytes; it does not parse or
canonicalize the payload. Signing is never required for journal operation. Trust comes
from separately distributing the public key, not from embedding a key in the signed
file.

## Consequences

Published artifacts have independently inspectable checksums, software inventories,
and provenance. Runtime corruption and partial upgrades fail closed with a bounded
rollback path. Optional signatures can protect exported backups or journals without
turning SQLite, a key service, or identity infrastructure into runtime dependencies.

These controls do not provide hostile same-user isolation, actor identity, encryption,
revocation, or transparency logs. Host manifest rollback and remote trust policy remain
future work.
