# ADR 0009: Capability-gated native harness lifecycle

- Status: accepted
- Date: 2026-08-14

## Context

A transport exit code, a newly allocated session ID, and recipient-visible delivery are
different facts. DAGrail must use native harness lifecycle APIs when they are available
without making an unstable host API part of the controller's durable kernel.

## Decision

Native harness support is adapter-owned and capability-gated. For Codex v0.6, DAGrail
requires both the local app-server daemon and stdio proxy commands. It initializes the
JSON-RPC v2 connection, starts or resumes a thread, and starts a turn with the stable
DAGrail action ID as `clientUserMessageId`.

`thread/start` proves session creation. `turn/start` proves transport acceptance. Only
an `item/completed` notification for a `userMessage` with the bound thread, turn, and
client message IDs proves recipient-visible delivery. Later reconciliation may use
`thread/read` to verify the same tuple and observe turn status without sending input.

Native receipt detail stores protocol identifiers and state, never the prompt. Turn
completion remains separate from DAG acceptance and Node completion. DAGrail does not
auto-approve commands, infer semantic outcomes, or bind a Role from a session ID.

When probing or any protocol step is unavailable, the adapter returns an explicit manual
launch/resume envelope or an `unknown` receipt. It never upgrades partial evidence into
delivery.

## Consequences

- App-server protocol drift fails safe and remains isolated in the Codex adapter.
- A transient DAGrail CLI can use the harness-owned durable daemon without becoming a
  DAGrail daemon.
- Reconcile can inspect a native session without caller-crafted receipt JSON.
- Claude Code and GitHub Copilot remain manual until their own v0.7 conformance evidence
  exists.
