// Package jqengine compiles and runs sandboxed gojq programs for the KOG field-mapping Tier-2 transform.
//
// Every program is compiled with a small built-in helper library available (null_to/to_null,
// unwrap_enabled, strip_fields) and a sandbox that denies environment, filesystem and network access, so a
// program can only transform the single JSON value it is handed. Programs are one-in-one-out: zero, or more
// than one, output is treated as a failure. Output is normalized to JSON-canonical Go types so it can be
// written straight back into an unstructured body.
package jqengine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/itchyny/gojq"
)

// DefaultTimeout bounds a single program execution. Programs come from a user's RestDefinition and run on
// the reconcile hot path, so an unbounded program (e.g. `until(false;.)`) must not hang a worker. A caller
// ctx with an earlier deadline still wins.
var DefaultTimeout = 5 * time.Second

// programCache memoizes compiled programs by source. Compilation is deterministic for a given source and a
// Program is safe for reuse, so this avoids reparsing/recompiling on every reconcile. The set of distinct
// programs is bounded by the RestDefinitions in the cluster.
var programCache sync.Map // map[string]*Program

// helperLibrary is prepended to every compiled program. The first block are pure-jq convenience transforms
// (no I/O). The second block redefines the non-deterministic clock builtins (now, localtime) as erroring
// stubs: a user def shadows the gojq builtin, so any program that reads the wall clock / local timezone
// fails closed rather than producing output that flaps between reconciles and defeats drift convergence.
// (gmtime/mktime/strftime are deterministic given their input and are left available.)
const helperLibrary = `
def null_to(s): if . == null then s else . end;
def to_null(s): if . == s then null else . end;
def unwrap_enabled(fields): reduce fields[] as $f (.; if (has($f) and (.[$f]|type) == "object" and (.[$f]|has("enabled"))) then .[$f] = .[$f].enabled else . end);
def strip_fields(fields): delpaths([fields[] | [.]]);
def now: error("builtin \"now\" is disabled in the KOG jq sandbox (non-deterministic)");
def localtime: error("builtin \"localtime\" is disabled in the KOG jq sandbox (non-deterministic)");
`

// Program is a compiled, reusable jq program. It is safe to run repeatedly and concurrently.
type Program struct {
	code *gojq.Code
}

// Compile parses and compiles a jq source with the built-in helper library available and a sandboxed
// environment: no environment variables (env/$ENV), no module loader or custom functions (no filesystem/
// network), and the non-deterministic clock builtins denied. Results are cached by source string.
func Compile(src string) (*Program, error) {
	if cached, ok := programCache.Load(src); ok {
		return cached.(*Program), nil
	}
	query, err := gojq.Parse(helperLibrary + "\n" + src)
	if err != nil {
		return nil, fmt.Errorf("parsing jq program: %w", err)
	}
	// WithEnvironLoader returning nil denies env/$ENV; no module loader or custom functions are registered,
	// so the program cannot reach the filesystem or network. The clock builtins are shadowed in helperLibrary.
	code, err := gojq.Compile(query, gojq.WithEnvironLoader(func() []string { return nil }))
	if err != nil {
		return nil, fmt.Errorf("compiling jq program: %w", err)
	}
	p := &Program{code: code}
	actual, _ := programCache.LoadOrStore(src, p)
	return actual.(*Program), nil
}

// Run executes the program against a single input and returns its single output, normalized to
// JSON-canonical Go types (map[string]interface{}, []interface{}, float64, string, bool, nil). Zero
// outputs, more than one output, or a jq runtime error are all failures. Execution is bounded by ctx.
func (p *Program) Run(ctx context.Context, input interface{}) (interface{}, error) {
	// Bound execution so a pathological program cannot pin a reconcile worker. An earlier caller deadline
	// still applies. NOTE: this bounds CPU/wall time, not memory — a program allocating unboundedly within
	// the window is a residual risk that a hard output/allocation cap (not offered by gojq) would close.
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	iter := p.code.RunWithContext(ctx, input)

	v, ok := iter.Next()
	if !ok {
		return nil, fmt.Errorf("jq program produced no output")
	}
	if err, isErr := v.(error); isErr {
		return nil, fmt.Errorf("jq program error: %w", err)
	}
	// A mapping must be one-in-one-out; reject additional outputs.
	if _, more := iter.Next(); more {
		return nil, fmt.Errorf("jq program produced more than one output")
	}

	return canonicalJSON(v)
}

// Canonical normalizes any value to JSON-canonical Go types (map[string]interface{}, []interface{},
// float64, string, bool, nil) so it is safe to write into an unstructured body via SetNestedField, which
// panics (through runtime.DeepCopyJSONValue) on non-JSON-compatible Go types such as a plain int or an
// int64/yaml-decoded value. Callers that write arbitrary values into an unstructured map should route them
// through this first.
func Canonical(v interface{}) (interface{}, error) {
	return canonicalJSON(v)
}

// canonicalJSON round-trips a gojq output value through JSON so it uses only JSON-compatible Go types.
// gojq may return a plain int, which unstructured.SetNestedField (via runtime.DeepCopyJSONValue) panics on.
func canonicalJSON(v interface{}) (interface{}, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("normalizing jq output: %w", err)
	}
	var out interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("normalizing jq output: %w", err)
	}
	return out, nil
}
