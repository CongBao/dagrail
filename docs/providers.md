# Provider development

DAGrail v0.4 providers are compiled into a custom executable. There is no dynamic
`.so`, WASM, Rego, or CEL loader. This keeps one portable binary and avoids granting an
extension direct access to controller storage.

## Provider kinds

The public Go package `github.com/CongBao/dagrail/sdk` defines seven interfaces:

- node kinds define input schemas and closed outcomes;
- predicates return a boolean from a typed request;
- policies return a closed outcome plus bounded facts;
- graph importers translate external planning formats into a Graph Definition;
- projections render verified, upcast event copies into a read model;
- effects and harnesses run through their dedicated saga and receipt paths.

Register implementations with `providers.Register()` in a custom distribution. A
single implementation may expose multiple kinds under one stable provider ID.

## Invocation contract

Callable providers implement `sdk.InputSchemaProvider`. The schema is self-contained
JSON Schema 2020-12: remote `$ref` loading is rejected. DAGrail validates input before
entering provider code, applies a deadline, recovers panic, and limits output to 64 KiB.
Output must be authority-safe JSON and cannot contain secret-like fields.

Use `experimental` while changing a contract. A stable provider binds the exact schema:

```go
schema := json.RawMessage(`{"type":"object","additionalProperties":false}`)
hash, err := sdk.InputSchemaHash(schema)
if err != nil { panic(err) }
metadata := sdk.Metadata{
    ID: "example.policy",
    Version: "1.0.0",
    SchemaHash: hash,
    Stability: sdk.StabilityStable,
}
```

Run conformance checks in both CI and the built distribution:

```sh
dagrail provider list --root .
dagrail provider check --root .
dagrail provider invoke --root . --kind policy --id example.policy \
  --input '{"policyId":"release","input":{}}'
```

Generic invocation is diagnostic: it does not mutate the graph or journal. Initial
graph import has an explicit authoritative application path:

```sh
dagrail graph import --root . --provider example.importer \
  --input '{"source":"plan.json"}' --idempotency-key import-plan-v1
```

The journal stores the provider metadata and a digest of the importer input, never the
source document unless it is part of the validated Graph Definition itself.
