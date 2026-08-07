---
type: API
title: The contract rest-dynamic-controller executes
description: RDC owns no CRDs and serves no HTTP API — its contract is the RestDefinition surface it executes, the status/conditions/events it writes, and the RBAC it self-provisions.
resource: ghcr.io/krateo-platformops/rest-dynamic-controller
tags: [oasgen, restdefinition, api]
timestamp: 2026-08-07T00:00:00Z
---

# API

RDC **owns no CRDs** (the release workflow's CRD-publish job skips it by design) and
serves no HTTP endpoints except an optional metrics server. Its contract has three
sides: what it *reads* (the `RestDefinition` + `<Kind>Configuration`), what it
*writes* on managed CRs (status, conditions, events), and its cluster side effects.

## Consumed: the RestDefinition surface (runtime mirror: `internal/tools/definitiongetter/getter.go`)

The CRD itself is owned and validated by
[oasgen-provider](https://github.com/krateo-platformops/oasgen-provider)
(`restdefinitions.ogen.krateo.io/v1alpha1`); RDC executes this subset per verb
(`spec.resource.verbsDescription[]`):

| Field | Semantics |
|---|---|
| `action` / `method` / `path` | One of `create`, `delete`, `get`, `findby`, `update` mapped onto an operation of the OAS at `spec.oasPath` (`http(s)://`, `configmap://<ns>/<name>/<key>`, local file). |
| `fieldMapping[]` | Unified request/response mapping. Request anchors `inPath`/`inQuery`/`inBody`, response anchor `inResponse`, CR side `inCustomResource` (dot, quoted-bracket, `[i]` index and `[?key=value]` predicate paths). `resolver.secretRef` sources a value from a Secret in the CR's namespace; `valueMapping` is `alias` (bidirectional) or `jq` (response direction only); `defaultIfAbsent` injects a default on response. |
| `requestTransform` / `responseTransform` | Whole-document jq programs, run after the request body is assembled / before the response feeds status and drift. jq programs may be inline or `ref:` module references (`configmap://`, `http(s)://`), materialized at load time. |
| `identifiersMatchPolicy` | `findby` matching: `OR` (default) or `AND`. |
| `pagination` | `findby` only; `continuationToken` (request token in query; response token from a header). |
| `successCodes` / `tolerateCodes` / `notFoundCodes` | Extra success codes; codes treated as successful-empty; codes remapped to not-found. |
| `notFoundBody` | jq boolean predicate over the raw observe body — body-signalled absence (empty wrapper lists, tombstones). Skipped while `Pending`. |
| `headers` / `queries` | Static per-verb headers / query parameters. |
| `async` | Long-running ops: `mode: blocking` (poll inline, default) or `requeue` (record handle, poll once per Observe); `operationRef` (body path, header, or jq), `poll` (path, `handleParam`, `statusPath`, success/failure values, interval/attempts/timeout), `postGet`. |

Resource-level: `identifiers`, `additionalStatusFields`, `compareScope`
(`fullSpec` default | `identifiersAndStatus` | `updatable` — see
`internal/controllers/helpers.go`), `configurationFields`, and the delegation refs
`observeApiRef` / `createApiRef` / `updateApiRef` / `deleteApiRef` (snowplow
`RESTAction` name/namespace + `extras`; observe also takes the `notFoundExpr` /
`upToDateExpr` jq predicates).

## Consumed: `<Kind>Configuration` (always `v1alpha1`)

Referenced by the managed CR's `spec.configurationRef`. `spec.configuration` carries
per-instance values for `configurationFields`; `spec.authentication` selects exactly
one of:

- `basic` — `usernameRef`/`passwordRef` Secret references;
- `bearer` — `tokenRef`;
- `apiKey` — `tokenRef` + `header` (required) + optional `valuePrefix`, sent verbatim.

All credentials are read from Secrets and whitespace-trimmed.

## Written: status, conditions, events

- **status** — the declared `identifiers` and `additionalStatusFields`, projected from
  the (normalized) API response; cleared before repopulation on create/update so stale
  identifiers can never deadlock reconciliation. Model B async operations also park
  their operation handle in status until terminal.
- **conditions** — a single `Ready` condition with reasons `Available`, `Creating`
  (also used after updates), `Deleting`, `Unavailable` (drift — the reason itself is
  rewritten to a `Resource is not up-to-date due to …` string naming the differing
  field and both values) and the RDC-specific `Pending` (async operation in flight;
  `internal/controllers/condition`).
- **events** — `ResourceCreated`, `ResourceUpdated`, `ResourceDeleted` (Normal on
  success, Warning on failure) emitted on the managed CR.

## Cluster side effects

Per CR instance that uses `secretRef` resolvers, RDC self-provisions a
namespace-scoped Role + RoleBinding named `<plural>-<version>-<fnv32a(ns/name)>-secrets`
granting itself `get/list/watch` on exactly the referenced Secret names, and deletes
the pair on CR deletion (`internal/tools/secretrbac`).

## Exposed: metrics

With `-metrics-server-port` set, a Prometheus endpoint serves the
`unstructured-runtime` reconcile metrics; with `OTEL_ENABLED`, the same telemetry is
exported over OTLP (`provider_runtime.reconcile.*` / `controller_reconcile_*`),
resource-tagged with `krateo.io/rest-gvr=<group>/<version>/<resource>`.
