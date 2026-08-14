# Security policy

Please report vulnerabilities privately through GitHub Security Advisories for this repository. Do not include real secrets, PII, private journal segments, or artifact URLs in a public issue.

## Alpha security boundary

DAGrail is currently a cooperative single-user local tool. Role capabilities prevent accidental collisions; they are not an authorization boundary against another local process running as the same OS user. Journal hashes detect mutation but do not prove an actor identity. External URIs and receipts may reveal metadata and should use least-privilege stores.

The controller rejects secret-like fields in effect requests and never intentionally stores prompts or chat transcripts. Operators remain responsible for keeping secrets outside Graph Definitions, checkpoints, journal exports, and evidence metadata.

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
