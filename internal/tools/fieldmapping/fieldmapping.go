// Package fieldmapping implements the runtime side of the RestDefinition unified field mapping.
//
// For the response direction it normalizes the observed API body into the CR-domain shape at the reconcile
// chokepoint — before status population and drift comparison — so those consumers keep working unchanged.
// This milestone implements the Tier-1 declarative alias transform; the Tier-2 jq tier is carried in the
// types but not yet executed (entries with a jq valueMapping are skipped so no partial transform leaks).
package fieldmapping

import (
	"fmt"
	"strings"

	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
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

// NormalizeResponseBody applies, in place, the response-direction field mappings declared on the verbs
// matching any of the given actions. Each entry relocates a response field to its CR-domain destination
// (dropping the stale source location when it moves) and optionally applies a Tier-1 alias. Entries that
// carry a Tier-2 jq valueMapping are skipped for now — applying only part of such a transform would be
// worse than leaving the raw body untouched.
//
// It is a no-op when no matching response entries are declared, so unmapped resources are unaffected.
func NormalizeResponseBody(verbs []getter.VerbsDescription, actions []string, body map[string]interface{}) error {
	if len(body) == 0 {
		return nil
	}
	for _, entry := range responseEntries(verbs, actions) {
		if err := applyResponseEntry(entry, body); err != nil {
			return err
		}
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

func applyResponseEntry(entry getter.FieldMappingItem, body map[string]interface{}) error {
	if entry.InResponse == "" || entry.InCustomResource == "" {
		return nil
	}
	// Tier-2 jq is not executed yet; skip so we never emit a partial/incorrect transform.
	if entry.ValueMapping != nil && entry.ValueMapping.Type == "jq" {
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

	if entry.ValueMapping != nil && entry.ValueMapping.Type == "alias" {
		val = applyAlias(val, entry.ValueMapping.Aliases, ResponseAPIToCR)
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
