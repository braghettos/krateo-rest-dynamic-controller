package restResources

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestObserveExtras(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "r1", "namespace": "demo", "uid": "u-1"},
		"spec":     map[string]interface{}{"id": "spec-id", "size": "large"},
		"status":   map[string]interface{}{"region": "eu"},
	}}

	extras := observeExtras(mg, map[string]interface{}{"apiVersion": "7.0", "name": "STATIC"}, []string{"id", "region"})

	// per-instance context is layered on top and wins over static
	assert.Equal(t, "r1", extras["name"], "per-instance name wins over static")
	assert.Equal(t, "demo", extras["namespace"])
	assert.Equal(t, "u-1", extras["uid"])
	assert.Equal(t, "7.0", extras["apiVersion"], "static extra preserved")

	// identifiers are forwarded (spec first, then status) — NOT the whole spec
	assert.Equal(t, "spec-id", extras["id"], "identifier resolved from spec")
	assert.Equal(t, "eu", extras["region"], "identifier resolved from status when absent in spec")
	_, hasSpec := extras["spec"]
	assert.False(t, hasSpec, "the whole spec is never forwarded")
	_, hasSize := extras["size"]
	assert.False(t, hasSize, "a non-identifier spec field is not forwarded")
}

func TestObserveExtras_NilStaticNoIdentifiers(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "r1", "namespace": "demo"},
		"spec":     map[string]interface{}{"secret": "do-not-send"},
	}}
	extras := observeExtras(mg, nil, nil)
	assert.Equal(t, "r1", extras["name"])
	assert.Equal(t, "demo", extras["namespace"])
	_, hasSpec := extras["spec"]
	assert.False(t, hasSpec, "no spec forwarded")
	_, hasSecret := extras["secret"]
	assert.False(t, hasSecret)
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
