---
name: execute-dag-node
description: Execute or resume one DAGrail task, gate, decision, or effect node. Use when a work package names a stable Role and Node, when replacing a prior worker session, or when checkpointing and finishing an assigned attempt.
---

# Execute one DAGrail node

Operate only the stable Role and Node named by the work package. If neither is named,
do not guess a Role or acquire a lease; ask the DAG controller for an assignment.

First verify that the DAGrail MCP tools are callable in this process. A visible skill can
outlive an older harness process and does not prove MCP activation. Run `dagrail mcp probe --root <project>`
for a fresh project round trip; `dagrail plugin status --harness <host>` tells you whether a
fresh session is required; neither proves this process loaded the tools. If tools are
absent, also run `dagrail doctor install`, then start a fresh session or use `dagrail context`,
`dagrail inspect`, `dagrail action apply`, `dagrail reconcile`, and `dagrail pre-wait`
as appropriate. Never replace a missing tool with a hand-authored lifecycle envelope.

1. Bind that Role to this harness session. Never reuse a session bound to another
   formal Role. Use takeover only after the former lease expires.
2. Call `dag_context` with `view: worker`, an explicit `root` when project discovery is
   ambiguous, and `role_id`/`node_id` for the assigned stable IDs. Use `role_ref` or
   `node_ref` only when DAGrail returned an opaque selector instead of a small ID; there
   is no `role` or `node` MCP field. Treat its cursor, checkpoint, resource refs, `authorization`, and
   `allowedActions` as authority. Follow an opaque ref
   with `dag_inspect` only when the bounded package is insufficient.
3. Apply only the current `allowedActions[].ref` with a stable idempotency key. In a CLI
   fallback, a unique `dagrail action apply --kind <kind> --role <role-or-ref> --node
   <node-or-ref>` selector may replace copying a long ref; never choose among
   multiple matches yourself. Never
   construct a transition, outcome envelope, hash, or resource ID yourself. If an apply
   call crashes, times out, or loses its result, persist the pending action's original
   ref, RFC 8785 canonical JSON input value, and idempotency key together. A successor
   session may replay only that canonical-equivalent triple to retrieve the same result.
   Never use the saved ref for changed input, a new key, or new work.
4. After every action, read `continuation`. If `safeToWait` is false, continue with work
   owned by this Role or inspect `nextActionsRef`. `owner: daemon` permits a safe yield
   only while the daemon finishes the already-authorized Effect saga; it does not mean
   the Node is complete. When DAGrail returns `role.renew`, renew explicitly because the
   operation's `requiredLeaseSeconds` exceeds the remaining lease; the daemon never
   renews a Role automatically.
5. Start with `node.start` or continue the current Attempt. After material progress and
   before yielding, apply `attempt.checkpoint` with a replacement-ready summary and
   digest-only evidence refs. Exclude prompts, secrets, transcripts, and artifact bodies.
6. Publish material execution through `evidence.publish`. Bind the candidate,
   prospective tree, command graph, protected inputs, observations, artifact digests,
   and provenance; keep large objects in their external store.
7. Close every active resource with its returned `resource.close` action. A failed or
   unknown receipt keeps capacity leased: use the later `resource.reconcile` action and
   do not complete the Attempt until closure is confirmed.
8. Use the NodeKind-specific terminal action:
   - task: `attempt.submit`, then `task.complete` for a success outcome;
   - decision: `decision.record` with one declared outcome and bounded evidence;
   - gate: `gate.evaluate` with the declared provider input—never invent its outcome;
   - effect: `effect.prepare`, reconcile `unknown`, then `effect.complete` only when the
     receipt state supports the chosen outcome. If unrelated work advanced the journal,
     inspect `effect-continuity:<action-id>` before treating the Effect contract as
     stale; continue only when its adapter ID, version, schema hash, and request binding
     remain unchanged;
   - custom kind: the exact terminal action returned by DAGrail.
9. Call `dag_pre_wait` before becoming passive. Address work owned by this Role; report
   other ready, submitted, incident, lease, or effect counts to the controller. Follow
   a paginated inspect ref only for an item this Role must act on. A status-only expired
   lease with no live responsibility is audit information and does not authorize
   reactivating a passive sender.

A native harness resume restores transport only. DAGrail's Role lease, Attempt,
checkpoint, Decision records, and receipts remain the recoverable authority.
Use the canonical installed `dagrail` runtime for live controller work; retained
version-pinned binaries are historical/offline fixtures and must not replace the live
daemon.
