# ADR 0015: The Explorer is a bounded projection, not a controller

Status: accepted

## Decision

The browser UI reads six v1beta1 query projections and exposes no mutation route. Full
graph rendering is capped; selecting a Node requests a deterministic bounded
neighborhood that retains the focus before distance/ID truncation. Lists use explicit
cursors or latest-object limits, all JSON is capped at 2 MiB, and Node/effect bodies are
represented by digests and receipt states. A published response schema is digest-bound
into the compatibility contract.

URL query keys identify a local view and selected Node but carry no allowed action,
lease authority, controller secret, or transition input.

## Consequences

The Explorer remains usable on large DAGs without becoming another authority or
scheduler. Operators must use CLI/MCP for every transition. A future remote dashboard
requires a separate authentication and transport decision rather than relaxing the
loopback boundary.
