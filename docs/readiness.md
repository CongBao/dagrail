# 1.0 readiness and external validation

DAGrail separates engineering completeness from evidence that the product works in an
independent, long-running environment. `dagrail readiness --source .` aggregates the
published source, compatibility, distribution, documentation, browser-boundary, and
optional local project/installation checks into ReadinessDecision v1alpha1.

The v0.18 decision `ready_for_external_validation` means the repository and release
machinery are complete enough to begin independent adoption. It does not mean 1.0 is
ready, production use is validated, or a release tag has passed its external gates.
The report therefore fixes both `oneDotZeroReady` and `productionValidated` to false.

## Local evaluation

```sh
dagrail readiness --source .
dagrail readiness --source . --project /path/to/a/disposable-or-real-project
dagrail readiness --source . --installation --harness codex,claude-code,copilot-cli
```

Project inspection adds the existing path-redacted security audit and disposable
recovery rehearsal. Installation inspection adds the path-free runtime, embedded
bundle, harness registration, and MCP diagnostic. A requested optional check becomes a
release blocker when it fails; an omitted optional check remains `not_run` and cannot
be mistaken for production evidence.

## Historical compatibility evidence

The closed beta window starts at v0.10. The manifest in
`internal/compatibility/beta-window.json` pins the exact v0.10–v0.17 source commits. A
dedicated CI and tag-release job builds every pinned binary plus the candidate, verifies
every adjacent runtime install/upgrade/rollback/re-forward pair, then asks the candidate
to verify and recover a journal created by v0.10. The manifest is immutable input: a
changed commit is a compatibility-contract change, not a routine fixture refresh.

This proves the tested binary and journal paths under the declared CI environment. It
does not prove application-specific semantic equivalence, every operating system's
installer state, or rollback after a newer binary has committed an unsupported future
journal schema.

## Evidence still required before 1.0

All four items must be recorded from real use rather than marked complete by CI:

1. an independent external adopter completes a governed DAG without repository-owner
   intervention;
2. a long-running live DAG survives orchestrator/session replacement and ordinary graph
   evolution;
3. real Codex, Claude Code, and Copilot host runs provide delivery/completion receipts
   without promoting them to DAG acceptance;
4. an operator performs and documents a backup/restore drill outside the test suite.

After those observations exist, a later release may define a signed or reviewable
adoption-evidence format and select a new readiness schema that can truthfully set
production validation and 1.0 readiness. v0.18 intentionally provides no flag or JSON
field that lets a caller self-assert those outcomes.
