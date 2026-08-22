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

The tag workflow also rebuilds the pinned v0.10.0–v0.26.6 beta binaries and the tag
candidate. Publication depends on adjacent runtime upgrade/rollback/re-forward tests,
candidate recovery of a v0.10-created journal, and a native Windows full-test job at
the exact tag SHA. `dagrail readiness --source .` must
still report `ready_for_external_validation`; it is not accepted as production adoption
evidence and does not authorize a 1.0 tag by itself.

`structuralCandidate: true` is not production validation. The report deliberately keeps
`productionValidated: false` while independent adoption, long-running live-DAG use,
real host receipt proof, and an operator backup/restore drill remain outstanding.

## Artifact contract

The publish job first creates sorted SHA-256 checksums over exactly six binary archives
and six SPDX JSON inventories. It then runs:

```sh
dagrail release manifest --directory dist --version VERSION --tag TAG \
  --commit FULL_SHA --source-date-epoch COMMIT_EPOCH
dagrail release verify --directory dist
```

`release-manifest.json` records every payload digest and size plus the checksum-file
digest. Verification rejects missing, extra, duplicate, symlinked, oversized, mutated,
or unsorted files; unsafe or non-deterministic archives; and incomplete SPDX documents.
The manifest proves internal consistency, not publisher identity. Consumers should also
verify the GitHub provenance attestation through their separately trusted GitHub policy.

## Tag workflow

Tags matching `v*` run tests, race detection, vet, bounded fuzz targets, dependency and
license checks, six static builds, reproducibility comparison, deterministic archives,
checksums, a closed release manifest, SPDX SBOM generation, and GitHub build-provenance
attestations. Publication depends on qualification, security, historical compatibility,
native Windows full tests, and all build jobs at the exact tag SHA.

The tag version must exactly match `internal/version/version.go`. Release archives
contain only the executable, LICENSE, and README; the executable carries the verified
public plugin bundle.

## Rollback and revocation

A bad unpublished candidate is replaced by a new commit. Published artifacts are never
silently overwritten. If a release is unsafe, mark it clearly in GitHub, publish a
fixed version, describe affected versions in SECURITY or the advisory, and preserve the
old checksums for audit. Runtime rollback changes the local executable only; it does not
rewrite journal history or silently downgrade host manifests.
