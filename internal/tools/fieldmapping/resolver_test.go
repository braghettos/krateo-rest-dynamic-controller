package fieldmapping

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

var secretGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}

func newFakeClientWithSecret(t *testing.T, ns, name, key, value string) *fake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{secretGVR: "SecretList"})

	sec := &unstructured.Unstructured{}
	sec.SetAPIVersion("v1")
	sec.SetKind("Secret")
	sec.SetNamespace(ns)
	sec.SetName(name)
	data := map[string]interface{}{key: base64.StdEncoding.EncodeToString([]byte(value))}
	if err := unstructured.SetNestedMap(sec.Object, data, "data"); err != nil {
		t.Fatalf("SetNestedMap: %v", err)
	}
	if _, err := dyn.Resource(secretGVR).Namespace(ns).Create(context.Background(), sec, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding secret: %v", err)
	}
	return dyn
}

func mgWithCredsRef(ns, secretName, secretKey string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{"namespace": ns, "name": "my-cr"},
			"spec": map[string]interface{}{
				"credentialsRef": map[string]interface{}{
					"name": secretName,
					"key":  secretKey,
				},
			},
		},
	}
}

func TestResolveRequestResolvers_SecretRef(t *testing.T) {
	dyn := newFakeClientWithSecret(t, "ns1", "db-creds", "password", "hunter2")
	mg := mgWithCredsRef("ns1", "db-creds", "password")

	mapping := []getter.FieldMappingItem{
		{
			InBody:           "token",
			InCustomResource: "spec.credentialsRef",
			Resolver: &getter.FieldResolver{
				Type: "secretRef",
				SecretRef: &getter.SecretRefResolver{
					NameFromCustomResource: "spec.credentialsRef.name",
					KeyFromCustomResource:  "spec.credentialsRef.key",
				},
			},
		},
	}

	resolved, err := ResolveRequestResolvers(context.Background(), dyn, mapping, mg, nil)
	if err != nil {
		t.Fatalf("ResolveRequestResolvers: %v", err)
	}
	key := ResolverKey(mapping[0])
	if resolved[key] != "hunter2" {
		t.Fatalf("expected resolved value %q, got %q", "hunter2", resolved[key])
	}
}

func TestResolveRequestResolvers_MissingSecretIsError(t *testing.T) {
	scheme := runtime.NewScheme()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{secretGVR: "SecretList"})
	mg := mgWithCredsRef("ns1", "does-not-exist", "password")

	mapping := []getter.FieldMappingItem{
		{
			InBody:           "token",
			InCustomResource: "spec.credentialsRef",
			Resolver: &getter.FieldResolver{
				Type: "secretRef",
				SecretRef: &getter.SecretRefResolver{
					NameFromCustomResource: "spec.credentialsRef.name",
					KeyFromCustomResource:  "spec.credentialsRef.key",
				},
			},
		},
	}

	_, err := ResolveRequestResolvers(context.Background(), dyn, mapping, mg, nil)
	if err == nil {
		t.Fatal("expected an error when the referenced secret does not exist")
	}
}

func TestResolveRequestResolvers_ApiLookupWithoutLookupFnIsError(t *testing.T) {
	dyn := newFakeClientWithSecret(t, "ns1", "db-creds", "password", "hunter2")
	mg := mgWithCredsRef("ns1", "db-creds", "password")

	mapping := []getter.FieldMappingItem{
		{
			InPath:           "id",
			InCustomResource: "spec.alias",
			Resolver: &getter.FieldResolver{
				Type: "apiLookup",
				ApiLookup: &getter.APILookupResolver{
					Action: "findby", RequestParam: "slug", ResponsePath: "id",
				},
			},
		},
	}

	_, err := ResolveRequestResolvers(context.Background(), dyn, mapping, mg, nil)
	if err == nil {
		t.Fatal("expected a nil lookupFn (caller doesn't support apiLookup here) to be a hard error, not a silent skip")
	}
}

func TestResolveRequestResolvers_ApiLookupDelegatesToLookupFn(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{"alias": "my-team-slug"}}}

	mapping := []getter.FieldMappingItem{
		{
			InPath:           "id",
			InCustomResource: "spec.alias",
			Resolver: &getter.FieldResolver{
				Type: "apiLookup",
				ApiLookup: &getter.APILookupResolver{
					Action: "findby", RequestParam: "slug", ResponsePath: "id",
				},
			},
		},
	}

	var gotAlias interface{}
	lookupFn := func(ctx context.Context, r *getter.APILookupResolver, aliasValue interface{}) (interface{}, error) {
		gotAlias = aliasValue
		if r.Action != "findby" {
			t.Fatalf("expected action %q to be passed through, got %q", "findby", r.Action)
		}
		return "resolved-id-123", nil
	}

	resolved, err := ResolveRequestResolvers(context.Background(), nil, mapping, mg, lookupFn)
	if err != nil {
		t.Fatalf("ResolveRequestResolvers: %v", err)
	}
	if gotAlias != "my-team-slug" {
		t.Fatalf("expected the alias read from the CR to be passed to lookupFn, got %v", gotAlias)
	}
	if resolved[ResolverKey(mapping[0])] != "resolved-id-123" {
		t.Fatalf("expected the lookupFn's result to be the resolved value, got %v", resolved)
	}
}

func TestResolveRequestResolvers_ApiLookupFnErrorPropagates(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{"alias": "my-team-slug"}}}
	mapping := []getter.FieldMappingItem{
		{
			InPath:           "id",
			InCustomResource: "spec.alias",
			Resolver: &getter.FieldResolver{
				Type: "apiLookup",
				ApiLookup: &getter.APILookupResolver{
					Action: "findby", RequestParam: "slug", ResponsePath: "id",
				},
			},
		},
	}
	lookupFn := func(ctx context.Context, r *getter.APILookupResolver, aliasValue interface{}) (interface{}, error) {
		return nil, fmt.Errorf("lookup failed")
	}

	_, err := ResolveRequestResolvers(context.Background(), nil, mapping, mg, lookupFn)
	if err == nil {
		t.Fatal("expected the lookupFn's error to propagate")
	}
}

func TestResolveRequestResolvers_NoResolversIsNilNoError(t *testing.T) {
	mg := mgWithCredsRef("ns1", "db-creds", "password")
	mapping := []getter.FieldMappingItem{
		{InBody: "name", InCustomResource: "spec.name"},
	}
	resolved, err := ResolveRequestResolvers(context.Background(), nil, mapping, mg, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("expected no resolved entries, got %v", resolved)
	}
}

func TestCollectSecretRefNames_AcrossAllVerbs(t *testing.T) {
	mg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"createSecretRef": map[string]interface{}{"name": "secret-a", "key": "k"},
				"updateSecretRef": map[string]interface{}{"name": "secret-b", "key": "k"},
			},
		},
	}
	verbs := []getter.VerbsDescription{
		{
			Action: "create",
			FieldMapping: []getter.FieldMappingItem{
				{
					InBody: "token", InCustomResource: "spec.createSecretRef",
					Resolver: &getter.FieldResolver{
						Type: "secretRef",
						SecretRef: &getter.SecretRefResolver{
							NameFromCustomResource: "spec.createSecretRef.name",
							KeyFromCustomResource:  "spec.createSecretRef.key",
						},
					},
				},
			},
		},
		{
			Action: "update",
			FieldMapping: []getter.FieldMappingItem{
				{
					InBody: "token", InCustomResource: "spec.updateSecretRef",
					Resolver: &getter.FieldResolver{
						Type: "secretRef",
						SecretRef: &getter.SecretRefResolver{
							NameFromCustomResource: "spec.updateSecretRef.name",
							KeyFromCustomResource:  "spec.updateSecretRef.key",
						},
					},
				},
			},
		},
	}

	names := CollectSecretRefNames(verbs, mg)
	if len(names) != 2 || names[0] != "secret-a" || names[1] != "secret-b" {
		t.Fatalf("expected [secret-a secret-b] (from both verbs, sorted), got %v", names)
	}
}

func TestCollectSecretRefNames_UnresolvableEntrySkipped(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{}}}
	verbs := []getter.VerbsDescription{
		{
			Action: "create",
			FieldMapping: []getter.FieldMappingItem{
				{
					InBody: "token", InCustomResource: "spec.credentialsRef",
					Resolver: &getter.FieldResolver{
						Type: "secretRef",
						SecretRef: &getter.SecretRefResolver{
							NameFromCustomResource: "spec.credentialsRef.name",
							KeyFromCustomResource:  "spec.credentialsRef.key",
						},
					},
				},
			},
		},
	}

	names := CollectSecretRefNames(verbs, mg)
	if len(names) != 0 {
		t.Fatalf("expected no names when the reference isn't resolvable yet, got %v", names)
	}
}

func TestResolverKey_StableAndDistinct(t *testing.T) {
	a := getter.FieldMappingItem{InPath: "id", InCustomResource: "spec.alias"}
	b := getter.FieldMappingItem{InQuery: "id", InCustomResource: "spec.alias"}
	if ResolverKey(a) == ResolverKey(b) {
		t.Fatal("expected different anchors to produce different keys")
	}
	if ResolverKey(a) != ResolverKey(a) {
		t.Fatal("expected ResolverKey to be deterministic")
	}
}

func TestResolveSecretRefValueNotLeakedInErrors(t *testing.T) {
	scheme := runtime.NewScheme()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{secretGVR: "SecretList"})
	mg := mgWithCredsRef("ns1", "does-not-exist", "password")

	mapping := []getter.FieldMappingItem{
		{
			InBody: "token", InCustomResource: "spec.credentialsRef",
			Resolver: &getter.FieldResolver{
				Type: "secretRef",
				SecretRef: &getter.SecretRefResolver{
					NameFromCustomResource: "spec.credentialsRef.name",
					KeyFromCustomResource:  "spec.credentialsRef.key",
				},
			},
		},
	}
	_, err := ResolveRequestResolvers(context.Background(), dyn, mapping, mg, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error must never contain a secret value: %v", err)
	}
}
