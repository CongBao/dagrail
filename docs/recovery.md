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

## Boundaries

- The recovery command uses an inspection-only open: it does not settle automatic
  nodes, synchronize SQLite, migrate its schema, or repair corruption before evidence
  is captured.
- The v1alpha1 JSON backup remains bounded to 256 MiB. Large deployments should retain
  the original segment directory through filesystem or object-store backup tooling;
  streaming portable archives remain a post-beta capability.
- Recovery does not retry external effects. After restoring authority, inspect every
  `unknown` or `reconciling` effect and use its stable action ID with `dagrail reconcile`.
- A projection fingerprint covers logical rows, not SQLite binary pages, paths, file
  permissions, or timestamps. Matching fingerprints are intentionally portable across
  operating systems and database rebuilds.
