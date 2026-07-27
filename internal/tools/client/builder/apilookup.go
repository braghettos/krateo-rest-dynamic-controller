package builder

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	restclient "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/client"
	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/pathparsing"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ResolveAPILookup resolves r (issue #30) by calling r.Action — a VerbsDescription.Action in the SAME
// RestDefinition/OAS document as clientInfo (there is no mechanism to reach another RestDefinition's Info,
// which is what makes cross-RestDefinition lookup structurally impossible, not just undocumented) — with
// aliasValue injected into whichever path or query parameter r.RequestParam names, then plucks
// r.ResponsePath out of the response (its first item, if the response is a list).
//
// This deliberately does NOT go through APICallBuilder: when r.Action happens to be literally "findby" (a
// common choice, since that is usually the only verb capable of a filtered lookup), APICallBuilder would
// dispatch to cli.FindBy, which self-matches the response against THIS CR's own IdentifierFields/Resource
// — exactly the hardcoded self-lookup semantics apiLookup must not reuse (it matches against aliasValue,
// an unrelated externally-supplied value, not against the reconciled CR). So this always issues the
// generic, non-matching cli.Call and interprets the raw response itself.
//
// Pagination on the lookup action is not supported yet: r.RequestParam is expected to already scope the
// server-side response to (at most) the one matching item, so a paginated action is a hard error rather
// than a guess at which page to walk.
func ResolveAPILookup(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, r *getter.APILookupResolver, aliasValue interface{}) (interface{}, error) {
	if r == nil {
		return nil, fmt.Errorf("apiLookup resolver is not configured")
	}

	verb := findVerb(clientInfo.Resource.VerbsDescription, r.Action)
	if verb == nil {
		return nil, fmt.Errorf("apiLookup action %q not found in this RestDefinition", r.Action)
	}
	if verb.Pagination != nil {
		return nil, fmt.Errorf("apiLookup does not support a paginated action (%q)", r.Action)
	}

	params, query, _, _, err := cli.RequestedParams(verb.Method, verb.Path)
	if err != nil {
		return nil, fmt.Errorf("retrieving requested params for apiLookup action %q: %w", r.Action, err)
	}

	reqConfiguration := &restclient.RequestConfiguration{
		Parameters: map[string]string{},
		Query:      map[string]string{},
		Headers:    map[string]string{},
		Cookies:    map[string]string{},
		Method:     verb.Method,
	}
	strVal := fmt.Sprintf("%v", aliasValue)
	switch {
	case params.Contains(r.RequestParam):
		reqConfiguration.Parameters[r.RequestParam] = strVal
	case query.Contains(r.RequestParam):
		reqConfiguration.Query[r.RequestParam] = strVal
	default:
		return nil, fmt.Errorf("requestParam %q is not a path or query parameter of apiLookup action %q", r.RequestParam, r.Action)
	}

	resp, err := cli.Call(ctx, &http.Client{}, verb.Path, reqConfiguration)
	if err != nil {
		return nil, fmt.Errorf("calling apiLookup action %q: %w", r.Action, err)
	}

	items, err := restclient.ExtractItemsFromResponse(resp.ResponseBody)
	if err != nil {
		return nil, fmt.Errorf("reading apiLookup response for action %q: %w", r.Action, err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("apiLookup action %q returned no items for the given alias", r.Action)
	}
	item, ok := items[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("apiLookup action %q returned a non-object item", r.Action)
	}

	pathSegments, err := pathparsing.ParsePath(r.ResponsePath)
	if err != nil || len(pathSegments) == 0 {
		return nil, fmt.Errorf("invalid responsePath %q for apiLookup action %q", r.ResponsePath, r.Action)
	}
	val, found, err := unstructured.NestedFieldNoCopy(item, pathSegments...)
	if err != nil || !found {
		return nil, fmt.Errorf("responsePath %q not found in apiLookup response for action %q", r.ResponsePath, r.Action)
	}
	return val, nil
}

// findVerb returns the VerbsDescription whose Action matches action (case-insensitively, matching
// APICallBuilder's own comparison), or nil if none does.
func findVerb(verbs []getter.VerbsDescription, action string) *getter.VerbsDescription {
	for i := range verbs {
		if strings.EqualFold(verbs[i].Action, action) {
			return &verbs[i]
		}
	}
	return nil
}
