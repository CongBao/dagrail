# Support

DAGrail is an open-source project without a paid support SLA.

- Use GitHub Discussions or an issue for usage questions, reproducible bugs, and feature
  proposals. Search existing reports first and include the DAGrail version, operating
  system, harness version, and the smallest safe reproduction.
- Run `dagrail support preview` before sharing diagnostics. Exported support reports are
  pseudonymous and omit authority payloads, paths, prompts, artifacts, and raw harness
  output, but you remain responsible for inspecting them.
- For recovery problems, preserve journal files, run the read-only commands in
  `docs/recovery.md`, and do not hand-edit hashes.
- Report vulnerabilities privately as described in SECURITY.md; do not open a public
  issue containing exploit details, credentials, secrets, or private project data.

Maintainers triage on a best-effort basis. Acknowledgement or a workaround is not a
promise of a release date. Native harness preview APIs may require manual fallback even
when the portable CLI/MCP control plane remains supported.
