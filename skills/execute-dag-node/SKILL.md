---
name: execute-dag-node
description: Execute or resume one DAGrail task, gate, decision, or effect node. Use when a work package names a stable Role and Node, when replacing a prior worker session, or when checkpointing and finishing an assigned attempt.
---

# Execute one DAGrail node

Work on only the assigned Role, Node, and Attempt.

1. Bind the named Role to this harness session. Do not reuse a session already bound to another formal role.
2. Call `dag_context` with `view: worker`, the Role, and Node. Inspect referenced evidence only when needed.
3. Start or resume using the returned allowed action. Never construct a lifecycle transition manually.
4. Before yielding or after material progress, apply `attempt.checkpoint` with a short durable summary and digest-only evidence refs. Do not include prompts, secrets, or large artifacts.
5. After a material execution, use the returned `evidence.publish` action to bind the candidate, prospective tree, command graph, protected inputs, observations, artifact digests, and provenance. Keep artifact bodies external.
6. Finish with one outcome declared by the NodeKind. If an effect is `unknown`, reconcile it instead of retrying.
7. Run `dag_pre_wait` before becoming passive.

The checkpoint must let a replacement session continue without chat history.
