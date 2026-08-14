# ADR 0013: Beta compatibility surfaces are explicit

Status: accepted

## Decision

The controller emits a machine-readable compatibility contract that inventories the
Graph, CLI, provider, journal, projection, MCP, and context-budget surfaces. Public
JSON evolves additively within its declared API version. MCP input schema digests make
otherwise subtle drift observable. Projection storage and human-facing text remain
non-contractual.

## Consequences

Integrators can test the exact binary they run instead of inferring promises from a
release number. Breaking pre-1.0 changes require an explicit API version and migration
path. The contract does not freeze implementation details or experimental host APIs.
