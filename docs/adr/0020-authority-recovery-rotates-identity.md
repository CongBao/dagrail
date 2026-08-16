# ADR 0020: Authority recovery rotates identity

## Status

Accepted in v0.22.0.

When an authority has advanced beyond its last known good backup, DAGrail creates a new
Project UUID with a fence-only journal and records lineage to both the old head and the
authenticated backup prefix. It never truncates or rewrites prior journal bytes; it
appends one terminal retirement fence that old reducers reject before mutation. Rotation
is refused while live leases, effects, resources, or incidents make the cut ambiguous.
This costs a subsequent explicit graph/history bootstrap, but preserves auditability and
avoids turning destructive reset into a normal recovery primitive.

The repository locator keeps the exact Project v1alpha1 field set understood by v0.21.
Each runtime generation instead holds a canonical-data-path-bound local authority claim
plus claim-bound recovery provenance. Journal append, restore, and retirement require
that claim. It is intentionally not portable: an old locator, backup, or complete
project data directory copied to another data root cannot revive the same writable UUID.
Normal v0.22 initialization also commits `authority.established` before publishing the
locator, so a stale v0.21 binary cannot become the first writer of a new UUID.
Full recovery without the original claimed store must create a new
identity and use authenticated lifecycle migration. Every later generation may rotate
again, and one cross-process retirement lock makes identical concurrent intent return
the same committed receipt while changed intent fails closed.

Pre-v0.22 stores are migrated only by an explicit exact-head adoption transaction that
retires the old UUID and creates a fresh one. DAGrail never mints a v0.22 claim for
legacy state. The selected old journal receives `authority.retired` under its historical
writer lock. The fresh journal receives `authority.established` before the Project
locator is published. Both use segment schema 4, so an already-admitted old append path
rejects the journal again inside its writer lock. The locator remains byte-shape
parseable by v0.21, but the fences intentionally prevent that binary from reading or
writing either authority. A crash at any boundary is completed by exact retry.

A claimed store without its first establishment fence is never promoted or repaired by
ordinary open or repeated initialization. Namespace publication is complete only after
each newly created directory and its containing parent have been flushed; fresh-process
retries recheck the deepest visible component and its parent without traversing unrelated
system ancestors. Same-intent legacy adoption validates
an already committed retirement from the fence's own authenticated backup digest, so a
concurrent loser cannot manufacture a changed intent by snapshotting after the winner's
commit.
