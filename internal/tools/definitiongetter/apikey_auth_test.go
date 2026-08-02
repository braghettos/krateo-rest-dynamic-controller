package getter

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
)

var apiKeySecretGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

func seedSecret(t *testing.T, ns, name, key, value string) dynamic.Interface {
	t.Helper()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{apiKeySecretGVR: "SecretList"})
	sec := &unstructured.Unstructured{}
	sec.SetAPIVersion("v1")
	sec.SetKind("Secret")
	sec.SetNamespace(ns)
	sec.SetName(name)
	require.NoError(t, unstructured.SetNestedMap(sec.Object,
		map[string]interface{}{key: base64.StdEncoding.EncodeToString([]byte(value))}, "data"))
	_, err := dyn.Resource(apiKeySecretGVR).Namespace(ns).Create(context.Background(), sec, metav1.CreateOptions{})
	require.NoError(t, err)
	return dyn
}

func apiKeyAuthMethods(extra map[string]interface{}) map[string]interface{} {
	m := map[string]interface{}{
		"tokenRef": map[string]interface{}{"name": "creds", "namespace": "demo", "key": "token"},
	}
	for k, v := range extra {
		m[k] = v
	}
	return map[string]interface{}{"apiKey": m}
}

// TestParseAuthentication_APIKey covers oasgen-provider#49: an OAS `type: apiKey, in: header` scheme sends
// the credential verbatim in a header the document names. Before this, apiKey produced no auth block at all
// and every request went out unauthenticated.
func TestParseAuthentication_APIKey(t *testing.T) {
	dyn := seedSecret(t, "demo", "creds", "token", "s3cr3t")

	t.Run("credential is sent verbatim in the declared header", func(t *testing.T) {
		info := &Info{}
		require.NoError(t, parseAuthentication(apiKeyAuthMethods(map[string]interface{}{"header": "X-Api-Key"}), dyn, info))
		req, _ := http.NewRequest("GET", "http://x/", nil)
		info.SetAuth(req)
		assert.Equal(t, "s3cr3t", req.Header.Get("X-Api-Key"), "no prefix by default — apiKey means send this value")
		assert.Empty(t, req.Header.Get("Authorization"), "must not assume Authorization")
	})

	t.Run("valuePrefix is prepended when declared", func(t *testing.T) {
		info := &Info{}
		require.NoError(t, parseAuthentication(apiKeyAuthMethods(map[string]interface{}{
			"header": "Authorization", "valuePrefix": "Bearer ",
		}), dyn, info))
		req, _ := http.NewRequest("GET", "http://x/", nil)
		info.SetAuth(req)
		assert.Equal(t, "Bearer s3cr3t", req.Header.Get("Authorization"))
	})

	t.Run("a Secret already holding the full value needs no prefix — no doubling", func(t *testing.T) {
		d := seedSecret(t, "demo", "creds", "token", "Bearer already-prefixed")
		info := &Info{}
		require.NoError(t, parseAuthentication(apiKeyAuthMethods(map[string]interface{}{"header": "Authorization"}), d, info))
		req, _ := http.NewRequest("GET", "http://x/", nil)
		info.SetAuth(req)
		assert.Equal(t, "Bearer already-prefixed", req.Header.Get("Authorization"),
			"defaulting a prefix would have produced 'Bearer Bearer ...'")
	})

	t.Run("empty header is an error, not a guess at Authorization", func(t *testing.T) {
		for _, h := range []interface{}{"", "   "} {
			info := &Info{}
			err := parseAuthentication(apiKeyAuthMethods(map[string]interface{}{"header": h}), dyn, info)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "missing header in apiKey auth")
		}
	})

	t.Run("missing tokenRef is an error", func(t *testing.T) {
		info := &Info{}
		err := parseAuthentication(map[string]interface{}{"apiKey": map[string]interface{}{"header": "X-Api-Key"}}, dyn, info)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing tokenRef in apiKey auth")
	})

	t.Run("bearer is unaffected", func(t *testing.T) {
		info := &Info{}
		require.NoError(t, parseAuthentication(map[string]interface{}{
			"bearer": map[string]interface{}{"tokenRef": map[string]interface{}{"name": "creds", "namespace": "demo", "key": "token"}},
		}, dyn, info))
		req, _ := http.NewRequest("GET", "http://x/", nil)
		info.SetAuth(req)
		assert.Equal(t, "Bearer s3cr3t", req.Header.Get("Authorization"))
	})
}
