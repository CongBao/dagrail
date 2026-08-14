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
- **Projection**: disposable SQLite or human-facing data rebuilt from journal segments.
- **Effect**: an external side effect managed as a saga, never represented as an ACID transaction.
- **Receipt**: typed observation of transport, session creation, visible delivery, acceptance, and completion. These states are not interchangeable.
- **Incident**: a durable blocker with owner, deadline, attempt budget, progress metric, classification, and dependency cut.
- **Resource lease**: ownership of bounded executor, browser, server, port, memory, disk, or another declared capacity.
- **Dependency cut**: only the transitive graph region frozen by an unsatisfied failure path.
- **Harness**: an agent host such as Codex, Claude Code, or GitHub Copilot CLI.
- **Provider**: compile-in extension returning pure decisions, proposals, or receipts without direct storage access.

## Invariants

1. Chat and prompts are never runtime authority.
2. Journal rename is the only local command commit point; SQLite can be deleted.
3. A stable Role has at most one unexpired active lease.
4. Every external effect has a stable action ID; ambiguity requires reconcile, never blind retry.
5. Active Node identity, kind, objective, and input contract are frozen. Terminal history is append-only.
6. Hooks may discover, inject bounded context, and observe sessions; they cannot transition lifecycle state.
7. Unknown event types and future unsupported journal schemas fail closed.
8. Secrets, PII, full prompts, transcripts, and large artifact bodies do not enter authority.

## Bounded contexts

- **Graph authoring**: Graph Definition, validation, two-phase GraphPatch, Graph Revision.
- **Runtime control**: Node runtime, frontier, Attempt, Role lease, checkpoint, incident, resource lease.
- **Effect control**: allowed action, outbox, prepared effect, dispatch, receipt, reconcile.
- **Harness integration**: capability probe, plugin manifest, hooks, launch/resume envelope.
- **Read model**: SQLite projection, context envelope, cursor delta, inspect ref, dashboard-ready queries.

## Non-goals for v0.1

Requirement management, autonomous scheduling, container orchestration, background polling, remote multi-tenant service, hostile-user authorization, signed identity, and geographically distributed availability.
