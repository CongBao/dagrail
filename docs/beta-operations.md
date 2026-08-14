# Beta operations guide

This checklist is the supported local operating path for DAGrail v0.10.

1. Validate and import a Graph Definition with a stable idempotency key.
2. Inspect `dagrail contract`, `doctor`, `status`, and `frontier` before assigning work.
3. Bind a stable Role to the current harness/session, then request bounded `context`.
4. Select an action returned by `action list`; never synthesize an action reference.
5. Checkpoint before handing an active Attempt to a replacement session.
6. Treat every `unknown` effect as possibly completed. Reconcile it before retrying.
7. Run `pre-wait` before declaring the graph idle or yielding an orchestrator session.
8. Periodically create and verify a portable journal backup. SQLite is disposable.

For an existing DAG, use the separate observe-only workflow before deciding whether to
migrate. The beta controller remains local-first, cooperative, single-user, and
LLM-driven: it does not autonomously assign ready Nodes or interpret semantic outcomes.

The executable sample under `examples/beta-project` demonstrates parallel Codex,
Claude Code, and Copilot worker Roles, replacement-session recovery, review, a manual
effect saga, and deterministic join/milestone settlement.
