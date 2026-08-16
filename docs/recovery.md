# Recovery and disaster rehearsal

DAGrail recovery starts from one invariant: the verified journal is authority and
SQLite is replaceable. Do not delete, truncate, rename, or copy live journal segments
until a read-only check has identified the damaged layer.

## Routine rehearsal

Run this after an upgrade and before a high-risk maintenance window:

```sh
dagrail recovery rehearse --root .
```

The command captures one immutable journal prefix and writes only to a fresh temporary
directory. It then verifies current schema readability, performs an exact-prefix
restore, replays all events through the current reducer, rebuilds a separate projection,
and compares the live and rebuilt logical-table fingerprints. The report is governed by
`schemas/recovery-rehearsal-v1alpha1.schema.json`.

A passing report proves local deterministic recovery for that head. It does not prove
that an external backup exists, that an artifact URI is still reachable, or that a
public signing key belongs to a claimed operator.

## Incident order

1. Stop issuing lifecycle mutations and capture `dagrail journal verify`,
   `dagrail journal compatibility`, and `dagrail recovery rehearse` output.
2. If the journal passes but projection integrity or equivalence fails, run
   `dagrail projection rebuild`. Re-run the rehearsal before resuming work.
3. If local journal files are missing, verify a separately stored backup with
   `dagrail backup verify`. Restore only when it belongs to the same project UUID and
   the live journal is an exact prefix. DAGrail refuses divergence and never overwrites
   an existing segment.
4. If the backup is signed, verify its exact bytes before restore. Signature validation
   is meaningful only when the public key came through a separately trusted channel.
5. If the journal itself fails hash, sequence, schema, or authority validation, preserve
   the files and stop. Do not hand-edit hashes or copy a later prefix over the failure.

## Replace contaminated authority without truncation

If a valid journal advanced past an authenticated last-known-good backup and policy
requires a new authority identity, first quiesce lifecycle work and capture the exact
current head. Then run:

```sh
dagrail recovery rotate-authority --root . --backup lkg-backup.json \
  --expected-current-head CURRENT_HEAD --reason "bounded recovery reason" \
  --idempotency-key authority-rotation/incident-id
```

The backup must be an exact prefix of the current journal. DAGrail refuses rotation
while a non-expired Role lease, unclosed Effect/Resource, or unresolved Incident makes
the cut ambiguous. Success atomically replaces only `.dagrail/project.yaml` with a new
Project UUID. Every old segment byte remains unchanged; DAGrail appends one terminal
`authority.retired` fence and writes a rebuildable retirement sidecar.
The replacement runtime directory contains a local writer claim and digest-bound
per-generation provenance. Those files stay outside Project v1alpha1 and portable
backups so a v0.21 rollback can still parse the locator. The new journal is fence-only:
its schema-4 `authority.established` bootstrap is committed before locator publication.
Ordinary open and `dagrail init` verify that fence against the surviving claim and,
for replacement authorities, the claim-bound lineage. A visible locator or runtime
directory with a missing establishment segment is a fail-closed recovery incident;
initialization never silently recreates the segment.
Re-import the Graph and authenticated lifecycle history explicitly
before any writer cutover. This command is not journal reset, rollback, or automatic
cutover.

Every v0.22 journal mutation validates a claim bound to the canonical local data path.
Copying a locator, backup, or complete project data directory to another
`DAGRAIL_HOME` therefore cannot recreate a writable copy of the UUID; the per-user
anchor is deliberately outside that selectable data root. The journal fences also make
the v0.21 reducer reject stale mutation. Exact-prefix restore may resume only
in the original claimed data root. If the complete local authority store was lost, do
not fabricate the claim or
restore the old UUID as writable; create/approve a new Project identity and import its
authenticated history through the migration contract.

The anchor root is derived from the operating-system account database and a fixed
per-platform location. Production binaries do not select it from `DAGRAIL_HOME`,
`DAGRAIL_AUTHORITY_HOME`, `HOME`, or `XDG_CONFIG_HOME`; changing process environment
therefore cannot create a second anchor namespace for the same OS user.

The same Project may rotate again later. Each generation has its own retirement marker,
claim, and provenance. Concurrent calls with identical intent return the same receipt;
changed intent fails closed.

Delayed exact retries traverse a cycle-checked lineage of up to 10,000 local authority
stores, rather than a generation-128 semantic ceiling. That is a defensive local
resource bound, not a compatibility reset; ordinary projects may rotate repeatedly and
old receipts remain retryable throughout that bound.

If a rotated generation loses or corrupts only its lineage file, every ordinary
mutation fails closed. Replaying the exact authenticated rotation request is the repair
path: DAGrail scans bounded local predecessor journals, requires a unique matching
retirement fence and a claim digest that already authenticates the reconstructed bytes,
then restores lineage and returns the original receipt. It never repairs a missing or
changed claim and never performs this scan during ordinary open.

## Explicitly adopt a pre-v0.22 local store

Ordinary open and inspection never mint a writer claim. Before the first v0.22 mutation
of a v0.21 or older store, retire the exact legacy identity and create a replacement:

```sh
dagrail recovery adopt-legacy-authority --root . \
  --expected-project-id PROJECT_UUID --expected-current-head HEAD_OR_empty \
  --reason "upgrade this local authority" --idempotency-key adoption/incident-id
```

The receipt binds the previous and replacement UUIDs, exact previous head, source
backup digest, reason, and idempotency key. Under the old writer lock DAGrail reserves
one canonical legacy root and appends `authority.retired`. It then prepares a fresh,
lineage-bound claim, appends `authority.established` to the replacement journal, and
only then publishes the locator. A crash at any boundary is resumed by the exact same
request. The replacement starts fence-only: import the Graph and authenticated
lifecycle history before normal work. A copied locator or v0.22 store cannot be
reclassified as legacy; two copies of one legacy UUID cannot both create the new
authority. Pre-v0.22 binaries intentionally cannot reduce or mutate either journal.
The source backup timestamp and reservation digest are derived from the verified source
prefix, not the retrying process clock, so same-intent concurrent calls and a crash after
reservation but before the journal fence converge on the same receipt.
Snapshot derivation is rechecked under the retirement transaction: a loser that passed
its initial sidecar check before another process committed validates the winner's exact
fence and returns that receipt instead of folding the new fence into a different backup.

Journal, claim, lineage, anchor, and locator publication flush their containing
directories. Directory creation proceeds one component at a time and flushes each new
directory plus its containing parent. A fresh retry rechecks the deepest visible
component and its parent before continuing, without traversing unrelated system-owned
ancestors to the volume root. Windows uses native directory handles and
`FlushFileBuffers` rather than treating directory sync as a no-op. A returned durability
error therefore remains a failed operation until an exact retry reconfirms the same
bytes and namespace entries.

## Boundaries

- The recovery command uses an inspection-only store: it does not settle automatic
  nodes, synchronize SQLite, migrate its schema, or repair corruption before evidence
  is captured, and the journal layer rejects generic append/restore calls through that
  handle. Only the dedicated legacy-retirement and rotation transactions may commit.
- The v1alpha1 JSON backup remains bounded to 256 MiB. Large deployments should retain
  the original segment directory through filesystem or object-store backup tooling;
  streaming portable archives remain a post-beta capability.
- Recovery does not retry external effects. After restoring authority, inspect every
  `unknown` or `reconciling` effect and use its stable action ID with `dagrail reconcile`.
- A projection fingerprint covers logical rows, not SQLite binary pages, paths, file
  permissions, or timestamps. Matching fingerprints are intentionally portable across
  operating systems and database rebuilds.
- Copying a project locator is not state isolation: Project UUID and `DAGRAIL_HOME`
  resolve the runtime data directory. Use `observe create-shadow` for qualification or
  `rotate-authority` for an approved identity replacement; never qualify a copied root
  against the authoritative UUID/data tuple.
