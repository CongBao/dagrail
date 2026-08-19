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

DAGrail is a lightweight control plane for long-running, multi-agent DAGs. The LLM or
human decides what to do; DAGrail keeps identity, state, evidence, and side effects out
of chat so any replacement session can safely continue.

It replaces fragile prompt memory and hand-edited runtime JSON with typed actions,
durable checkpoints, deterministic readiness, and recoverable external effects.

## How it works

- Typed Nodes, Roles, predicates, resources, outcomes, and nested groups describe the
  graph without prescribing which agent should make semantic decisions.
- An immutable hash-chained journal is authority; SQLite and the read-only DAG UI are
  rebuildable projections.
- An on-demand, owner-local daemon shares verified snapshots across the CLI, six MCP
  tools, and Explorer. Agents never construct lifecycle events or retain the full graph
  in context; the daemon never schedules work on its own.
- Leases, checkpoints, idempotency, incidents, and explicit Effect reconciliation make
  session replacement and ambiguous external writes recoverable.

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

The installer verifies the release, installs one shared runtime, and configures all
supported harnesses. Restart open agent applications, then verify:

```bash
dagrail plugin status
dagrail mcp probe
dagrail doctor install
```

## Supported harnesses

| Harness | Integration | Safe fallback |
| --- | --- | --- |
| OpenAI Codex | Plugin, skill, hooks, MCP | Typed CLI |
| Claude Code | Plugin, skill, hooks, MCP | Typed CLI |
| GitHub Copilot CLI | Plugin, skill, hooks, MCP | Typed CLI |
| Other agents | stdio MCP or CLI | Manual typed workflow |

Native dispatch/resume is capability-probed; transport, visible delivery, acceptance,
and completion remain separate receipt states.

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

Use `dagrail action list` to obtain current signed actions. Apply one by ref, or let the
daemon atomically resolve a unique current `--kind --role --node` selector. Every result
includes a bounded continuation stating who owns the next step and whether it is safe
to wait. Use `dagrail reconcile` before retrying an ambiguous Effect. Dynamic graph
changes use `graph preview-change` followed by `graph apply-change`. Run
`dagrail commands` for the machine-readable command catalog.

## Project boundary

Only `.dagrail/project.yaml` belongs in a project repository. Runtime data stays in the
per-user DAGrail directory. Prompts, transcripts, secrets, PII, and large artifacts do
not enter the journal; store only bounded metadata, digests, and external references.

DAGrail is local-first, single-user, pre-1.0 software. The journal is tamper-evident,
not an identity signature or hostile-user security boundary.

## Learn more

- [Tutorial](docs/tutorial.md)
- [CLI and MCP API](docs/api.md) · [Recovery](docs/recovery.md) ·
  [Compatibility](COMPATIBILITY.md) · [Security](SECURITY.md)
- [Changelog](CHANGELOG.md) · [Contributing](CONTRIBUTING.md)

Apache-2.0 licensed. See [LICENSE](LICENSE) and
[third-party notices](THIRD_PARTY_NOTICES.md).
