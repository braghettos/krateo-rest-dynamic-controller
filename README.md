# rest-dynamic-controller

The dynamic REST operator of Krateo PlatformOps: one controller image that manages any
external resource reachable over a REST API, driven entirely by a `RestDefinition` and
the OpenAPI document it points at.

## What is this

`rest-dynamic-controller` (RDC) is instantiated by the
[oasgen-provider](https://github.com/krateo-platformops/oasgen-provider): for every
`RestDefinition`, oasgen-provider generates the CRDs and deploys one RDC Deployment
pinned to that Group/Version/Resource. Each instance reconciles its Custom Resources
against the remote API — observe, create, update, delete — using the verbs, field
mappings, auth and async semantics declared in the `RestDefinition`.
Full picture: [docs/index.md](docs/index.md).

## Install

RDC is never installed on its own: it ships as an image
(`ghcr.io/krateo-platformops/rest-dynamic-controller`) pulled by oasgen-provider, whose
chart pins the RDC tag (`values.rdc.image`). Normally both arrive via the **Krateo
installer**. Standalone (brings RDC with it):

```sh
helm install oasgen-provider-crds oci://ghcr.io/krateo-platformops/charts/oasgen-provider-crds \
  --namespace krateo-system --create-namespace
helm install oasgen-provider oci://ghcr.io/krateo-platformops/charts/oasgen-provider \
  --namespace krateo-system
```

Details and the dev loop: [docs/usage.md](docs/usage.md).

## Configure

See [docs/configuration.md](docs/configuration.md). Most used:

| Setting | Default | Effect |
|---|---|---|
| `REST_CONTROLLER_DEBUG` | `false` | Verbose (debug-level) logs for the whole instance. |
| `krateo.io/connector-verbose: "true"` (CR annotation) | unset | Dumps the HTTP exchanges for one CR only. |
| `REST_CONTROLLER_RESYNC_INTERVAL` | `3m` | Full re-observe interval per CR. |

`REST_CONTROLLER_SERVICEACCOUNT_NAME` / `_NAMESPACE` are **required** — the controller
exits at startup without them (oasgen-provider supplies them via the per-instance
ConfigMap).

## Examples

- [examples/sample-resource](examples/sample-resource) — a `RestDefinition` for a mock
  CRUD API (ConfigMap-hosted OAS) plus a `Sample` CR with bearer-auth configuration.

## Docs

- [docs/index.md](docs/index.md) — the map
- [docs/overview.md](docs/overview.md) — what it does and how it works
- [docs/usage.md](docs/usage.md) — how it is deployed / consumed
- [docs/configuration.md](docs/configuration.md) — the whole config surface
- [docs/api.md](docs/api.md) — the contract it executes (RestDefinition surface, status, events)
- [docs/examples.md](docs/examples.md) — examples index
- [docs/release.md](docs/release.md) — how a release ships
- [docs/log.md](docs/log.md) — curated history

## Develop & release

`go test -tags=unit,integration -p 1 ./...` (the integration tag compiles the
envtest/kind suite; dropping it silently skips those tests). Tag `X.Y.Z` (no `v`)
ships the multi-platform image — runbook: [docs/release.md](docs/release.md).
