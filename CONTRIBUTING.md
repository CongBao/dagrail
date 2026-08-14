# Contributing

DAGrail requires Go 1.26 and uses test-driven changes at its durable seams.

Before opening a change:

```sh
gofmt -w .
go test ./...
go test -race ./...
CGO_ENABLED=0 go build -trimpath ./cmd/dagrail
```

Changes to graph, journal, provider, MCP, or receipt contracts require a compatibility test and an ADR when they are difficult to reverse. Never add a second authority beside the journal. New provider implementations must be deterministic or explicitly return an external receipt; they cannot receive storage handles.

Journal readers must verify the exact stored bytes and hash chain before applying an
in-memory upcast. Historical segments and fixtures are immutable. Projection schema
changes require a forward migration test and a rebuild-from-journal test.

Evidence changes must keep execution observations separate from semantic policy
outcomes. Reuse tests need both an unchanged protected core and at least one changed-core
reason; a reuse decision must remain replay-verifiable from journal data alone.

Keep control-plane transactions short. Add focused tests first, converge locally, then run the full race and cross-build gates once.
