---
type: Component
title: rest-dynamic-controller — index
description: The map of the rest-dynamic-controller doc bundle — the dynamic REST operator deployed per RestDefinition by oasgen-provider.
resource: ghcr.io/krateo-platformops/rest-dynamic-controller
tags: [oasgen, dynamic-controller, rest]
timestamp: 2026-08-07T00:00:00Z
---

# rest-dynamic-controller

`rest-dynamic-controller` (RDC) is the **dynamic REST operator** of Krateo
PlatformOps: a single controller binary that can manage any external resource exposed
over a REST API. It has no compiled-in types — each instance is started by
[oasgen-provider](https://github.com/krateo-platformops/oasgen-provider) for one
Group/Version/Resource (one Deployment per `RestDefinition`) and executes the verbs,
field mappings, authentication and async semantics that the `RestDefinition` and its
OpenAPI document declare. This repo carries only the controller code and image; the
CRDs it serves are generated (and its Deployments are owned) by oasgen-provider.

## The bundle (start here)

- [overview](./overview.md) — what it does and how it works: the reconcile loop,
  RestDefinition lookup, the OAS-driven client, auth, drift, async, RESTAction
  delegation, secretRef RBAC.
- [usage](./usage.md) — how RDC reaches a cluster (installer → oasgen-provider →
  per-RestDefinition Deployment) and the local dev loop.
- [configuration](./configuration.md) — every flag and environment variable, verified
  against `main.go`, and how oasgen-provider supplies them.
- [api](./api.md) — the contract: the RestDefinition surface RDC executes, the status
  it writes, conditions, events, RBAC side effects, metrics.
- [examples](./examples.md) — the runnable example under `examples/`.
- [release](./release.md) — how a release ships (tag → tested multi-platform image).
- [log](./log.md) — curated history of the 0.x line.
- [llms.txt](./llms.txt) — the version-pinned agent index of this bundle.

## Related repos

- [oasgen-provider](https://github.com/krateo-platformops/oasgen-provider) — owns the
  `RestDefinition` CRD, generates the managed CRDs, deploys RDC instances, and pins
  the RDC image tag in its chart (`values.rdc.image`). Its monorepo also mirrors this
  code tree under `go/rest-dynamic-controller/` for joint integration testing.
- [snowplow](https://github.com/krateo-platformops/snowplow) — resolves the
  `RESTAction`s that `observeApiRef`/`createApiRef`/`updateApiRef`/`deleteApiRef`
  delegate to.

## Not part of the bundle

`_diagrams/` holds legacy draw.io/SimpleMind sources named after
composition-dynamic-controller, predating this bundle; the diagrams in
[overview](./overview.md) supersede them.
