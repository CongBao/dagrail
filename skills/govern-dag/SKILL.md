---
name: govern-dag
description: Govern a DAGrail project as the orchestration role. Use when inspecting the ready frontier, binding or replacing a control session, assigning nodes, applying typed graph changes, supervising a trusted lifecycle bootstrap, reconciling external effects, or checking whether it is safe to wait.
---

# Govern a DAGrail project

Treat DAGrail—not chat, a roadmap projection, or a harness thread—as runtime authority.

Before binding a Role, verify that all six DAGrail MCP tools are callable in this process.
Skill discovery does not prove MCP activation. `dagrail mcp probe --root <project>` proves a
fresh project round trip and exact schemas; `dagrail plugin status --harness <host>`
reports the fresh-session boundary. Neither proves this process loaded MCP. If a tool is
absent, run those diagnostics plus `dagrail doctor install`, then start a fresh session
or use `dagrail context`, `dagrail inspect`, `dagrail action apply`, `dagrail graph
preview-change|apply-change`, `dagrail reconcile`, and `dagrail pre-wait`—never a
hand-built transition.

1. Bind only the assigned control Role. It needs the capabilities for actions it will
   perform, such as `graph.change`; takeover is valid only after the former lease expires.
2. Call `dag_context` with `view: orchestrator`, an explicit `root` when project
   discovery is ambiguous, `role_id`/`node_id` for stable IDs or `role_ref`/`node_ref`
   for controller-issued opaque selectors, and a cursor. There is no `role` or `node`
   MCP field. Inspect opaque refs selectively; do not load the complete graph, journal, or artifact bodies.
   When an ID is too large for a bounded tool input, pass the returned `role_ref`,
   `node_ref`, `actor_role_ref`, or `effect_ref`; do not reconstruct or paste the ID.
   Use its `authorization`, `remediations`, and `projectAllowedActions` as the bounded
   operations plan. Routine in-scope actions do not need a new conversational approval;
   the listed escalation boundaries still do.
3. Advance work only through current controller-issued action refs. In a CLI fallback,
   `dagrail action apply --kind <kind> --role <role-or-ref> --node <node-or-ref>` may
   atomically resolve one current action so the session need not copy a long ref; zero or
   multiple matches are blockers, never permission to guess. Preserve one stable
   idempotency key per intended action. Do not copy refs, leases, hashes, or Attempt IDs
   between Roles, Nodes, or projects. One recovery exception exists: when a
   call crashes, times out, or loses its result, persist the pending action's original
   ref, RFC 8785 canonical JSON input value, and idempotency key together. A successor
   session may replay only that canonical-equivalent triple to retrieve the same result.
   Never pair the saved ref with changed input, a new key, another Role/Node/project, or
   a new intent.
4. After every action, read its `continuation` before choosing the next step. When
   `safeToWait` is false, resolve the owned reason codes or inspect `nextActionsRef` and
   continue; at most three next actions are inlined. `owner: daemon` means only that the
   daemon is finishing one already-authorized Effect saga and the agent may safely yield;
   it is not acceptance, completion, or permission for the daemon to choose more work.
   A `role.renew` remediation means the current lease budget cannot safely cover the
   requested operation. Renew explicitly; DAGrail never extends a Role lease on its own.
5. Keep semantic responsibilities in their typed Nodes. A task produces work, a review
   resolves approve/return, a decision records a closed human/LLM choice, a gate invokes
   its policy provider, and an effect owns the external saga. The control Role does not
   repeat review or promote delivery into acceptance.
6. Modify the graph only with `dag_graph_change` preview followed by apply of that exact,
   unexpired token. Re-preview after any head or revision change. Planned contracts and
   Role capabilities may change; active contracts are frozen, terminal history is
   append-only, and active resources must close before supersession.
7. Reconcile every `unknown` effect before retrying. If unrelated journal activity made
   a saved ref look stale, inspect `effect-continuity:<action-id>`: a global head advance
   is not a causal contract change when the bound adapter ID, version, schema hash, and
   canonical request digest are unchanged. Missing legacy metadata or a same-ID adapter
   upgrade is a real blocker, not a reason to force reconciliation. Prefer native
   observation; otherwise provide only verified typed evidence. Transport acceptance,
   session creation, recipient-visible delivery, acceptance, completion, and DAG
   outcome are distinct.
8. Follow `decision:`, `evidence-package:`, and `reuse-decision:` refs. A
   `reuse_execution` result permits policy reevaluation without rerunning the protected
   execution core; it is not approval.
9. Keep incidents owned and bounded. Record progress, apply one closed recovery
   disposition (`retry`, `rollback`, `lkg`, `quarantine`, `off-critical-path`, or
   `escalate`), and respect an open circuit instead of repeating adjacent fixes. When
   the graph contains a valid repair successor, use the returned `incident.supersede`
   action so the old alert closes with a typed, auditable handoff.
10. Call `dag_pre_wait` before yielding, waiting, or declaring blocked. Do not wait while
   ready Nodes, submitted Attempts, stale/expired leases, incidents, resource closure,
   or unreconciled effects still require a bounded action. Read exact `counts` first;
   when `truncated` is true, follow only the relevant operations-action, pre-wait, or
   dependency-cut inspect page rather than expanding every blocker into conversation
   context. If a snapshot-bound page reports stale, refresh context/pre-wait; never
   reuse its numeric offset against a new head or liveness inventory. Decode an
   oversized detail in order from its base64 chunks and verify the shared digest; do not
   paste every chunk into the conversation when the compact signed action ref is enough.

Historical lifecycle import is an operator bootstrap, not an MCP lifecycle action. Use
the CLI only for a pristine graph-only project, require a separately trusted source
authority digest, and validate the complete mapped source prefix before one atomic
import. Keep source-specific conversion code and vocabulary outside DAGrail.

Authority adoption, rotation, and relocation are operator recovery transactions, not
orchestrator actions. Freeze lifecycle writes and require an approved exact source head,
backup, locator identity, reason, and idempotency key. Allocate a new key only for the
first call of a genuinely new intent. After a crash, timeout, or unknown result, reuse
the original key and every bound field exactly. If a replacement authority was already
established under the wrong local runtime, use the documented
`recovery relocate-authority` continuation; never repeat adoption or edit/copy anchors,
claims, journals, locators, or SQLite to simulate reattachment. After relocation, keep
cutover false until Graph/history bootstrap and parity checks pass.

Never edit the journal, SQLite, action secret, or generated projections. Hooks may add
bounded guidance but cannot assign, accept, merge, complete, or infer a lifecycle result.
Use the canonical installed `dagrail` for live controller work. A version-pinned binary is
historical/offline only and must not restart or replace the daemon; automatic replacement reports `client_outdated`, while restart is rejected by the live daemon.
