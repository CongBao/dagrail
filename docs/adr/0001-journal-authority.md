# ADR 0001: Immutable journal is canonical authority

- Status: Accepted
- Date: 2026-08-14

## Context

Long-running multi-agent DAGs must survive session replacement, duplicate commands, and local database loss. Keeping runtime truth in Git JSON, chat, and SQLite simultaneously creates split authority.

## Decision

Each modifying command commits one RFC 8785 canonical, hash-chained journal segment. Atomic rename is the commit point. SQLite and all human-facing views are rebuildable projections. The repository stores only `.dagrail/project.yaml`; runtime journal data remains in the per-user data root.

## Consequences

Journal formats require strict compatibility and upcasting. SQLite can be rebuilt or quarantined after corruption. Git is useful for exported definitions and audit copies but is not required for each lifecycle transition.
