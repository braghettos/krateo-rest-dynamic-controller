package restResources

import (
	"testing"

	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOrchestrationCreatePlan(t *testing.T) {
	assert.Nil(t, orchestrationCreatePlan(nil))
	assert.Nil(t, orchestrationCreatePlan(&getter.Info{}), "no orchestration -> nil")
	assert.Nil(t, orchestrationCreatePlan(&getter.Info{Resource: getter.Resource{Orchestration: &getter.Orchestration{}}}), "orchestration without create -> nil")

	ci := &getter.Info{Resource: getter.Resource{Orchestration: &getter.Orchestration{
		Create: &getter.StepPlan{Steps: []getter.Step{{Name: "repo"}, {Name: "hook"}}},
	}}}
	p := orchestrationCreatePlan(ci)
	require.NotNil(t, p)
	assert.Len(t, p.Steps, 2)
}

func TestSynthInfoForStepVerb(t *testing.T) {
	ci := &getter.Info{
		URL:               "https://oas",
		ConfigurationSpec: map[string]interface{}{"auth": "x"},
		Resource:          getter.Resource{Kind: "Repo", Identifiers: []string{"id"}},
	}
	v := getter.VerbsDescription{Action: "create", Method: "POST", Path: "/repos"}
	si := synthInfoForStepVerb(ci, v)

	assert.Equal(t, "https://oas", si.URL)
	assert.Equal(t, ci.ConfigurationSpec, si.ConfigurationSpec)
	assert.Equal(t, []string{"id"}, si.Resource.Identifiers, "identifiers carried so BuildCallConfig resolves them")
	require.Len(t, si.Resource.VerbsDescription, 1, "exactly the one lifted verb, so APICallBuilder selects it")
	assert.Equal(t, "/repos", si.Resource.VerbsDescription[0].Path)
}

func TestOrchestrationStepStates(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{}}
	assert.Nil(t, orchestrationStepStates(mg), "no status.orchestration -> nil (not provisioned)")

	require.NoError(t, unstructured.SetNestedMap(mg.Object, map[string]interface{}{"done": true}, "status", "orchestration", "steps", "repo"))
	states := orchestrationStepStates(mg)
	require.NotNil(t, states)
	assert.Contains(t, states, "repo")
}

func TestPopulateStepOutputs(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{}}
	body := map[string]interface{}{
		"id":    float64(42),
		"name":  "acme",
		"owner": map[string]interface{}{"login": "me"},
		"extra": "not captured",
	}

	require.NoError(t, populateStepOutputs(mg, "repo", []string{"id", "name", "owner.login"}, body))

	// done marker set
	done, found, _ := unstructured.NestedBool(mg.Object, "status", "orchestration", "steps", "repo", "done")
	assert.True(t, found)
	assert.True(t, done)

	// scalar captures published at status.orchestration.steps.repo.<path>; a whole-number id is stored as
	// int64 so it renders cleanly ("42") when a later step reads it into a request.
	id, found, _ := unstructured.NestedFieldNoCopy(mg.Object, "status", "orchestration", "steps", "repo", "id")
	assert.True(t, found)
	assert.Equal(t, int64(42), id)

	name, _, _ := unstructured.NestedString(mg.Object, "status", "orchestration", "steps", "repo", "name")
	assert.Equal(t, "acme", name)

	// nested capture path preserved (later step reads status.orchestration.steps.repo.owner.login)
	login, found, _ := unstructured.NestedString(mg.Object, "status", "orchestration", "steps", "repo", "owner", "login")
	assert.True(t, found)
	assert.Equal(t, "me", login)

	// uncaptured field is not published
	_, found, _ = unstructured.NestedFieldNoCopy(mg.Object, "status", "orchestration", "steps", "repo", "extra")
	assert.False(t, found)
}

func TestPopulateStepOutputs_AbsentCaptureSkipped(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{}}
	body := map[string]interface{}{"id": float64(1)}

	// 'missing' is not in the body: it is skipped, not an error, and the step is still marked done.
	require.NoError(t, populateStepOutputs(mg, "s", []string{"id", "missing"}, body))
	done, _, _ := unstructured.NestedBool(mg.Object, "status", "orchestration", "steps", "s", "done")
	assert.True(t, done)
	_, found, _ := unstructured.NestedFieldNoCopy(mg.Object, "status", "orchestration", "steps", "s", "missing")
	assert.False(t, found)
}

func TestPopulateStepOutputs_PureMutationStep(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{}}
	// A step with no capture still gets marked done (e.g. a PATCH that publishes nothing).
	require.NoError(t, populateStepOutputs(mg, "branch", nil, map[string]interface{}{"whatever": true}))
	done, found, _ := unstructured.NestedBool(mg.Object, "status", "orchestration", "steps", "branch", "done")
	assert.True(t, found)
	assert.True(t, done)
}
