package orchestration

import (
	"testing"

	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
)

func TestLiftStep(t *testing.T) {
	step := getter.Step{
		Name:   "hook",
		Method: "POST",
		Path:   "/repos/{owner}/{repo}/hooks",
		FieldMapping: []getter.FieldMappingItem{
			// request-direction: cross-step data flow (repo name from a prior step's captured output)
			{InPath: "owner", InCustomResource: "spec.owner"},
			{InPath: "repo", InCustomResource: "status.orchestration.steps.repo.name"},
			{InBody: "config.url", InCustomResource: "spec.webhookUrl"},
			// response-direction: normalized before capture
			{InResponse: "id", InCustomResource: "hookId"},
		},
		SuccessCodes:  []int{201},
		TolerateCodes: []int{422},
		Headers:       []getter.HeaderItem{{Name: "Accept", Value: "application/json"}},
		Async:         &getter.AsyncConfig{},
	}

	v := LiftStep(step)
	assert.Equal(t, SyntheticAction, v.Action)
	assert.Equal(t, "POST", v.Method)
	assert.Equal(t, "/repos/{owner}/{repo}/hooks", v.Path)
	assert.Equal(t, []int{201}, v.SuccessCodes)
	assert.Equal(t, []int{422}, v.TolerateCodes)
	assert.Len(t, v.Headers, 1)
	assert.NotNil(t, v.Async)

	// request entries lifted to RequestFieldMapping (the side the request builder actually reads)
	assert.Len(t, v.RequestFieldMapping, 3)
	assert.Equal(t, "status.orchestration.steps.repo.name", v.RequestFieldMapping[1].InCustomResource)
	assert.Equal(t, "repo", v.RequestFieldMapping[1].InPath)
	assert.Equal(t, "config.url", v.RequestFieldMapping[2].InBody)
	// response entry kept for NormalizeResponseBody
	assert.Len(t, v.FieldMapping, 1)
	assert.Equal(t, "id", v.FieldMapping[0].InResponse)
}

func TestLiftStepCall(t *testing.T) {
	sc := getter.StepCall{
		Method:        "DELETE",
		Path:          "/repos/{owner}/{repo}",
		FieldMapping:  []getter.FieldMappingItem{{InPath: "repo", InCustomResource: "status.orchestration.steps.repo.name"}},
		NotFoundCodes: []int{404},
	}
	v := LiftStepCall(sc)
	assert.Equal(t, "DELETE", v.Method)
	assert.Equal(t, []int{404}, v.NotFoundCodes)
	assert.Len(t, v.RequestFieldMapping, 1)
	assert.Equal(t, "status.orchestration.steps.repo.name", v.RequestFieldMapping[0].InCustomResource)
}

func steps(names ...string) []getter.Step {
	out := make([]getter.Step, len(names))
	for i, n := range names {
		out[i] = getter.Step{Name: n, Method: "POST", Path: "/" + n}
	}
	return out
}

func doneState(names ...string) map[string]interface{} {
	m := map[string]interface{}{}
	for _, n := range names {
		m[n] = map[string]interface{}{StepDoneKey: true}
	}
	return m
}

func TestFirstUnfinished(t *testing.T) {
	plan := steps("repo", "hook", "branch")

	s, i, ok := FirstUnfinished(plan, nil)
	assert.True(t, ok)
	assert.Equal(t, "repo", s.Name)
	assert.Equal(t, 0, i)

	s, i, ok = FirstUnfinished(plan, doneState("repo"))
	assert.True(t, ok)
	assert.Equal(t, "hook", s.Name)
	assert.Equal(t, 1, i)

	s, i, ok = FirstUnfinished(plan, doneState("repo", "hook"))
	assert.True(t, ok)
	assert.Equal(t, "branch", s.Name)

	_, _, ok = FirstUnfinished(plan, doneState("repo", "hook", "branch"))
	assert.False(t, ok, "all done")
	assert.True(t, AllDone(plan, doneState("repo", "hook", "branch")))
}

func TestFirstUnfinished_ResumeSkipsCompletedPrefix(t *testing.T) {
	// The cursor is implicit: a resumed reconcile must land on 'hook', never re-run 'repo'.
	plan := steps("repo", "hook", "branch")
	s, _, ok := FirstUnfinished(plan, doneState("repo"))
	assert.True(t, ok)
	assert.Equal(t, "hook", s.Name, "resume lands on the first not-done step, never re-running the done prefix")
}

func TestDoneStepsReversed(t *testing.T) {
	plan := steps("repo", "hook", "branch")

	// Only the created prefix (repo, hook) is torn down, in reverse; the never-run 'branch' is untouched.
	rev := DoneStepsReversed(plan, doneState("repo", "hook"))
	assert.Len(t, rev, 2)
	assert.Equal(t, "hook", rev[0].Name)
	assert.Equal(t, "repo", rev[1].Name)

	assert.Empty(t, DoneStepsReversed(plan, nil), "nothing done -> nothing to tear down")

	rev = DoneStepsReversed(plan, doneState("repo", "hook", "branch"))
	assert.Equal(t, []string{"branch", "hook", "repo"}, []string{rev[0].Name, rev[1].Name, rev[2].Name})
}

func TestStepDone(t *testing.T) {
	assert.False(t, StepDone(nil))
	assert.False(t, StepDone(map[string]interface{}{}))
	assert.False(t, StepDone(map[string]interface{}{StepDoneKey: false}))
	assert.True(t, StepDone(map[string]interface{}{StepDoneKey: true}))
	assert.True(t, StepDone(map[string]interface{}{StepDoneKey: true, "id": float64(42)}))
}
