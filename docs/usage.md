---
type: Usage
title: How rest-dynamic-controller is deployed and consumed
description: RDC ships as an image deployed per RestDefinition by oasgen-provider — the installer path, the direct oasgen-provider install, and the local dev loop.
resource: ghcr.io/krateo-platformops/rest-dynamic-controller
tags: [oasgen, install]
timestamp: 2026-08-07T00:00:00Z
---

# Usage

RDC has no Helm chart and is never installed by hand. It ships as an image —
`ghcr.io/krateo-platformops/rest-dynamic-controller:<tag>` — that
**oasgen-provider** deploys: for each `RestDefinition`, oasgen-provider renders a
Deployment + per-instance ConfigMap + RBAC from its chart assets
(`helm/oasgen-provider/assets/rdc/` in the oasgen-provider repo) and starts one RDC
instance with `-group`/`-version`/`-resource` pinned to the generated GVR.

## Via the Krateo installer (canonical)

A stock installer deploy includes oasgen-provider; nothing else is needed. Applying a
`RestDefinition` makes oasgen-provider generate the CRDs and spin up the matching RDC
instance automatically.

## Direct oasgen-provider install (standalone)

```sh
helm install oasgen-provider-crds oci://ghcr.io/krateo-platformops/charts/oasgen-provider-crds \
  --namespace krateo-system --create-namespace
helm install oasgen-provider oci://ghcr.io/krateo-platformops/charts/oasgen-provider \
  --namespace krateo-system
```

The RDC image tag is pinned in the oasgen-provider chart at `values.rdc.image.tag` —
**hand-maintained**, because several RestDefinition features are joint contracts
between oasgen-provider (validates the field) and RDC (executes it); an older RDC may
ignore such a field silently. Override it only knowingly:

```sh
helm upgrade oasgen-provider oci://ghcr.io/krateo-platformops/charts/oasgen-provider \
  --namespace krateo-system --reuse-values --set rdc.image.tag=<X.Y.Z>
```

Extra environment for every spawned RDC instance goes through the chart's `rdc.env`
map, which oasgen-provider copies into each per-instance ConfigMap (see
[configuration](./configuration.md)).

## Consuming a deployed instance

Users interact only with the generated CRs (and their `<Kind>Configuration` sibling) —
see [examples](./examples.md). Per-CR HTTP debugging is enabled with the
`krateo.io/connector-verbose: "true"` annotation.

## Local dev loop

The controller runs fine outside the cluster against a kubeconfig:

```sh
export REST_CONTROLLER_GROUP=sample.krateo.io \
       REST_CONTROLLER_VERSION=v1alpha1 \
       REST_CONTROLLER_RESOURCE=samples \
       REST_CONTROLLER_SERVICEACCOUNT_NAME=default \
       REST_CONTROLLER_SERVICEACCOUNT_NAMESPACE=default
go run . -kubeconfig "$HOME/.kube/config" -debug
```

The SA name/namespace pair is hard-required (main.go exits without it). A mock CRUD
API implementing `testdata/restdefinitions/cm/oas.yaml` is bundled for this loop:

```sh
go run ./internal/controllers/mockserver   # listens on :30007
```

Tests: `go test -race -tags=unit,integration -p 1 ./...` — the `integration` tag is
what compiles the envtest/kind suite in `internal/controllers`; dropping the tag
silently skips it (this is why CI always passes it, `.github/workflows/test.yaml`).
