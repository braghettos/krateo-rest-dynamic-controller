package fieldmapping

import (
	"testing"

	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func getVerb(items ...getter.FieldMappingItem) []getter.VerbsDescription {
	return []getter.VerbsDescription{{Action: "get", Method: "GET", Path: "/x", FieldMapping: items}}
}

func TestNormalizeResponseBody_RelocateNested(t *testing.T) {
	body := map[string]interface{}{
		"run": map[string]interface{}{
			"info": map[string]interface{}{"run_id": "abc", "experiment_id": float64(7)},
		},
	}
	verbs := getVerb(
		getter.FieldMappingItem{InResponse: "run.info.run_id", InCustomResource: "status.run_id"},
		getter.FieldMappingItem{InResponse: "run.info.experiment_id", InCustomResource: "status.experiment_id"},
	)

	require.NoError(t, NormalizeResponseBody(verbs, []string{"get"}, body))

	// Relocated to the CR-domain (body-relative) destination...
	assert.Equal(t, "abc", body["run_id"])
	assert.Equal(t, float64(7), body["experiment_id"])
	// ...and the stale nested source is dropped.
	_, found, _ := unstructured.NestedFieldNoCopy(body, "run", "info", "run_id")
	assert.False(t, found, "nested source run.info.run_id should be lifted away")
}

func TestNormalizeResponseBody_Alias(t *testing.T) {
	body := map[string]interface{}{"role_name": "pull", "other": "unmapped"}
	verbs := getVerb(getter.FieldMappingItem{
		InResponse:       "role_name",
		InCustomResource: "spec.permission",
		ValueMapping: &getter.ValueMapping{
			Type: "alias",
			Aliases: []getter.ValueAlias{
				{CustomResourceValue: "read", APIValue: "pull"},
				{CustomResourceValue: "write", APIValue: "push"},
			},
		},
	})

	require.NoError(t, NormalizeResponseBody(verbs, []string{"get"}, body))

	assert.Equal(t, "read", body["permission"], "API value 'pull' aliased to CR value 'read'")
	_, found, _ := unstructured.NestedFieldNoCopy(body, "role_name")
	assert.False(t, found, "source relocated away")
}

func TestNormalizeResponseBody_AliasInPlaceIdentityPassthrough(t *testing.T) {
	// Same source and destination (no prefix) -> alias applied in place; an unmapped value passes through.
	body := map[string]interface{}{"permission": "admin"}
	verbs := getVerb(getter.FieldMappingItem{
		InResponse:       "permission",
		InCustomResource: "permission",
		ValueMapping: &getter.ValueMapping{
			Type:    "alias",
			Aliases: []getter.ValueAlias{{CustomResourceValue: "read", APIValue: "pull"}},
		},
	})

	require.NoError(t, NormalizeResponseBody(verbs, []string{"get"}, body))
	assert.Equal(t, "admin", body["permission"], "unmapped value passes through unchanged, source not dropped")
}

func TestNormalizeResponseBody_JQSkipped(t *testing.T) {
	body := map[string]interface{}{"x": "raw"}
	verbs := getVerb(getter.FieldMappingItem{
		InResponse:       "x",
		InCustomResource: "status.y",
		ValueMapping:     &getter.ValueMapping{Type: "jq", JQ: &getter.JQProgram{Inline: "."}},
	})

	require.NoError(t, NormalizeResponseBody(verbs, []string{"get"}, body))
	assert.Equal(t, "raw", body["x"], "jq entry must be skipped (untouched) until the jq engine lands")
	_, found, _ := unstructured.NestedFieldNoCopy(body, "y")
	assert.False(t, found, "jq entry must not relocate")
}

func TestNormalizeResponseBody_NoOpCases(t *testing.T) {
	// Unmapped resource: no fieldMapping at all -> body untouched.
	body := map[string]interface{}{"a": "b"}
	require.NoError(t, NormalizeResponseBody(getVerb(), []string{"get"}, body))
	assert.Equal(t, map[string]interface{}{"a": "b"}, body)

	// Missing source path -> no error, no change.
	body2 := map[string]interface{}{"a": "b"}
	verbs := getVerb(getter.FieldMappingItem{InResponse: "does.not.exist", InCustomResource: "status.z"})
	require.NoError(t, NormalizeResponseBody(verbs, []string{"get"}, body2))
	assert.Equal(t, map[string]interface{}{"a": "b"}, body2)

	// Action mismatch: response entry on 'get' is not applied for a 'create' pass.
	body3 := map[string]interface{}{"role_name": "pull"}
	verbs2 := getVerb(getter.FieldMappingItem{InResponse: "role_name", InCustomResource: "status.permission"})
	require.NoError(t, NormalizeResponseBody(verbs2, []string{"create"}, body3))
	assert.Equal(t, map[string]interface{}{"role_name": "pull"}, body3)
}

func TestApplyAlias_Directions(t *testing.T) {
	aliases := []getter.ValueAlias{{CustomResourceValue: "read", APIValue: "pull"}}
	assert.Equal(t, "read", applyAlias("pull", aliases, ResponseAPIToCR))
	assert.Equal(t, "pull", applyAlias("read", aliases, RequestCRToAPI))
	assert.Equal(t, "admin", applyAlias("admin", aliases, ResponseAPIToCR), "unmapped passes through")
}
