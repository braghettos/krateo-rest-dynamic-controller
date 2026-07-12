package getter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/jqmodule"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveJQRefs_ModuleWithEntrypoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("def normalize: .name;"))
	}))
	defer srv.Close()

	g := &dynamicGetter{jqResolver: jqmodule.New(nil, srv.Client())}
	info := &Info{Resource: Resource{VerbsDescription: []VerbsDescription{{
		Action:            "get",
		ResponseTransform: &JQProgram{Ref: srv.URL, Entrypoint: "normalize"},
		FieldMapping: []FieldMappingItem{{
			InResponse: "id", InCustomResource: "status.id",
			ValueMapping: &ValueMapping{Type: "jq", JQ: &JQProgram{Ref: srv.URL, Entrypoint: "normalize"}},
		}},
		Async: &AsyncConfig{OperationRef: OperationRef{In: "body", Path: "url", JQ: &JQProgram{Ref: srv.URL}}},
	}}}}

	require.NoError(t, g.resolveJQRefs(context.Background(), info, ""))

	v := info.Resource.VerbsDescription[0]
	// module + entrypoint materialized as "<defs>\n<entrypoint>" into Inline; ref/entrypoint cleared
	assert.Equal(t, "def normalize: .name;\nnormalize", v.ResponseTransform.Inline)
	assert.Empty(t, v.ResponseTransform.Ref)
	assert.Empty(t, v.ResponseTransform.Entrypoint)
	// nested locations (valueMapping jq, async operationRef jq) are resolved too
	assert.Equal(t, "def normalize: .name;\nnormalize", v.FieldMapping[0].ValueMapping.JQ.Inline)
	// no entrypoint -> module body used verbatim
	assert.Equal(t, "def normalize: .name;", v.Async.OperationRef.JQ.Inline)
}

func TestResolveJQRefs_InlineUnchanged(t *testing.T) {
	g := &dynamicGetter{jqResolver: jqmodule.New(nil, nil)}
	info := &Info{Resource: Resource{VerbsDescription: []VerbsDescription{{
		Action:       "get",
		NotFoundBody: &JQProgram{Inline: ".deleted == true"},
	}}}}
	require.NoError(t, g.resolveJQRefs(context.Background(), info, ""))
	assert.Equal(t, ".deleted == true", info.Resource.VerbsDescription[0].NotFoundBody.Inline)
}

func TestResolveJQRefs_ObserveApiRefPredicates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("def absent: .status.id == null;"))
	}))
	defer srv.Close()

	g := &dynamicGetter{jqResolver: jqmodule.New(nil, srv.Client())}
	info := &Info{Resource: Resource{ObserveApiRef: &ApiRef{
		Name: "obs", Namespace: "demo",
		NotFoundExpr: &JQProgram{Ref: srv.URL, Entrypoint: "absent"},
		UpToDateExpr: &JQProgram{Inline: ".spec.size == .status.size"},
	}}}
	require.NoError(t, g.resolveJQRefs(context.Background(), info, ""))

	ar := info.Resource.ObserveApiRef
	// the module-ref notFoundExpr is materialized into inline (so evalBoolPredicate doesn't hard-fail)
	assert.Equal(t, "def absent: .status.id == null;\nabsent", ar.NotFoundExpr.Inline)
	assert.Empty(t, ar.NotFoundExpr.Ref)
	// an inline predicate is left as-is
	assert.Equal(t, ".spec.size == .status.size", ar.UpToDateExpr.Inline)
}
