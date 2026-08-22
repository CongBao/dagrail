# ADR 0024: Controller Incident Closure Preserves Ownership

## Status

Accepted for v0.26.4.

## Context

A terminal Attempt can open an Incident owned by the Role that performed the work. The
executor may already be durably handed off and passive while a separate controller is
responsible for a closed rollback, quarantine, LKG, or off-critical-path decision.
Borrowing the executor Role would create false audit identity; making ordinary
`incident.manage` cross-owner would grant substantially broader authority.

## Decision

DAGrail defines the separate `incident.control` capability and exact
`incident.control-resolve` verb. It atomically applies one non-retry recovery disposition
and resolution only when the source is a terminal failed or cancelled Attempt and the
actor holds a current lease with that capability. The Incident retains its original
owner and source identity. A nested control audit records authority, truthful actor,
original owner, disposition, resolution, note, and timestamp.

The operation is rejected for owner-local use, non-terminal or successful/retryable
Attempts, Resource or Effect Incidents, retry/escalate, and the reserved repair
supersession resolution. `incident.supersede` and observation-driven closure remain
independent contracts.

## Consequences

Controller closure is available as a signed allowed action through bounded context and
MCP, as a direct typed CLI command, and as pre-wait remediation. Lifecycle migration
revalidates the exact controller lease/capability and complete audit binding. Projects
must explicitly add `incident.control` to a reviewed controller Role; no role name is
privileged by the kernel.
