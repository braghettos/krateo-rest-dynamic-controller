package restResources

import (
	"context"
	"fmt"
	"math"
	"net/http"

	"github.com/krateoplatformops/plumbing/kubeutil/event"
	customcondition "github.com/krateoplatformops/rest-dynamic-controller/internal/controllers/condition"
	restclient "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/client/apiaction"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/client/builder"
	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/fieldmapping"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/jqengine"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/orchestration"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/pathparsing"
	"github.com/krateoplatformops/unstructured-runtime/pkg/controller"
	"github.com/krateoplatformops/unstructured-runtime/pkg/logging"
	"github.com/krateoplatformops/unstructured-runtime/pkg/tools"
	unstructuredtools "github.com/krateoplatformops/unstructured-runtime/pkg/tools/unstructured"
	"github.com/krateoplatformops/unstructured-runtime/pkg/tools/unstructured/condition"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const reasonOrchestration event.Reason = "Orchestration"

// orchestrationCreatePlan returns the resource's create step plan, or nil if none is declared.
func orchestrationCreatePlan(clientInfo *getter.Info) *getter.StepPlan {
	if clientInfo == nil || clientInfo.Resource.Orchestration == nil {
		return nil
	}
	return clientInfo.Resource.Orchestration.Create
}

// synthInfoForStepVerb wraps a single lifted step verb in a throwaway getter.Info so APICallBuilder — which
// selects a verb by Action out of Resource.VerbsDescription — can build the step's call with the real
// client, OAS validation, auth and configuration.
func synthInfoForStepVerb(clientInfo *getter.Info, v getter.VerbsDescription) *getter.Info {
	return &getter.Info{
		URL:               clientInfo.URL,
		ConfigurationSpec: clientInfo.ConfigurationSpec,
		SetAuth:           clientInfo.SetAuth,
		Resource: getter.Resource{
			Kind:             clientInfo.Resource.Kind,
			Identifiers:      clientInfo.Resource.Identifiers,
			VerbsDescription: []getter.VerbsDescription{v},
		},
	}
}

// orchestrationStepStates reads the persisted per-step states (status.orchestration.steps), or nil.
func orchestrationStepStates(mg *unstructured.Unstructured) map[string]interface{} {
	states, _, _ := unstructured.NestedMap(mg.Object, "status", "orchestration", "steps")
	return states
}

// executeCall builds and issues a single lifted verb's call (a step or a deleteCall), driving a blocking
// async step to completion, and returns the (normalized-input) response.
func (h *handler) executeCall(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, mg *unstructured.Unstructured, lifted getter.VerbsDescription, async *getter.AsyncConfig, label string, log logging.Logger) (restclient.Response, error) {
	synthInfo := synthInfoForStepVerb(clientInfo, lifted)
	apiCall, callInfo, err := builder.APICallBuilder(cli, synthInfo, apiaction.Create)
	if err != nil {
		return restclient.Response{}, fmt.Errorf("%s: building call: %w", label, err)
	}
	if apiCall == nil || callInfo == nil {
		return restclient.Response{}, fmt.Errorf("%s: operation %s %s not found in the OpenAPI document", label, lifted.Method, lifted.Path)
	}
	reqConfig := builder.BuildCallConfig(callInfo, mg, clientInfo.ConfigurationSpec)
	if reqConfig == nil {
		return restclient.Response{}, fmt.Errorf("%s: building call configuration failed", label)
	}
	resp, err := apiCall(ctx, &http.Client{}, callInfo.Path, reqConfig)
	if err != nil {
		return restclient.Response{}, fmt.Errorf("%s: %w", label, err)
	}
	// A blocking async step polls to completion inline (requeue mode is reserved for a later increment).
	if async != nil && !asyncRequeueEnabled(async) {
		resp, err = driveAsync(ctx, cli, synthInfo, mg, async, orchestration.SyntheticAction, resp, reqConfig, log)
		if err != nil {
			return restclient.Response{}, fmt.Errorf("%s async: %w", label, err)
		}
	}
	return resp, nil
}

// runStep executes one plan step and records its captured outputs (and done marker) into mg.Object under
// status.orchestration.steps.<name>. Persistence is the caller's responsibility.
func (h *handler) runStep(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, mg *unstructured.Unstructured, step getter.Step, log logging.Logger) error {
	lifted := orchestration.LiftStep(step)
	resp, err := h.executeCall(ctx, cli, clientInfo, mg, lifted, step.Async, "orchestration step "+step.Name, log)
	if err != nil {
		return err
	}
	body, _ := resp.ResponseBody.(map[string]interface{})
	if body != nil {
		if err := fieldmapping.NormalizeResponseBody(ctx, []getter.VerbsDescription{lifted}, []string{orchestration.SyntheticAction}, body); err != nil {
			return fmt.Errorf("orchestration step %s: normalizing response: %w", step.Name, err)
		}
	}
	return populateStepOutputs(mg, step.Name, step.Capture, body)
}

// populateStepOutputs writes a step's captured response values (and done:true) under
// status.orchestration.steps.<name>, so later steps and the reverse-delete can read them and a resumed
// reconcile skips this step.
func populateStepOutputs(mg *unstructured.Unstructured, stepName string, capture []string, body map[string]interface{}) error {
	base := []string{"status", "orchestration", "steps", stepName}
	for _, capPath := range capture {
		if body == nil {
			break
		}
		segs, err := pathparsing.ParsePath(capPath)
		if err != nil || len(segs) == 0 {
			return fmt.Errorf("invalid capture path %q on step %s", capPath, stepName)
		}
		val, found, err := unstructured.NestedFieldNoCopy(body, segs...)
		if err != nil {
			return fmt.Errorf("reading capture %q on step %s: %w", capPath, stepName, err)
		}
		if !found {
			continue // captured field absent in this response; skip
		}
		canon, err := jqengine.Canonical(val)
		if err != nil {
			return fmt.Errorf("canonicalizing capture %q on step %s: %w", capPath, stepName, err)
		}
		// JSON decoding yields float64 for all numbers; store a whole-number identifier as int64 so it
		// round-trips cleanly into a later step's request (e.g. a path param renders as "42", not "42.0" or
		// an exponent). Values >= 2^53 already lost precision at decode time — a documented limitation.
		if f, ok := canon.(float64); ok && f == math.Trunc(f) && !math.IsInf(f, 0) {
			canon = int64(f)
		}
		dst := append(append([]string{}, base...), segs...)
		if err := unstructured.SetNestedField(mg.Object, canon, dst...); err != nil {
			return fmt.Errorf("writing capture %q on step %s: %w", capPath, stepName, err)
		}
	}
	// Mark done last, so a crash before this leaves the step not-done and it is safely re-run.
	if err := unstructured.SetNestedField(mg.Object, true, append(append([]string{}, base...), orchestration.StepDoneKey)...); err != nil {
		return fmt.Errorf("marking step %s done: %w", stepName, err)
	}
	return nil
}

// seedOrchestration initializes the durable cursor (an empty steps map) and marks the resource Pending, so
// the next Observe's drivePlan runs the first step. Called from Create instead of the single create verb.
func (h *handler) seedOrchestration(ctx context.Context, mg *unstructured.Unstructured, log logging.Logger) error {
	if orchestrationStepStates(mg) == nil {
		if err := unstructured.SetNestedMap(mg.Object, map[string]interface{}{}, "status", "orchestration", "steps"); err != nil {
			return fmt.Errorf("seeding orchestration cursor: %w", err)
		}
	}
	if err := unstructuredtools.SetConditions(mg, condition.Creating()); err != nil {
		return err
	}
	updated, err := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{Pluralizer: h.pluralizer, DynamicClient: h.dynamicClient})
	if err != nil {
		return fmt.Errorf("persisting orchestration cursor: %w", err)
	}
	*mg = *updated
	log.Debug("Seeded orchestration plan; steps will run in Observe")
	return nil
}

// drivePlan advances the create step plan by running the next not-done step (one per reconcile, non-blocking
// like the async requeue driver). It returns handled=true while steps remain (Pending) or one errors, and
// handled=false when there is no plan / no cursor yet / all steps are done — so the caller falls through to a
// normal observe of the primary resource.
func (h *handler) drivePlan(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, mg *unstructured.Unstructured, log logging.Logger) (controller.ExternalObservation, bool, error) {
	plan := orchestrationCreatePlan(clientInfo)
	if plan == nil {
		return controller.ExternalObservation{}, false, nil
	}
	states := orchestrationStepStates(mg)
	if states == nil {
		// Not provisioned yet: let the normal observe report not-found so the runtime calls Create, which
		// seeds the cursor.
		return controller.ExternalObservation{}, false, nil
	}
	step, _, unfinished := orchestration.FirstUnfinished(plan.Steps, states)
	if !unfinished {
		// Plan complete: the resource is provisioned. Mark it Available and OWN the observation — we do not
		// fall through to a get/findby. Primary-resource drift/update is deferred to a later increment, and a
		// fall-through observe that cannot resolve a step-assigned identifier would be masked by the
		// notfound+Pending guard as up-to-date, leaving the resource stuck Pending forever.
		if unstructuredtools.IsConditionSet(mg, condition.Available()) {
			return controller.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, true, nil
		}
		if serr := unstructuredtools.SetConditions(mg, condition.Available()); serr != nil {
			return controller.ExternalObservation{}, true, serr
		}
		updated, uerr := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{Pluralizer: h.pluralizer, DynamicClient: h.dynamicClient})
		if uerr != nil {
			return controller.ExternalObservation{}, true, fmt.Errorf("marking orchestration complete: %w", uerr)
		}
		*mg = *updated
		log.Debug("Orchestration plan complete; resource Available")
		return controller.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, true, nil
	}

	if err := h.runStep(ctx, cli, clientInfo, mg, step, log); err != nil {
		log.Error(err, "Running orchestration step", "step", step.Name)
		return controller.ExternalObservation{}, true, err
	}
	if serr := unstructuredtools.SetConditions(mg, customcondition.Pending()); serr != nil {
		return controller.ExternalObservation{}, true, serr
	}
	updated, uerr := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{Pluralizer: h.pluralizer, DynamicClient: h.dynamicClient})
	if uerr != nil {
		return controller.ExternalObservation{}, true, fmt.Errorf("persisting orchestration step %s: %w", step.Name, uerr)
	}
	*mg = *updated
	log.Debug("Orchestration step completed", "step", step.Name)
	// Stay Pending; the status change requeues us and the next reconcile runs the next step.
	return controller.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, true, nil
}

// runReverseDelete tears down a create plan's completed steps in reverse declaration order, issuing each
// step's deleteCall (blocking; only recorded-done steps are torn down, so a partial create is cleaned up
// exactly). On success it clears status.orchestration and returns nil so the finalizer is released.
func (h *handler) runReverseDelete(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, mg *unstructured.Unstructured, plan *getter.StepPlan, log logging.Logger) error {
	states := orchestrationStepStates(mg)
	for _, step := range orchestration.DoneStepsReversed(plan.Steps, states) {
		if step.DeleteCall == nil {
			continue
		}
		lifted := orchestration.LiftStepCall(*step.DeleteCall)
		if _, err := h.executeCall(ctx, cli, clientInfo, mg, lifted, nil, "orchestration delete "+step.Name, log); err != nil {
			log.Error(err, "Tearing down orchestration step", "step", step.Name)
			return err
		}
		log.Debug("Orchestration step torn down", "step", step.Name)
	}
	unstructured.RemoveNestedField(mg.Object, "status", "orchestration")
	if err := unstructuredtools.SetConditions(mg, condition.Deleting()); err != nil {
		return err
	}
	if _, err := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{Pluralizer: h.pluralizer, DynamicClient: h.dynamicClient}); err != nil {
		return fmt.Errorf("clearing orchestration state: %w", err)
	}
	h.eventRecorder.Event(mg, event.Normal(reasonOrchestration, "Delete", "Orchestrated teardown complete"))
	return nil
}
