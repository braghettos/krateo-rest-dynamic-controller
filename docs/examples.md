---
type: ExampleIndex
title: rest-dynamic-controller examples
description: Index of the runnable examples under examples/.
resource: ghcr.io/krateo-platformops/rest-dynamic-controller
tags: [oasgen, examples]
timestamp: 2026-08-07T00:00:00Z
---

# Examples

- [sample-resource](../examples/sample-resource/README.md) — the full chain on a mock
  CRUD API: a ConfigMap-hosted OpenAPI document, the `RestDefinition` that makes
  oasgen-provider generate the `Sample` CRDs and deploy an RDC instance, and a
  bearer-authenticated `Sample` CR. Mirrors the repo's own integration testdata
  (`testdata/restdefinitions/`, `testdata/rest/`), so the mock API implementation is
  bundled: `go run ./internal/controllers/mockserver`.
