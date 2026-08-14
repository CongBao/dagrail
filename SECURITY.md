# Security policy

Please report vulnerabilities privately through GitHub Security Advisories for this repository. Do not include real secrets, PII, private journal segments, or artifact URLs in a public issue.

## Alpha security boundary

DAGrail v0.1 is a cooperative single-user local tool. Role capabilities prevent accidental collisions; they are not an authorization boundary against another local process running as the same OS user. Journal hashes detect mutation but do not prove an actor identity. External URIs and receipts may reveal metadata and should use least-privilege stores.

The controller rejects secret-like fields in effect requests and never intentionally stores prompts or chat transcripts. Operators remain responsible for keeping secrets outside Graph Definitions, checkpoints, journal exports, and evidence metadata.
