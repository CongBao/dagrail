# Read-only DAG Explorer

`dagrail ui --root .` opens a foreground, loopback-only view of verified DAGrail state.
The Explorer never owns scheduling and exposes no transition, action, reconcile, graph
change, or effect-dispatch route.
Starting the Explorer uses a journal-derived read-only open: it does not settle pending
automatic Nodes, migrate/repair SQLite, or synchronize a projection.

Loopback binding is not treated as browser authentication. The server accepts only
`localhost` or loopback-IP Host values, rejects DNS-rebinding hostnames, and rejects an
explicit Origin unless its HTTP host and port exactly match the request Host. Responses
set same-origin resource/opener policies and never emit CORS access. This prevents a
page served from another localhost port from reading Explorer data; it does not protect
against another malicious process running as the same OS user.

## Views and deep links

- **Project DAG** is the default for a grouped Graph. It renders every visible group in
  fixed generic lanes while keeping collapsed internal Nodes outside the render cap.
- **Execution Detail** retains the exact flat Node topology. Selecting a Node focuses its
  deterministic one-to-four-hop neighborhood.
- **Nodes** searches ID, title, kind, Role, and parent, with status/kind/Role filters and
  deterministic cursor pages.
- **Timeline** pages through payload-free command and event-type history.
- **Operations** summarizes attempts, Role leases, incidents, resources, and receipt
  states without returning effect requests or raw receipts.

The Node inspector is a modal read surface: background controls become inert, keyboard
focus remains inside the inspector across automatic refreshes, and closing returns to
the same Node in the current view when it is still present.

The URL preserves `view`, `node`, `group`, compact `groupState=expanded|collapsed`,
repeated per-group `expanded`/`collapsed` exceptions, `q`, `status`, `kind`, `role`,
`cursor`, `before`, and `depth`. Repeated values keep group IDs opaque—even IDs containing
commas round-trip exactly—while the compact state keeps expand/collapse-all within the
query budget at the declared 256-group maximum. A copied local URL therefore reopens the
same summary/detail state and inspector while that foreground server runs.

## Bounded v1beta2 API

| Endpoint | Bound |
| --- | --- |
| `/api/v1/overview` | 100 ready IDs plus aggregate counts |
| `/api/v1/nodes` | 200 Nodes per page |
| `/api/v1/topology` | all matching top-level groups; 500 expanded/detail Nodes and 100 aggregate-edge summaries maximum; UI requests 200 Nodes |
| `/api/v1/aggregate-edges?ref=…` | 100 aggregate-edge summaries per cursor page; ref binds the complete grouping projection |
| `/api/v1/group-edges?ref=…` | 100 exact edge IDs per cursor page; summary carries only count/digest/ref |
| `/api/v1/node?id=…` | one payload-free Node detail; 100 items per child collection plus counts/truncation |
| `/api/v1/history` | 100 payload-free entries |
| `/api/v1/operations` | 200 latest objects of each kind |

Every JSON response is capped at 2 MiB and rejects unknown, duplicate, oversized, or
out-of-range query parameters. `HEAD` performs the same validation and projection work
as `GET`, then suppresses the body. Results are sorted deterministically and bind the
current Graph Revision and journal head. The earlier `/api/v1/snapshot` route remains
available for v0.10 compatibility, but it now fails atomically with 413 rather than
returning a partial response above the same cap.

The public response and error contract is
[`schemas/ui-api-v1beta2.schema.json`](../schemas/ui-api-v1beta2.schema.json). Its exact
SHA-256 digest is emitted by `dagrail contract`. Timeline navigation uses an exclusive
`before` cursor so older/newer traversal is non-overlapping even when the oldest page is
short. Node details omit Graph metadata, external-reference URLs, input bodies, effect
requests, raw receipts, and artifact bodies.
Decision rows expose only identity, closed outcome, source, provider identity, and time;
resource rows expose typed closure state but never the closure receipt body.

Topology is an operational map, not a graph editor. Project DAG rollups, health,
membership digests, fixed lanes, and collapsed edges come from one GraphRevision/head;
dense aggregate indexes remain recoverable through the aggregate-edge ref and exact source
edge IDs through each group-edge ref. A focused Execution Detail
response orders the focus first, then increasing hop distance and stable Node ID, so
truncation never removes the selected Node.
