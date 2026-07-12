package restResources

import (
	"context"
	"testing"

	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvalBoolPredicate_SpecStatusContext(t *testing.T) {
	ctx := context.Background()
	// The observe existence/drift predicates evaluate against {spec, status}.
	body := map[string]interface{}{
		"spec":   map[string]interface{}{"size": "large"},
		"status": map[string]interface{}{"size": "small"},
	}

	// notFoundExpr: the resource is absent when the composed status has no id
	nf, err := evalBoolPredicate(ctx, &getter.JQProgram{Inline: `.status.id == null`}, body, "notFoundExpr")
	require.NoError(t, err)
	assert.True(t, nf, "no status.id -> not found")

	// upToDateExpr: comparing desired spec to observed status
	utd, err := evalBoolPredicate(ctx, &getter.JQProgram{Inline: `.spec.size == .status.size`}, body, "upToDateExpr")
	require.NoError(t, err)
	assert.False(t, utd, "spec.size != status.size -> drift")

	// nil predicate is a no-op (false)
	b, err := evalBoolPredicate(ctx, nil, body, "x")
	require.NoError(t, err)
	assert.False(t, b)

	// a non-boolean result fails loudly
	_, err = evalBoolPredicate(ctx, &getter.JQProgram{Inline: `.spec.size`}, body, "upToDateExpr")
	require.Error(t, err)
	assert.ErrorContains(t, err, "must return a boolean")
}

func TestNotFoundBodyForAction(t *testing.T) {
	verbs := []getter.VerbsDescription{
		{Action: "get", Method: "GET", Path: "/x", NotFoundBody: &getter.JQProgram{Inline: `.deleted == true`}},
		{Action: "findby", Method: "GET", Path: "/x"},
	}
	assert.NotNil(t, notFoundBodyForAction(verbs, "get"))
	assert.NotNil(t, notFoundBodyForAction(verbs, "GET"), "action match is case-insensitive")
	assert.Nil(t, notFoundBodyForAction(verbs, "findby"), "verb without predicate returns nil")
	assert.Nil(t, notFoundBodyForAction(verbs, "create"), "missing verb returns nil")
}

func TestEvalNotFoundBody(t *testing.T) {
	ctx := context.Background()

	t.Run("nil program is present (not absent)", func(t *testing.T) {
		absent, err := evalNotFoundBody(ctx, nil, map[string]interface{}{"id": "x"})
		require.NoError(t, err)
		assert.False(t, absent)
	})

	t.Run("empty inline with no ref is a no-op", func(t *testing.T) {
		absent, err := evalNotFoundBody(ctx, &getter.JQProgram{}, map[string]interface{}{"id": "x"})
		require.NoError(t, err)
		assert.False(t, absent)
	})

	t.Run("unresolved ref (should have been materialized upstream) errors", func(t *testing.T) {
		_, err := evalNotFoundBody(ctx, &getter.JQProgram{Ref: "configmap://ns/name/key"}, map[string]interface{}{})
		require.Error(t, err)
		assert.ErrorContains(t, err, "not resolved to inline")
	})

	t.Run("empty list means absent", func(t *testing.T) {
		prog := &getter.JQProgram{Inline: `.items | length == 0`}
		absent, err := evalNotFoundBody(ctx, prog, map[string]interface{}{"items": []interface{}{}})
		require.NoError(t, err)
		assert.True(t, absent)

		absent, err = evalNotFoundBody(ctx, prog, map[string]interface{}{"items": []interface{}{"one"}})
		require.NoError(t, err)
		assert.False(t, absent, "a non-empty list means the resource exists")
	})

	t.Run("deleted flag means absent", func(t *testing.T) {
		prog := &getter.JQProgram{Inline: `.status == "deleted"`}
		absent, err := evalNotFoundBody(ctx, prog, map[string]interface{}{"status": "deleted"})
		require.NoError(t, err)
		assert.True(t, absent)
	})

	t.Run("non-boolean result is an error", func(t *testing.T) {
		prog := &getter.JQProgram{Inline: `.count`}
		_, err := evalNotFoundBody(ctx, prog, map[string]interface{}{"count": float64(5)})
		require.Error(t, err)
		assert.ErrorContains(t, err, "must return a boolean")
	})

	t.Run("compile error surfaces", func(t *testing.T) {
		_, err := evalNotFoundBody(ctx, &getter.JQProgram{Inline: `. |`}, map[string]interface{}{})
		require.Error(t, err)
		assert.ErrorContains(t, err, "compiling notFoundBody")
	})

	t.Run("tombstone predicate on a matched item (findby semantics)", func(t *testing.T) {
		// On findby the input is the single matched item, so a tombstone predicate is the correct shape.
		prog := &getter.JQProgram{Inline: `.status == "deleted"`}
		absent, err := evalNotFoundBody(ctx, prog, map[string]interface{}{"id": "x", "status": "deleted"})
		require.NoError(t, err)
		assert.True(t, absent)
	})

	t.Run("list-shaped predicate against a single item is the documented findby footgun", func(t *testing.T) {
		// Regression guard for why NotFoundBody docs warn against list predicates on findby: findby's input
		// is a single matched item, and gojq's `.items` on such an item is null, null|length is 0, so
		// `.items | length == 0` evaluates to TRUE against a resource that clearly exists. This is not a bug
		// in evalNotFoundBody (the predicate is simply mis-shaped for findby) — it is the reason the get vs
		// findby input shapes are documented distinctly.
		prog := &getter.JQProgram{Inline: `.items | length == 0`}
		absent, err := evalNotFoundBody(ctx, prog, map[string]interface{}{"id": "x", "status": "active"})
		require.NoError(t, err)
		assert.True(t, absent, "list predicate misfires on a single item — hence the verb-specific docs")
	})
}
