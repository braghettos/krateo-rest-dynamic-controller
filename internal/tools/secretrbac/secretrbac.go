// Package secretrbac self-provisions the per-CR-instance, least-privilege RBAC (issue #31) that lets
// this controller read exactly the Secrets a reconciled resource's secretRef fields reference — a
// namespace-scoped Role naming those Secrets via resourceNames, bound to this controller's own
// ServiceAccount. It is a thin orchestration layer over plumbing's dynamic.Interface-based primitives
// (objectclient, rbacgen): this package owns the naming scheme, the "already provisioned" drift check
// (decision D), and the Role+RoleBinding pairing; the object shapes and the create-or-update mechanics
// live in plumbing so oasgen-provider's auth-secret RBAC can reuse the same code.
package secretrbac

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/krateo-platformops/plumbing/kubeutil/objectclient"
	"github.com/krateo-platformops/plumbing/kubeutil/rbacgen"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

var (
	roleGVR    = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}
	bindingGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}
)

// secretVerbs are the only verbs granted: reading a Secret's value never requires create/update/delete.
var secretVerbs = []string{"get", "list", "watch"}

// RoleName is deterministic and namespace-unique for a CR instance:
// <plural>-<version>-<fnv32a(namespace/name)>-secrets. The hash suffix (not the raw CR name) keeps the
// result within Kubernetes' 253-char name limit regardless of CR name length, and is recomputed
// identically every reconcile — there is no generated name to store or look up.
func RoleName(gvr schema.GroupVersionResource, cr types.NamespacedName) string {
	h := fnv.New32a()
	h.Write([]byte(cr.Namespace + "/" + cr.Name))
	return fmt.Sprintf("%s-%s-%x-secrets", gvr.Resource, gvr.Version, h.Sum32())
}

// EnsureSecretRole creates or updates the Role+RoleBinding pair named roleName in namespace ns, scoping
// read access to exactly secretNames, bound to selfSA.
//
// expectExisting distinguishes first-provisioning (Create, false: a missing Role is normal — this is the
// first reconcile that resolves a secretRef) from "this object was already provisioned and is expected to
// still be here" (Update, true). When expectExisting is true and the Role is missing, EnsureSecretRole
// returns a hard error rather than silently recreating it: the RBAC having disappeared between Create and
// Update means something outside this controller's control removed it, which the caller should surface as
// a reconcile failure (decision D), not paper over.
func EnsureSecretRole(ctx context.Context, dyn dynamic.Interface, ns, roleName string, secretNames []string, selfSA types.NamespacedName, expectExisting bool) error {
	if expectExisting {
		if _, err := objectclient.Get(ctx, dyn, roleGVR, ns, roleName); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("secretRef RBAC Role %s/%s is missing but was expected to already exist (removed out-of-band?)", ns, roleName)
			}
			return fmt.Errorf("checking existing secretRef RBAC Role %s/%s: %w", ns, roleName, err)
		}
	}

	role, err := toUnstructured(rbacgen.BuildRoleForSecret(ns, roleName, secretNames, secretVerbs))
	if err != nil {
		return fmt.Errorf("converting secretRef RBAC Role %s/%s: %w", ns, roleName, err)
	}
	if err := objectclient.Apply(ctx, dyn, roleGVR, role, objectclient.ApplyOptions{FieldManager: "rest-dynamic-controller"}); err != nil {
		return fmt.Errorf("applying secretRef RBAC Role %s/%s: %w", ns, roleName, err)
	}

	binding, err := toUnstructured(rbacgen.BuildRoleBindingForRole(ns, roleName, roleName, selfSA))
	if err != nil {
		return fmt.Errorf("converting secretRef RBAC RoleBinding %s/%s: %w", ns, roleName, err)
	}
	if err := objectclient.Apply(ctx, dyn, bindingGVR, binding, objectclient.ApplyOptions{FieldManager: "rest-dynamic-controller"}); err != nil {
		return fmt.Errorf("applying secretRef RBAC RoleBinding %s/%s: %w", ns, roleName, err)
	}
	return nil
}

// DeleteSecretRole tears down the Role+RoleBinding pair named roleName in namespace ns. It is
// unconditional and IsNotFound-tolerant: teardown of this controller's own self-granted RBAC never
// depends on whether the pair was ever successfully provisioned.
func DeleteSecretRole(ctx context.Context, dyn dynamic.Interface, ns, roleName string) error {
	if err := objectclient.Uninstall(ctx, dyn, bindingGVR, ns, roleName, objectclient.UninstallOptions{}); err != nil {
		return fmt.Errorf("deleting secretRef RBAC RoleBinding %s/%s: %w", ns, roleName, err)
	}
	if err := objectclient.Uninstall(ctx, dyn, roleGVR, ns, roleName, objectclient.UninstallOptions{}); err != nil {
		return fmt.Errorf("deleting secretRef RBAC Role %s/%s: %w", ns, roleName, err)
	}
	return nil
}

// toUnstructured converts a typed rbacv1 object (Role/RoleBinding, whose TypeMeta rbacgen already sets)
// into the *unstructured.Unstructured shape objectclient's dynamic-client calls require.
func toUnstructured(obj interface{}) (*unstructured.Unstructured, error) {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: raw}, nil
}
