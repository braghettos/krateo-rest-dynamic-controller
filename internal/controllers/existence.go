package restResources

import (
	"context"
	"fmt"
	"strings"

	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/jqengine"
)

// notFoundBodyForAction returns the NotFoundBody predicate declared on the verb matching action, or nil.
func notFoundBodyForAction(verbs []getter.VerbsDescription, action string) *getter.JQProgram {
	for _, v := range verbs {
		if strings.EqualFold(v.Action, action) {
			return v.NotFoundBody
		}
	}
	return nil
}

// evalNotFoundBody runs the NotFoundBody gojq predicate against the (raw) observe-response body and reports
// whether the external resource should be treated as not existing. The program must return a boolean; any
// other result is an error so a mistyped predicate fails loudly rather than silently reading as "present".
func evalNotFoundBody(ctx context.Context, prog *getter.JQProgram, body interface{}) (bool, error) {
	if prog == nil {
		return false, nil
	}
	if prog.Inline == "" {
		// ref/entrypoint (jq-module loader) is not wired for existence predicates yet; fail loudly rather
		// than silently ignoring a declared-but-unhonored predicate.
		if prog.Ref != "" {
			return false, fmt.Errorf("notFoundBody.ref is not supported yet (use inline)")
		}
		return false, nil
	}
	p, err := jqengine.Compile(prog.Inline)
	if err != nil {
		return false, fmt.Errorf("compiling notFoundBody: %w", err)
	}
	out, err := p.Run(ctx, body)
	if err != nil {
		return false, fmt.Errorf("running notFoundBody: %w", err)
	}
	b, ok := out.(bool)
	if !ok {
		return false, fmt.Errorf("notFoundBody must return a boolean, got %T", out)
	}
	return b, nil
}
