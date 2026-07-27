package secretrbac

import (
	"context"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
)

func newFakeClient(t *testing.T) *fake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		roleGVR:    "RoleList",
		bindingGVR: "RoleBindingList",
	}
	return fake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
}

var selfSA = types.NamespacedName{Namespace: "rdc-system", Name: "rest-dynamic-controller"}

func TestRoleNameDeterministicAndDistinct(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "widgets.example.io", Version: "v1alpha1", Resource: "widgets"}
	a := types.NamespacedName{Namespace: "ns1", Name: "my-widget"}
	b := types.NamespacedName{Namespace: "ns1", Name: "other-widget"}

	if RoleName(gvr, a) != RoleName(gvr, a) {
		t.Fatal("expected RoleName to be deterministic for the same input")
	}
	if RoleName(gvr, a) == RoleName(gvr, b) {
		t.Fatal("expected distinct CR instances to get distinct role names")
	}
	if !strings.HasSuffix(RoleName(gvr, a), "-secrets") {
		t.Fatalf("expected role name to end in -secrets, got %q", RoleName(gvr, a))
	}
	if !strings.HasPrefix(RoleName(gvr, a), "widgets-v1alpha1-") {
		t.Fatalf("expected role name to start with <plural>-<version>-, got %q", RoleName(gvr, a))
	}
}

func TestEnsureSecretRoleFirstProvisioning(t *testing.T) {
	dyn := newFakeClient(t)
	ctx := context.Background()

	err := EnsureSecretRole(ctx, dyn, "ns1", "widget-abc-secrets", []string{"db-creds"}, selfSA, false)
	if err != nil {
		t.Fatalf("EnsureSecretRole: %v", err)
	}

	role, err := dyn.Resource(roleGVR).Namespace("ns1").Get(ctx, "widget-abc-secrets", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected Role to be created: %v", err)
	}
	var typedRole rbacv1.Role
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(role.Object, &typedRole); err != nil {
		t.Fatalf("converting Role from unstructured: %v", err)
	}
	if len(typedRole.Rules) != 1 || len(typedRole.Rules[0].ResourceNames) != 1 || typedRole.Rules[0].ResourceNames[0] != "db-creds" {
		t.Fatalf("unexpected Role rules: %+v", typedRole.Rules)
	}

	binding, err := dyn.Resource(bindingGVR).Namespace("ns1").Get(ctx, "widget-abc-secrets", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected RoleBinding to be created: %v", err)
	}
	var typedBinding rbacv1.RoleBinding
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(binding.Object, &typedBinding); err != nil {
		t.Fatalf("converting RoleBinding from unstructured: %v", err)
	}
	if len(typedBinding.Subjects) != 1 || typedBinding.Subjects[0].Name != selfSA.Name || typedBinding.Subjects[0].Namespace != selfSA.Namespace {
		t.Fatalf("unexpected RoleBinding subjects: %+v", typedBinding.Subjects)
	}
}

func TestEnsureSecretRoleUpdatesSecretNameSet(t *testing.T) {
	dyn := newFakeClient(t)
	ctx := context.Background()

	if err := EnsureSecretRole(ctx, dyn, "ns1", "widget-abc-secrets", []string{"db-creds"}, selfSA, false); err != nil {
		t.Fatalf("initial EnsureSecretRole: %v", err)
	}
	if err := EnsureSecretRole(ctx, dyn, "ns1", "widget-abc-secrets", []string{"api-token"}, selfSA, true); err != nil {
		t.Fatalf("update EnsureSecretRole: %v", err)
	}

	role, err := dyn.Resource(roleGVR).Namespace("ns1").Get(ctx, "widget-abc-secrets", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var typedRole rbacv1.Role
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(role.Object, &typedRole); err != nil {
		t.Fatalf("converting Role: %v", err)
	}
	names := typedRole.Rules[0].ResourceNames
	if len(names) != 1 || names[0] != "api-token" {
		t.Fatalf("expected the secret-name set to be fully replaced to [api-token], got %v", names)
	}
}

func TestEnsureSecretRoleHardFailsWhenExpectedButMissing(t *testing.T) {
	dyn := newFakeClient(t)
	ctx := context.Background()

	err := EnsureSecretRole(ctx, dyn, "ns1", "widget-abc-secrets", []string{"db-creds"}, selfSA, true)
	if err == nil {
		t.Fatal("expected a hard error when expectExisting=true but the Role was never provisioned")
	}

	if _, getErr := dyn.Resource(roleGVR).Namespace("ns1").Get(ctx, "widget-abc-secrets", metav1.GetOptions{}); getErr == nil {
		t.Fatal("expected no Role to have been created as a side effect of the failed call")
	}
}

func TestDeleteSecretRoleRemovesBoth(t *testing.T) {
	dyn := newFakeClient(t)
	ctx := context.Background()

	if err := EnsureSecretRole(ctx, dyn, "ns1", "widget-abc-secrets", []string{"db-creds"}, selfSA, false); err != nil {
		t.Fatalf("EnsureSecretRole: %v", err)
	}

	if err := DeleteSecretRole(ctx, dyn, "ns1", "widget-abc-secrets"); err != nil {
		t.Fatalf("DeleteSecretRole: %v", err)
	}

	if _, err := dyn.Resource(roleGVR).Namespace("ns1").Get(ctx, "widget-abc-secrets", metav1.GetOptions{}); err == nil {
		t.Fatal("expected Role to be gone")
	}
	if _, err := dyn.Resource(bindingGVR).Namespace("ns1").Get(ctx, "widget-abc-secrets", metav1.GetOptions{}); err == nil {
		t.Fatal("expected RoleBinding to be gone")
	}
}

func TestDeleteSecretRoleIdempotentWhenNeverProvisioned(t *testing.T) {
	dyn := newFakeClient(t)
	if err := DeleteSecretRole(context.Background(), dyn, "ns1", "never-provisioned"); err != nil {
		t.Fatalf("expected teardown of never-provisioned RBAC to succeed, got %v", err)
	}
}
