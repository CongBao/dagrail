# Compatibility policy

DAGrail v0.10 starts a deliberately narrow beta compatibility line. Run
`dagrail contract` to inspect the exact surfaces, schema versions, MCP input-schema
digests, context budgets, and command inventory implemented by the current binary.

## Promised through the v0.x beta line

- Verified journal history is never rewritten. A release either reads a stored schema
  through an explicit upcaster or fails closed before reduction.
- The six MCP tool names remain stable. Documented input fields and CLI JSON fields are
  additive unless a new API version is selected.
- Stable provider contracts remain source-compatible. Additions use new optional
  interfaces or types; existing Go interfaces do not gain required methods.
- Explorer v1beta1 response/error fields are governed by the schema path and exact
  digest in `dagrail contract`; fields are additive and its documented deep-link query
  keys remain accepted. The Explorer remains loopback-only and has no mutation route.
- Graph Definition v1alpha1 remains importable. A future graph format uses another
  `apiVersion` rather than silently changing existing semantics.
- SQLite is never portable authority and may be rebuilt from the verified journal.
- SecurityAudit and JournalVerification v1alpha1 fields are governed by the schema
  paths and exact digests in `dagrail contract`. They are additive, read-only reports;
  they do not upgrade the local-user boundary into an authorization system.
- PluginConformance and SupportReport v1alpha1 fields are governed by the schema paths
  and exact digests in `dagrail contract`. Conformance diagnostics are path-free;
  SupportReport remains aggregate and free of authority payloads and host output.

## Not promised

Human-readable wording, command help layout, ordering of JSON object keys, experimental
provider contracts, native harness preview protocols, and the SQLite schema are not
stable interfaces. Pre-1.0 beta releases may remove a surface only with a versioned
replacement, migration instructions, and an explicit changelog entry.

The compatibility contract is descriptive rather than negotiable input: callers must
not edit it and expect the controller to change behavior. Automation should select on
API versions and schema hashes, not the DAGrail executable's display version.
