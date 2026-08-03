package fieldmapping

import (
	"context"
	"encoding/json"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
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

func TestNormalizeResponseBody_NonJSONValueNoPanic(t *testing.T) {
	// A raw Go int (not JSON float64) on the plain-relocate/alias path must be canonicalized before
	// SetNestedField, which would otherwise panic via runtime.DeepCopyJSONValue.
	body := map[string]interface{}{"count": int(5)}
	verbs := getVerb(getter.FieldMappingItem{InResponse: "count", InCustomResource: "status.count_out"})

	require.NotPanics(t, func() {
		require.NoError(t, NormalizeResponseBody(context.Background(), verbs, []string{"get"}, body))
	})
	assert.Equal(t, float64(5), body["count_out"], "non-JSON int canonicalized to float64")
}

func TestNormalizeResponseBody_TwoPhaseLiftOrdering(t *testing.T) {
	// entry 1 lifts the whole ancestor "a"; entry 2 reads a descendant "a.b". With a single-pass
	// implementation entry 1's removal would delete entry 2's source before it is read. The two-phase
	// resolve-then-write must let both land.
	body := map[string]interface{}{"a": map[string]interface{}{"b": "deep"}}
	verbs := getVerb(
		getter.FieldMappingItem{InResponse: "a", InCustomResource: "status.whole"},
		getter.FieldMappingItem{InResponse: "a.b", InCustomResource: "status.b"},
	)

	require.NoError(t, NormalizeResponseBody(context.Background(), verbs, []string{"get"}, body))

	assert.Equal(t, map[string]interface{}{"b": "deep"}, body["whole"], "ancestor lifted as a whole")
	assert.Equal(t, "deep", body["b"], "descendant source survived the sibling lift (two-phase)")
}

func TestNormalizeResponseBody_DefaultIfAbsent(t *testing.T) {
	// The API omits allPipelines when authorized:false; inject the default so the observed body carries it
	// and the drift compare against a spec that sets it converges (Azure DevOps case).
	verbs := getVerb(getter.FieldMappingItem{
		InResponse:       "allPipelines",
		InCustomResource: "spec.allPipelines",
		DefaultIfAbsent:  json.RawMessage(`{"authorized":false}`),
	})

	// Absent -> injected.
	body := map[string]interface{}{"other": "x"}
	require.NoError(t, NormalizeResponseBody(context.Background(), verbs, []string{"get"}, body))
	assert.Equal(t, map[string]interface{}{"authorized": false}, body["allPipelines"])

	// Present -> default ignored, existing value kept (relocated in place: src==dst here).
	body2 := map[string]interface{}{"allPipelines": map[string]interface{}{"authorized": true}}
	require.NoError(t, NormalizeResponseBody(context.Background(), verbs, []string{"get"}, body2))
	assert.Equal(t, map[string]interface{}{"authorized": true}, body2["allPipelines"], "present value is not overwritten by the default")
}

func TestNormalizeResponseBody_DefaultIfAbsent_Scalar(t *testing.T) {
	verbs := getVerb(getter.FieldMappingItem{
		InResponse:       "count",
		InCustomResource: "status.count",
		DefaultIfAbsent:  json.RawMessage(`0`),
	})
	body := map[string]interface{}{}
	require.NoError(t, NormalizeResponseBody(context.Background(), verbs, []string{"get"}, body))
	assert.Equal(t, float64(0), body["count"])
}

// TestApplyRequestTransform covers the outgoing whole-document transform (oasgen-provider#43). Until this
// landed the field was parsed, validated and materialized, then never run — the request went out as if it
// were absent.
func TestApplyRequestTransform(t *testing.T) {
	ctx := context.Background()

	t.Run("rewrites the assembled body", func(t *testing.T) {
		body := map[string]interface{}{"user": map[string]interface{}{"name": "alice"}, "drop": true}
		out, err := ApplyRequestTransform(ctx, &getter.JQProgram{Inline: `{name: .user.name}`}, body)
		require.NoError(t, err)
		assert.Equal(t, map[string]interface{}{"name": "alice"}, out)
	})

	t.Run("sees the finished article, so it can act on field-mapped values", func(t *testing.T) {
		// The ordering contract: per-field mappings compose the body first, this runs on the result.
		body := map[string]interface{}{"a": 1.0, "b": 2.0}
		out, err := ApplyRequestTransform(ctx, &getter.JQProgram{Inline: `. + {sum: (.a + .b)}`}, body)
		require.NoError(t, err)
		assert.Equal(t, 3.0, out.(map[string]interface{})["sum"])
	})

	t.Run("nil program is a no-op and returns the same body", func(t *testing.T) {
		body := map[string]interface{}{"a": 1}
		out, err := ApplyRequestTransform(ctx, nil, body)
		require.NoError(t, err)
		assert.Equal(t, body, out)
	})

	t.Run("empty body is a no-op: a GET is never given a body it never had", func(t *testing.T) {
		for _, empty := range []interface{}{nil, map[string]interface{}{}} {
			out, err := ApplyRequestTransform(ctx, &getter.JQProgram{Inline: `{invented: "body"}`}, empty)
			require.NoError(t, err)
			assert.Equal(t, empty, out, "must not invent a body")
		}
	})

	t.Run("only Inline is read — refs are materialized upstream", func(t *testing.T) {
		body := map[string]interface{}{"a": 1}
		out, err := ApplyRequestTransform(ctx, &getter.JQProgram{Ref: "configmap://ns/cm/m.jq", Entrypoint: "f"}, body)
		require.NoError(t, err)
		assert.Equal(t, body, out, "an unmaterialized ref is a no-op here, not an error")
	})

	t.Run("compile failure is an error, not a silently untransformed body", func(t *testing.T) {
		_, err := ApplyRequestTransform(ctx, &getter.JQProgram{Inline: `{`}, map[string]interface{}{"a": 1})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "compiling requestTransform")
	})

	t.Run("run failure is an error", func(t *testing.T) {
		_, err := ApplyRequestTransform(ctx, &getter.JQProgram{Inline: `.a | tonumber`}, map[string]interface{}{"a": "nope"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "running requestTransform")
	})
}
