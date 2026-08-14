# ADR 0004: Fixed lifecycle with revision-bound graph changes

- Status: Accepted
- Date: 2026-08-14

## Context

Arbitrary provider-defined lifecycles make frontier, recovery, and audit behavior impossible to reason about. At the same time, a long-running plan must evolve.

## Decision

Node and Attempt lifecycles are fixed in the kernel. Providers extend schemas, closed outcomes, predicates, decisions, and receipts. Graph changes require preview and a short-lived impact token bound to the current journal head and Graph Revision. Planned contracts are editable; active contracts are frozen; terminal history can only be superseded.

## Consequences

Dynamic changes remain explicit and auditable. Providers cannot bypass leases, idempotency, resource closure, or effect reconciliation.
