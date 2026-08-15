# ADR 0017: Validation subjects do not define kernel contracts

- Status: Accepted
- Date: 2026-08-15

## Context

A complex existing project is valuable for proving topology, recovery, and bounded-context behavior. Reusing its names or file conventions inside DAGrail would make one validation fixture an accidental product authority and hide whether the controller is genuinely reusable.

## Decision

DAGrail treats every adopted repository as an External validation subject. Repository-specific discovery, conversion, and result comparison live in a disposable Qualification driver outside the product repository and use only public CLI, MCP, SDK, schema, and observe-only contracts. DAGrail source, schemas, bundled providers, examples, and skills may describe generic governance concepts but cannot depend on a validation subject's paths, identifiers, requirement system, or lifecycle registry.

For v0.21 lifecycle migration this boundary is explicit: the kernel remains
repository-neutral, accepts only a generic native-event manifest, and leaves each
external converter, authority-discovery rule, source vocabulary, and cutover policy
outside DAGrail. Shadow and migration results are evidence about the generic product;
they do not create a built-in integration or production-readiness claim.

## Consequences

Real projects can expose missing generic capabilities without silently becoming dependencies. A shadow test may fail and motivate a product change, but that change must first be stated as a reusable domain contract with an independent fixture. Qualification drivers are not shipped as first-party adapters unless a later product decision explicitly promotes a broadly useful integration.
