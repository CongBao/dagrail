# Beta multi-harness example

This graph is a non-autonomous control-plane example. One architect decision opens
three parallel worker Nodes assigned to Codex, Claude Code, and Copilot Roles. A
deterministic join opens review, review opens a manual publication effect, and a
milestone settles only after the effect is explicitly reconciled and accepted.

The executable qualification in `internal/e2e/beta_project_test.go` deliberately
replaces the Codex session after a checkpoint, reopens the controller repeatedly,
retries one effect with the same idempotency key, rebuilds SQLite from the journal,
and asserts that all role-specific contexts stay within their byte budgets.
