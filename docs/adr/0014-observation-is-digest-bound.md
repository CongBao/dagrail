# ADR 0014: Existing projects are observed through digest-bound shadows

Status: accepted

## Decision

Migration assessment imports a caller-provided Graph Definition into a separate
DAGrail project. Portable journal provenance contains only relative source paths,
digests, sizes, counts, and the resulting Graph Revision. Absolute local locators stay
in owner-only shadow data and can be discarded.

## Consequences

An observation is reproducible and drift-detecting without leaking workstation paths
into portable history or modifying source authority. It proves byte identity and graph
validity, not semantic equivalence or lifecycle migration.
