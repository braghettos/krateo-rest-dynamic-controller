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

	"github.com/itchyny/gojq"
)

// helperLibrary is prepended to every compiled program. These are pure jq (no I/O) so the sandbox holds.
const helperLibrary = `
def null_to(s): if . == null then s else . end;
def to_null(s): if . == s then null else . end;
def unwrap_enabled(fields): reduce fields[] as $f (.; if (has($f) and (.[$f]|type) == "object" and (.[$f]|has("enabled"))) then .[$f] = .[$f].enabled else . end);
def strip_fields(fields): delpaths([fields[] | [.]]);
`

// Program is a compiled, reusable jq program. It is safe to run repeatedly and concurrently.
type Program struct {
	code *gojq.Code
}

// Compile parses and compiles a jq source with the built-in helper library available and a sandboxed
// environment: no environment variables, no custom function modules, and no filesystem/network access.
func Compile(src string) (*Program, error) {
	query, err := gojq.Parse(helperLibrary + "\n" + src)
	if err != nil {
		return nil, fmt.Errorf("parsing jq program: %w", err)
	}
	// WithEnvironLoader returning nil denies access to process environment via env/$ENV. No module loader
	// and no custom functions are registered, so the program cannot reach the filesystem or network.
	code, err := gojq.Compile(query, gojq.WithEnvironLoader(func() []string { return nil }))
	if err != nil {
		return nil, fmt.Errorf("compiling jq program: %w", err)
	}
	return &Program{code: code}, nil
}

// Run executes the program against a single input and returns its single output, normalized to
// JSON-canonical Go types (map[string]interface{}, []interface{}, float64, string, bool, nil). Zero
// outputs, more than one output, or a jq runtime error are all failures. Execution is bounded by ctx.
func (p *Program) Run(ctx context.Context, input interface{}) (interface{}, error) {
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
