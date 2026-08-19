# ADR 0022: owner-local daemon and verified snapshots

- Status: Accepted
- Date: 2026-08-19
- Supersedes: ADR 0002's no-daemon process topology

## Context

Starting one CLI or MCP process per request repeats project discovery, journal replay,
reduction, and projection synchronization. A large project can therefore exceed a
harness MCP startup timeout before protocol initialization, while concurrent UI and
agent queries independently reconstruct the same state. External Effects also need an
owner process after an already-authorized dispatch crosses the client connection.

The controller must reduce this operational cost without becoming a scheduler, a new
authority, or a store of conversation state.

## Decision

- DAGrail uses one on-demand daemon per OS user. It manages multiple projects, starts
  on the first project command, and has no login-start or autonomous scheduling loop.
- macOS/Linux use an owner-only Unix socket; Windows uses a named pipe restricted to the
  current SID. A lifetime instance lock and a serialized startup lock prevent split
  brain, including stale-socket and concurrent-start recovery.
- Each Project has an actor boundary: mutations serialize and reads share one immutable
  verified snapshot. The daemon stores no prompt, transcript, or semantic plan.
- The daemon status binds a path-hiding digest of its authority-data namespace. A
  client whose `DAGRAIL_HOME` (or platform default data home) differs drains and
  restarts the process before opening a Project; a singleton must never reinterpret a
  locator against another data store merely because its version matches.
- The journal remains canonical authority. SQLite, sealed checkpoints, in-memory state,
  logs, sockets, and process IDs are disposable. A checkpoint HMAC binds its Project,
  head, GraphRevision, provider set, state digest, and segment-file identities. Any
  mismatch falls back to authority replay. Full verify, recovery, and security audit
  always validate the journal rather than trusting the shortcut.
- A client-selected `effect.prepare` may transfer that already-authorized saga to the
  daemon outbox. The daemon may finish dispatch and persist an observation; it may not
  choose another Effect, allocate work, renew a Role, or advance any unrelated Node.
  An ambiguous crash prefix remains `unknown` and requires reconcile.
- Stdio MCP initializes and lists tools without opening a Project or starting the
  daemon. The first tool call selects a root and uses the local controller. A missing
  Project is a structured tool error, not MCP process termination.
- Explicit `--offline` recovery is permitted only while the daemon is stopped and still
  obtains the existing Project lock.

## Consequences

Normal CLI, MCP, and Explorer queries reuse one hot verified state and avoid redundant
full replay. The daemon is operationally stateful but semantically passive. Restart or
cache deletion may make the next query slower, never change lifecycle truth. Runtime
upgrade must drain in-flight authorized Effects; failure to drain is a typed blocker,
not permission to kill and retry an external side effect.
