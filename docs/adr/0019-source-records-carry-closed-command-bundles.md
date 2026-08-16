# ADR 0019: Source records carry closed command bundles

## Status

Accepted in v0.22.0.

An immutable external lifecycle record may represent several ordered DAGrail writer
commands. v1beta1 therefore stores `commands[]` inside that one source record instead of
splitting or duplicating source identity. Each command is simulated and proof-closed
independently; support events cannot cross command boundaries. v1alpha1 retains its
single-command meaning. We rejected merely allowing multiple `action.applied` events in
one proof ledger because that would make causality and evidence consumption ambiguous.
