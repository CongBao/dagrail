# Lifecycle migration

Lifecycle migration is an operator-controlled bootstrap for an existing workflow. It
does not discover a repository's lifecycle, convert project-specific records, or switch
an existing controller automatically.

## Contract

An external converter produces a `LifecycleMigration` v1alpha1 JSON file. The converter
may understand a requirement tool, registry, issue tracker, or another controller;
DAGrail does not. Its output must contain:

- the target Project UUID, Graph Revision, and current journal head;
- one complete, contiguous, bounded source prefix;
- a source event ID/hash chain and declared source head;
- a canonical records digest;
- one or more closed DAGrail native lifecycle events for every source record.

`source.system`, `source.project`, and source event IDs are closed portable identifiers matching
`[A-Za-z0-9][A-Za-z0-9._@-]*`; they are not filesystem paths or URLs. Keep private
authority locators in the external converter or operator evidence.

Every `sourceEventHash` is SHA-256 over the bytes
`dagrail-lifecycle-source-event-v1\0` followed by RFC 8785 canonical JSON containing
that record's sequence, ID, optional previous hash, timestamp, and native events. After
computing the chain and `recordsDigest`, compute `source.authorityHash` over
`dagrail-lifecycle-source-authority-v1\0` followed by the complete canonical manifest
with `source.authorityHash` set to the empty string. This statement binds the source
identity, target Project/Graph/head, complete prefix, and native mapping.

Only generic Role, Attempt, checkpoint, Decision, evidence, Effect, Resource, action,
and Incident events are accepted. Graph import/revision events, automatic settlement,
and nested migration receipts cannot be imported. Unknown fields and event types fail
closed. Action kinds and Incident source types use DAGrail's closed public vocabulary;
source-project terms belong in the external converter, not native event fields. The
target may contain only its initial Graph import and DAGrail's deterministic
join/milestone settlement from that import; it must contain no assigned runtime work.
Each normalized source record represents one source command. Keep the command's native
events and its `action.applied` summary in that same record. An action input is required
and may be any valid authority JSON when the current writer treats it as opaque; actions
that summarize checkpoints, evidence, Decisions, completion, Effects, or Resources are
matched to their exact native events during preflight. These matches form a one-shot
proof ledger: one support event cannot justify two actions, unmatched support events
are rejected at record end, and each accepted record must equal one closed current-writer
command shape. Effect requests, action inputs, and Resource/Effect receipt bodies may be
any valid authority JSON; `null` is still rejected where the current writer requires an
actual request or receipt.

## Trust and validation

The manifest cannot authenticate itself. Obtain the computed authority-statement digest
from the approved converter/source workflow through a separately trusted channel and
pass it explicitly:

```sh
dagrail lifecycle validate-history --root . --file migration.json \
  --source-authority-hash sha256:SOURCE_AUTHORITY_DIGEST
```

Validation checks the target authority, trust anchor, complete source prefix, unique
chain IDs/hashes, monotonic timestamps, records digest, native event envelopes, reducer
replay, and resulting state invariants. Attempt introduction must be on the ready
frontier and use the NodeKind's declared Role capability. Imported Role leases retain
the writer's 24-hour maximum. Native timestamps cannot occur after their source record,
and checkpoint, resource, evidence, Decision, and completion times must follow the
Attempt they reference. A semantic Decision must match the completed outcome and facts.
Incident updates require the owner Role's active lease and `incident.manage` capability,
except Resource closure updates already proven by a same-record, leased
`resource.closure-observed` event. Same-session Role lease renewal remains a valid writer
prefix. Incident state transitions use the current writer's closed state machine;
automatic Resource/Effect companions must match the exact observation, disposition,
deadline, counters, dependency cut, and timestamp, and a circuit-open Incident cannot
silently reopen. Resource and Effect Incidents cannot be manually resolved; only a
confirmed observation resolves the underlying ambiguity. An explicit `retry`
disposition may reset a circuit-open budget and deadline to permit another bounded
reconcile; automatic observations preserve that disposition's operator/time, progress
audit, and reset deadline, and no other transition reopens the circuit. Public migration
and projection schemas use the same closed action and Incident vocabularies as runtime
validation. Resource receipt inputs use the same arbitrary non-null JSON contract in
generated allowed actions, the native writer, and migration validation.
Effect preparation must match the Graph-declared adapter and canonical request, its
prepared adapter binding must agree, and the closed receipt body's status must equal
the observation status. Failure classification is bound to the completion input whenever
that input remains in the action record; provider Gate failure uses the deterministic
default. Every current Effect `prepared`, `dispatched`,
`reconciling`, and observed crash prefix
remains valid without pretending an external result is exactly-once. A successful
validation does not mutate the project and does not approve a cutover.

The current writer serializes dispatch, reconcile, and Effect-sourced Incident mutation
for one Effect across local processes from the prepared commit through receipt
persistence. Migration accepts the resulting writer prefixes but rejects a history that
downgrades a confirmed Effect, bypasses an Incident circuit, observes without consuming
one dispatch/reconcile admission, or treats a `reconciling` receipt as a fresh admission.

The native writer uses one persistence timestamp per command. A policy decision or
Effect preparation that performs bounded external work is still only a proposal until
the original head, action-ref expiry, session binding, and Role lease are rechecked at
that persistence boundary. Migration preflight enforces the resulting partial order.

## Atomic import

After separately approving the converter and cutover window, use a stable idempotency
key and an explicit operator Role label:

```sh
dagrail lifecycle import-history --root . --file migration.json \
  --source-authority-hash sha256:SOURCE_AUTHORITY_DIGEST \
  --actor-role migration-operator --idempotency-key migration/source-prefix-1
```

The migration receipt and all mapped native events commit in one journal segment.
Journal rename is the commit point. A changed retry fails; an exact retry returns the
same receipt and repairs a stale SQLite projection. The 8 MiB input, 10,000-record, and
10,000-event segment bounds are fixed in v1alpha1.

Export the rebuildable, deterministic view with:

```sh
dagrail lifecycle projection --root .
```

The projection excludes action input bodies and replaces Effect and Resource receipt
bodies with canonical digests. Evidence references contain digest, type, and size but
never their external URI. It is still operational data and may contain checkpoint or
Incident text, so review it before sharing.

## Project-neutral boundary

Keep converters, source paths, authority-discovery rules, domain vocabulary, and
acceptance comparisons outside the DAGrail repository and binary. A real project may be
the first validation subject without becoming a built-in adapter or kernel convention.
Use `dagrail observe` first when only structural shadow validation is authorized.
