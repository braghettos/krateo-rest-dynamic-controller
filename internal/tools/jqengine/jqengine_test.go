package jqengine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func run(t *testing.T, src string, input interface{}) (interface{}, error) {
	t.Helper()
	p, err := Compile(src)
	if err != nil {
		return nil, err
	}
	return p.Run(context.Background(), input)
}

func TestJQ_BasicAndCanonicalTypes(t *testing.T) {
	out, err := run(t, ".a", map[string]interface{}{"a": float64(1)})
	require.NoError(t, err)
	assert.Equal(t, float64(1), out)

	// gojq would return a plain int for arithmetic; canonicalJSON must make it a float64.
	out, err = run(t, "1+1", nil)
	require.NoError(t, err)
	assert.IsType(t, float64(0), out)
	assert.Equal(t, float64(2), out)
}

func TestJQ_BuiltinHelpers(t *testing.T) {
	// null_to: API null -> CR sentinel
	out, err := run(t, "null_to(-1)", nil)
	require.NoError(t, err)
	assert.Equal(t, float64(-1), out)

	// null_to leaves non-null untouched
	out, err = run(t, "null_to(-1)", float64(5))
	require.NoError(t, err)
	assert.Equal(t, float64(5), out)

	// unwrap_enabled: {enforce_admins:{enabled:true}} -> {enforce_admins:true}
	out, err = run(t, `unwrap_enabled(["enforce_admins"])`,
		map[string]interface{}{"enforce_admins": map[string]interface{}{"enabled": true}})
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{"enforce_admins": true}, out)

	// strip_fields: drop server-only keys
	out, err = run(t, `strip_fields(["url"])`,
		map[string]interface{}{"url": "https://x", "keep": "y"})
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{"keep": "y"}, out)
}

func TestJQ_RealProxyCases(t *testing.T) {
	// pipeline folder backslash strip (study §4.1): "\\test" -> "test"
	out, err := run(t, `ltrimstr("\\")`, `\test`)
	require.NoError(t, err)
	assert.Equal(t, "test", out)

	// non-bijective conditional alias (collaborator role_name)
	out, err = run(t, `if . == "read" then "pull" elif . == "write" then "push" else . end`, "write")
	require.NoError(t, err)
	assert.Equal(t, "push", out)
	out, err = run(t, `if . == "read" then "pull" elif . == "write" then "push" else . end`, "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", out)

	// whole-body normalize: unwrap + strip + null->-1 across an array
	body := map[string]interface{}{
		"enforce_admins":      map[string]interface{}{"enabled": true},
		"required_signatures": map[string]interface{}{"enabled": false},
		"required_status_checks": map[string]interface{}{
			"checks": []interface{}{map[string]interface{}{"context": "ci", "app_id": nil}},
		},
	}
	prog := `unwrap_enabled(["enforce_admins"]) | strip_fields(["required_signatures"]) | .required_status_checks.checks |= map(.app_id |= null_to(-1))`
	out, err = run(t, prog, body)
	require.NoError(t, err)
	m := out.(map[string]interface{})
	assert.Equal(t, true, m["enforce_admins"])
	_, hasSig := m["required_signatures"]
	assert.False(t, hasSig, "server-only field stripped")
	checks := m["required_status_checks"].(map[string]interface{})["checks"].([]interface{})
	assert.Equal(t, float64(-1), checks[0].(map[string]interface{})["app_id"])
}

func TestJQ_Sandbox_NoEnv(t *testing.T) {
	// WithEnvironLoader(nil) must make the environment appear empty.
	out, err := run(t, "env | length", nil)
	require.NoError(t, err)
	assert.Equal(t, float64(0), out, "environment must be inaccessible inside the sandbox")
}

func TestJQ_OneInOneOut(t *testing.T) {
	// zero outputs
	_, err := run(t, "empty", map[string]interface{}{"a": 1})
	assert.ErrorContains(t, err, "no output")

	// more than one output
	_, err = run(t, ".[]", []interface{}{1, 2})
	assert.ErrorContains(t, err, "more than one output")
}

func TestJQ_OutputSizeCapped(t *testing.T) {
	// A program emitting a value larger than the output cap is rejected instead of propagating downstream.
	_, err := run(t, `"x" * 5000000`, nil) // ~5 MiB string > 4 MiB cap
	require.Error(t, err)
	assert.ErrorContains(t, err, "output exceeds")

	// A normal-sized output is unaffected.
	out, err := run(t, `"x" * 10`, nil)
	require.NoError(t, err)
	assert.Equal(t, "xxxxxxxxxx", out)
}

func TestJQ_MalformedRejectedAtCompile(t *testing.T) {
	_, err := Compile("this is | not ( valid")
	require.Error(t, err)
	assert.ErrorContains(t, err, "jq program")
}

func TestJQ_RuntimeErrorSurfaced(t *testing.T) {
	// Indexing a string with a field access is a runtime type error in jq.
	_, err := run(t, ".foo", "a string")
	require.Error(t, err)
	assert.ErrorContains(t, err, "jq program error")
}

func TestJQ_DeniedClockBuiltins(t *testing.T) {
	// now/localtime are nondeterministic and must fail closed, not flap the drift compare.
	_, err := run(t, "now", nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "disabled")

	_, err = run(t, "localtime", float64(0))
	require.Error(t, err)
	assert.ErrorContains(t, err, "disabled")

	// A deterministic clock builtin given fixed input remains available.
	out, err := run(t, "gmtime | type", float64(0))
	require.NoError(t, err)
	assert.Equal(t, "array", out)
}

func TestJQ_TimeoutAbortsPathologicalProgram(t *testing.T) {
	p, err := Compile("until(false; .)")
	require.NoError(t, err)
	// A caller deadline earlier than DefaultTimeout wins, so the infinite loop is aborted promptly.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = p.Run(ctx, map[string]interface{}{})
	require.Error(t, err, "an unbounded program must be aborted, not hang")
	assert.Less(t, time.Since(start), 3*time.Second, "must abort near the deadline")
}

func TestJQ_CompileCached(t *testing.T) {
	p1, err := Compile(".a.b")
	require.NoError(t, err)
	p2, err := Compile(".a.b")
	require.NoError(t, err)
	assert.Same(t, p1, p2, "identical source must return the cached program")
}
