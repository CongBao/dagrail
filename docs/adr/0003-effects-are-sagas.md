# ADR 0003: External effects use sagas and reconciliation

- Status: Accepted
- Date: 2026-08-14

## Context

Git merges, harness dispatch, CI, and artifact publication cannot share a transaction with a local journal. A process can fail before or after the external system changes.

## Decision

Every effect has a stable action ID and follows `prepared → dispatched → confirmed | failed | unknown → reconciling`. Preparation is journaled before invocation. Once dispatch intent is durable, restart recovery reconciles; it does not retry blindly. Receipts distinguish transport, session, visible delivery, acceptance, and completion.

## Consequences

DAGrail promises duplicate-resistant commands and deterministic reconciliation, not universal exactly-once delivery. An unknown effect freezes only its dependency cut while unrelated lanes continue.
