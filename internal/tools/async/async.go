// Package async implements the runtime side of the RestDefinition async/LRO engine (design #241, Model A).
//
// After a mutating trigger call returns an operation handle, PollUntilTerminal drives a blocking poll of an
// operations endpoint until it reaches a terminal status, turning an asynchronous API into a synchronous
// reconcile. This file holds the two pure, unit-testable cores — handle extraction and the poll loop; the
// controller wiring (issuing the actual poll HTTP call and the optional post-get) is layered on top.
package async

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/jqengine"
	"github.com/krateoplatformops/rest-dynamic-controller/internal/tools/pathparsing"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	defaultInterval    = 1 * time.Second
	defaultMaxAttempts = 10

	// Upper bounds so an operator-set (or misconfigured) poll budget cannot pin a reconcile worker
	// for an unreasonable duration. maxTotalTimeout caps the whole poll loop even when TimeoutSeconds
	// is unset; a single poll request is additionally bounded by RequestTimeout at the call site.
	maxInterval     = 60 * time.Second
	maxAttemptsCap  = 120
	maxTotalTimeout = 30 * time.Minute

	// RequestTimeout bounds a single poll (or post-get) HTTP request so a black-hole endpoint that
	// accepts the connection but never sends response headers cannot block a reconcile worker. Exported
	// for the controller wiring that constructs the poll http.Client.
	RequestTimeout = 30 * time.Second
)

// after is the delay primitive, overridable in tests to keep the poll loop fast.
var after = time.After

// ExtractOperationHandle derives the operation handle from the trigger response per the OperationRef.
//
//   - in="body": a JSONPath into the response body (map), optionally refined by an inline jq program.
//   - in="header": a response header name (e.g. Operation-Location on a 202 Accepted), optionally refined
//     by jq to parse an id out of a URL. Header lookup is case-insensitive (http.Header canonicalization).
//
// In both cases an optional inline jq program refines the raw value (e.g. `capture("operations/(?<id>[^?]+)").id`
// to pull an operationId out of a URL). The poll itself still targets the OAS-declared poll path with the
// extracted handle bound to {operationId}; polling an absolute URL taken verbatim from a header is future work.
func ExtractOperationHandle(ctx context.Context, body map[string]interface{}, headers http.Header, opRef getter.OperationRef) (string, error) {
	var val interface{}
	switch strings.ToLower(opRef.In) {
	case "body":
		segs, err := pathparsing.ParsePath(opRef.Path)
		if err != nil || len(segs) == 0 {
			return "", fmt.Errorf("invalid operationRef.path %q", opRef.Path)
		}
		v, found, err := unstructured.NestedFieldNoCopy(body, segs...)
		if err != nil || !found {
			return "", fmt.Errorf("operation handle not found at %q", opRef.Path)
		}
		val = v
	case "header":
		if headers == nil {
			return "", fmt.Errorf("operationRef.in=header but the trigger response carried no headers")
		}
		raw := headers.Get(opRef.Path)
		if raw == "" {
			return "", fmt.Errorf("operation handle header %q not found or empty", opRef.Path)
		}
		val = raw
	default:
		return "", fmt.Errorf("operationRef.in %q is not supported (want 'body' or 'header')", opRef.In)
	}

	if opRef.JQ != nil && opRef.JQ.Inline != "" {
		prog, cErr := jqengine.Compile(opRef.JQ.Inline)
		if cErr != nil {
			return "", fmt.Errorf("compiling operationRef jq: %w", cErr)
		}
		out, rErr := prog.Run(ctx, val)
		if rErr != nil {
			return "", fmt.Errorf("running operationRef jq: %w", rErr)
		}
		val = out
	}

	return scalarToString(val, opRef.Path)
}

// scalarToString renders a JSON scalar handle to a clean string (avoiding fmt's exponential form for
// whole-number floats, which JSON decoding produces).
func scalarToString(v interface{}, path string) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", fmt.Errorf("operation handle at %q is null", path)
	case string:
		if t == "" {
			return "", fmt.Errorf("operation handle at %q is empty", path)
		}
		return t, nil
	case float64:
		if t == math.Trunc(t) && !math.IsInf(t, 0) {
			return strconv.FormatInt(int64(t), 10), nil
		}
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(t), nil
	case int32:
		return strconv.FormatInt(int64(t), 10), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case bool:
		return strconv.FormatBool(t), nil
	default:
		return "", fmt.Errorf("operation handle at %q is not a scalar (%T)", path, v)
	}
}

// RenderScalar renders a JSON scalar to a clean string for comparison, avoiding fmt's exponential form for
// whole-number floats (which JSON decoding produces) so a numeric status such as 200 compares as "200"
// rather than "2e+02". Non-scalars fall back to fmt's default formatting.
func RenderScalar(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == math.Trunc(t) && !math.IsInf(t, 0) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// PollFunc performs a single poll for the given operation and returns the observed status value.
type PollFunc func(ctx context.Context, operationID string) (status string, err error)

// PollUntilTerminal polls (blocking) until the operation status reaches a success or failure value, or the
// attempt/timeout budget is exhausted. It polls first, then waits between attempts, so an already-terminal
// operation returns without delay. Status comparison is case-insensitive.
func PollUntilTerminal(ctx context.Context, cfg getter.PollConfig, operationID string, poll PollFunc) error {
	interval := time.Duration(cfg.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = defaultInterval
	}
	if interval > maxInterval {
		interval = maxInterval
	}
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	if maxAttempts > maxAttemptsCap {
		maxAttempts = maxAttemptsCap
	}

	// Always bound the overall duration, even when TimeoutSeconds is unset: the reconcile ctx may carry
	// no deadline, and this ctx deadline also cancels an in-flight poll request, so a stuck (or
	// misconfigured) operation can never pin a worker beyond the budget. When unset, derive a budget from
	// the (clamped) attempt plan, including per-attempt request time.
	total := time.Duration(cfg.TimeoutSeconds) * time.Second
	if total <= 0 {
		total = time.Duration(maxAttempts)*(interval+RequestTimeout) + interval
	}
	if total > maxTotalTimeout {
		total = maxTotalTimeout
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, total)
	defer cancel()

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("polling operation %q cancelled: %w", operationID, err)
		}

		status, err := poll(ctx, operationID)
		if err != nil {
			return fmt.Errorf("polling operation %q: %w", operationID, err)
		}
		if containsFold(cfg.SuccessValues, status) {
			return nil
		}
		if containsFold(cfg.FailureValues, status) {
			return fmt.Errorf("operation %q reached terminal failure status %q", operationID, status)
		}

		// Not terminal yet: wait before the next attempt (unless this was the last).
		if attempt < maxAttempts-1 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("polling operation %q cancelled: %w", operationID, ctx.Err())
			case <-after(interval):
			}
		}
	}
	return fmt.Errorf("operation %q did not reach a terminal status within %d attempts", operationID, maxAttempts)
}

func containsFold(vals []string, s string) bool {
	for _, v := range vals {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
