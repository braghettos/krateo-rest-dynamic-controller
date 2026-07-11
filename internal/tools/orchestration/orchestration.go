// Package orchestration holds the pure, unit-testable core of the RestDefinition orchestration engine
// (design WS-D): turning a resource whose lifecycle is a SEQUENCE of idempotent REST calls into a set of
// synthetic single-call verbs plus a durable per-step cursor, so no hand-written proxy is needed to chain
// the calls. The controller wiring (issuing the calls, persisting status, reverse-delete) is layered on top.
package orchestration

import (
	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// SyntheticAction is the Action stamped on a lifted step verb so APICallBuilder selects it out of a
// single-verb synthetic Info. Its concrete value only needs to be stable (config-spec auth keys on
// "create" apply to the step calls, which is the desired behaviour for a create-plan step).
const SyntheticAction = "create"

// StepState keys recorded per step under status.orchestration.steps.<name>.
const (
	// StepDoneKey marks a step as terminally completed (succeeded or skipped).
	StepDoneKey = "done"
)

// liftFieldMapping splits a step's unified FieldMapping into the request-direction entries the request
// builder actually reads (RequestFieldMapping) and the response-direction entries NormalizeResponseBody
// reads (kept as FieldMappingItem). The unified FieldMapping request side is NOT consumed on the request
// path, so cross-step data flow must go through RequestFieldMapping.
func liftFieldMapping(fms []getter.FieldMappingItem) (request []getter.RequestFieldMappingItem, response []getter.FieldMappingItem) {
	for _, fm := range fms {
		if fm.InResponse != "" {
			response = append(response, fm)
			continue
		}
		if fm.InPath != "" || fm.InQuery != "" || fm.InBody != "" {
			request = append(request, getter.RequestFieldMappingItem{
				InPath:           fm.InPath,
				InQuery:          fm.InQuery,
				InBody:           fm.InBody,
				InCustomResource: fm.InCustomResource,
			})
		}
	}
	return request, response
}

// LiftStep converts a plan Step into a synthetic VerbsDescription so the existing APICallBuilder /
// BuildCallConfig / NormalizeResponseBody / async engine run against it verbatim.
func LiftStep(step getter.Step) getter.VerbsDescription {
	req, resp := liftFieldMapping(step.FieldMapping)
	return getter.VerbsDescription{
		Action:              SyntheticAction,
		Method:              step.Method,
		Path:                step.Path,
		RequestFieldMapping: req,
		FieldMapping:        resp,
		RequestTransform:    step.RequestTransform,
		ResponseTransform:   step.ResponseTransform,
		SuccessCodes:        step.SuccessCodes,
		TolerateCodes:       step.TolerateCodes,
		NotFoundCodes:       step.NotFoundCodes,
		Headers:             step.Headers,
		Queries:             step.Queries,
		Async:               step.Async,
	}
}

// LiftStepCall converts a StepCall (a step's deleteCall) into a synthetic verb for reverse-order teardown.
// For teardown a "not found" response means the sub-resource is already gone == success, so NotFoundCodes is
// merged into TolerateCodes: the client short-circuits a tolerated code to a nil-error success (letting the
// reverse loop continue), whereas it remaps a notFoundCodes match to a 404 *error* — which would otherwise
// wedge the teardown and hold the finalizer.
func LiftStepCall(sc getter.StepCall) getter.VerbsDescription {
	req, resp := liftFieldMapping(sc.FieldMapping)
	tolerate := append(append([]int{}, sc.TolerateCodes...), sc.NotFoundCodes...)
	return getter.VerbsDescription{
		Action:              SyntheticAction,
		Method:              sc.Method,
		Path:                sc.Path,
		RequestFieldMapping: req,
		FieldMapping:        resp,
		SuccessCodes:        sc.SuccessCodes,
		TolerateCodes:       tolerate,
		Headers:             sc.Headers,
		Queries:             sc.Queries,
	}
}

// StepDone reports whether a step has terminally completed, given its persisted per-step state
// (status.orchestration.steps.<name>). A nil/absent state means not done.
func StepDone(stepState map[string]interface{}) bool {
	if stepState == nil {
		return false
	}
	done, _ := stepState[StepDoneKey].(bool)
	return done
}

// FirstUnfinished returns the first step (in declaration order) not yet done, given the persisted per-step
// states keyed by step name. ok=false when every step is done.
func FirstUnfinished(steps []getter.Step, states map[string]interface{}) (step getter.Step, index int, ok bool) {
	for i, s := range steps {
		st, _ := states[s.Name].(map[string]interface{})
		if !StepDone(st) {
			return s, i, true
		}
	}
	return getter.Step{}, 0, false
}

// AllDone reports whether every step in the plan is done.
func AllDone(steps []getter.Step, states map[string]interface{}) bool {
	_, _, unfinished := FirstUnfinished(steps, states)
	return !unfinished
}

// DoneStepsReversed returns the completed steps in reverse declaration order — the safe teardown order, and
// crucially only the steps that actually ran, so a partially-completed create tears down exactly its
// created prefix and never touches an uncreated sub-resource.
func DoneStepsReversed(steps []getter.Step, states map[string]interface{}) []getter.Step {
	var out []getter.Step
	for i := len(steps) - 1; i >= 0; i-- {
		st, _ := states[steps[i].Name].(map[string]interface{})
		if StepDone(st) {
			out = append(out, steps[i])
		}
	}
	return out
}
