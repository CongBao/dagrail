# DAGrail domain context

## Product thesis

DAGrail lets an LLM or human drive a development DAG while a small durable controller enforces topology, identity, lifecycle, idempotency, bounded context, and external-effect recovery.

## Ubiquitous language

- **Project**: a governed repository identified by a stable UUID in `.dagrail/project.yaml`.
- **Graph Definition**: the human-portable YAML or JSON input accepted for initial import.
- **Graph Revision**: an immutable, canonicalized graph snapshot in the journal. It is runtime graph authority after import.
- **Node**: stable work identity. Its `NodeKind` defines inputs and closed outcomes.
- **Node runtime**: `planned | active | terminal | superseded`. `ready` is derived, never assigned.
- **Edge predicate**: a positive, closed AST over outcome, decision, evidence, policy, `all`, and `any`.
- **Role**: stable responsibility independent of a process, agent, thread, or session.
- **Executor binding**: the temporary harness/session audit identity stored in a Role lease.
- **Attempt**: one execution of a Node: `leased | running | waiting | submitted | terminal`.
- **Checkpoint**: a bounded recovery summary and digest-only evidence references owned by an Attempt.
- **Allowed action**: a signed opaque ref binding project, journal head, graph revision, Role lease, Node, Attempt, provider schemas, and expiry.
- **Journal segment**: the atomic command commit containing one or more immutable events.
- **Stored event**: the exact versioned event bytes committed inside a Journal segment and covered by its hash.
- **Normalized event**: the current in-memory representation produced from a verified Stored event.
- **Upcast**: a deterministic, side-effect-free conversion from a verified older event version to the current Normalized event; it never rewrites history.
- **Compatibility window**: the closed set of historical segment and event versions a DAGrail release promises to read.
- **Execution package**: an immutable manifest binding one Attempt to candidate,
  prospective tree, command graph, protected inputs, observations, artifacts, and
  provenance by digest; artifact bodies remain external.
- **Protected core**: the candidate, prospective tree, command graph, Node contract,
  and declared execution inputs whose combined digest decides whether execution
  evidence can be reused.
- **Reuse decision**: a deterministic, policy-bound comparison that states whether
  an Execution package may be reused or execution must rerun; it is not a semantic
  approval or policy outcome.
- **Projection**: disposable SQLite or human-facing data rebuilt from journal segments.
- **Effect**: an external side effect managed as a saga, never represented as an ACID transaction.
- **Receipt**: typed observation of transport, session creation, visible delivery, acceptance, and completion. These states are not interchangeable.
- **Incident**: a durable blocker with owner, deadline, attempt budget, progress metric, classification, and dependency cut.
- **Circuit breaker**: an Incident state that stops repeated work after its deadline or no-progress budget is exhausted without blocking unrelated lanes.
- **Resource lease**: ownership of bounded executor, browser, server, port, memory, disk, or another declared capacity.
- **Dependency cut**: only the transitive graph region frozen by an unsatisfied failure path.
- **Harness**: an agent host such as Codex, Claude Code, or GitHub Copilot CLI.
- **Native harness receipt**: adapter observation binding a DAGrail action to a proved
  host session and either an exact visible message or the matching synchronous prompt
  completion response, without promoting host completion into DAG acceptance.
- **Provider**: compile-in extension returning pure decisions, proposals, or receipts without direct storage access.
- **Provider Runtime**: bounded invocation boundary that validates schemas, deadlines,
  panics, output size, sensitive fields, and stable schema hashes before an application
  service may use a provider result.
- **Runtime receipt**: local install record binding the active executable and at most
  one digest-addressed rollback executable to exact versions and SHA-256 digests.
- **Detached signature**: optional Ed25519 envelope over an exact portable file digest;
  it is independent of journal hashing and does not establish actor identity.
- **Qualification matrix**: executable evidence covering commit-window crashes,
  writer contention, corruption, long journals, fuzz inputs, and bounded large-graph
  contexts without turning fault injection into a user-facing runtime capability.
- **Compatibility contract**: machine-readable inventory of the exact public API
  versions, journal window, MCP input hashes, context budgets, and commands exposed by
  one DAGrail binary.
- **Observation snapshot**: portable, digest-bound record of a caller-selected source
  authority set and imported Graph Revision; it contains no absolute source locator.
- **Shadow project**: isolated DAGrail state used to qualify an existing DAG without
  writing to, controlling, or migrating the source project.
- **DAG Explorer**: loopback-only, mutation-free browser projection over bounded,
  deterministic v1beta1 query APIs. A selected Node deep link is a view locator, never
  a lifecycle capability.

## Invariants

1. Chat and prompts are never runtime authority.
2. Journal rename is the only local command commit point; SQLite can be deleted.
3. A stable Role has at most one unexpired active lease.
4. Every external effect has a stable action ID; ambiguity requires reconcile, never blind retry.
5. Active Node identity, kind, objective, and input contract are frozen. Terminal history is append-only.
6. Hooks may discover, inject bounded context, and observe sessions; they cannot transition lifecycle state.
7. Unknown event types and future unsupported journal schemas fail closed.
8. Secrets, PII, full prompts, transcripts, and large artifact bodies do not enter authority.
9. A harness protocol capability is not a durable lifecycle capability until it is
   verified across the process boundary in which DAGrail will use it.
10. Runtime publication is accepted only after exact digest and fresh-process version
    verification; rollback never trusts a mutable path without its receipt digest.
11. Context truncation always returns an inspectable bounded summary; a large ready
    frontier cannot force a caller to exceed its declared byte budget.
12. Observe-only work writes only to a separately resolved shadow root; portable
    provenance contains relative paths and digests, while private local locators remain
    outside the journal.
13. Beta API drift is explicit: additive fields stay in-version, while incompatible
    semantics select a new API version and migration path.
14. Explorer routes accept only `GET` and `HEAD`; collections, queries, responses, and
    graph neighborhoods are bounded, `HEAD` shares GET validation, focused truncation
    preserves the selected Node, and no raw event/effect payload or action ref is
    returned.

## Bounded contexts

- **Graph authoring**: Graph Definition, validation, two-phase GraphPatch, Graph Revision.
- **Runtime control**: Node runtime, frontier, Attempt, Role lease, checkpoint, incident, resource lease.
- **Effect control**: allowed action, outbox, prepared effect, dispatch, receipt, reconcile.
- **Harness integration**: capability probe, plugin manifest, hooks, launch/resume envelope.
- **Read model**: SQLite projection, context envelope, cursor delta, inspect ref, dashboard-ready queries.
- **Explorer projection**: bounded overview, Node inventory, focused topology, Node
  detail, payload-free timeline, and operational summaries with stable local deep links.
- **Operational surface**: payload-free status/history, verified journal backup,
  runtime upgrade/rollback, optional portable-file signatures, and a local read-only
  UI derived from authority.
- **Migration observation**: bounded source digests, private locators, isolated shadow
  import, and repeatable drift verification without lifecycle control.

## Current non-goals

Requirement management, autonomous scheduling, container orchestration, background polling, remote multi-tenant service, hostile-user authorization, signed identity, and geographically distributed availability.
