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
)

// after is the delay primitive, overridable in tests to keep the poll loop fast.
var after = time.After

// ExtractOperationHandle derives the operation handle from the trigger response body per the OperationRef.
// Only in="body" is supported for now: a JSONPath into the response body, optionally refined by an inline
// jq program (e.g. to parse an id out of a URL). in="header" is deferred (the Response type does not yet
// expose headers).
func ExtractOperationHandle(ctx context.Context, body map[string]interface{}, opRef getter.OperationRef) (string, error) {
	if !strings.EqualFold(opRef.In, "body") {
		return "", fmt.Errorf("operationRef.in %q is not supported yet (only 'body')", opRef.In)
	}
	segs, err := pathparsing.ParsePath(opRef.Path)
	if err != nil || len(segs) == 0 {
		return "", fmt.Errorf("invalid operationRef.path %q", opRef.Path)
	}
	val, found, err := unstructured.NestedFieldNoCopy(body, segs...)
	if err != nil || !found {
		return "", fmt.Errorf("operation handle not found at %q", opRef.Path)
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
	case int64:
		return strconv.FormatInt(t, 10), nil
	case bool:
		return strconv.FormatBool(t), nil
	default:
		return "", fmt.Errorf("operation handle at %q is not a scalar (%T)", path, v)
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
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	if cfg.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
		defer cancel()
	}

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
