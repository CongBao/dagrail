# Local operations

DAGrail keeps its immutable journal in the per-user project data directory. SQLite is a
rebuildable projection. The commands below operate on a project discovered from
`--root`; they do not require a daemon.

## Observe

```sh
dagrail status --root .
dagrail history --root . --after 0 --limit 25
dagrail frontier --root . --format json
dagrail pre-wait --root .
dagrail ui --root .
```

`status` includes journal head, lifecycle counts, expired leases, overdue incidents,
and the explainable frontier. Status, history, evidence, and frontier remain ordinary
JSON while small; over 24 KiB they return exact counts and a digest-bound detail ref.
`pre-wait` returns exact counts with finite previews;
follow its snapshot-bound `pre-wait-page:<head>:<digest>:<offset>` and dependency-cut
inspect refs only when detail is needed. Refresh `pre-wait` when a page reports stale;
never apply an offset to a changed liveness inventory. The opaque `operations:<key>`
index and its action pages are independently capped at 24 KiB and preserve exact counts.
For an unusually large declared identifier or input schema, follow the returned detail
ref and concatenate its base64-decoded chunks, checking the common SHA-256 digest and
total byte count. The signed action ref itself remains compact and applies the exact
same head/Role/Node binding; chunk refs are read-only detail, not lifecycle authority.
Oversized Role/session authorization and imported Attempt IDs follow the same rule.
For a declared terminal outcome that cannot fit in an action input, choose its short
`outcomeRef` from `x-dagrailOutcomeOptions`; follow `idDetailRef` only when the full
outcome identifier is actually needed.

Schema-legal identifiers may be larger than a CLI or MCP selector. Use the opaque refs
returned by context, pre-wait, operations, or inspect: `--role-ref`, `--node-ref`,
`--attempt-ref`, `--incident-ref`, `--effect-ref`, and `--actor-role-ref`. Do not paste
the recovered large identifier back through a bounded control surface.

`history` returns payload-free command metadata in pages
of at most 100 entries. `ui` starts a foreground, loopback-only, strictly read-only web
Explorer and opens the default browser; use `--open=false` when automatic launch is not
wanted. See [`ui.md`](ui.md) for bounded APIs, deep links, filtering, and large-graph
behavior.

If `action apply` returns no definitive result because of a crash, timeout, or lost
response, preserve the original action ref, RFC 8785 canonical JSON input value, and
idempotency key as one pending-action record. A successor session may replay that
canonical-equivalent triple to recover the result; whitespace and object-key order may
differ. Never combine the saved ref with a changed JSON value, a new key, or a new
intent.

## Audit the local trust boundary

```sh
dagrail security audit --root .
dagrail journal verify --root .
```

The security audit reports typed pass/warn/fail checks without absolute project paths
or authority payloads. On POSIX it verifies that runtime authority is owner-only and
that the repository locator is not group/other writable. On Windows it verifies file
structure and explicitly delegates ACL inspection to host tooling. `journal verify`
returns the verified head, compatibility window, canonical export size, and digest.
Neither command establishes actor identity or malicious same-user isolation.

## Manage incidents

An incident has a stable owner Role, closed classification, deadline, attempt budget,
progress metric, recovery disposition, and dependency cut. Its owner must hold the
current Role lease and declare `incident.manage`.

```sh
dagrail incident progress --root . --incident INCIDENT --actor-role ROLE \
  --note "candidate narrowed" --made-progress=true --idempotency-key progress-1
dagrail incident disposition --root . --incident INCIDENT --actor-role ROLE \
  --disposition quarantine --note "isolate failing adapter" \
  --idempotency-key disposition-1
dagrail incident trip --root . --incident INCIDENT --actor-role ROLE \
  --reason "attempt budget exhausted" --idempotency-key trip-1
dagrail incident resolve --root . --incident INCIDENT --actor-role ROLE \
  --resolution "fixed by candidate sha256:..." --idempotency-key resolve-1
```

Two consecutive no-progress reports trip the default circuit breaker. A missed deadline
also trips on the next progress evaluation. Resource and Effect Incidents resolve only
when a confirmed observation removes the underlying ambiguity. To authorize another
bounded attempt after a circuit opens, set `--disposition retry`; this explicitly resets
the circuit budget and deadline before reconcile. Later automatic observations preserve
that disposition, operator/time, progress audit, and reset deadline. Dispatch,
reconcile, and Effect-sourced Incident mutation are serialized across local processes
until the receipt is persisted, so a stale observation cannot downgrade a confirmed
Effect or erase an operator circuit. Each external observation consumes one prior
dispatch/reconcile admission; a `reconciling` receipt does not authorize another
observation. CLI interrupt/caller cancellation also reaches an Incident command waiting
for this lock; once cancellation is observed, no journal event is appended, and the same
idempotency key may be retried after the lock is available. Other dispositions do not
reopen the circuit, and unrelated graph lanes remain available.

## Back up and restore

```sh
dagrail backup create --root . --output dagrail-backup.json
dagrail backup verify --root . --file dagrail-backup.json
dagrail backup restore --root . --file dagrail-backup.json
```

The backup is canonical JSON containing exact journal segments plus a content digest.
Verification checks the digest, hash chain, sequence, and project UUID. Restore accepts
only an empty journal or an exact prefix of the backup, never overwrites a divergent
segment, requires the destination data root's local authority claim, and automatically
rebuilds SQLite. Re-running the same restore is idempotent. An empty data root reached
only through a copied locator has no claim and cannot revive the backup's writable UUID.
Claims are bound to the canonical data path, so copying the complete project runtime
directory to another `DAGRAIL_HOME` also remains read-only. Pre-v0.22 stores use the
explicit exact-head `recovery adopt-legacy-authority` command documented in the recovery
runbook; inspection never performs adoption.
An already-established replacement at an unsuitable runtime uses the separate
exact-head/backup `recovery relocate-authority` continuation; backup restore cannot
rebind that UUID at another path.

Backups may contain operational metadata and external artifact URIs. Treat them as
sensitive project records even though DAGrail rejects secrets and artifact bodies from
authority.

## Sign portable files

Detached signatures are optional and cover exact file bytes. Generate keys outside a
governed project and distribute the public key through a separately trusted channel.

```sh
dagrail signature keygen --private-key dagrail-signing.pem \
  --public-key dagrail-signing.pub.pem
dagrail signature sign --file dagrail-backup.json \
  --private-key dagrail-signing.pem --output dagrail-backup.json.sig.json
dagrail signature verify --file dagrail-backup.json \
  --signature dagrail-backup.json.sig.json --public-key dagrail-signing.pub.pem
```

Signing does not encrypt the export or identify actors inside its journal. Keep the
private key outside repositories, journals, evidence, and backups. On POSIX, DAGrail
refuses private keys readable by group or other users.

## Verify or roll back the shared runtime

```sh
dagrail plugin runtime-status
dagrail plugin rollback
dagrail plugin runtime-status
```

An upgrade preserves one immutable binary under a SHA-256-addressed data path and
validates every candidate in a fresh process. Rollback is reversible: the displaced
runtime becomes the next rollback candidate. This switches the stable DAGrail
executable only; it does not restore older harness manifests or marketplace metadata.

Before upgrading to v0.23 or later, reconcile every nonterminal Effect with the exact
adapter version that prepared it, or retain the verified prior runtime until those
Effects close. v0.23 persists adapter version and schema identity on new Effects; an
older ambiguous Effect without that binding remains visible but automatic reconciliation
fails closed rather than guessing which implementation created it.

Before recruiting an external adopter, run `dagrail readiness --source .`. Add
`--project` only for a project the operator intends to inspect, and add `--installation`
when local host registrations should be release-blocking. A passing report means the
candidate is ready to begin external validation; follow `docs/readiness.md` before any
1.0 decision.

## Verify host integration and prepare support evidence

```sh
dagrail plugin bundle-status
dagrail plugin conformance
dagrail support preview --root .
dagrail support export --root . --output dagrail-support.json
```

The default installer materializes a digest-addressed local marketplace from the
running executable. `plugin conformance` never emits executable paths or raw host
output; native dispatch may be unavailable while an explicit manual fallback remains
valid. A support report contains no graph IDs, authority payloads, absolute paths,
prompts, artifact bodies, or harness output. Export uses a new owner-only file and
refuses overwrite, so inspect `preview` before choosing to share it.

`dagrail plugin uninstall` removes the MCP registration, plugin, and the marketplace
registration selected by the install plan. It intentionally retains the verified
runtime and immutable bundle for rollback and support inspection. An explicitly
configured legacy remote marketplace is never guessed at or removed by a default
bundled uninstall.

## Repair projections

```sh
dagrail recovery rehearse --root .
dagrail journal verify --root .
dagrail journal compatibility --root .
dagrail journal replay --root .
dagrail projection rebuild --root .
dagrail doctor --root .
```

Run the rehearsal before deleting or replacing anything. A passing report binds schema
compatibility, exact-prefix journal restore, reducer replay, disposable projection
rebuild, and live/rebuilt logical fingerprints to one head. If only SQLite is damaged,
`projection rebuild` is sufficient; journal restore is reserved for a verified backup
whose prefix does not diverge. See `docs/recovery.md` for the bounded runbook.
If a valid authority must be replaced rather than restored, use the separately
documented `recovery rotate-authority` flow. It creates a new UUID and fence-only journal
from an authenticated LKG lineage; it never truncates the current journal.
If a replacement authority was already established at an unsuitable local runtime path,
use the separately documented `recovery relocate-authority` continuation with its exact
source backup/head and intended locator ancestor. Never re-run adoption or edit identity
files to move it.

Verify authority before repair. Projection rebuild never edits journal segments.

## Bootstrap authenticated lifecycle history

Historical import is allowed only before the target graph starts work. First validate a
converter-produced generic manifest against its canonical authority-statement digest
obtained through a different trusted channel. That digest binds the target, normalized
source chain, and native mapping. Import then commits the receipt and complete mapped
prefix as one journal segment.

```sh
dagrail lifecycle validate-history --root . --file migration.json \
  --source-authority-hash sha256:SOURCE_AUTHORITY_DIGEST
dagrail lifecycle import-history --root . --file migration.json \
  --source-authority-hash sha256:SOURCE_AUTHORITY_DIGEST \
  --actor-role migration-operator --idempotency-key migration/source-prefix-1
dagrail lifecycle projection --root .
```

Do not use a manifest to infer its own trust anchor. Do not import into a project with
leases, Attempts, Decisions, effects, resources, Incidents, or graph revisions beyond
the initial import. Deterministic join/milestone settlement from that initial import is
allowed. Validation rejects non-ready Attempts, missing Role capabilities, leases over
24 hours, project-specific native vocabulary, contradictory Decisions, invalid event
time order, and Effect histories that do not match a recoverable writer prefix. See
[`migration.md`](migration.md) for conversion and cutover bounds.
