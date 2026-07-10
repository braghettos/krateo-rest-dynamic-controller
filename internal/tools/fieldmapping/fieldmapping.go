// Package fieldmapping implements the runtime side of the RestDefinition unified field mapping.
//
// For the response direction it normalizes the observed API body into the CR-domain shape at the reconcile
// chokepoint — before status population and drift comparison — so those consumers keep working unchanged.
// This milestone implements the Tier-1 declarative alias transform; the Tier-2 jq tier is carried in the
// types but not yet executed (entries with a jq valueMapping are skipped so no partial transform leaks).
package fieldmapping

import (
	"context"
	"fmt"
	"strings"

	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/jqengine"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/pathparsing"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Direction indicates which way a value transform is applied.
type Direction int

const (
	// ResponseAPIToCR transforms an API-domain value into the CR domain (response direction).
	ResponseAPIToCR Direction = iota
	// RequestCRToAPI transforms a CR-domain value into the API domain (request direction).
	RequestCRToAPI
)

// NormalizeResponseBody normalizes the observed body, in place, into the CR-domain shape using the
// response-direction transforms declared on the verbs matching any of the given actions. It runs, in order:
//
//  1. the whole-document responseTransform jq program (if any), which rewrites the entire body; then
//  2. each per-entry response fieldMapping, relocating a response field to its CR-domain destination
//     (dropping the stale source when it moves) and applying its value transform — a Tier-1 alias or a
//     Tier-2 inline jq program.
//
// Module-referenced (ref:) jq programs are not yet supported and are left unapplied. Execution is bounded
// by ctx. It is a no-op when no matching response transforms are declared, so unmapped resources are
// unaffected.
func NormalizeResponseBody(ctx context.Context, verbs []getter.VerbsDescription, actions []string, body map[string]interface{}) error {
	if len(body) == 0 {
		return nil
	}
	// 1) Whole-document responseTransform first, so per-entry mappings see a normalized body.
	for _, prog := range documentResponseTransforms(verbs, actions) {
		if err := applyDocumentTransform(ctx, prog, body); err != nil {
			return err
		}
	}
	// 2) Per-entry response field mappings.
	for _, entry := range responseEntries(verbs, actions) {
		if err := applyResponseEntry(ctx, entry, body); err != nil {
			return err
		}
	}
	return nil
}

// documentResponseTransforms collects the inline whole-document responseTransform programs from the verbs
// whose action matches any of the requested actions, preserving order. Module-ref transforms are deferred.
func documentResponseTransforms(verbs []getter.VerbsDescription, actions []string) []*getter.JQProgram {
	var out []*getter.JQProgram
	for _, action := range actions {
		for _, verb := range verbs {
			if !strings.EqualFold(verb.Action, action) {
				continue
			}
			if verb.ResponseTransform != nil && verb.ResponseTransform.Inline != "" {
				out = append(out, verb.ResponseTransform)
			}
		}
	}
	return out
}

// applyDocumentTransform runs a whole-document jq program against the body and replaces the body's contents
// in place with the (object) result.
func applyDocumentTransform(ctx context.Context, jq *getter.JQProgram, body map[string]interface{}) error {
	prog, err := jqengine.Compile(jq.Inline)
	if err != nil {
		return fmt.Errorf("compiling responseTransform: %w", err)
	}
	out, err := prog.Run(ctx, body)
	if err != nil {
		return fmt.Errorf("running responseTransform: %w", err)
	}
	newBody, ok := out.(map[string]interface{})
	if !ok {
		return fmt.Errorf("responseTransform must output a JSON object, got %T", out)
	}
	for k := range body {
		delete(body, k)
	}
	for k, v := range newBody {
		body[k] = v
	}
	return nil
}

// responseEntries collects the response-direction (inResponse) mapping entries from the verbs whose action
// matches any of the requested actions, preserving declaration order.
func responseEntries(verbs []getter.VerbsDescription, actions []string) []getter.FieldMappingItem {
	var out []getter.FieldMappingItem
	for _, action := range actions {
		for _, verb := range verbs {
			if !strings.EqualFold(verb.Action, action) {
				continue
			}
			for _, m := range verb.FieldMapping {
				if m.InResponse != "" {
					out = append(out, m)
				}
			}
		}
	}
	return out
}

func applyResponseEntry(ctx context.Context, entry getter.FieldMappingItem, body map[string]interface{}) error {
	if entry.InResponse == "" || entry.InCustomResource == "" {
		return nil
	}

	srcPath, err := pathparsing.ParsePath(entry.InResponse)
	if err != nil || len(srcPath) == 0 {
		return nil
	}
	val, found, err := unstructured.NestedFieldNoCopy(body, srcPath...)
	if err != nil || !found {
		return nil
	}

	if entry.ValueMapping != nil {
		switch entry.ValueMapping.Type {
		case "alias":
			val = applyAlias(val, entry.ValueMapping.Aliases, ResponseAPIToCR)
		case "jq":
			jq := entry.ValueMapping.JQ
			if jq == nil || jq.Inline == "" {
				// Module-referenced (ref:) jq is not yet supported; leave the entry unapplied.
				return nil
			}
			prog, cErr := jqengine.Compile(jq.Inline)
			if cErr != nil {
				return fmt.Errorf("compiling jq for response field %q: %w", entry.InResponse, cErr)
			}
			out, rErr := prog.Run(ctx, val)
			if rErr != nil {
				return fmt.Errorf("running jq for response field %q: %w", entry.InResponse, rErr)
			}
			val = out
		}
	}

	dstPath, err := bodyRelativePath(entry.InCustomResource)
	if err != nil || len(dstPath) == 0 {
		return nil
	}

	if err := unstructured.SetNestedField(body, val, dstPath...); err != nil {
		return fmt.Errorf("setting normalized field %q: %w", entry.InCustomResource, err)
	}
	// The "lift": when the value moved to a different location, drop the stale source path.
	if !equalPath(srcPath, dstPath) {
		unstructured.RemoveNestedField(body, srcPath...)
	}
	return nil
}

// applyAlias rewrites a value across the CR<->API boundary using a finite set of bidirectional pairs.
// Any value without a matching pair passes through unchanged.
func applyAlias(v interface{}, aliases []getter.ValueAlias, dir Direction) interface{} {
	s := fmt.Sprintf("%v", v)
	for _, a := range aliases {
		switch dir {
		case ResponseAPIToCR:
			if s == a.APIValue {
				return a.CustomResourceValue
			}
		case RequestCRToAPI:
			if s == a.CustomResourceValue {
				return a.APIValue
			}
		}
	}
	return v
}

// bodyRelativePath converts a CR-domain destination (e.g. "spec.owner", "status.metadata.id") into a path
// relative to the response body by stripping a leading "spec." or "status." segment: the observed body is
// compared against spec and read for status by bare field paths, so the normalized value must land there.
func bodyRelativePath(inCustomResource string) ([]string, error) {
	trimmed := inCustomResource
	switch {
	case strings.HasPrefix(trimmed, "spec."):
		trimmed = strings.TrimPrefix(trimmed, "spec.")
	case strings.HasPrefix(trimmed, "status."):
		trimmed = strings.TrimPrefix(trimmed, "status.")
	}
	return pathparsing.ParsePath(trimmed)
}

func equalPath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
