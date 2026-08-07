---
type: Log
title: rest-dynamic-controller — curated history
description: Notable changes and decisions of the 0.x line, mapped to release tags; release notes stay in GitHub Releases.
resource: ghcr.io/krateo-platformops/rest-dynamic-controller
tags: [history]
timestamp: 2026-08-07T00:00:00Z
---

# Curated history

- **post-0.20.0 (main, 2026-08)** — Go module identity migrated to
  `github.com/krateo-platformops/*` (full independence from the dead org; the upstream
  libopenapi fork is preserved) (#49); CI moved to the shared reusable workflows
  (security/otel-audit #48, multi-platform image build #50).
- **0.20.0 (2026-08-03)** — `compareScope: updatable`: drift restricted to the fields
  the update verb's request body can express, ending unfixable update loops on
  server-assigned/create-only fields (#47). Secret-sourced credentials are
  whitespace-trimmed — a trailing newline in a Secret used to surface as an opaque
  `net/http: invalid header field value` (#46).
- **0.19.0 (2026-08-02)** — apiKey-in-header authentication (OAS `apiKey` schemes):
  header name + optional `valuePrefix` read from the `<Kind>Configuration` (#44).
  Joint contract with oasgen-provider 0.19.0.
- **0.18.0 (2026-08-01)** — `async.poll.handleParam` names the path parameter that
  receives the operation handle (previously hardwired to `operationId`) (#43);
  RESTAction delegation forwards the CR spec in every direction, not just
  create/update (#42).
- **0.17.0 (2026-07-30)** — `requestTransform` actually executes on the outgoing body
  (it was previously materialized but never run) (#40); the real `valueMapping`
  support matrix documented (`jq` is response-direction only) (#39); CI gained the
  push-to-main + release test gate (#38).
- **0.16.x (2026-07-29)** — content-predicate array paths `[?key=value]` in
  fieldMapping/secretRef paths.
- **0.15.0 (2026-07-29)** — **breaking**: the `apiLookup` resolver (shipped 0.12.0)
  removed; `secretRef` is the only field resolver kind.
- **0.14.0 (2026-07-28)** — a `fieldMapping` into a body field no longer drops that
  field's unmapped siblings.
- **0.13.0 (2026-07-28)** — array-index paths in mappings (#33); the
  `<Kind>Configuration` GVK no longer inherits the managed resource's version — it is
  always `v1alpha1`, matching what oasgen generates (#32).
- **0.12.0 (2026-07-27)** — `secretRef` field resolvers (issue #31) with
  per-CR-instance **self-provisioned RBAC**: RDC grants its own ServiceAccount read
  access to exactly the referenced Secrets, which made
  `REST_CONTROLLER_SERVICEACCOUNT_NAME/_NAMESPACE` hard-required at startup.
- **0.11.0** — `compareScope: identifiersAndStatus` drift mode.
- **0.10.0** — the delegation + async wave: `observeApiRef` /
  `createApiRef` / `updateApiRef` / `deleteApiRef` via snowplow `/call` under an
  authn-issued identity; async Model B (`mode: requeue`) with header-based operation
  handles; body-based absence via the `notFoundBody` jq predicate; jq module loader
  (`ref:` programs); delete holds the finalizer on transient definition-lookup
  failures.
- **≤0.9.x** — the foundation: the dynamic GVR controller over unstructured-runtime,
  OAS-driven client with request validation, `get`/`findby` observe with
  `identifiersMatchPolicy` and `continuationToken` pagination, per-verb
  `successCodes`/`tolerateCodes`/`notFoundCodes`/static headers/queries, async Model A
  (blocking) engine, basic/bearer auth from `<Kind>Configuration`.
