// Package fieldmapping implements the runtime side of the RestDefinition unified field mapping.
//
// For the response direction it normalizes the observed API body into the CR-domain shape at the reconcile
// chokepoint — before status population and drift comparison — so those consumers keep working unchanged.
// This milestone implements the Tier-1 declarative alias transform; the Tier-2 jq tier is carried in the
// types but not yet executed (entries with a jq valueMapping are skipped so no partial transform leaks).
package fieldmapping

import (
	"context"
	"encoding/json"
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
	// A nil map cannot be written to (SetNestedField). An empty but non-nil body is still processed, because
	// defaultIfAbsent may need to inject a field into it.
	if body == nil {
		return nil
	}
	// 1) Whole-document responseTransform first, so per-entry mappings see a normalized body.
	for _, prog := range documentResponseTransforms(verbs, actions) {
		if err := applyDocumentTransform(ctx, prog, body); err != nil {
			return err
		}
	}
	// 2) Per-entry response field mappings, in two phases so an earlier entry that lifts a path cannot
	//    delete a later entry's source before it is read: resolve every value first (no mutation), then
	//    write, then remove lifted sources.
	var resolved []resolvedEntry
	for _, entry := range responseEntries(verbs, actions) {
		r, ok, err := resolveResponseEntry(ctx, entry, body)
		if err != nil {
			return err
		}
		if ok {
			resolved = append(resolved, r)
		}
	}
	dstKeys := make(map[string]bool, len(resolved))
	for _, r := range resolved {
		dstKeys[pathKey(r.dst)] = true
	}
	for _, r := range resolved {
		if err := unstructured.SetNestedField(body, r.val, r.dst...); err != nil {
			return fmt.Errorf("setting normalized field: %w", err)
		}
	}
	for _, r := range resolved {
		// Never remove an in-place transform, nor a source that another entry writes to (chained relocation).
		if equalPath(r.src, r.dst) || dstKeys[pathKey(r.src)] {
			continue
		}
		unstructured.RemoveNestedField(body, r.src...)
	}
	return nil
}

func pathKey(p []string) string { return strings.Join(p, "\x00") }

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

// resolvedEntry is a response mapping whose value has been read and transformed but not yet written.
type resolvedEntry struct {
	src []string
	dst []string
	val interface{}
}

// resolveResponseEntry reads the source value for a response entry, applies its value transform (Tier-1
// alias or Tier-2 inline jq), and canonicalizes the result — WITHOUT mutating the body. It returns
// ok=false when the entry is not applicable (missing anchor, source not present, or a deferred module-ref
// jq). Canonicalizing here guarantees SetNestedField never panics on a non-JSON value on any write path
// (the alias/relocate path can otherwise carry a raw int/int64/yaml-decoded value).
func resolveResponseEntry(ctx context.Context, entry getter.FieldMappingItem, body map[string]interface{}) (resolvedEntry, bool, error) {
	if entry.InResponse == "" || entry.InCustomResource == "" {
		return resolvedEntry{}, false, nil
	}
	srcPath, err := pathparsing.ParsePath(entry.InResponse)
	if err != nil || len(srcPath) == 0 {
		return resolvedEntry{}, false, nil
	}
	val, found, err := unstructured.NestedFieldNoCopy(body, srcPath...)
	if err != nil {
		return resolvedEntry{}, false, nil
	}
	if !found {
		// Source absent: inject defaultIfAbsent if declared, otherwise skip. No value transform or source
		// lift applies (there is nothing to read or remove) — src == dst so the write phase leaves it be.
		if len(entry.DefaultIfAbsent) == 0 {
			return resolvedEntry{}, false, nil
		}
		def, derr := decodeDefault(entry.DefaultIfAbsent)
		if derr != nil {
			return resolvedEntry{}, false, fmt.Errorf("decoding defaultIfAbsent for response field %q: %w", entry.InResponse, derr)
		}
		dstPath, perr := bodyRelativePath(entry.InCustomResource)
		if perr != nil || len(dstPath) == 0 {
			return resolvedEntry{}, false, nil
		}
		return resolvedEntry{src: dstPath, dst: dstPath, val: def}, true, nil
	}

	if entry.ValueMapping != nil {
		switch entry.ValueMapping.Type {
		case "alias":
			val = applyAlias(val, entry.ValueMapping.Aliases, ResponseAPIToCR)
		case "jq":
			jq := entry.ValueMapping.JQ
			if jq == nil || jq.Inline == "" {
				// Module-referenced (ref:) jq is not yet supported; leave the entry unapplied.
				return resolvedEntry{}, false, nil
			}
			prog, cErr := jqengine.Compile(jq.Inline)
			if cErr != nil {
				return resolvedEntry{}, false, fmt.Errorf("compiling jq for response field %q: %w", entry.InResponse, cErr)
			}
			out, rErr := prog.Run(ctx, val)
			if rErr != nil {
				return resolvedEntry{}, false, fmt.Errorf("running jq for response field %q: %w", entry.InResponse, rErr)
			}
			val = out
		}
	}

	dstPath, err := bodyRelativePath(entry.InCustomResource)
	if err != nil || len(dstPath) == 0 {
		return resolvedEntry{}, false, nil
	}

	val, err = jqengine.Canonical(val)
	if err != nil {
		return resolvedEntry{}, false, fmt.Errorf("canonicalizing normalized field %q: %w", entry.InCustomResource, err)
	}
	return resolvedEntry{src: srcPath, dst: dstPath, val: val}, true, nil
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

// decodeDefault unmarshals a defaultIfAbsent JSON value into a JSON-canonical Go value.
func decodeDefault(raw json.RawMessage) (interface{}, error) {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
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
