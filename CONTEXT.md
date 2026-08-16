# DAGrail domain context

## Product thesis

DAGrail lets an LLM or human drive a development DAG while a small durable controller enforces topology, identity, lifecycle, idempotency, bounded context, and external-effect recovery.

## Ubiquitous language

- **Project**: a governed repository identified by a stable UUID in `.dagrail/project.yaml`.
- **Graph Definition**: the human-portable YAML or JSON input accepted for initial import.
- **Graph Revision**: an immutable, canonicalized graph snapshot in the journal. It is runtime graph authority after import.
- **Node**: stable work identity. Its `NodeKind` defines inputs and closed outcomes.
- **Node runtime**: `planned | active | terminal | superseded | skipped`. `ready` is derived, never assigned; `skipped` closes a permanently unreachable positive branch without inventing success.
- **Edge predicate**: a positive, closed AST over outcome, decision, evidence, policy, `all`, and `any`.
- **Role**: stable responsibility independent of a process, agent, thread, or session.
- **Executor binding**: the temporary harness/session audit identity stored in a Role lease.
- **Attempt**: one execution of a Node: `leased | running | waiting | submitted | terminal`.
- **Checkpoint**: a bounded recovery summary and digest-only evidence references owned by an Attempt.
- **Allowed action**: a signed opaque ref binding project, journal head, graph revision, Role lease, Node, Attempt, provider schemas, and expiry.
- **Decision contract**: the closed key and `human | llm | provider` source declared by a Decision or Gate Node; provider contracts also bind provider and policy IDs.
- **Decision record**: immutable journal authority binding one closed outcome and its facts/evidence to Project, Graph Revision, Node, Attempt, Role, input digest, and optional exact provider version/schema.
- **Journal segment**: the atomic command commit containing one or more immutable events.
- **Command intent digest**: an RFC 8785 digest over a mutation's bounded request,
  bound to its idempotency key so a retry cannot silently change intent.
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
- **Incident**: a durable blocker with owner, deadline, attempt budget, progress metric, closed classification, recovery disposition, and dependency cut.
- **Recovery disposition**: a closed operator choice: `retry | rollback | lkg | quarantine | off-critical-path | escalate`.
- **Circuit breaker**: an Incident state that stops repeated work after its deadline or no-progress budget is exhausted without blocking unrelated lanes.
- **Resource lease**: ownership of bounded executor, browser, server, port, memory, disk, or another declared capacity. Capacity is released only by a confirmed closure receipt; unknown closure remains active and reconcilable.
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
- **External validation subject**: any repository or workflow observed through public DAGrail contracts. Its local vocabulary never becomes a kernel type merely because it is used for qualification.
- **Qualification driver**: disposable, repository-external conversion and comparison logic that exercises public CLI, MCP, or SDK surfaces against an External validation subject.
- **Lifecycle migration manifest**: a bounded mapping of one complete external
  append-only prefix to closed DAGrail native events. Its records digest and source
  chain are portable input; source-specific conversion stays outside the kernel.
- **Source command bundle**: the ordered v1beta1 mapping inside one immutable external
  source record. It may contain several current-writer-equivalent commands without
  pretending the source emitted several records.
- **Source command proof ledger**: the per-command, one-shot association between one
  current-writer command shape, its optional `action.applied`, and every native event
  that proves that action. A proof cannot cross a command boundary, be reused, or remain
  orphaned when that command closes.
- **Source authority trust anchor**: an out-of-band SHA-256 digest supplied separately
  from a migration manifest. Import fails unless it exactly matches the manifest's
  claimed source authority.
- **Lifecycle projection**: a deterministic, redacted view of imported and native
  runtime state. Raw action inputs and effect/resource receipts are omitted or digested.
- **DAG Explorer**: loopback-only, mutation-free browser projection over bounded,
  deterministic v1beta1 query APIs. A selected Node deep link is a view locator, never
  a lifecycle capability.
- **Security audit**: path-redacted v1alpha1 evidence over the cooperative OS-user
  boundary, filesystem permissions, journal verification, and projection integrity;
  it is not an authorization decision.
- **Bundled marketplace**: the digest-addressed, host-neutral plugin projection linked
  into a DAGrail executable and materialized as a local marketplace with relative
  plugin sources; it is separate from runtime authority.
- **Plugin conformance**: path-free typed evidence that the linked runtime, bundled
  marketplace, host registration, MCP launcher, optional native capability, receipt
  proof, and manual fallback agree.
- **Support report**: pseudonymous, aggregate, schema-bound diagnostics that explicitly
  exclude authority payloads, absolute paths, prompts, artifact bodies, harness output,
  and graph identifiers.
- **Recovery rehearsal**: a read-only proof bound to one journal head that restores its
  exact prefix in disposable storage, replays the reducer, rebuilds SQLite, and compares
  logical projection fingerprints.
- **Release qualification**: a structural, schema-bound source and workflow audit that
  lists external production evidence separately and never marks it complete by proxy.
- **Release manifest**: a closed distribution inventory binding the six binary archives,
  six SPDX inventories, sorted checksums, tag, commit, and source-date epoch by digest.
- **Release verification**: a path-free offline proof that the manifest, payload set,
  archive metadata, expansion bounds, checksums, and SPDX envelopes agree; it does not
  authenticate a publisher.
- **Command catalog**: the bounded machine-readable source for top-level commands,
  subcommands, effect class, project requirement, output mode, completion, and broad
  process error classes.
- **CLI error**: an opt-in bounded process envelope for usage, interruption,
  diagnostics, or otherwise unclassified operation failure; it does not replace typed
  domain reports.
- **Installation diagnostic**: path-free local evidence over the verified runtime,
  linked plugin bundle, selected harness registrations, and MCP launcher state.
- **Harness activation boundary**: plugin and MCP registration can be verified from a
  fresh process, but an already-running harness cannot be declared hot-reloaded. It
  must expose the tools itself, start a fresh session, or use the typed CLI fallback.
- **Authority lineage**: claim-bound local provenance binding a replacement Project UUID
  to an immutable prior journal head and authenticated backup/LKG prefix. Rotation
  creates empty new authority, appends one terminal fence, and never rewrites old bytes.
- **Historical binary matrix**: a closed manifest of exact beta release commits used to
  build real old binaries and test adjacent runtime upgrade and rollback paths.
- **Readiness decision**: an aggregate structural verdict that may allow external
  validation to begin but cannot self-assert production validation or 1.0 readiness.

## Invariants

1. Chat and prompts are never runtime authority.
2. Journal rename is the only local command commit point; SQLite can be deleted.
3. A stable Role has at most one unexpired active lease.
4. Every external effect has a stable action ID; ambiguity requires reconcile, never blind retry.
5. Active Node identity, kind, objective, and input contract are frozen. Terminal history is append-only.
6. Hooks may discover, inject bounded context, and observe sessions; they cannot transition lifecycle state.
   Explorer, hooks, and public project queries use a read-only open and cannot settle
   automatic Nodes, synchronize the projection, or create SQLite sidecars. Explicit
   output artifacts are outside this protected-project boundary. Diagnostics fail
   closed rather than ignore or checkpoint an existing WAL/SHM state.
7. Unknown event types and future unsupported journal schemas fail closed.
8. Secrets, PII, full prompts, transcripts, and large artifact bodies do not enter authority.
9. A harness protocol capability is not a durable lifecycle capability until it is
   verified across the process boundary in which DAGrail will use it.
10. Runtime publication is accepted only after exact digest and fresh-process version
    verification; rollback never trusts a mutable path without its receipt digest.
11. Context truncation always returns an inspectable bounded summary; a large ready
    frontier cannot force a caller to exceed its declared byte budget. Only the three
    declared views exist, and callers cannot raise their fixed maximum.
12. Observe-only work writes only to a separately resolved shadow root; portable
    provenance contains relative paths and digests, while private local locators remain
    outside the journal.
13. Beta API drift is explicit: additive fields stay in-version, while incompatible
    semantics select a new API version and migration path.
14. Explorer routes accept only `GET` and `HEAD`; collections, queries, responses, and
    graph neighborhoods are bounded, `HEAD` shares GET validation, focused truncation
    preserves the selected Node, and no raw event/effect payload or action ref is
    returned.
15. Authority JSON is duplicate-free, depth/value bounded, and closed where its
    envelope is typed; no reducer consumes a journal segment before these checks.
16. Roles do not isolate a malicious same-user process. Security diagnostics state
    this boundary and never expose authority payloads or absolute project paths.
17. Default plugin installation uses bytes linked into the verified runtime; host
    registration never turns plugin marketplace state into DAG authority.
18. A support report contains only aggregate or pseudonymous diagnostics and is
    previewable before an exclusive owner-only export.
19. Recovery rehearsal never replaces live authority or projection files; every restore
    and rebuild write is confined to disposable storage and compared by stable digest.
20. A release candidate may be structurally qualified while production validation stays
    false; adoption evidence must be observed, not inferred from CI.
21. A published release set is closed and manifest-bound before publication; checksum
    consistency never substitutes for provenance or publisher trust.
22. Command discovery, compatibility inventory, and completion share one closed
    catalog; process interruption is never collapsed into a generic failure exit.
23. Historical compatibility evidence builds the pinned commits; replacing a fixture
    with current code is not an acceptable substitute for an old binary.
24. A loopback UI request must also pass Host and exact-Origin checks; localhost ports
    are separate browser origins and receive no implicit capability sharing.
25. Structural readiness cannot set production validation or close adoption gaps.
26. A Role can mutate a governed Node only when it declares the capability required by that NodeKind; every mutation rechecks both an unexpired Role lease and that capability.
27. Human, LLM, and provider judgments become state only as revision-bound Decision records; opaque provider output and chat remain non-authoritative.
28. Attempt completion and Node supersession cannot release active resources; only a confirmed closure receipt can return declared capacity.
29. A repository used for qualification may drive conversion fixtures and comparisons, but its domain names, paths, and lifecycle conventions cannot enter the DAGrail kernel or public generic contracts.
30. A new mutation command binds its idempotency key to actor, object, command kind,
    and normalized request intent; a changed retry fails closed instead of returning an
    unrelated earlier result.
31. A validation subject may supply an external converter and acceptance evidence, but
    its names, repository paths, requirement system, and lifecycle vocabulary never
    become DAGrail kernel, schema, MCP, hook, or skill contracts.
32. Historical lifecycle import requires a pristine graph-only target, a complete
    bounded source prefix, an out-of-band authority digest, reducer/invariant preflight,
    and one atomic journal segment. It is not a normal agent action or implicit cutover.
33. A migratable native history must preserve the same ready-frontier, Role capability,
    24-hour lease ceiling, event-time partial order, Decision/completion binding, and
    Effect crash prefixes as the current writer; project-specific action or Incident
    vocabulary is rejected before journal commit.
34. A v1alpha1 normalized source record represents exactly one source command. A
    v1beta1 source record may carry an ordered Source command bundle, but every command
    has a separate proof ledger and closes before the next begins. Its `action.applied`
    summary binds the exact support events in that command; proof cannot be shared, and
    final projection equality cannot substitute for command causality.
35. A persisted mutation has one authoritative event time. Slow policy/effect
    preparation must recheck the original head, action expiry, session binding, and
    Role lease at that persistence boundary; external observations after an authorized
    dispatch remain recordable for reconcile without inventing a new authorization.
36. Resource and Effect Incidents are resolved only by the confirmed observation that
    closes their underlying ambiguity; an operator may disposition or trip them but
    cannot manually resolve and later reopen them. Imported Effect preparation must
    equal its Graph-declared adapter/request, and an Effect receipt's closed body status
    must equal its observation status.
37. Dispatch, reconcile, and Effect-sourced Incident mutation share one cross-process
    observation lease from prepared publication through receipt persistence. Each
    observation consumes exactly one dispatch/reconcile admission; a `reconciling`
    receipt is not a new admission. Automatic observations preserve an explicit retry
    disposition, its actor/time, progress audit, and reset deadline; a stale observation
    cannot downgrade a confirmed Effect or erase an operator circuit. Incident lock
    waits carry caller cancellation end to end and never commit after cancellation is
    observed.
38. Authority recovery replaces identity instead of truncating history. The selected
    backup must be an authenticated exact prefix of the old authority; the replacement
    receives a fresh UUID, a bootstrap writer fence, and digest-bound lineage, while the old
    journal receives one append-only retirement fence and no prior byte is rewritten.
    Rotation rejects live Role, Effect, Resource, or Incident
    ownership that could make the cut ambiguous.
39. Runtime installation, bundled skills, and MCP registration do not prove that an
    existing harness process loaded them. Diagnostics and hooks state this uncertainty;
    a caller uses a fresh session or the typed CLI fallback before lifecycle work.
40. Every journal mutation requires a local claim bound to the canonical runtime path.
    Ordinary open never creates claims. Explicit exact-head adoption retires a
    pre-v0.22 UUID and creates a fresh, lineage-bound replacement; it never makes the
    legacy UUID writable under v0.22. The replacement's schema-4 establishment fence
    commits before locator publication. Newly initialized v0.22 authorities use the
    same fence-before-locator order. Copies, missing provenance, stale pre-v0.22 writers,
    and terminal retirement fences fail closed.
41. A replacement authority established under an unsuitable local runtime is moved only
    by the explicit relocation continuation. Its fixed per-user anchor authenticates the
    source; claim-bound lineage must descend from the target locator identity; source
    head, backup, canonical target and destination-runtime roots, reason, and key bind
    one deterministic fresh UUID. The source is retired before the new fence and locator publish. Relocation
    never rebinds a UUID or carries Graph/lifecycle state implicitly.

## Bounded contexts

- **Graph authoring**: Graph Definition, validation, two-phase GraphPatch, Graph Revision.
- **Runtime control**: Node runtime, frontier, Attempt, Role lease, checkpoint, incident, resource lease.
- **Effect control**: allowed action, outbox, prepared effect, dispatch, receipt, reconcile.
- **Harness integration**: capability probe, plugin manifest, hooks, launch/resume envelope.
- **Read model**: SQLite projection, context envelope, cursor delta, inspect ref, dashboard-ready queries.
- **Explorer projection**: bounded overview, Node inventory, focused topology, Node
  detail, payload-free timeline, and operational summaries with stable local deep links.
- **Operational surface**: payload-free status/history, verified journal backup,
  runtime upgrade/rollback, optional portable-file signatures, local security audit,
  bundled-plugin conformance, command catalog, installation diagnostics, shareable
  support diagnostics, and a local read-only UI derived from authority.
- **Migration observation**: bounded source digests, private locators, isolated shadow
  import, and repeatable drift verification without lifecycle control.
- **Lifecycle bootstrap**: external native-event conversion, out-of-band trust anchor,
  complete-prefix validation, per-command proof closure, atomic import receipt, and
  redacted rebuildable projection.
- **Authority recovery**: authenticated backup-prefix selection, non-destructive Project
  identity rotation/relocation, local writer claims, durable per-generation provenance,
  and explicit later graph/history re-bootstrap.
- **Release readiness**: closed historical binary inputs, source qualification,
  optional project/install evidence, and explicitly outstanding adoption evidence.

## Current non-goals

Requirement management, autonomous scheduling, container orchestration, background polling, remote multi-tenant service, hostile-user authorization, signed identity, and geographically distributed availability.
