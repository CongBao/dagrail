# ADR 0006: Separate execution evidence from policy decisions

- Status: Accepted
- Date: 2026-08-14

## Context

An expensive execution matrix may remain valid when a validator, fixture projection,
or review policy changes. Treating every policy edit as an execution-input edit creates
unbounded review loops. Conversely, reusing results after the candidate, prospective
tree, command graph, Node contract, or protected inputs changed would be unsound.

## Decision

DAGrail records an immutable Execution Package for an Attempt. The package stores only
bounded metadata and content digests for its candidate, prospective tree, command graph,
protected inputs, execution observations, artifacts, and provenance. Artifact bodies,
logs, secrets, prompts, and transcripts remain outside the journal.

A domain-separated Protected Core digest covers the candidate, prospective tree,
command graph, Node contract, and normalized protected inputs. It deliberately excludes
policy identity and policy version. A Reuse Decision binds a specific policy version to
the package and a newly declared core, then deterministically returns either
`reuse_execution` or `rerun_required` with closed reason codes.

The decision says only whether the execution evidence is still applicable. It never
claims that a policy passed, that a human approved the result, or that an external URI
is currently reachable. Policy provider execution remains a separate control-plane
operation.

## Consequences

Policy repair can reevaluate an unchanged package without rerunning execution. Any
protected-core difference fails closed to `rerun_required`. Package and decision IDs are
content-derived, journal history is append-only, and SQLite indexes remain disposable.
