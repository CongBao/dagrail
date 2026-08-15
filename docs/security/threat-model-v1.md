# Threat model v1

This model applies to DAGrail v0.20's local CLI, stdio MCP server, compile-in
providers, harness subprocesses, loopback Explorer, immutable journal, SQLite
projection, portable exports, and plugin runtime. It is versioned because changing a
trust boundary is an architecture change, not a documentation edit.

## Security objective

DAGrail must preserve lifecycle integrity, prevent accidental duplicate effects, keep
authority free of obvious credentials and unbounded inputs, and make tampering visible
after an LLM session or process is replaced. It does not protect one process from
another malicious process running as the same OS user.

The linked plugin marketplace is distribution material, not authority. It contains only
public manifests, skills, hooks, and brand assets; its exact file set is digest-bound
before a host command can install it. Support reports are a separate, aggregate
diagnostic format and never contain journal payloads or graph identifiers.

## Assets and boundaries

| Asset | Authority | Protection | Explicit residual risk |
| --- | --- | --- | --- |
| Graph and lifecycle | canonical journal segments | hash chain, strict schemas, duplicate-key rejection, owner-only data directory | the same OS user can replace both bytes and expected local state |
| Allowed actions | HMAC-bound opaque refs | owner-only 32-byte secret, revision/head/Role/session/expiry binding | no actor identity or hardware-backed key |
| SQLite projection | never authoritative | integrity check and rebuild from verified journal | query availability may be lost until rebuild |
| Effects | external systems | prepare/dispatch/receipt/reconcile saga and stable action ID | a remote system may not expose enough evidence to resolve `unknown` |
| Portable files | backup/export bytes | digest plus optional detached Ed25519 signature | signature trust depends on separately distributed public keys |
| Artifact bodies and secrets | external stores | only digest, size, type, provenance, and credential-free URI enter authority | field screening cannot detect every secret or PII form |

The project locator is repository content and may be world-readable, but must not be
group/other writable on POSIX. Runtime data, journal segments, projections, observation
locators, and the action secret are owner-only. On Windows, v0.12 verifies structure and
reports that ACL inspection is delegated to host tooling; it does not claim an ACL was
proved by the portable binary.

## Entry points and abuse cases

- Graph, GraphPatch, project locator, backup, journal segment, signature envelope,
  install receipt, observation locator, hook input, MCP message, MCP tool input,
  provider input/output, Explorer query, and API response are bounded before durable
  use.
- Authority JSON rejects duplicate object keys, floating-point authority numbers,
  integers outside the RFC 8785 safe range, more than 64 nesting levels, excessive
  value/key/string counts, unknown typed fields where a closed envelope is expected,
  and trailing documents.
- Journal readers reject symlinks, oversized segments, excessive segments/events,
  non-canonical bytes, unsupported schemas, unknown envelope fields, filename/hash
  drift, chain drift, and v1/v2 segments carrying v3 command fields before reduction.
- MCP stdio rejects a message over 1 MiB before the SDK decodes another frame. Each
  high-level tool also rejects more than 64 KiB of typed input. Context accepts only
  orchestrator, worker, or reviewer views and cannot exceed that view's fixed maximum.
- Hooks treat malformed, trailing, or oversized host payloads as inactive and never
  echo a prompt. Hook and Explorer startup use read-only authority access and cannot
  settle a pending automatic lifecycle event.
- Effect bindings, reconciliation evidence, and receipts are authority-validated,
  size-bounded, and screened for credential-like fields before journaling.
- The journal writer independently screens every new command and event payload, so an
  omitted entry-point check cannot silently persist recognized credential material.
- Dynamic-graph impact tokens authorize one apply attempt and are removed before the
  resulting Graph Revision event is committed.
- New mutation commands bind idempotency keys to actor, target, kind, and canonical
  request digest, preventing a changed retry from inheriting an earlier result. Graph
  and provider imports plus GraphPatch apply validate the current request before replay.
- Plugin materialization uses a closed embedded file set, host-specific relative local
  marketplace sources, a digest-addressed destination, and exact mutation detection.
  Conformance binds the receipt artifact, hook PATH resolution, and structurally parsed
  MCP command/arguments to one real path and SHA-256.
- Reconciliation uses a per-action OS/process lock around adapter observation, not the
  journal lock; concurrent retries share the first committed result and a crash releases
  the observation lock for recovery.
- Support output pseudonymizes project identity and excludes absolute paths, Graph and
  event payloads, Node/Role IDs, prompts, artifacts, and raw harness output before an
  owner-only exclusive export is allowed.
- Recovery rehearsal binds all checks to one captured journal head, restores only into
  disposable storage, and compares a stable logical-table fingerprint rather than
  SQLite page bytes. It cannot overwrite or truncate the live journal.
- Release qualification accepts only fixed, bounded, regular source files under the
  selected root, verifies workflow action commit pins, emits no paths, and keeps
  structural automation declarations separate from external adoption evidence.
- Release verification accepts one closed top-level artifact set, streams bounded
  digests, inspects archive members without extraction, validates SPDX structure, and
  emits no local paths. Its manifest is integrity metadata, not a publisher credential.
- Explorer requests reject non-loopback/DNS-rebinding Host values. An explicit Origin
  must match the request's exact HTTP host and port; no CORS capability is granted
  between localhost ports.
- Historical compatibility extracts only bounded regular files/directories from exact
  commit archives, builds each old binary, and keeps its results structural rather than
  treating CI as real adoption.

## Threats intentionally not solved

- malicious code, debugger access, or file replacement by the same OS user;
- remote multi-tenant authorization, network authentication, encryption at rest, key
  rotation, revocation, transparency logs, or signed journal actor identity;
- compromised harnesses, Git servers, CI, artifact stores, compiler toolchains, or
  compile-in providers;
- complete secret, PII, malware, or legal-content detection;
- universal exactly-once delivery where the external system lacks an idempotency or
  observation surface.

## Verification

Run `dagrail security audit --root .` for a path-redacted structural, permission,
journal, and projection report. Run `dagrail journal verify --root .` for the head,
schema window, canonical export byte count, and SHA-256 digest. CI additionally runs
`govulncheck`, module verification, and a forbidden/unknown-license gate. These are
release gates against known evidence, not proof that the product is vulnerability-free.
