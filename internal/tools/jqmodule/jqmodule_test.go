package jqmodule

import (
	"context"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestFetch_HTTP(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("def m: .+1; m"))
	}))
	defer srv.Close()

	r := New(nil, srv.Client())
	b, err := r.Fetch(context.Background(), srv.URL+"/mod.jq", "")
	require.NoError(t, err)
	assert.Equal(t, "def m: .+1; m", string(b))
	assert.Equal(t, 1, hits)

	// cached within TTL: no second request
	_, err = r.Fetch(context.Background(), srv.URL+"/mod.jq", "")
	require.NoError(t, err)
	assert.Equal(t, 1, hits, "second fetch served from cache")
}

func TestFetch_HTTPNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()
	r := New(nil, srv.Client())
	_, err := r.Fetch(context.Background(), srv.URL, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestFetch_ConfigMap(t *testing.T) {
	cm := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "jqmods", "namespace": "krateo"},
		"data":       map[string]interface{}{"normalize.jq": "def normalize: .name; normalize"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), cm)

	r := New(dyn, nil)
	// ownNamespace matches the ref's namespace
	b, err := r.Fetch(context.Background(), "configmap://krateo/jqmods/normalize.jq", "krateo")
	require.NoError(t, err)
	assert.Equal(t, "def normalize: .name; normalize", string(b))

	_, err = r.Fetch(context.Background(), "configmap://krateo/jqmods/missing.jq", "krateo")
	assert.Error(t, err, "missing key errors")

	_, err = r.Fetch(context.Background(), "configmap://bad-format", "krateo")
	assert.Error(t, err, "malformed ref errors")
}

func TestFetch_ConfigMap_CrossNamespaceRejected(t *testing.T) {
	cm := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"name": "secrets", "namespace": "other"},
		"data":     map[string]interface{}{"x.jq": "."},
	}}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), cm)
	r := New(dyn, nil)

	// A RestDefinition in "krateo" must not read a ConfigMap in "other".
	_, err := r.Fetch(context.Background(), "configmap://other/secrets/x.jq", "krateo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be in the RestDefinition's namespace")
}

func TestFetch_UnsupportedScheme(t *testing.T) {
	r := New(nil, nil)
	_, err := r.Fetch(context.Background(), "file:///etc/passwd", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

func TestFetch_CacheExpiryAndNegativeCache(t *testing.T) {
	var hits int
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if fail {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("m"))
	}))
	defer srv.Close()

	r := New(nil, srv.Client())
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }

	// first fetch fails and is negatively cached
	_, err := r.Fetch(context.Background(), srv.URL, "")
	require.Error(t, err)
	assert.Equal(t, 1, hits)
	// within the (short) negative TTL: not re-fetched
	_, err = r.Fetch(context.Background(), srv.URL, "")
	require.Error(t, err)
	assert.Equal(t, 1, hits, "failed fetch negatively cached")
	// after the negative TTL: re-fetched (now succeeds)
	fail = false
	now = now.Add(negativeTTL + time.Second)
	_, err = r.Fetch(context.Background(), srv.URL, "")
	require.NoError(t, err)
	assert.Equal(t, 2, hits, "re-fetched after negative TTL")
}

func TestDenyInternalAddr(t *testing.T) {
	block := func(addr string) bool { return denyInternalAddr("tcp", addr, syscall.RawConn(nil)) != nil }
	assert.True(t, block("169.254.169.254:80"), "link-local (cloud metadata) blocked")
	assert.True(t, block("127.0.0.1:8080"), "loopback blocked")
	assert.True(t, block("[::1]:443"), "ipv6 loopback blocked")
	assert.False(t, block("10.0.0.5:80"), "in-cluster RFC1918 allowed")
	assert.False(t, block("93.184.216.34:443"), "public address allowed")
}

func TestSafeHTTPClient_DeniesRedirects(t *testing.T) {
	c := safeHTTPClient()
	require.NotNil(t, c.CheckRedirect)
	err := c.CheckRedirect(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redirects are not allowed")
}
