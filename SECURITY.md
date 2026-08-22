# Security policy

Please report vulnerabilities privately through GitHub Security Advisories for this repository. Do not include real secrets, PII, private journal segments, or artifact URLs in a public issue.

## Local beta security boundary

DAGrail is currently a cooperative single-user local tool. Role capabilities prevent accidental collisions; they are not an authorization boundary against another local process running as the same OS user. Journal hashes detect mutation but do not prove an actor identity. External URIs and receipts may reveal metadata and should use least-privilege stores.

Run `dagrail security audit --root .` to verify the declared boundary, owner-only POSIX
runtime state, journal bytes, and SQLite integrity without emitting authority payloads
or absolute project paths. Windows results deliberately report that ACL verification
requires host tooling. See the versioned
[`threat model`](docs/security/threat-model-v1.md) for assets, entry points, controls,
and residual risks.

Source and release builds require Go 1.26.6 or newer. The module toolchain directive
prevents silently using earlier Go 1.26 standard libraries that fail the pinned
`govulncheck` release gate.

The controller rejects secret-like fields in effect requests and never intentionally stores prompts or chat transcripts. Operators remain responsible for keeping secrets outside Graph Definitions, checkpoints, journal exports, and evidence metadata.

Secret screening is defense in depth rather than a complete secret scanner. It rejects
credential-like keys, common token prefixes, bearer values, URI userinfo, and sensitive
query parameters, but callers must still pass references or digests instead of secret
material.

Graph Definition and GraphPatch inputs must be regular files no larger than 8 MiB.
Predicate ASTs may nest at most 64 levels. These limits bound parser and recursive
validation exposure; they do not substitute for reviewing graph provenance.

Git artifact closure manifests must be regular, non-symlink files no larger than 1 MiB
and are read through the caller's cancellation context. Git evidence commands remove
repository/object-store redirection variables, disable replacement objects and lazy
fetch, and derive commit parents and reachability from raw object bytes. Repository
replace refs, legacy grafts, or ambient `GIT_DIR`/`GIT_WORK_TREE` therefore cannot
rewrite an exact closure or scope report.

All authority JSON rejects duplicate keys, unsafe numeric forms, more than 64 nesting
levels, excessive values, overlong keys/strings, and trailing documents. Journal
segments are regular non-symlink canonical files limited to 16 MiB, 10,000 events per
segment, and one million segments per local project. MCP stdio messages are limited to
1 MiB and each high-level tool input to 64 KiB.

Compile-in providers are trusted code, not a sandbox. The Provider Runtime validates
JSON Schema, deadlines, panics, output size, and secret-like fields, but Go code that
ignores cancellation may continue in a detached goroutine. Providers receive no DAGrail
storage handles; custom distributions should review provider filesystem and network use.

Execution Packages accept only digest metadata and bounded absolute artifact URIs. URI
userinfo, query strings, fragments, and data schemes are rejected because they commonly
carry credentials or inline artifact bodies.

The Explorer is a local read-only projection. It refuses non-loopback binds,
accepts only `GET` and `HEAD`, loads no third-party assets, emits restrictive browser
security and Permissions Policy headers, and omits allowed-action references,
controller tokens, event payloads, effect requests, prompts, Graph metadata,
external-reference URLs, and artifact bodies. Every collection and response, including
the legacy snapshot route, has a fixed bound; `HEAD` performs the same validation as
`GET`. It is not a remotely deployable authenticated dashboard.

Loopback is a network bind restriction, not a browser-origin boundary. The Explorer
rejects non-loopback and DNS-rebinding Host values, and an explicit Origin must match
the exact HTTP Host including port. Cross-port localhost requests are rejected, CORS is
never enabled, and same-origin resource/opener policies are emitted. This limits
browser-mediated cross-origin reads; it does not isolate another malicious process
running under the same OS account.

The owner-local daemon uses a mode-0600 Unix socket on macOS/Linux or a named pipe
restricted to the current Windows SID. These permissions prevent accidental cross-user
use; they do not authenticate competing processes owned by the same user. Daemon logs
contain only a generated operation number, top-level command, duration, and stable error
class. They omit CLI arguments, input/receipt bodies, prompts, and secrets. In-memory
verified snapshots and HMAC-sealed SQLite checkpoints are disposable cache state and
never authorize an append without the journal, path-bound authority claim, Role lease,
and signed action checks.

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

The release manifest independently closes the complete 12-payload distribution set.
Offline verification rejects symlinks, path traversal, duplicate archive entries,
unexpected files, excessive compressed expansion, unreadable ZIP members,
non-deterministic archive timestamps, checksum drift, and incomplete SPDX envelopes.
Manifest consistency is not a signature; publisher trust still depends on the GitHub
provenance policy and trusted repository identity.

Release binaries embed only the public plugin projection: host manifests, local
marketplace catalogs, skills, hooks, and brand assets. Materialization verifies the
exact linked file set and digest, uses relative local plugin sources, and rejects
mutation or extra files. The bundle contains no runtime state, repository history,
source code, credentials, or project authority. Host plugin installation remains an
external operation under the cooperative OS-user boundary.

Host plugin management captures at most 64 KiB of combined subprocess output, honors
caller cancellation, and terminates an individual host command after two minutes.
`doctor install` converts runtime, bundle, registration, and MCP state into closed codes
without executable paths or raw host output. CLIError envelopes are local operator
output, not shareable support reports; their message is bounded but can still include
an operator-supplied path returned by an underlying command.

Support reports are deliberately narrower than backups: they pseudonymize project
identity and contain aggregate counts plus typed security, journal, and status-only
doctor evidence. They exclude Graph and event payloads, identifiers, absolute paths,
prompts, artifact bodies, and raw harness output. Export is owner-only and exclusive;
operators should still preview the exact report before sharing it.

Recovery rehearsal copies a verified immutable journal prefix only into a fresh
disposable directory, replays it, and rebuilds a separate SQLite projection. It never
replaces live files. Its state and projection fingerprints are digests, not signatures;
they prove deterministic local equivalence but do not establish an external identity.

Release qualification reads only fixed, bounded, non-symlink public source files and
emits closed status codes rather than source or project paths. Optional project evidence
uses inspection-only security and recovery checks. Structural qualification never
claims that CI actually ran for a particular tag or that production adoption occurred;
the tag workflow supplies build evidence and the report leaves adoption gaps explicit.

The historical compatibility job builds exact commit-pinned v0.10.0–v0.26.6 sources in
temporary directories. Source archives reject traversal, links, unsupported entry
types, excessive entries, and excessive expanded bytes. Readiness reports cite the
manifest digest but cannot turn this CI evidence into production validation.

Detached Ed25519 signatures are optional and cover the SHA-256 digest of exact file
bytes with a DAGrail domain separator. Private keys must be protected separately and
public keys must be distributed through a trusted channel. Export signatures neither
encrypt data nor identify individual journal actors.

Signing and verification stream regular payload files up to 1 GiB. Journal export
refuses to overwrite an existing destination; operators should sign a newly created
export rather than a mutable working file. In-memory portable journal exports and
backups are capped at 256 MiB; larger-history streaming portability remains a later
lifecycle-maturity capability.

Observe-only migration opens caller-selected authority as regular files and rejects
absolute or escaping entries, duplicate paths, symlink escape, and oversized inputs.
The shadow root must resolve outside the source project. Portable journal provenance
contains only relative paths, sizes, and digests; absolute source and graph locators are
kept in an owner-only private shadow file. This prevents ordinary path disclosure, not
metadata inference from user-selected relative names or file sizes.
