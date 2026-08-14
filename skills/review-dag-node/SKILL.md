---
name: review-dag-node
description: Review one DAGrail review or decision node using bounded evidence. Use when assigned semantic review, cold review, actual-artifact inspection, finding classification, or a closed approve/return decision.
---

# Review one DAGrail node

Keep review scope closed and evidence-bound.

1. Call `dag_context` with `view: reviewer`, the assigned Role, and Node.
2. Inspect only the candidate, artifact, policy decision, or evidence refs required by the node contract.
3. Bind each finding to an owner, exact field or artifact, repair target, and risk reduction. Do not open unrelated hardening loops.
4. Checkpoint the finding set before yielding. Use `evidence.assess-reuse` when a policy or validator changed: reuse only when DAGrail returns `reuse_execution`, and still perform the new semantic policy judgment.
5. Select one declared terminal outcome such as approve or return. Do not perform merge or another role's lifecycle action.

Never treat delivery acknowledgement, transport acceptance, or another reviewer's opinion as this node's decision.
