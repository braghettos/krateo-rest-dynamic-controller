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
// Scope (increment 1): this is for observe-ONLY resources — the RESTAction composes the observed state each
// reconcile and the resource is reported existing + up-to-date, so no create/update is driven and there is
// no "observed but not found" / drift signal (deferred). The referenced RESTAction is trusted platform
// configuration: its composed .status is projected into this resource's status (except the runtime-managed
// conditions / observedGeneration), so it must be authored to return the intended shape.
func (h *handler) observeViaRestAction(ctx context.Context, mg *unstructured.Unstructured, ref *getter.ApiRef, identifiers []string, log logging.Logger) (controller.ExternalObservation, bool, error) {
	if ref == nil {
		return controller.ExternalObservation{}, false, nil
	}
	if h.snowplowClient == nil {
		return controller.ExternalObservation{}, true, fmt.Errorf("resource declares observeApiRef %s/%s but no snowplow client is configured (set the snowplow/authn URLs)", ref.Namespace, ref.Name)
	}

	extras := observeExtras(mg, ref.Extras, identifiers)
	result, err := h.snowplowClient.Resolve(ctx, snowplow.ApiRef{Name: ref.Name, Namespace: ref.Namespace}, extras)
	if err != nil {
		log.Error(err, "Resolving observe RESTAction", "restAction", ref.Namespace+"/"+ref.Name)
		return controller.ExternalObservation{}, true, fmt.Errorf("resolving observe RESTAction %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	if err := writeObservedStatus(mg, result); err != nil {
		return controller.ExternalObservation{}, true, err
	}
	if err := unstructuredtools.SetConditions(mg, condition.Available()); err != nil {
		return controller.ExternalObservation{}, true, err
	}
	updated, uerr := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{Pluralizer: h.pluralizer, DynamicClient: h.dynamicClient})
	if uerr != nil {
		return controller.ExternalObservation{}, true, fmt.Errorf("updating status from observe RESTAction: %w", uerr)
	}
	*mg = *updated
	log.Debug("Observed via RESTAction", "restAction", ref.Namespace+"/"+ref.Name)

	// The RESTAction composes the observed state each reconcile; the controller owns no create/update for an
	// observe-only resource, so it is reported as existing and up-to-date.
	return controller.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, true, nil
}

// observeExtras builds the request extras passed to snowplow: the RESTAction's static extras form the base,
// and the per-instance context is layered on top and WINS on conflict. The per-instance context is the
// resource's name/namespace/uid plus the values of its declared IDENTIFIERS (resolved from spec, else
// status) — deliberately NOT the whole spec, which would put arbitrary, potentially sensitive and unbounded
// data onto the /call query string. The identifiers are what the RESTAction needs to locate the resource.
func observeExtras(mg *unstructured.Unstructured, static map[string]interface{}, identifiers []string) map[string]any {
	out := make(map[string]any, len(static)+len(identifiers)+3)
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
