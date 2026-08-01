package restResources

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildExtras_StaticIdentifiersAndSpec(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "r1", "namespace": "demo", "uid": "u-1"},
		"spec":     map[string]interface{}{"id": "spec-id", "size": "large"},
		"status":   map[string]interface{}{"region": "eu"},
	}}

	extras := buildExtras(mg, map[string]interface{}{"apiVersion": "7.0", "name": "STATIC"}, []string{"id", "region"})

	// per-instance context is layered on top and wins over static
	assert.Equal(t, "r1", extras["name"], "per-instance name wins over static")
	assert.Equal(t, "demo", extras["namespace"])
	assert.Equal(t, "u-1", extras["uid"])
	assert.Equal(t, "7.0", extras["apiVersion"], "static extra preserved")

	// identifiers are forwarded dot-keyed (spec first, then status), alongside the whole spec
	assert.Equal(t, "spec-id", extras["id"], "identifier resolved from spec")
	assert.Equal(t, "eu", extras["region"], "identifier resolved from status when absent in spec")
	spec, ok := extras["spec"].(map[string]interface{})
	require.True(t, ok, "the whole spec is forwarded in every direction (#41)")
	assert.Equal(t, "large", spec["size"], "a non-identifier spec field is reachable via .spec")
}

func TestBuildExtras_WholeSpecForwarded(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "r1", "namespace": "demo"},
		"spec":     map[string]interface{}{"size": "large", "region": "eu"},
	}}
	extras := buildExtras(mg, nil, nil)
	assert.Equal(t, "r1", extras["name"])
	spec, ok := extras["spec"].(map[string]interface{})
	require.True(t, ok, "whole spec forwarded on the create path")
	assert.Equal(t, "large", spec["size"])
}

func TestBuildExtras_NilStaticNoIdentifiers(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "r1", "namespace": "demo"},
		"spec":     map[string]interface{}{"secret": "do-not-send"},
	}}
	extras := buildExtras(mg, nil, nil)
	assert.Equal(t, "r1", extras["name"])
	assert.Equal(t, "demo", extras["namespace"])
	spec, ok := extras["spec"].(map[string]interface{})
	require.True(t, ok, "spec forwarded in every direction")
	assert.Equal(t, "do-not-send", spec["secret"],
		"withholding spec here never protected anything: the create direction already forwards the identical spec")
	_, hasSecretTopLevel := extras["secret"]
	assert.False(t, hasSecretTopLevel, "spec fields are namespaced under .spec, not hoisted to the top level")
}

func TestWriteObservedStatus(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"conditions": []interface{}{map[string]interface{}{"type": "Ready"}},
			"stale":      "old",
		},
	}}

	result := map[string]interface{}{
		"api":        map[string]interface{}{"repo": map[string]interface{}{"id": float64(42)}},
		"phase":      "running",
		"conditions": []interface{}{map[string]interface{}{"type": "SHOULD_NOT_WIN"}},
	}

	require.NoError(t, writeObservedStatus(mg, result))

	// composed keys are merged into status
	api, found, _ := unstructured.NestedMap(mg.Object, "status", "api")
	assert.True(t, found)
	assert.Contains(t, api, "repo")
	phase, _, _ := unstructured.NestedString(mg.Object, "status", "phase")
	assert.Equal(t, "running", phase)

	// pre-existing status keys survive
	stale, _, _ := unstructured.NestedString(mg.Object, "status", "stale")
	assert.Equal(t, "old", stale)

	// runtime-managed conditions are NOT clobbered by the composed result
	conds, _, _ := unstructured.NestedSlice(mg.Object, "status", "conditions")
	require.Len(t, conds, 1)
	c0, _ := conds[0].(map[string]interface{})
	assert.Equal(t, "Ready", c0["type"], "conditions preserved, not overwritten by the RESTAction result")
}

func TestWriteObservedStatus_NoPriorStatus(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{}}
	require.NoError(t, writeObservedStatus(mg, map[string]interface{}{"phase": "ok"}))
	phase, found, _ := unstructured.NestedString(mg.Object, "status", "phase")
	assert.True(t, found)
	assert.Equal(t, "ok", phase)
}

// TestBuildExtras_ParentScopingFieldReachesDelete is the regression for #41: a delete RESTAction on a
// parent-scoped API needs a spec field that is NOT an identifier. Withholding the spec made that field
// unreachable, and the failure was a silent finalizer deadlock rather than an error — the RESTAction's
// guards saw nulls, every step skipped, snowplow returned 200, and the caller's existence check then kept
// the finalizer forever.
func TestBuildExtras_ParentScopingFieldReachesDelete(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "srv-1", "namespace": "demo", "uid": "u-9"},
		// projectId scopes every URL (/projects/{projectId}/...) but must not be an identifier:
		// under the default OR findby policy it would match every server in the project.
		"spec": map[string]interface{}{"projectId": "proj-42", "name": "srv-1"},
	}}

	extras := buildExtras(mg, nil, []string{"name"})

	spec, ok := extras["spec"].(map[string]interface{})
	require.True(t, ok, "delete must receive the spec")
	assert.Equal(t, "proj-42", spec["projectId"],
		"the parent-scoping field must be reachable as .spec.projectId without being an identifier")
	assert.Equal(t, "srv-1", extras["name"], "identifier still forwarded dot-keyed as before")
}
