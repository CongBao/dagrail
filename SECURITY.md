# Security policy

Please report vulnerabilities privately through GitHub Security Advisories for this repository. Do not include real secrets, PII, private journal segments, or artifact URLs in a public issue.

## Alpha security boundary

DAGrail is currently a cooperative single-user local tool. Role capabilities prevent accidental collisions; they are not an authorization boundary against another local process running as the same OS user. Journal hashes detect mutation but do not prove an actor identity. External URIs and receipts may reveal metadata and should use least-privilege stores.

The controller rejects secret-like fields in effect requests and never intentionally stores prompts or chat transcripts. Operators remain responsible for keeping secrets outside Graph Definitions, checkpoints, journal exports, and evidence metadata.

Secret screening is defense in depth rather than a complete secret scanner. It rejects
credential-like keys, common token prefixes, bearer values, URI userinfo, and sensitive
query parameters, but callers must still pass references or digests instead of secret
material.

Compile-in providers are trusted code, not a sandbox. The Provider Runtime validates
JSON Schema, deadlines, panics, output size, and secret-like fields, but Go code that
ignores cancellation may continue in a detached goroutine. Providers receive no DAGrail
storage handles; custom distributions should review provider filesystem and network use.

Execution Packages accept only digest metadata and bounded absolute artifact URIs. URI
userinfo, query strings, fragments, and data schemes are rejected because they commonly
carry credentials or inline artifact bodies.

The v0.5 web UI is a local read-only projection. It refuses non-loopback binds, accepts
only `GET` and `HEAD`, loads no third-party assets, emits restrictive browser security
headers, and omits allowed-action references, controller tokens, event payloads, prompts,
and artifact bodies. It is not a remotely deployable authenticated dashboard.

The Codex native adapter talks only to the detected local executable through the
harness-owned app-server daemon and stdio proxy. Receipt detail omits the generated work
prompt. Capability probing does not start a thread, and automated tests use protocol
fixtures rather than an account. The adapter does not auto-approve commands or treat
Codex turn completion as a DAG semantic outcome.

Claude Code native dispatch uses documented headless JSON flags, has a two-hour turn
deadline, and records only output size and digests. Existing Claude settings still
govern tool permissions. Copilot native dispatch uses ACP v1 over a child process's
stdio, caps each message at 16 MiB, and runs one synchronous turn. ACP permission
requests default to `reject_once`; a graph may opt into `allow-once`, but DAGrail never
selects `allow_always`. Copilot's ACP surface is a public preview and is reported as
experimental. Neither adapter turns harness completion into Node acceptance.

Release workflows pin third-party actions to full commits, reproduce each target before
publication, emit checksums and per-target SPDX SBOMs, and request build-provenance
attestations. Installers validate a closed archive allowlist and exactly one checksum
entry. Runtime upgrades execute fresh-process probes and retain a digest-addressed
rollback binary; this protects against accidental corruption, not a malicious process
running as the same OS user.

Detached Ed25519 signatures are optional and cover the SHA-256 digest of exact file
bytes with a DAGrail domain separator. Private keys must be protected separately and
public keys must be distributed through a trusted channel. Export signatures neither
encrypt data nor identify individual journal actors.
