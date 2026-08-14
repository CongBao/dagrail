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
and the explainable frontier. `history` returns payload-free command metadata in pages
of at most 100 entries. `ui` starts a foreground, loopback-only, strictly read-only web
Explorer and opens the default browser; use `--open=false` when automatic launch is not
wanted. See [`ui.md`](ui.md) for bounded APIs, deep links, filtering, and large-graph
behavior.

## Manage incidents

An incident has a stable owner Role, deadline, attempt budget, progress metric, and
dependency cut. Its owner must hold the current Role lease.

```sh
dagrail incident progress --root . --incident INCIDENT --role ROLE \
  --note "candidate narrowed" --made-progress=true --idempotency-key progress-1
dagrail incident trip --root . --incident INCIDENT --role ROLE \
  --reason "attempt budget exhausted" --idempotency-key trip-1
dagrail incident resolve --root . --incident INCIDENT --role ROLE \
  --resolution "fixed by candidate sha256:..." --idempotency-key resolve-1
```

Two consecutive no-progress reports trip the default circuit breaker. A missed deadline
also trips on the next progress evaluation. A circuit-open effect incident must be
resolved before that effect can reconcile; unrelated graph lanes remain available.

## Back up and restore

```sh
dagrail backup create --root . --output dagrail-backup.json
dagrail backup verify --root . --file dagrail-backup.json
dagrail backup restore --root . --file dagrail-backup.json
```

The backup is canonical JSON containing exact journal segments plus a content digest.
Verification checks the digest, hash chain, sequence, and project UUID. Restore accepts
only an empty journal or an exact prefix of the backup, never overwrites a divergent
segment, and automatically rebuilds SQLite. Re-running the same restore is idempotent.

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

## Repair projections

```sh
dagrail journal verify --root .
dagrail journal compatibility --root .
dagrail journal replay --root .
dagrail projection rebuild --root .
dagrail doctor --root .
```

Verify authority before repair. Projection rebuild never edits journal segments.
