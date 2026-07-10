package fieldmapping

import (
	"context"
	"testing"

	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestNormalizeResponseBody_PerFieldJQ(t *testing.T) {
	// null->sentinel via the built-in null_to helper, relocating rate_limit into the CR domain.
	body := map[string]interface{}{"rate_limit": nil, "role_name": "write"}
	verbs := getVerb(
		getter.FieldMappingItem{
			InResponse:       "rate_limit",
			InCustomResource: "spec.rate_limit",
			ValueMapping:     &getter.ValueMapping{Type: "jq", JQ: &getter.JQProgram{Inline: "null_to(-1)"}},
		},
		getter.FieldMappingItem{
			InResponse:       "role_name",
			InCustomResource: "spec.permission",
			ValueMapping: &getter.ValueMapping{Type: "jq", JQ: &getter.JQProgram{
				Inline: `if . == "read" then "pull" elif . == "write" then "push" else . end`,
			}},
		},
	)

	require.NoError(t, NormalizeResponseBody(context.Background(), verbs, []string{"get"}, body))

	assert.Equal(t, float64(-1), body["rate_limit"], "null_to(-1) applied on the response value")
	assert.Equal(t, "push", body["permission"], "conditional jq alias write->push")
	_, found, _ := unstructured.NestedFieldNoCopy(body, "role_name")
	assert.False(t, found, "source lifted after transform")
}

func TestNormalizeResponseBody_DocumentResponseTransform(t *testing.T) {
	// Whole-body normalizer (branchprotection-style): unwrap {enabled}, strip server-only field, and map
	// app_id null->-1 across an array — the plugin-killer path.
	body := map[string]interface{}{
		"enforce_admins":      map[string]interface{}{"enabled": true},
		"required_signatures": map[string]interface{}{"enabled": false},
		"required_status_checks": map[string]interface{}{
			"checks": []interface{}{map[string]interface{}{"context": "ci", "app_id": nil}},
		},
	}
	verbs := []getter.VerbsDescription{{
		Action: "get", Method: "GET", Path: "/x",
		ResponseTransform: &getter.JQProgram{
			Inline: `unwrap_enabled(["enforce_admins"]) | strip_fields(["required_signatures"]) | .required_status_checks.checks |= map(.app_id |= null_to(-1))`,
		},
	}}

	require.NoError(t, NormalizeResponseBody(context.Background(), verbs, []string{"get"}, body))

	assert.Equal(t, true, body["enforce_admins"], "unwrapped {enabled:true} -> true")
	_, hasSig := body["required_signatures"]
	assert.False(t, hasSig, "server-only field stripped")
	checks := body["required_status_checks"].(map[string]interface{})["checks"].([]interface{})
	assert.Equal(t, float64(-1), checks[0].(map[string]interface{})["app_id"], "array null->-1")
}

func TestNormalizeResponseBody_TransformThenFieldMapping(t *testing.T) {
	// Document transform runs first (unwrap), then a per-field entry relocates+aliases the result.
	body := map[string]interface{}{
		"enforce_admins": map[string]interface{}{"enabled": true},
		"role_name":      "write",
	}
	verbs := []getter.VerbsDescription{{
		Action: "get", Method: "GET", Path: "/x",
		ResponseTransform: &getter.JQProgram{Inline: `unwrap_enabled(["enforce_admins"])`},
		FieldMapping: []getter.FieldMappingItem{{
			InResponse:       "role_name",
			InCustomResource: "spec.permission",
			ValueMapping: &getter.ValueMapping{Type: "alias", Aliases: []getter.ValueAlias{
				{CustomResourceValue: "pull", APIValue: "read"},
				{CustomResourceValue: "push", APIValue: "write"},
			}},
		}},
	}}

	require.NoError(t, NormalizeResponseBody(context.Background(), verbs, []string{"get"}, body))

	assert.Equal(t, true, body["enforce_admins"], "document transform applied first")
	assert.Equal(t, "push", body["permission"], "then per-field alias maps API 'write' -> CR 'push'")
	_, found, _ := unstructured.NestedFieldNoCopy(body, "role_name")
	assert.False(t, found, "source lifted after alias")
}

func TestNormalizeResponseBody_MalformedJQIsError(t *testing.T) {
	body := map[string]interface{}{"x": "v"}
	verbs := getVerb(getter.FieldMappingItem{
		InResponse:       "x",
		InCustomResource: "status.y",
		ValueMapping:     &getter.ValueMapping{Type: "jq", JQ: &getter.JQProgram{Inline: "this ( is not valid"}},
	})
	err := NormalizeResponseBody(context.Background(), verbs, []string{"get"}, body)
	require.Error(t, err, "a malformed jq program must fail the reconcile, not silently pass")
}
