# Reliability qualification

DAGrail v0.9 treats reliability claims as executable contracts. The suite runs against
temporary projects and never needs a daemon, remote service, real harness account, or
external side effect.

## Matrix

| Boundary | Injected condition | Required result |
| --- | --- | --- |
| Journal before rename | create, write, fsync, rename failure; hard process exit | no committed segment; retry commits sequence once |
| Journal after rename | hard process exit or directory-sync ambiguity | segment verifies; same idempotency key returns it without another event |
| Writer lease | 12 OS processes use one key, then 12 unique keys | one shared result, then a contiguous hash chain with no duplicate keys |
| Authority corruption | truncation, byte mutation, non-canonical JSON, wrong filename | verification fails closed |
| Projection corruption | invalid SQLite bytes after a valid journal commit | corrupt file is quarantined and the projection is rebuilt from the journal |
| Longevity | 256 sequential segments and a repeated final command | sequence/hash chain verifies and retry remains idempotent |
| Scale/context | 2,048 simultaneously ready nodes | full frontier is inspectable; orchestrator, worker, and reviewer contexts stay in budget |
| Structured inputs | graph, patch, segment, and receipt fuzz corpora | no panic, unsafe receipt promotion, or unbounded accepted input |
| Trust boundary | duplicate/unknown/deep inputs, oversized MCP/hook/journal frames, permission drift | closed rejection and path-redacted audit evidence |

The rename is the logical commit point. A returned error after rename is deliberately
ambiguous: callers reconcile by replaying the journal and repeating the stable
idempotency key. Directory durability cannot be perfectly simulated across power loss;
POSIX builds now surface directory-sync errors, while Windows relies on the platform's
rename semantics and subsequent verification.

## Run locally

```sh
go test ./...
go test -race ./...
go test ./internal/journal -run 'FaultMatrix|ProcessCrash|CrossProcess|CorruptionMatrix|LongJournal' -count=1
go test ./internal/service -run 'LargeReadyFrontier|DefinitionInputs' -count=1
```

Run individual fuzz targets for longer investigations:

```sh
go test ./internal/domain -run '^$' -fuzz '^FuzzValidateGraphJSON$' -fuzztime=30s
go test ./internal/journal -run '^$' -fuzz '^FuzzValidateSegments$' -fuzztime=30s
go test ./internal/service -run '^$' -fuzz '^FuzzDecodeGraphPatch$' -fuzztime=30s
go test ./internal/harness -run '^$' -fuzz '^FuzzNativeReceiptConformance$' -fuzztime=30s
```

GitHub CI runs every seed corpus as part of normal tests and performs a bounded fuzz
smoke for each target on every push. Longer fuzz runs are an investigation tool, not a
claim that all possible failures have been proven absent.

## Security qualification

Every push verifies module checksums, runs pinned `govulncheck` against reachable code,
and rejects dependencies classified as forbidden or unknown by the pinned license
tool. The test suite validates published SecurityAudit and JournalVerification schemas,
permission drift, duplicate keys, deep JSON, strict project locators, unknown journal
fields, MCP frame limits, and fail-safe hook parsing. These gates cover known evidence;
they do not prove the absence of undisclosed vulnerabilities or license mistakes.

## Explorer qualification

The v0.11 Explorer suite imports a 2,048-Node chain and verifies deterministic 200-Node
pages, a capped topology, five-Node focused neighborhoods, sub-2-MiB responses, and a
five-second local query budget. A 600-neighbor star proves the focus survives truncation;
108 journal segments prove older/newer history navigation is non-overlapping and
reversible. Additional tests reject malformed and unknown GET/HEAD queries, validate all
v1beta1 response/error envelopes against the published schema, exercise Node detail
escaping and payload omission, bound the v0.10 snapshot route, and prove that all HTTP
mutation methods remain unavailable.

A second observe-only qualification converted an external 58-Stage roadmap plus four
typed gates into a 62-Node, 142-edge shadow. Seven caller-selected authority files were
digest-bound, the full Explorer was exercised from an isolated runtime, and source
HEAD/status/content digests were unchanged for the duration of that snapshot. This is a
static topology, isolation, and UI-capacity result only: lifecycle events were not
migrated, so the shadow frontier is not claimed to equal the source project's live
frontier. Verification remains bound to the recorded source snapshot and correctly
reports drift if that independently active source advances later.
