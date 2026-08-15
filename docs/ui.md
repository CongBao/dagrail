# Read-only DAG Explorer

`dagrail ui --root .` opens a foreground, loopback-only view of verified DAGrail state.
The Explorer never owns scheduling and exposes no transition, action, reconcile, graph
change, or effect-dispatch route.

Loopback binding is not treated as browser authentication. The server accepts only
`localhost` or loopback-IP Host values, rejects DNS-rebinding hostnames, and rejects an
explicit Origin unless its HTTP host and port exactly match the request Host. Responses
set same-origin resource/opener policies and never emit CORS access. This prevents a
page served from another localhost port from reading Explorer data; it does not protect
against another malicious process running as the same OS user.

## Views and deep links

- **Topology** renders at most 200 Nodes by default. Selecting a Node replaces the full
  view with its deterministic one-to-four-hop neighborhood.
- **Nodes** searches ID, title, kind, Role, and parent, with status/kind/Role filters and
  deterministic cursor pages.
- **Timeline** pages through payload-free command and event-type history.
- **Operations** summarizes attempts, Role leases, incidents, resources, and receipt
  states without returning effect requests or raw receipts.

The Node inspector is a modal read surface: background controls become inert, keyboard
focus remains inside the inspector across automatic refreshes, and closing returns to
the same Node in the current view when it is still present.

The URL preserves `view`, `node`, `q`, `status`, `kind`, `role`, `cursor`, `before`, and
`depth`. A copied local URL therefore reopens the same view and Node inspector while
that foreground server remains running.

## Bounded v1beta1 API

| Endpoint | Bound |
| --- | --- |
| `/api/v1/overview` | 100 ready IDs plus aggregate counts |
| `/api/v1/nodes` | 200 Nodes per page |
| `/api/v1/topology` | 500 Nodes maximum; UI requests 200 |
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
[`schemas/ui-api-v1beta1.schema.json`](../schemas/ui-api-v1beta1.schema.json). Its exact
SHA-256 digest is emitted by `dagrail contract`. Timeline navigation uses an exclusive
`before` cursor so older/newer traversal is non-overlapping even when the oldest page is
short. Node details omit Graph metadata, external-reference URLs, input bodies, effect
requests, raw receipts, and artifact bodies.
Decision rows expose only identity, closed outcome, source, provider identity, and time;
resource rows expose typed closure state but never the closure receipt body.

Topology is an operational map, not a graph editor. A focused response orders the focus
first, then increasing hop distance and stable Node ID, so truncation never removes the
selected Node. On a very large DAG, use search or the Nodes table to choose a focus Node
rather than increasing the render cap.
