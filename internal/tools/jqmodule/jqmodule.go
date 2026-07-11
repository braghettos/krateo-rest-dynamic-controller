// Package jqmodule resolves JQProgram module references (configmap://<ns>/<name>/<key> or http(s)://<url>)
// to their jq source text, with a small TTL cache so a module is not re-fetched on every reconcile. The
// definitiongetter uses it to materialize JQProgram.Ref into JQProgram.Inline at load time, so the jq
// consumers (fieldmapping, notFoundBody, async operationRef) only ever deal with inline source.
package jqmodule

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	defaultTTL     = 5 * time.Minute
	negativeTTL    = 30 * time.Second // failed fetches are cached briefly so a broken ref isn't retried every reconcile
	fetchTimeout   = 15 * time.Second // per-fetch bound on the reconcile hot path
	maxModuleBytes = 1 << 20          // 1 MiB — jq modules are small; bound a hostile/oversized source.
)

type cacheEntry struct {
	data      []byte
	err       error
	fetchedAt time.Time
}

// Resolver fetches jq module sources by reference, caching them for a TTL.
type Resolver struct {
	kube dynamic.Interface
	http *http.Client
	ttl  time.Duration

	now   func() time.Time // injectable clock for tests
	mu    sync.Mutex
	cache map[string]cacheEntry
}

// New returns a Resolver. httpClient may be nil (defaults to an SSRF-guarded client — see safeHTTPClient).
// kube is required for configmap:// references.
func New(kube dynamic.Interface, httpClient *http.Client) *Resolver {
	if httpClient == nil {
		httpClient = safeHTTPClient()
	}
	return &Resolver{
		kube:  kube,
		http:  httpClient,
		ttl:   defaultTTL,
		now:   time.Now,
		cache: map[string]cacheEntry{},
	}
}

// safeHTTPClient returns an http client hardened against SSRF via jq module refs: it refuses redirects (so a
// benign URL cannot 302 into an internal endpoint) and blocks connections to loopback and link-local
// addresses — the cloud-metadata (169.254.169.254 / fe80::) and localhost targets — at connect time on the
// resolved IP (so a hostname resolving there is caught too). In-cluster hosts (RFC1918/service CIDRs) remain
// reachable so http module hosting still works; use configmap:// for stricter tenant isolation.
func safeHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: denyInternalAddr}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("redirects are not allowed for jq module fetches")
		},
	}
}

// denyInternalAddr blocks a connection to a loopback or link-local address at connect time (on the already
// resolved ip:port), the primary SSRF targets.
func denyInternalAddr(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parsing dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("unresolved dial host %q", host)
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("jq module fetch to internal address %s is blocked", ip)
	}
	return nil
}

// Fetch returns the jq source text for a module reference (cached for the resolver's TTL; failures cached
// briefly). ownNamespace, when non-empty, restricts a configmap:// reference to that namespace (the
// RestDefinition's own namespace) so a definition cannot read ConfigMaps from other namespaces. Supported
// schemes: configmap://<namespace>/<name>/<key> and http(s)://<url>.
func (r *Resolver) Fetch(ctx context.Context, ref, ownNamespace string) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("jq module resolver not configured")
	}
	// cache key includes ownNamespace so the same ref under different policies never cross-serves.
	key := ownNamespace + "\x00" + ref
	r.mu.Lock()
	if e, ok := r.cache[key]; ok {
		ttl := r.ttl
		if e.err != nil {
			ttl = negativeTTL
		}
		if r.now().Sub(e.fetchedAt) < ttl {
			r.mu.Unlock()
			return e.data, e.err
		}
	}
	r.mu.Unlock()

	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	data, err := r.fetch(fctx, ref, ownNamespace)

	r.mu.Lock()
	r.cache[key] = cacheEntry{data: data, err: err, fetchedAt: r.now()}
	r.mu.Unlock()
	return data, err
}

func (r *Resolver) fetch(ctx context.Context, ref, ownNamespace string) ([]byte, error) {
	switch {
	case strings.HasPrefix(ref, "http://"), strings.HasPrefix(ref, "https://"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request for jq module %q: %w", ref, err)
		}
		resp, err := r.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching jq module %q: %w", ref, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetching jq module %q: status %d", ref, resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, maxModuleBytes))
	case strings.HasPrefix(ref, "configmap://"):
		if r.kube == nil {
			return nil, fmt.Errorf("configmap jq module %q requires a kube client", ref)
		}
		parts := strings.Split(strings.TrimPrefix(ref, "configmap://"), "/")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return nil, fmt.Errorf("invalid configmap jq module ref %q: want configmap://<namespace>/<name>/<key>", ref)
		}
		ns, name, key := parts[0], parts[1], parts[2]
		if ownNamespace != "" && ns != ownNamespace {
			return nil, fmt.Errorf("configmap jq module %q must be in the RestDefinition's namespace %q, not %q", ref, ownNamespace, ns)
		}
		uns, err := r.kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).
			Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("getting configmap for jq module %q: %w", ref, err)
		}
		data, found, err := unstructured.NestedString(uns.Object, "data", key)
		if err != nil || !found {
			return nil, fmt.Errorf("key %q not found in configmap %s/%s (jq module %q)", key, ns, name, ref)
		}
		return []byte(data), nil
	default:
		return nil, fmt.Errorf("unsupported jq module ref %q (want configmap:// or http(s)://)", ref)
	}
}
