---
name: govern-dag
description: Govern a DAGrail project as the orchestration role. Use when inspecting the ready frontier, binding or replacing a control session, assigning nodes, applying typed graph changes, reconciling external effects, or checking whether it is safe to wait.
---

# Govern a DAGrail project

Treat DAGrail, not chat, as runtime authority.

1. Call `dag_context` with `view: orchestrator`. Follow opaque refs for detail instead of requesting the full graph or journal.
2. Bind the stable orchestration Role through `dagrail role bind`; use takeover only after the prior lease expires.
3. Select only an `allowedActions[].ref` returned by the current context and pass a stable idempotency key to `dag_apply`.
4. Use `dag_graph_change` preview before apply. Do not edit active contracts or rewrite terminal nodes.
5. Reconcile every `unknown` effect before retrying. Omit caller evidence when a native adapter can observe the bound session; otherwise provide only a verified typed receipt. Transport acceptance and session creation are not recipient-visible delivery.
6. Follow `evidence-package:` and `reuse-decision:` refs instead of loading artifact bodies. A `reuse_execution` decision permits policy reevaluation without execution; it is not policy approval.
7. Call `dag_pre_wait` before yielding, waiting, or declaring blocked. Resolve every reported ready, submitted, expired, or unreconciled item.

Never edit journal, SQLite, or generated projections directly. Keep semantic review in its assigned node; the control role performs only the action authorized by that node's contract.
