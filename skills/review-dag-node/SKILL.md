---
name: review-dag-node
description: Review one DAGrail review or decision node using bounded evidence. Use when assigned semantic review, cold review, actual-artifact inspection, finding classification, or a closed approve/return decision.
---

# Review one DAGrail node

Review only the Role, Node, candidate, and evidence boundary in the assigned package.

First verify that the DAGrail MCP tools are callable in this process. Skill discovery is
not proof that a long-running harness loaded an upgraded MCP registration. If tools are
absent, run `dagrail doctor install`, start a fresh session, or use `dagrail context`,
`dagrail inspect`, `dagrail action apply`, and `dagrail pre-wait`; never construct a
review transition manually.

1. Bind the assigned review Role and call `dag_context` with `view: reviewer` and its
   Role/Node selectors. Use `role_ref` or `node_ref` for an opaque large identity.
   Never reuse a worker or controller session as the formal reviewer.
2. Inspect only the candidate, actual artifact, Decision, policy result, or evidence refs
   required by the review contract. Artifact indexes and digests are not substitutes for
   actual-artifact inspection when that inspection is assigned. For Git admission, use
   `dagrail artifact inspect-scope` to separate candidate changes, target history, and
   prospective-tree deltas. Before disposable refs or worktrees are removed, require a
   valid `dagrail artifact verify-git-closure` report for every retained commit, tree,
   tag, and ref named by the handoff contract.
3. Separate semantic review, cold review, actual-artifact inspection, and deterministic
   admission. Do not repeat a completed responsibility from another Node.
4. Bind every finding to an owner, exact field or artifact, repair target, and concrete
   risk reduction. Stop at the declared scope; do not create an unbounded hardening loop.
5. Checkpoint the finding set before yielding. If only policy, validator, or fixture
   logic changed, apply `evidence.assess-reuse`; reuse execution only when DAGrail returns
   `reuse_execution`, then perform the new semantic judgment separately.
6. Apply only the returned `review.resolve` or `decision.record` action with one declared
   outcome and digest-only evidence refs. Do not merge, dispatch, close another Role's
   resources, or perform the receiving controller's lifecycle action. If the apply call
   crashes, times out, or loses its result, persist the pending action's original ref,
   RFC 8785 canonical JSON input value, and idempotency key together. A successor session
   may replay only that canonical-equivalent triple to retrieve the same result; never
   reuse that ref for a changed verdict or new intent.
7. Call `dag_pre_wait` before becoming passive and report any unresolved item outside
   this review Role to the controller by bounded count/target; do not expand unrelated
   paginated blocker or dependency-cut detail.

Never treat transport acceptance, session creation, visible delivery, another reviewer's
opinion, or evidence reuse as this Node's approval.
