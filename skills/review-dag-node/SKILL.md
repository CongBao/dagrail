---
name: review-dag-node
description: Review one DAGrail review or decision node using bounded evidence. Use when assigned semantic review, cold review, actual-artifact inspection, finding classification, or a closed approve/return decision.
---

# Review one DAGrail node

Review only the Role, Node, candidate, and evidence boundary in the assigned package.

1. Bind the assigned review Role and call `dag_context` with `view: reviewer`, `role_id`,
   and `node_id`. Never reuse a worker or controller session as the formal reviewer.
2. Inspect only the candidate, actual artifact, Decision, policy result, or evidence refs
   required by the review contract. Artifact indexes and digests are not substitutes for
   actual-artifact inspection when that inspection is assigned.
3. Separate semantic review, cold review, actual-artifact inspection, and deterministic
   admission. Do not repeat a completed responsibility from another Node.
4. Bind every finding to an owner, exact field or artifact, repair target, and concrete
   risk reduction. Stop at the declared scope; do not create an unbounded hardening loop.
5. Checkpoint the finding set before yielding. If only policy, validator, or fixture
   logic changed, apply `evidence.assess-reuse`; reuse execution only when DAGrail returns
   `reuse_execution`, then perform the new semantic judgment separately.
6. Apply only the returned `review.resolve` or `decision.record` action with one declared
   outcome and digest-only evidence refs. Do not merge, dispatch, close another Role's
   resources, or perform the receiving controller's lifecycle action.
7. Call `dag_pre_wait` before becoming passive and report any unresolved item outside
   this review Role to the controller.

Never treat transport acceptance, session creation, visible delivery, another reviewer's
opinion, or evidence reuse as this Node's approval.
