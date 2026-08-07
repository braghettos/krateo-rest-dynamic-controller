---
type: Configuration
title: rest-dynamic-controller configuration
description: Every CLI flag and environment variable the controller reads (verified against main.go), the per-CR annotation, and how oasgen-provider supplies them.
resource: ghcr.io/krateo-platformops/rest-dynamic-controller
tags: [oasgen, configuration]
timestamp: 2026-08-07T00:00:00Z
---

# Configuration

Every setting is a CLI flag with an environment-variable fallback (flag wins). The
full surface, from `main.go`:

## Identity of the managed resource

| Flag | Env | Default | Description |
|---|---|---|---|
| `-group` | `REST_CONTROLLER_GROUP` | — | API group of the managed resource. |
| `-version` | `REST_CONTROLLER_VERSION` | — | API version of the managed resource. |
| `-resource` | `REST_CONTROLLER_RESOURCE` | — | Plural resource name. |
| `-namespace` | `REST_CONTROLLER_NAMESPACE` | `""` | Namespace to watch; empty = all namespaces. |

## Controller identity (required)

| Flag | Env | Default | Description |
|---|---|---|---|
| `-serviceaccount-name` | `REST_CONTROLLER_SERVICEACCOUNT_NAME` | — | **Required.** This controller's own ServiceAccount name — the RoleBinding subject for self-provisioned secretRef RBAC. Startup fails loud without it. |
| `-serviceaccount-namespace` | `REST_CONTROLLER_SERVICEACCOUNT_NAMESPACE` | — | **Required.** Namespace of that ServiceAccount. |

## Reconcile tuning

| Flag | Env | Default | Description |
|---|---|---|---|
| `-workers` | `REST_CONTROLLER_WORKERS` | `5` | Number of reconcile workers. |
| `-resync-interval` | `REST_CONTROLLER_RESYNC_INTERVAL` | `3m` | Interval between full resyncs. |
| `-max-error-retry-interval` | `REST_CONTROLLER_MAX_ERROR_RETRY_INTERVAL` | `90s` | Max backoff between error retries (keep below half the resync interval). |
| `-min-error-retry-interval` | `REST_CONTROLLER_MIN_ERROR_RETRY_INTERVAL` | `1s` | Min backoff between error retries. |
| `-max-error-retries` | `REST_CONTROLLER_MAX_ERROR_RETRIES` | `5` | Retries before a resource is dropped from the queue. |

## Debugging & metrics

| Flag | Env | Default | Description |
|---|---|---|---|
| `-debug` | `REST_CONTROLLER_DEBUG` | `false` | Debug-level logs for the whole instance. |
| `-pretty-json-debug` | `REST_CONTROLLER_PRETTY_JSON_DEBUG` | `false` | Pretty-print JSON bodies in HTTP debug output. |
| `-metrics-server-port` | `REST_CONTROLLER_METRICS_SERVER_PORT` | `0` | Bind port of the metrics server; `0`/unset disables it. |
| `-kubeconfig` | `KUBECONFIG` | `""` | Kubeconfig path; empty = in-cluster config. |

## RESTAction delegation (snowplow / authn)

| Flag | Env | Default | Description |
|---|---|---|---|
| `-snowplow-url` | `URL_SNOWPLOW` | `""` | Snowplow base URL for resolving `*ApiRef` RESTActions; empty disables delegation (a CR that declares one then errors at Observe). |
| `-authn-url` | `URL_AUTHN` | `""` | authn base URL for exchanging the projected SA token for a service JWT; empty = call snowplow unauthenticated. |
| `-serviceaccount-token-path` | `REST_CONTROLLER_SERVICEACCOUNT_TOKEN_PATH` | `/var/run/secrets/krateo.io/serviceaccount/token` | Path of the projected (authn-audience) ServiceAccount token. |

## OpenTelemetry (all gated off by default)

| Flag | Env | Default | Description |
|---|---|---|---|
| `-otel-enabled` | `OTEL_ENABLED` | `false` | OTLP metrics export (reconcile telemetry). |
| `-otel-tracing-enabled` | `OTEL_TRACING_ENABLED` | `false` | OTLP trace export; the W3C propagator is installed even when off, so inbound `traceparent` is honored. |
| `-otel-service-name` | `OTEL_SERVICE_NAME` | `rest-dynamic-controller` | `service.name` on exported telemetry. |
| `-otel-export-interval` | `OTEL_EXPORT_INTERVAL` | `30s` | Metrics export interval. |
| `-deployment-name` | `DEPLOYMENT_NAME` | `""` | Stable resource identification in metrics. |
| — | `SERVICE_VERSION` | `""` | Image version stamped as `service.version`. |

The managed GVR is appended to `OTEL_RESOURCE_ATTRIBUTES` as `krateo.io/rest-gvr`, so
telemetry is attributable per dynamic CR type.

## Per-CR annotation

`krateo.io/connector-verbose: "true"` on a managed CR dumps that resource's HTTP
exchanges (request/response) to the logs, independent of `-debug`.

## How the values arrive in a real deploy

oasgen-provider renders each instance's Deployment with
`-group/-version/-resource` as args and `envFrom` a per-instance ConfigMap
(`<name>-configmap`) that carries `REST_CONTROLLER_SERVICEACCOUNT_NAME`,
`REST_CONTROLLER_SERVICEACCOUNT_NAMESPACE`, `HOME=/tmp`, plus every key of the
oasgen-provider chart's `rdc.env` map — that map is the operator-facing knob for
everything else in the tables above (e.g. `URL_SNOWPLOW`, `OTEL_ENABLED`).

Everything about *what* the instance manages — verbs, field mappings, auth type and
Secret references, async, compareScope — is not controller configuration at all: it
lives on the `RestDefinition` and the `<Kind>Configuration` CR ([api](./api.md)).
