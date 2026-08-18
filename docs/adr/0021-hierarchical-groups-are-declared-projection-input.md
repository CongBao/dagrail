# ADR 0021: hierarchical groups are declared projection input

Status: accepted

## Context

A complete execution DAG can contain many lifecycle, review, repair, gate, and Effect
Nodes for every user-visible work unit. Flattening those Nodes is auditable but makes the
default project topology unreadable. Inferring groups from project-specific names or
metadata would create an invisible authority and couple the kernel to its first adopter.

## Decision

- `GroupDefinition` and `Node.groupId` are additive GraphRevision declarations.
- Dependency and predicate authority remains Node-to-Node. A group has no Attempt,
  lease, outcome, or lifecycle writer of its own.
- Group summaries, health, lanes, collapsed edges, membership digests, and layout are
  deterministic read projections rebuilt from one GraphRevision and journal head.
- `summaryNodeId`, when present, is the exact lifecycle anchor. Operational health is
  reported separately and may prevent a clean-completed display without rewriting the
  summary Node outcome.
- Collapse, expansion, focus, and filters are browser URL state only. They never enter
  the journal or SQLite authority.
- Hierarchy changes use revision-bound GraphPatch preview/apply. Moving an active Node
  changes only view membership; its execution contract remains frozen.
- DAGrail ships no project-specific grouping heuristic. Importers, skills, or compile-in
  providers may propose explicit GraphPatch operations, which become authoritative only
  after normal validation and apply.

## Consequences

Old ungrouped Graphs remain valid and retain Execution Detail. Summary mode is available
after explicit grouping, scales independently of internal Node count, and remains fully
auditable through expansion and exact aggregate-edge inspection. Integrations own their
mapping vocabulary; the kernel owns validation, determinism, revision binding, bounds,
and byte-nonmutating inspection.
