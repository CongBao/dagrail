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
protocol, and fallback. Do not infer support from a version string. A capability is true
only after its required command surface is detected.

In v0.6 Codex can use its harness-owned app-server daemon and stdio proxy. Claude Code
and GitHub Copilot still emit explicit manual envelopes; native conformance is planned
for v0.7.

DAGrail initializes without experimental API capability and sends only fields present in
the generated stable v2 schema. Individual JSON-RPC messages are capped at 16 MiB; a
larger resume/read response remains unproven rather than expanding controller context.

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

## Receipt semantics

| Receipt field | Codex evidence |
| --- | --- |
| `transportStatus: accepted` | `turn/start` returned |
| `sessionStatus: created` | `thread/start` or exact `thread/resume` returned |
| `deliveryStatus: visible` | matching completed `userMessage` item observed |
| `completionStatus` | bound turn status from `thread/read` |
| `acceptanceStatus` | remains pending until the DAG workflow records acceptance |

If dispatch returns `unknown`, run reconciliation without inventing evidence:

```sh
dagrail reconcile --root . --action ACTION_ID --idempotency-key reconcile-1
```

For a native Codex receipt, DAGrail calls `thread/read`. For a manual adapter, pass an
explicit typed receipt only after the recipient-visible state is independently known.

## Replacement sessions

Role identity is separate from the Codex thread ID. A worker checkpoints the Attempt,
releases its Role lease when possible, and the replacement binds that stable Role to its
new thread. If the old process disappeared, takeover is allowed only after lease expiry.
The replacement calls `dag_context` and continues from the durable checkpoint; chat
history is neither required nor authoritative.

Native resume sends a new bounded work instruction to an existing Codex thread. It does
not prove Node acceptance or completion, and it never bypasses the Role lease.
