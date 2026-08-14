# Observe-only migration

`dagrail observe` assesses an existing DAG and creates isolated DAGrail state without
writing into the source project. It is intended for qualification and migration
planning, not for controlling the source project's live work.

```bash
dagrail observe assess \
  --source-root /path/to/source \
  --graph /path/to/converted-graph.yaml \
  --authority governance/dag.json \
  --authority governance/registry.json

dagrail observe create-shadow \
  --source-root /path/to/source \
  --graph /path/to/converted-graph.yaml \
  --authority governance/dag.json \
  --authority governance/registry.json \
  --shadow-root /path/outside/source/dagrail-shadow

dagrail observe verify-shadow --shadow-root /path/outside/source/dagrail-shadow
```

The caller chooses an already valid DAGrail Graph Definition and a bounded set of
source authority files. Assessment records only portable relative paths, sizes, and
SHA-256 digests. The immutable shadow journal binds that snapshot to the imported
Graph Revision. Absolute local locators are stored separately with owner-only
permissions in the shadow's private data directory; they do not enter the journal.

Safety properties:

- source authority is opened read-only and never copied, renamed, or rewritten;
- the shadow must be absent or empty and resolve outside the source root;
- absolute, escaping, duplicate, non-regular, and symlink-escaping authority paths are
  rejected;
- a graph is limited to 8 MiB; each authority file to 64 MiB; all authority files to
  256 MiB and 256 entries;
- verification checks journal integrity, provenance, Graph Revision, and current source
  digests, reporting drift without changing either project.

Observe does not infer semantic equivalence, convert arbitrary source formats, start a
Node, bind a Role, or migrate lifecycle state. If creation stops after the shadow
journal commits but before its private locator is written, discard that incomplete
shadow directory and create a fresh one; the source remains untouched.
