# ADR 0015: The Explorer is a bounded projection, not a controller

Status: accepted

## Decision

The browser UI reads bounded query projections and exposes no mutation route. The
v1beta3 Project Map keeps every top-level Group, retrieves snapshot-bound Group members
and dense-edge pages lazily, and fails stale rather than mixing heads. Execution Detail
retains deterministic bounded neighborhoods. Lists use explicit cursors or
latest-object limits, all JSON is capped at 2 MiB, and Node/effect bodies are represented
by digests and receipt states. Published response schemas are digest-bound into the
compatibility contract. ADR 0023 defines the hierarchical interaction contract.

URL query keys identify a local view and selected Node but carry no allowed action,
lease authority, controller secret, or transition input.

## Consequences

The Explorer remains usable on large DAGs without becoming another authority or
scheduler. Operators must use CLI/MCP for every transition. A future remote dashboard
requires a separate authentication and transport decision rather than relaxing the
loopback boundary.
