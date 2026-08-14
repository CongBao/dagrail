# First governed DAG

This tutorial uses the repository's example graph and keeps the LLM or human in charge
of choosing work. DAGrail validates topology, leases, transitions, recovery, and effect
receipts; it does not run an autonomous scheduler.

## 1. Install and initialize

Build from source or install a release binary, then install the host projections you
use:

```sh
dagrail plugin install --harness codex,claude-code,copilot-cli
dagrail plugin conformance
dagrail init --root . --name example
```

Only `.dagrail/project.yaml` belongs in the repository. Journal and SQLite data remain
in DAGrail's per-user data directory.

## 2. Validate and import a graph

```sh
dagrail graph validate --file examples/development-dag.yaml
dagrail graph import --root . --file examples/development-dag.yaml \
  --actor-role architect --idempotency-key example-import-v1
dagrail frontier --root . --format json
```

The imported Graph Revision becomes runtime authority. Hierarchy metadata does not
create dependencies; only typed edges do.

## 3. Bind a stable role and obtain bounded context

```sh
dagrail role bind --root . --role developer --harness codex \
  --session SESSION_ID --ttl 15m --idempotency-key bind-SESSION_ID
dagrail context --root . --view worker --role developer --node implement
dagrail action list --root . --role developer --node implement
```

Choose one returned action. Do not construct a transition ref yourself:

```sh
dagrail action apply --root . --ref ALLOWED_ACTION_REF \
  --input '{}' --idempotency-key start-implement-1
```

Checkpoint before replacing a session. A successor binds the same stable Role under a
new session audit ID and receives the durable checkpoint in its bounded work package.

## 4. Change the graph safely

Prepare a typed Graph Patch, then bind impact analysis to the current revision:

```sh
dagrail graph preview-change --root . --file patch.json
dagrail graph apply-change --root . --file patch.json --token IMPACT_TOKEN \
  --actor-role architect --idempotency-key graph-change-1
```

Planned Nodes can change. Active contracts are frozen, terminal history is append-only,
and a stale token is rejected.

## 5. Observe, wait, and recover

```sh
dagrail pre-wait --root .
dagrail status --root .
dagrail recovery rehearse --root .
dagrail ui --root .
```

`pre-wait` prevents a session from claiming idle while ready work, submitted work,
pending effects, live incidents, or capacity remain. The recovery rehearsal restores
and replays the current journal in disposable storage; it never repairs evidence before
checking it. The UI is loopback-only and read-only.

For a multi-harness graph, continue with `examples/beta-project/README.md`. For incident,
backup, and ambiguous-effect procedures, use `docs/operations.md` and
`docs/recovery.md`.
