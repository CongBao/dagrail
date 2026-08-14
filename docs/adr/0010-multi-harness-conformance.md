# ADR 0010: Native harness support is evidence-graded

Status: accepted for v0.7.

## Context

Codex, Claude Code, and GitHub Copilot CLI expose materially different lifecycle
surfaces. Treating a process exit, session ID, transport response, message visibility,
turn completion, and DAG acceptance as one boolean recreates the ambiguity DAGrail is
intended to remove.

Capability declarations are also insufficient by themselves. During v0.7 qualification,
Copilot CLI 1.0.68 negotiated ACP v1 and advertised `loadSession`, but a fresh stdio ACP
server could not load the session created by the previous server process. The session
was therefore not a durable resume identity in DAGrail's process model.

## Decision

All first-party native adapters pass one receipt conformance contract:

- `confirmed` requires recipient-visible delivery evidence;
- visible delivery requires an accepted transport, created session, and stable external
  ID;
- harness turn completion requires a proved delivery;
- an adapter may report only `pending` or `unknown` DAG acceptance;
- model output, full prompts, and transcripts are never persisted in receipt detail.

Capability probes additionally report stability, execution mode, and proof classes.
Codex uses asynchronous app-server turns. Claude Code uses a synchronous headless JSON
turn. Copilot uses a synchronous ACP v1 prompt response; its native session is considered
ephemeral, so resume and read-only observation remain manual in v0.7.

Copilot ACP permission requests default to one-shot rejection. A Graph Definition may
explicitly request `permissionPolicy: allow-once`; persistent automatic approval is not
supported.

## Consequences

The three adapters do not claim feature parity. Native support can improve independently
without changing the DAG lifecycle. Protocol drift or missing proof degrades to a typed
`unknown` receipt or explicit manual envelope instead of an optimistic success.

References:

- [GitHub Copilot CLI ACP server](https://docs.github.com/en/copilot/reference/copilot-cli-reference/acp-server)
- [Agent Client Protocol v1](https://agentclientprotocol.com/protocol/overview)
- [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference)
