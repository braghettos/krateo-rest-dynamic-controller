---
type: Runbook
title: How a rest-dynamic-controller release ships
description: Tag X.Y.Z (no v prefix) gates the test suite, builds the multi-platform image to GHCR, then the oasgen-provider chart pin is bumped by hand.
resource: ghcr.io/krateo-platformops/rest-dynamic-controller
tags: [release, ci]
timestamp: 2026-08-07T00:00:00Z
---

# Release runbook

## Ship

1. Merge to `main` via PR. PR CI (`.github/workflows/release-pullrequest.yaml`) runs
   the shared component checks: a validate-only multi-platform image build
   (`component-image-build`, `push: false`), Go checks (`component-go-checks` with
   `-race -tags=unit,integration -p 1`), and the docs lint (`lint-docs`).
2. Tag plain semver, **no `v` prefix** (the trigger is `[0-9]+.[0-9]+.[0-9]+`):

   ```sh
   git tag X.Y.Z && git push origin X.Y.Z
   ```

3. `release-tag.yaml` then runs:
   - **test** — the canonical suite (`test.yaml`, `-tags=unit,integration`), added
     as a release gate after 0.16.0/0.16.1 shipped untested (#38). Since the #50
     migration to the shared build, `build` no longer declares `needs: [test]`: the
     two jobs start in parallel, so the suite runs on every tag but does not
     currently block the image push.
   - **build** — the shared reusable multi-platform build
     (`krateo-platformops/.github` `component-image-build.yaml`): one
     linux/amd64 + linux/arm64 image at
     `ghcr.io/krateo-platformops/rest-dynamic-controller:X.Y.Z`.
   - **crds** — runs after build and skips cleanly: this repo has no `make generate`
     target because RDC owns no CRDs.

## Roll out

The image is **not** picked up anywhere automatically. The oasgen-provider chart pins
the RDC tag at `values.rdc.image.tag` (hand-maintained — the chart's release
`APP_VERSION` substitution touches `Chart.yaml` only, never `values.yaml`), and
several RestDefinition features are joint oasgen/RDC contracts where an old RDC fails
*silently*. So after a release:

1. Open a PR on `krateo-platformops/oasgen-provider` bumping `helm/oasgen-provider/values.yaml`
   `rdc.image.tag` to `X.Y.Z` (keep the pin-rationale comment honest).
2. Ship an oasgen-provider chart release; the installer pin then picks it up.

Running RDC instances are re-rolled by oasgen-provider when their Deployment template
changes; a tag-only bump re-rolls on the next RestDefinition reconcile of each
instance.

## Versioning

Plain semver `0.x`: breaking runtime-contract changes bump minor with a `feat!` commit
(e.g. 0.15.0 removed the `apiLookup` resolver). Release notes live in GitHub
Releases; the curated history is [log](./log.md).
