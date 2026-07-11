package snowplow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve(t *testing.T) {
	t.Run("builds the /call request and returns the .status api map", func(t *testing.T) {
		var gotQuery url.Values
		var gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.Query()
			gotAuth = r.Header.Get("Authorization")
			// snowplow returns the resolved RESTAction CR with a keyed .status.api map.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": map[string]any{"api": map[string]any{"repo": map[string]any{"id": 42}}},
			})
		}))
		defer srv.Close()

		c := New(srv.URL, func(ctx context.Context) (string, error) { return "jwt-123", nil })
		out, err := c.Resolve(context.Background(), ApiRef{Name: "obs", Namespace: "demo"},
			map[string]any{"name": "r1", "namespace": "demo"})
		require.NoError(t, err)

		// contract: GET /call?apiVersion=templates.krateo.io/v1&resource=restactions&name&namespace&extras
		assert.Equal(t, "templates.krateo.io/v1", gotQuery.Get("apiVersion"))
		assert.Equal(t, "restactions", gotQuery.Get("resource"))
		assert.Equal(t, "obs", gotQuery.Get("name"))
		assert.Equal(t, "demo", gotQuery.Get("namespace"))
		assert.Contains(t, gotQuery.Get("extras"), "\"name\":\"r1\"")
		assert.Equal(t, "Bearer jwt-123", gotAuth)

		api, _ := out["api"].(map[string]any)
		require.NotNil(t, api)
		assert.Contains(t, api, "repo")
	})

	t.Run("requires name and namespace", func(t *testing.T) {
		c := New("http://x", nil)
		_, err := c.Resolve(context.Background(), ApiRef{Name: "x"}, nil)
		assert.Error(t, err)
	})

	t.Run("non-200 is an error carrying the body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusBadGateway)
		}))
		defer srv.Close()
		c := New(srv.URL, nil)
		_, err := c.Resolve(context.Background(), ApiRef{Name: "a", Namespace: "b"}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "502")
	})

	t.Run("transport error does not leak the request URL (with extras)", func(t *testing.T) {
		// Point at a closed port so Do fails at the transport layer, returning a *url.Error that embeds the
		// full URL. The client must strip it so the extras (which may carry resource data) never leak.
		c := New("http://127.0.0.1:1", nil)
		_, err := c.Resolve(context.Background(), ApiRef{Name: "a", Namespace: "b"},
			map[string]any{"secret-ish": "must-not-appear"})
		require.Error(t, err)
		// The extras (which may carry resource data) live in the query string; they must be stripped. (The
		// inner dial error may still name the host:port — that is the configured service address, not data.)
		assert.NotContains(t, err.Error(), "must-not-appear", "extras must not appear in the error")
		assert.NotContains(t, err.Error(), "extras=", "the extras query param must not appear in the error")
	})

	t.Run("token error is surfaced (no request sent)", func(t *testing.T) {
		c := New("http://unused", func(ctx context.Context) (string, error) {
			return "", assert.AnError
		})
		_, err := c.Resolve(context.Background(), ApiRef{Name: "a", Namespace: "b"}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "token")
	})

	t.Run("oversized extras is rejected before any request", func(t *testing.T) {
		c := New("http://unused", nil)
		_, err := c.Resolve(context.Background(), ApiRef{Name: "a", Namespace: "b"},
			map[string]any{"spec": strings.Repeat("x", 64*1024)}) // > maxExtrasBytes
		require.Error(t, err)
		assert.Contains(t, err.Error(), "too large")
	})
}
