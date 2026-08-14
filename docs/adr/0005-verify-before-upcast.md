# ADR 0005: Verify stored events before upcasting

- Status: Accepted
- Date: 2026-08-14

## Context

DAGrail journals must remain readable across releases without weakening the hash chain or silently changing historical meaning. Rewriting old segments would change their bytes, filenames, hashes, and every later `previousHash`. Hashing an upcasted representation instead would no longer prove the bytes actually stored on disk.

## Decision

DAGrail verifies canonical bytes, segment hashes, filenames, sequence, and the previous-hash chain using the exact stored schema. Only after verification may a deterministic in-memory upcaster convert an older event into the current reducer representation. New writes use the current segment schema and explicit event schema versions. Old segment files are never rewritten in place.

SQLite projection migrations remain independent from journal compatibility. A projection may be migrated or discarded and rebuilt from verified, normalized events.

## Consequences

Every supported historical schema needs immutable fixtures and mixed-version replay tests. Removing an upcaster is a breaking compatibility change. Compatibility reports can distinguish legacy data from corrupt or future data without making SQLite a second authority.
