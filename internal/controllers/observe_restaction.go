package restResources

import (
	"context"
	"fmt"

	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/pathparsing"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/snowplow"
	"github.com/krateoplatformops/unstructured-runtime/pkg/controller"
	"github.com/krateoplatformops/unstructured-runtime/pkg/logging"
	"github.com/krateoplatformops/unstructured-runtime/pkg/tools"
	unstructuredtools "github.com/krateoplatformops/unstructured-runtime/pkg/tools/unstructured"
	"github.com/krateoplatformops/unstructured-runtime/pkg/tools/unstructured/condition"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// observeViaRestAction delegates observation to a Snowplow RESTAction: it resolves the referenced RESTAction
// (via snowplow /call, under the controller's own identity) with the resource's per-instance context as
// request extras, then projects the RESTAction's composed .status into this resource's status and marks it
// Available. It returns handled=true so the caller returns immediately (this replaces the get/findby observe);
// handled=false only when no observeApiRef is declared, so the caller falls through to the normal observe.
//
// With no notFoundExpr/upToDateExpr the RESTAction composes the observed state each reconcile and the
// resource is reported existing + up-to-date (observe-only projection). When notFoundExpr / upToDateExpr are
// declared, the delegated observe reports non-existence (=> create) / drift (=> update), so it composes with
// create/update/delete RESTActions. The referenced RESTAction is trusted platform configuration: its
// composed .status is projected into this resource's status (except the runtime-managed conditions /
// observedGeneration), so it must be authored to return the intended shape.
func (h *handler) observeViaRestAction(ctx context.Context, mg *unstructured.Unstructured, ref *getter.ApiRef, identifiers []string, log logging.Logger) (controller.ExternalObservation, bool, error) {
	if ref == nil {
		return controller.ExternalObservation{}, false, nil
	}
	if h.snowplowClient == nil {
		return controller.ExternalObservation{}, true, fmt.Errorf("resource declares observeApiRef %s/%s but no snowplow client is configured (set the snowplow/authn URLs)", ref.Namespace, ref.Name)
	}
	// If a Model B async operation is in flight (from an OAS create/update verb), defer to the normal observe
	// so it is polled to completion, rather than short-circuiting to the RESTAction observe.
	if opID, _ := asyncOperationInFlight(mg); opID != "" {
		return controller.ExternalObservation{}, false, nil
	}

	extras := buildExtras(mg, ref.Extras, identifiers, false)
	result, err := h.snowplowClient.Resolve(ctx, snowplow.ApiRef{Name: ref.Name, Namespace: ref.Namespace}, extras)
	if err != nil {
		log.Error(err, "Resolving observe RESTAction", "restAction", ref.Namespace+"/"+ref.Name)
		return controller.ExternalObservation{}, true, fmt.Errorf("resolving observe RESTAction %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	// The existence / drift predicates evaluate against {spec, status}, where status is the composed result.
	predCtx := map[string]interface{}{"status": result}
	if spec, found, _ := unstructured.NestedMap(mg.Object, "spec"); found {
		predCtx["spec"] = spec
	}

	// Existence: if the observe reports the resource absent, return not-exists so the controller creates it
	// (composing with createApiRef). Do not project status / mark Available in that case.
	if notFound, ferr := evalBoolPredicate(ctx, ref.NotFoundExpr, predCtx, "observeApiRef.notFoundExpr"); ferr != nil {
		return controller.ExternalObservation{}, true, fmt.Errorf("evaluating observeApiRef.notFoundExpr: %w", ferr)
	} else if notFound {
		log.Debug("Observe RESTAction reports the resource absent", "restAction", ref.Namespace+"/"+ref.Name)
		return controller.ExternalObservation{ResourceExists: false, ResourceUpToDate: false}, true, nil
	}

	// Drift: default up-to-date unless a SOURCED upToDateExpr reports otherwise (composing with updateApiRef).
	// A declared-but-source-less predicate is treated as "no drift signal" (up-to-date) rather than reading
	// as false, which would thrash Update every reconcile.
	upToDate := true
	if ex := ref.UpToDateExpr; ex != nil && (ex.Inline != "" || ex.Ref != "") {
		utd, derr := evalBoolPredicate(ctx, ex, predCtx, "observeApiRef.upToDateExpr")
		if derr != nil {
			return controller.ExternalObservation{}, true, fmt.Errorf("evaluating observeApiRef.upToDateExpr: %w", derr)
		}
		upToDate = utd
	}

	if err := writeObservedStatus(mg, result); err != nil {
		return controller.ExternalObservation{}, true, err
	}
	// Available when up-to-date; report Unavailable while drifted (an update will be driven).
	cond := condition.Available()
	if !upToDate {
		cond = condition.Unavailable()
		cond.Message = "drift reported by observeApiRef.upToDateExpr"
	}
	if err := unstructuredtools.SetConditions(mg, cond); err != nil {
		return controller.ExternalObservation{}, true, err
	}
	updated, uerr := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{Pluralizer: h.pluralizer, DynamicClient: h.dynamicClient})
	if uerr != nil {
		return controller.ExternalObservation{}, true, fmt.Errorf("updating status from observe RESTAction: %w", uerr)
	}
	*mg = *updated
	log.Debug("Observed via RESTAction", "restAction", ref.Namespace+"/"+ref.Name, "upToDate", upToDate)

	// The RESTAction composes the observed state each reconcile. With no notFoundExpr/upToDateExpr the
	// resource is reported existing + up-to-date (observe-only); with them, non-existence/drift compose with
	// create/update RESTActions.
	return controller.ExternalObservation{ResourceExists: true, ResourceUpToDate: upToDate}, true, nil
}

// buildExtras builds the request extras passed to snowplow: the RESTAction's static extras form the base,
// and the per-instance context is layered on top and WINS on conflict. The per-instance context is always
// the resource's name/namespace/uid plus the values of its declared IDENTIFIERS (resolved from spec, else
// status). includeSpec additionally forwards the WHOLE spec — needed by the create RESTAction (the desired
// state), but never for observe/delete (which only need to locate the resource), so those keep the payload
// small and free of arbitrary spec data.
func buildExtras(mg *unstructured.Unstructured, static map[string]interface{}, identifiers []string, includeSpec bool) map[string]any {
	out := make(map[string]any, len(static)+len(identifiers)+4)
	for k, v := range static {
		out[k] = v
	}
	out["name"] = mg.GetName()
	out["namespace"] = mg.GetNamespace()
	out["uid"] = string(mg.GetUID())
	for _, id := range identifiers {
		segs, err := pathparsing.ParsePath(id)
		if err != nil || len(segs) == 0 {
			continue
		}
		if v, found, _ := unstructured.NestedFieldNoCopy(mg.Object, append([]string{"spec"}, segs...)...); found {
			out[id] = v
			continue
		}
		if v, found, _ := unstructured.NestedFieldNoCopy(mg.Object, append([]string{"status"}, segs...)...); found {
			out[id] = v
		}
	}
	if includeSpec {
		if spec, found, _ := unstructured.NestedMap(mg.Object, "spec"); found {
			out["spec"] = spec
		}
	}
	return out
}

// writeObservedStatus merges the RESTAction's composed status map into the resource's status, leaving the
// runtime-managed conditions untouched.
func writeObservedStatus(mg *unstructured.Unstructured, result map[string]interface{}) error {
	status, _, err := unstructured.NestedMap(mg.Object, "status")
	if err != nil {
		return fmt.Errorf("reading status: %w", err)
	}
	if status == nil {
		status = map[string]interface{}{}
	}
	for k, v := range result {
		// Never let the composed result clobber runtime-managed status fields.
		if k == "conditions" || k == "observedGeneration" {
			continue
		}
		status[k] = v
	}
	if err := unstructured.SetNestedMap(mg.Object, status, "status"); err != nil {
		return fmt.Errorf("writing observed status: %w", err)
	}
	return nil
}
