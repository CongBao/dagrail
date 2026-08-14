# Release policy

DAGrail uses semantic versions. Before 1.0, documented beta surfaces are additive and
breaking changes require a new API version, migration notes, and a changelog entry.

## Candidate qualification

From a clean source checkout, run:

```sh
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/dagrail qualify release --source .
```

The qualification report verifies public-file completeness, compatibility-contract
validity, published schema digests, plugin metadata versions, the linked closed bundle,
CI/release gate declarations, and commit-pinned workflow actions. `--project PATH` adds
inspection-only security and recovery evidence from a real DAGrail project.

`structuralCandidate: true` is not production validation. The report deliberately keeps
`productionValidated: false` while independent adoption, long-running live-DAG use,
real host receipt proof, and an operator backup/restore drill remain outstanding.

## Tag workflow

Tags matching `v*` run tests, race detection, vet, bounded fuzz targets, dependency and
license checks, six static builds, reproducibility comparison, deterministic archives,
checksums, SPDX SBOM generation, and GitHub build-provenance attestations. Publication
depends on all qualification, security, and build jobs.

The tag version must exactly match `internal/version/version.go`. Release archives
contain only the executable, LICENSE, and README; the executable carries the verified
public plugin bundle.

## Rollback and revocation

A bad unpublished candidate is replaced by a new commit. Published artifacts are never
silently overwritten. If a release is unsafe, mark it clearly in GitHub, publish a
fixed version, describe affected versions in SECURITY or the advisory, and preserve the
old checksums for audit. Runtime rollback changes the local executable only; it does not
rewrite journal history or silently downgrade host manifests.
