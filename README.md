<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/logo-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="./assets/logo.svg">
    <img alt="DAGrail logo" src="./assets/logo.svg" width="120">
  </picture>
</p>

<h1 align="center">DAGrail</h1>

<p align="center"><strong>LLM-led DAG governance with durable, machine-checked state.</strong></p>

[![CI](https://github.com/CongBao/dagrail/actions/workflows/ci.yml/badge.svg)](https://github.com/CongBao/dagrail/actions/workflows/ci.yml)
[![Apache-2.0 License](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](go.mod)

DAGrail is a lightweight control plane for long-running, multi-agent development DAGs.
The LLM or human still decides what should happen; DAGrail keeps the exact runtime state
outside chat so a new session can safely continue the work.

It solves the control-plane problems that become fragile in prompts and hand-edited
JSON: ready-node calculation, Role ownership, session replacement, checkpoints,
idempotent actions, dynamic graph changes, external side effects, incidents, resource
closure, and recovery.

## How it works

- A typed graph declares Nodes, Roles, positive edge predicates, resources, and outcomes.
- An immutable hash-chained journal is runtime authority; SQLite is only a rebuildable
  local projection.
- CLI and six MCP tools return bounded context and signed allowed actions, so an agent
  does not have to remember hashes or construct lifecycle events.
- Stable Roles and Attempts survive thread or session replacement through leases and
  checkpoints.
- External writes use explicit `prepared`, `unknown`, `confirmed`, and `reconcile`
  states instead of pretending that Git, CI, or a harness is part of one DB transaction.

DAGrail does not schedule work autonomously, manage requirements, run containers, or
replace your agent harness. It supplies the durable guardrails while the LLM remains in
control.

## Install

macOS or Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/CongBao/dagrail/main/scripts/install.sh | sh
```

Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/CongBao/dagrail/main/scripts/install.ps1 | iex
```

The installer downloads the latest release, verifies its checksum, installs the shared
runtime, and configures all supported harnesses. Restart open agent applications after
installation, then verify:

```bash
dagrail plugin status
dagrail doctor install
```

## Supported harnesses

| Harness | Plugin and MCP | Native execution when available | Fallback |
| --- | --- | --- | --- |
| OpenAI Codex | Yes | Thread start/resume/observation | Typed CLI envelope |
| Claude Code | Yes | Headless start/resume | Typed CLI envelope |
| GitHub Copilot CLI | Yes | ACP dispatch | Typed CLI envelope |
| Other agents | CLI or stdio MCP | Compile-in adapter | Manual typed workflow |

Native features are capability-probed. A transport response, new session, visible
delivery, acceptance, and completion remain separate receipt states.

## Quick start

```bash
dagrail init --root . --name example
cat > graph.json <<'JSON'
{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"example"},"spec":{"roles":[{"id":"developer","capabilities":["node.run"]}],"nodes":[{"id":"implement","kind":"task","role":"developer","title":"Implement the change","outcomes":[{"id":"done","class":"success"}]}],"edges":[]}}
JSON
dagrail graph validate --file graph.json
dagrail graph import --root . --file graph.json \
  --idempotency-key import-v1

dagrail frontier --root .
dagrail role bind --root . --role developer --harness codex \
  --session quickstart --ttl 15m --idempotency-key bind-developer
dagrail context --root . --view worker --role developer --node implement
dagrail pre-wait --root .
dagrail ui --root .
```

For a mutation, first obtain a current signed action. Apply the returned `node.start`
ref with an empty object, then list again to obtain the checkpoint ref:

```bash
dagrail action list --root . --role developer --node implement
dagrail action apply --root . --ref NODE_START_ACTION_REF \
  --input '{}' --idempotency-key start-implement-1
dagrail action list --root . --role developer --node implement
dagrail action apply --root . --ref CHECKPOINT_ACTION_REF \
  --input '{"summary":"durable checkpoint"}' --idempotency-key checkpoint-1
```

## Useful commands

| Command | Purpose |
| --- | --- |
| `dagrail context` | Get a bounded orchestrator, worker, or reviewer work package |
| `dagrail frontier` | Show ready and blocked Nodes |
| `dagrail inspect` | Resolve one opaque runtime or evidence reference |
| `dagrail graph preview-change` | Validate a dynamic graph change and its impact |
| `dagrail reconcile` | Resolve an ambiguous external Effect |
| `dagrail artifact inspect-scope` | Separate candidate, target, and prospective Git changes |
| `dagrail artifact verify-git-closure` | Verify retained Git commits, trees, tags, and refs |
| `dagrail journal verify` | Verify the immutable journal chain |
| `dagrail projection rebuild` | Rebuild disposable SQLite state from the journal |
| `dagrail ui` | Open the loopback-only, read-only DAG Explorer |

Run `dagrail commands` for the complete machine-readable command catalog.

## Project boundary

Only `.dagrail/project.yaml` belongs in a project repository. Runtime journals,
projections, leases, and action secrets live in DAGrail's per-user data directory.
Prompts, chat transcripts, secrets, PII, and large artifact bodies never belong in the
journal; store only bounded metadata, digests, and external references.

DAGrail is local-first, single-user, pre-1.0 software. The journal is tamper-evident,
not an identity signature or hostile-user security boundary.

## Learn more

- [Tutorial](docs/tutorial.md)
- [CLI and MCP API](docs/api.md)
- [Recovery](docs/recovery.md)
- [Compatibility contract](COMPATIBILITY.md)
- [Security model](SECURITY.md)
- [Changelog](CHANGELOG.md)
- [Contributing](CONTRIBUTING.md)

Apache-2.0 licensed. See [LICENSE](LICENSE).
