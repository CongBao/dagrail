# ADR 0002: No daemon and compile-in providers for v0.1

- Status: Accepted
- Date: 2026-08-14

## Context

The first release needs one portable binary across three harnesses without introducing a remote service, plugin ABI instability, or another scheduler.

## Decision

CLI and stdio MCP processes share a project file lock and journal. There is no daemon or background polling. Extensions implement the public Go SDK and register at compile time. Provider metadata binds a stable ID, SemVer, and schema hash.

## Consequences

Local concurrent processes have single-writer semantics without service administration. Adding a provider requires building a custom distribution. A future daemon can preserve the same command, journal, and receipt contracts.
