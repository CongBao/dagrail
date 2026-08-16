# Harness lifecycle adapters

DAGrail treats a harness as an external-effect adapter. Native capabilities are optional;
the immutable journal and Node lifecycle do not depend on a specific agent host.

## Probe capabilities

```sh
dagrail harness probe --harness codex
dagrail harness probe --harness claude-code
dagrail harness probe --harness copilot-cli
```

The result reports detected executable, version, closed capability booleans, native mode,
protocol stability, execution mode, proof classes, and fallback. Do not infer support
from a version string. A capability is true only after its required command surface is
detected.

`dagrail plugin conformance` is stricter than name discovery: the verified runtime
receipt, the textual hook launcher resolved through `PATH`, and the parsed MCP command
plus `mcp --stdio` arguments must all identify the same real executable and SHA-256.
Ambiguous host output or a same-version executable at another path fails closed and
leaves the manual fallback available. Codex and Copilot conformance accept only one
`dagrail` entry from an explicitly supported top-level JSON server collection. Claude
Code conformance uses the public, name-specific `mcp get dagrail` command and requires
one exact `Type`, `Command`, and `Args` field set. Hosts whose current output does not
match their closed probe remain unverified.

Plugin installation and status cannot prove that an already-running harness process
hot-loaded a newly registered MCP server. After install or upgrade, start a fresh
harness session and verify the six tool names are callable. A visible DAGrail skill is
not that proof. If restart is deferred, use the matching typed CLI commands
(`context`, `inspect`, `action apply`, `graph preview-change|apply-change`, `reconcile`,
and `pre-wait`) and never handcraft a lifecycle envelope. `dagrail doctor install`
separates persisted `configurationReady` from activation `ready`, reports
`fresh-session-or-cli-fallback`, and keeps both `ready` and
`currentProcessVerified` false until process-visible proof exists.

Native support is intentionally asymmetric:

| Harness | Native mode | Lifecycle boundary |
| --- | --- | --- |
| Codex | stable app-server daemon/proxy | asynchronous start, resume, and read-only turn observation |
| Claude Code | stable headless JSON CLI | synchronous start/resume; no read-only reconcile API |
| GitHub Copilot CLI | experimental ACP v1 stdio | synchronous dispatch only; session ends with the ACP child process |

Every other operation uses the returned explicit manual envelope. In particular, an ACP
`loadSession` capability bit is not treated as durable resume support: qualification
against Copilot CLI 1.0.68 showed that a new stdio server could not load the session made
by the previous server process.

For Codex, DAGrail initializes without experimental API capability and sends only fields
present in the generated stable v2 schema. Codex and ACP JSON-RPC messages are capped at
16 MiB; a larger response remains unproven rather than expanding controller context.

## Dispatch from an effect Node

Use a normal `effect` Node with adapter `harness.codex`:

```yaml
- id: dispatch-worker
  kind: effect
  role: orchestrator
  title: Dispatch implementation worker
  inputs:
    adapter: harness.codex
    request:
      workingDirectory: .
      roleId: developer
      nodeId: implement
  outcomes:
    - id: delivered
      class: success
    - id: delivery-failed
      class: failure
```

The effect's prepared binding resolves the working directory inside the project. The
adapter starts a persistent Codex thread, sends a generated bounded work instruction,
and binds the turn's user message to the stable DAGrail action ID.

Change the adapter ID to `harness.claude-code` or `harness.copilot-cli` only after
`harness probe` reports `dispatch: true` for that installed executable. Copilot ACP
permission calls default to one-shot rejection. A graph author may opt into one-shot
approval for that effect only:

```yaml
inputs:
  adapter: harness.copilot-cli
  request:
    workingDirectory: .
    roleId: developer
    nodeId: implement
    permissionPolicy: allow-once
```

`allow-always` is rejected. Claude Code continues to use the user's own Claude permission
configuration; DAGrail passes no bypass flag.

## Receipt semantics

| Receipt field | Codex | Claude Code | Copilot CLI |
| --- | --- | --- | --- |
| `transportStatus: accepted` | `turn/start` returned | headless process produced a bound result | ACP prompt returned |
| `sessionStatus: created` | exact start/resume returned | result carried the preselected session ID | ACP `session/new` returned |
| `deliveryStatus: visible` | exact completed `userMessage` item | synchronous result for the exact generated prompt | matching JSON-RPC prompt response and stop reason |
| `completionStatus` | bound turn status from `thread/read` | headless result terminal state | ACP stop reason |
| `acceptanceStatus` | pending | pending | pending |

Claude and Copilot calls are synchronous and bounded to two hours. They execute after the
effect's durable `prepared` and `dispatched` events and outside the journal lock. A crash
during the child process stays `unknown`; do not send the prompt again merely because a
receipt is missing.

If dispatch returns `unknown`, run reconciliation without inventing evidence:

```sh
dagrail reconcile --root . --action ACTION_ID --idempotency-key reconcile-1
```

For a native Codex receipt, DAGrail calls `thread/read`. Claude has no read-only native
reconcile in this release. Copilot ACP sessions created by the stdio adapter are not
advertised as cross-process resumable or inspectable. For those cases, pass an explicit
typed receipt only after the recipient-visible state is independently known.

## Replacement sessions

Role identity is separate from the Codex thread ID. A worker checkpoints the Attempt,
releases its Role lease when possible, and the replacement binds that stable Role to its
new thread. If the old process disappeared, takeover is allowed only after lease expiry.
The replacement calls `dag_context` and continues from the durable checkpoint; chat
history is neither required nor authoritative.

MCP registrations always use the verified runtime's absolute path. Host hook manifests
use the portable `dagrail hook ...` launcher form, so installation and conformance also
start a fresh process and require that `dagrail` on `PATH` resolves to the exact same
runtime. A missing or shadowed launcher fails closed before host installation.

Native Codex or Claude resume sends a new bounded work instruction to an existing host
session. It does not prove Node acceptance, and it never bypasses the Role lease. Copilot
resume uses the manual envelope until a durable, capability-probed host lifecycle is
available.
