package fieldmapping

import (
	"context"
	"fmt"
	"sort"

	"github.com/krateoplatformops/plumbing/kubeutil/secretref"
	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/pathparsing"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// ResolverKey returns a stable key identifying a FieldMappingItem's resolver entry. It is a content key
// (the entry's anchor + source path), not a slice index, so it stays correct even if the caller resolves
// against one copy of a FieldMapping slice and applies against another.
func ResolverKey(m getter.FieldMappingItem) string {
	return m.InPath + "\x00" + m.InQuery + "\x00" + m.InBody + "\x00" + m.InCustomResource
}

// APILookupFunc resolves an apiLookup Resolver against an alias value already read from the CR. It is
// injected by the caller (internal/controllers) rather than called directly from this package: apiLookup
// needs builder.APICallBuilder and a restclient.UnstructuredClientInterface, and fieldmapping cannot
// import builder without an import cycle (builder already imports fieldmapping, for ApplyAlias). A nil
// APILookupFunc means the caller does not support apiLookup at this call site; an apiLookup entry then
// fails with a clear error rather than silently sending the request without its value.
type APILookupFunc func(ctx context.Context, r *getter.APILookupResolver, aliasValue interface{}) (interface{}, error)

// ResolveRequestResolvers resolves every request-direction FieldMapping entry's Resolver against the
// given CR instance, returning a map from ResolverKey to the resolved value. Entries without a Resolver
// are absent from the result. secretRef is resolved directly; apiLookup is delegated to lookupFn. Any
// other resolver type, or an apiLookup entry when lookupFn is nil, is a hard error — an unresolvable entry
// must never be silently skipped here, since that would send the request without the value it was
// supposed to carry.
//
// Resolution is cached per call, keyed by content (the secret's name+key; the lookup's action+requestParam
// +alias) rather than by ResolverKey: two different FieldMapping entries that happen to reference the same
// secret or resolve the same alias trigger exactly one Secret read / one lookup call, not one per entry.
// The cache does not persist across calls (a fresh ResolveRequestResolvers call per Create/Update/Delete),
// so it never serves a stale value across reconciles.
func ResolveRequestResolvers(ctx context.Context, dyn dynamic.Interface, mapping []getter.FieldMappingItem, mg *unstructured.Unstructured, lookupFn APILookupFunc) (map[string]interface{}, error) {
	if len(mapping) == 0 {
		return nil, nil
	}
	out := make(map[string]interface{})
	secretCache := make(map[string]string)
	lookupCache := make(map[string]interface{})

	for _, m := range mapping {
		if m.Resolver == nil {
			continue
		}
		if m.InPath == "" && m.InQuery == "" && m.InBody == "" {
			continue // response-direction entry; CRD-level validation already forbids resolver here
		}
		switch m.Resolver.Type {
		case "secretRef":
			r := m.Resolver.SecretRef
			if r == nil {
				return nil, fmt.Errorf("secretRef resolver is not configured (field %q)", m.InCustomResource)
			}
			name, err := readStringPath(mg, r.NameFromCustomResource)
			if err != nil {
				return nil, fmt.Errorf("reading secret name from %q: %w", r.NameFromCustomResource, err)
			}
			key, err := readStringPath(mg, r.KeyFromCustomResource)
			if err != nil {
				return nil, fmt.Errorf("reading secret key from %q: %w", r.KeyFromCustomResource, err)
			}
			cacheKey := name + "\x00" + key
			val, cached := secretCache[cacheKey]
			if !cached {
				val, err = secretref.GetSecretValue(ctx, dyn, mg.GetNamespace(), name, key)
				if err != nil {
					return nil, fmt.Errorf("resolving secretRef for field %q: %w", m.InCustomResource, err)
				}
				secretCache[cacheKey] = val
			}
			out[ResolverKey(m)] = val
		case "apiLookup":
			if lookupFn == nil {
				return nil, fmt.Errorf("apiLookup resolver requested but not supported at this call site (field %q)", m.InCustomResource)
			}
			r := m.Resolver.ApiLookup
			if r == nil {
				return nil, fmt.Errorf("apiLookup resolver is not configured (field %q)", m.InCustomResource)
			}
			aliasVal, err := readAnyPath(mg, m.InCustomResource)
			if err != nil {
				return nil, fmt.Errorf("reading apiLookup alias for field %q: %w", m.InCustomResource, err)
			}
			cacheKey := fmt.Sprintf("%s\x00%s\x00%v", r.Action, r.RequestParam, aliasVal)
			val, cached := lookupCache[cacheKey]
			if !cached {
				val, err = lookupFn(ctx, r, aliasVal)
				if err != nil {
					return nil, fmt.Errorf("resolving apiLookup for field %q: %w", m.InCustomResource, err)
				}
				lookupCache[cacheKey] = val
			}
			out[ResolverKey(m)] = val
		default:
			return nil, fmt.Errorf("resolver type %q is not supported (field %q)", m.Resolver.Type, m.InCustomResource)
		}
	}
	return out, nil
}

// CollectSecretRefNames returns the deduped, sorted set of Secret names every secretRef resolver, across
// ALL of the resource's verbs (not just the one currently being invoked), currently resolves to for this
// CR instance. The RBAC self-provisioning grant must cover every verb's need: EnsureSecretRole fully
// replaces the granted secret-name set on each call, so scoping this to only the current verb would drop
// access another verb still needs (e.g. an Update call would revoke a secret only Create's resolvers use).
// An entry whose name path is not yet resolvable on this instance is skipped rather than failing the
// whole collection — a genuinely required-but-missing secretRef surfaces later, with a field-specific
// error, when ResolveRequestResolvers actually resolves it for the verb being invoked.
func CollectSecretRefNames(verbs []getter.VerbsDescription, mg *unstructured.Unstructured) []string {
	seen := map[string]struct{}{}
	for _, verb := range verbs {
		for _, m := range verb.FieldMapping {
			if m.Resolver == nil || m.Resolver.Type != "secretRef" || m.Resolver.SecretRef == nil {
				continue
			}
			name, err := readStringPath(mg, m.Resolver.SecretRef.NameFromCustomResource)
			if err != nil {
				continue
			}
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// readAnyPath reads the raw value at path, whatever its JSON type (an apiLookup alias need not be a
// string — e.g. a numeric id used to look up another numeric id).
func readAnyPath(mg *unstructured.Unstructured, path string) (interface{}, error) {
	segments, err := pathparsing.ParsePath(path)
	if err != nil || len(segments) == 0 {
		return nil, fmt.Errorf("invalid path %q", path)
	}
	val, found, err := unstructured.NestedFieldNoCopy(mg.Object, segments...)
	if err != nil || !found {
		return nil, fmt.Errorf("path %q not found on the custom resource", path)
	}
	return val, nil
}

func readStringPath(mg *unstructured.Unstructured, path string) (string, error) {
	segments, err := pathparsing.ParsePath(path)
	if err != nil || len(segments) == 0 {
		return "", fmt.Errorf("invalid path %q", path)
	}
	val, found, err := unstructured.NestedFieldNoCopy(mg.Object, segments...)
	if err != nil || !found {
		return "", fmt.Errorf("path %q not found on the custom resource", path)
	}
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("path %q is not a string", path)
	}
	return s, nil
}
