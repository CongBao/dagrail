# Read-only DAG Explorer

`dagrail ui --root .` asks the owner-local daemon to open a loopback-only view of one
verified DAGrail snapshot, returns its URL, and exits. Use `dagrail ui status|stop` to
inspect or stop that server.
The Explorer never owns scheduling and exposes no transition, action, reconcile, graph
change, or effect-dispatch route.
Starting the Explorer uses a journal-derived read-only open: it does not settle pending
automatic Nodes, migrate/repair SQLite, or synchronize a projection.
The complete interaction contract is frozen in
[ADR 0023](adr/0023-hierarchical-dag-explorer.md).

Loopback binding is not treated as browser authentication. The server accepts only
`localhost` or loopback-IP Host values, rejects DNS-rebinding hostnames, and rejects an
explicit Origin unless its HTTP host and port exactly match the request Host. Responses
set same-origin resource/opener policies and never emit CORS access. This prevents a
page served from another localhost port from reading Explorer data; it does not protect
against another malicious process running as the same OS user.

## Views and deep links

- **Project map** is the default for a grouped Graph. ELK lays out every visible Group
  and global Node in stable top-down layers with orthogonal dependency routes. Internal
  Nodes remain outside the top-level render cap. Exactly one Group may be expanded;
  opening another closes the first, centers the new compound Node and its one-hop
  neighborhood, and Collapse/Escape restores the previous viewport and keyboard focus.
  Graphs may declare ordered `spec.lanes` and assign `group.laneId` or `node.laneId`;
  otherwise the backward-compatible built-in lanes apply.
- **Execution Detail** retains the exact flat Node topology. Selecting a Node focuses its
  deterministic one-to-four-hop neighborhood.
- The left **Navigator** combines locate search, a lazy hierarchy, and an interactive
  minimap. A hit inside a collapsed Group preserves its ancestor context before opening
  the Node. Tree, canvas, Inspector, minimap, and URL share the same selection.
- **Timeline** pages through payload-free command and event-type history.
- **Operations** summarizes attempts, Role leases, incidents, resources, and receipt
  states without returning effect requests or raw receipts.

The docked Node Inspector opens immediately with a skeleton and loads only that Node; it
never waits for a full topology refresh. Its width is pointer- and keyboard-adjustable,
and it becomes a bottom sheet on small screens without covering desktop DAG navigation.

Automatic refresh is disabled by default. The header offers explicit 30- and 60-second
polling; polling pauses while a request, drawer, input, or hidden tab is active and first
checks the lightweight journal head. Refresh and navigation requests are cancellable and
deduplicated. A failed refresh retains the last verified snapshot and reports its time
and disconnected/stale state instead of clearing the page.

The v1beta3 Project map URL preserves `view`, one expanded `group`, selected `node`, and
the locate query `q`. A copied local URL therefore recovers the same hierarchy and
Inspector state. The retained v1beta2 audit routes preserve their documented cursor and
filter query parameters independently.

## Bounded APIs

| Endpoint | Bound |
| --- | --- |
| `/api/v1/project-map` | every top-level Group plus deterministic dependency backbone and complete edge-index ref |
| `/api/v1/group-members?id=…` | one expanded compound Group, nested Groups, and at most 500 visible Nodes |
| `/api/v1/locate?q=…` | 100 matching Nodes/Groups maximum with ancestor paths |
| `/api/v1/head` | current tail identity plus cached-snapshot identity; no full replay |
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

The Project map response contract is
[`schemas/ui-api-v1beta3.schema.json`](../schemas/ui-api-v1beta3.schema.json); retained
audit routes use [`schemas/ui-api-v1beta2.schema.json`](../schemas/ui-api-v1beta2.schema.json).
Their exact SHA-256 digests are emitted by `dagrail contract`. Timeline navigation uses an exclusive
`before` cursor so older/newer traversal is non-overlapping even when the oldest page is
short. Node details omit Graph metadata, external-reference URLs, input bodies, effect
requests, raw receipts, and artifact bodies.
Decision rows expose only identity, closed outcome, source, provider identity, and time;
resource rows expose typed closure state but never the closure receipt body.

Topology is an operational map, not a graph editor. Project DAG rollups, health,
membership digests, declared lanes, and collapsed edges come from one GraphRevision/head.
The default canvas draws a deterministic connectivity backbone rather than thousands of
crossing lines. Selection loads every aggregate dependency in the complete one-hop
neighborhood; dense indexes remain recoverable through the aggregate-edge ref and exact
source edge IDs through each group-edge ref. Layout runs in a cancellable Web Worker.
All views reuse one verified/reduced snapshot. An enabled head poll reports
when that prefix is stale and advances it with one shared replay; manual Refresh forces the
same verification explicitly. A focused Execution Detail
response orders the focus first, then increasing hop distance and stable Node ID, so
truncation never removes the selected Node.

Release CI runs the pinned Chromium visual suite in `internal/ui/web/visual`. Its light,
dark, expanded-Group, accordion, search, reduced-motion, and narrow-screen Inspector
screenshots are reviewed golden artifacts, complementing the API, accessibility, and
large-graph tests without making browser pixels part of DAG authority.
