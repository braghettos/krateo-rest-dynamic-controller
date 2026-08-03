package restResources

import (
	"context"
	"fmt"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/fieldmapping"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/secretrbac"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// ensureSecretRefRBACAndResolve provisions/refreshes this CR instance's secretRef RBAC (issue #31) and
// resolves callInfo's FieldMapping resolver entries against mg, returning the map BuildCallConfig needs to
// finish assembling the request.
//
// The RBAC grant covers every verb's secretRef needs (fieldmapping.CollectSecretRefNames walks ALL verbs,
// not just the one being invoked), since EnsureSecretRole fully replaces the granted secret-name set on
// every call — scoping it to only the current verb would revoke access a different verb still needs.
//
// expectExisting is false on Create (first provisioning; a missing Role is normal) and true on Update (the
// Role should already exist from Create; if it doesn't, that is a hard error — see secretrbac.EnsureSecretRole).
func (h *handler) ensureSecretRefRBACAndResolve(ctx context.Context, clientInfo *getter.Info, mapping []getter.FieldMappingItem, mg *unstructured.Unstructured, expectExisting bool) (map[string]interface{}, error) {
	if secretNames := fieldmapping.CollectSecretRefNames(clientInfo.Resource.VerbsDescription, mg); len(secretNames) > 0 {
		gvr, err := h.pluralizer.GVKtoGVR(mg.GroupVersionKind())
		if err != nil {
			return nil, fmt.Errorf("resolving GVR for secretRef RBAC: %w", err)
		}
		roleName := secretrbac.RoleName(gvr, types.NamespacedName{Namespace: mg.GetNamespace(), Name: mg.GetName()})
		if err := secretrbac.EnsureSecretRole(ctx, h.dynamicClient, mg.GetNamespace(), roleName, secretNames, h.selfServiceAccount, expectExisting); err != nil {
			return nil, fmt.Errorf("ensuring secretRef RBAC: %w", err)
		}
	}

	resolved, err := fieldmapping.ResolveRequestResolvers(ctx, h.dynamicClient, mapping, mg)
	if err != nil {
		return nil, fmt.Errorf("resolving field mapping resolvers: %w", err)
	}
	return resolved, nil
}

// deleteSecretRefRBAC tears down this CR instance's secretRef RBAC. Unconditional and IsNotFound-tolerant
// (see secretrbac.DeleteSecretRole): teardown never depends on the RestDefinition or the external resource
// still existing, so it is safe to call even on the "RestDefinition not found" early-return path in Delete.
func (h *handler) deleteSecretRefRBAC(ctx context.Context, mg *unstructured.Unstructured) error {
	gvr, err := h.pluralizer.GVKtoGVR(mg.GroupVersionKind())
	if err != nil {
		return fmt.Errorf("resolving GVR for secretRef RBAC teardown: %w", err)
	}
	roleName := secretrbac.RoleName(gvr, types.NamespacedName{Namespace: mg.GetNamespace(), Name: mg.GetName()})
	return secretrbac.DeleteSecretRole(ctx, h.dynamicClient, mg.GetNamespace(), roleName)
}
