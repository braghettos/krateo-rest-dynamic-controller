package async

import (
	"context"
	"net/http"
	"testing"
	"time"

	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastAfter replaces the poll-loop delay so tests don't actually sleep.
func fastAfter(t *testing.T) {
	t.Helper()
	orig := after
	after = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	t.Cleanup(func() { after = orig })
}

func TestExtractOperationHandle(t *testing.T) {
	t.Run("body string handle", func(t *testing.T) {
		body := map[string]interface{}{"id": "op-123", "status": "queued"}
		h, err := ExtractOperationHandle(context.Background(), body, nil, getter.OperationRef{In: "body", Path: "id"})
		require.NoError(t, err)
		assert.Equal(t, "op-123", h)
	})

	t.Run("body numeric handle rendered without exponent", func(t *testing.T) {
		body := map[string]interface{}{"id": float64(2147483648)}
		h, err := ExtractOperationHandle(context.Background(), body, nil, getter.OperationRef{In: "body", Path: "id"})
		require.NoError(t, err)
		assert.Equal(t, "2147483648", h)
	})

	t.Run("jq derives id from a URL", func(t *testing.T) {
		body := map[string]interface{}{"url": "https://dev.azure.com/org/_apis/operations/abc-42?api-version=7.1"}
		ref := getter.OperationRef{In: "body", Path: "url", JQ: &getter.JQProgram{Inline: `capture("operations/(?<id>[^?]+)").id`}}
		h, err := ExtractOperationHandle(context.Background(), body, nil, ref)
		require.NoError(t, err)
		assert.Equal(t, "abc-42", h)
	})

	t.Run("missing handle errors", func(t *testing.T) {
		_, err := ExtractOperationHandle(context.Background(), map[string]interface{}{}, nil, getter.OperationRef{In: "body", Path: "id"})
		require.Error(t, err)
	})

	t.Run("header handle (case-insensitive)", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("Operation-Location", "https://dev.azure.com/org/_apis/operations/op-55")
		// Path uses a different case to prove http.Header canonicalization makes lookup case-insensitive.
		h, err := ExtractOperationHandle(context.Background(), nil, headers, getter.OperationRef{In: "header", Path: "operation-location", JQ: &getter.JQProgram{Inline: `capture("operations/(?<id>[^?]+)").id`}})
		require.NoError(t, err)
		assert.Equal(t, "op-55", h)
	})

	t.Run("header handle without jq returns the raw value", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("X-Operation-Id", "abc-99")
		h, err := ExtractOperationHandle(context.Background(), nil, headers, getter.OperationRef{In: "header", Path: "X-Operation-Id"})
		require.NoError(t, err)
		assert.Equal(t, "abc-99", h)
	})

	t.Run("missing header errors", func(t *testing.T) {
		_, err := ExtractOperationHandle(context.Background(), nil, http.Header{}, getter.OperationRef{In: "header", Path: "Operation-Location"})
		assert.ErrorContains(t, err, "not found")
	})

	t.Run("header requested but no headers present", func(t *testing.T) {
		_, err := ExtractOperationHandle(context.Background(), nil, nil, getter.OperationRef{In: "header", Path: "Operation-Location"})
		assert.ErrorContains(t, err, "no headers")
	})

	t.Run("unsupported in errors", func(t *testing.T) {
		_, err := ExtractOperationHandle(context.Background(), map[string]interface{}{}, nil, getter.OperationRef{In: "query", Path: "x"})
		assert.ErrorContains(t, err, "not supported")
	})
}

func TestScalarToString(t *testing.T) {
	// gojq emits native Go ints for whole numbers; a numeric handle must still render (not error).
	got, err := scalarToString(42, "id")
	require.NoError(t, err)
	assert.Equal(t, "42", got)

	got, err = scalarToString(int32(7), "id")
	require.NoError(t, err)
	assert.Equal(t, "7", got)

	got, err = scalarToString(int64(9), "id")
	require.NoError(t, err)
	assert.Equal(t, "9", got)
}

func TestRenderScalar(t *testing.T) {
	assert.Equal(t, "running", RenderScalar("running"))
	assert.Equal(t, "200", RenderScalar(float64(200)), "whole-number float renders without exponent")
	assert.Equal(t, "200", RenderScalar(200))
	assert.Equal(t, "true", RenderScalar(true))
	assert.Equal(t, "1.5", RenderScalar(1.5))
}

func TestPollUntilTerminal_ClampsMaxAttempts(t *testing.T) {
	fastAfter(t)
	cfg := getter.PollConfig{SuccessValues: []string{"succeeded"}, MaxAttempts: 100000}
	calls := 0
	err := PollUntilTerminal(context.Background(), cfg, "op", func(context.Context, string) (string, error) {
		calls++
		return "inProgress", nil
	})
	require.Error(t, err)
	assert.Equal(t, maxAttemptsCap, calls, "MaxAttempts is clamped to the cap so a mis-set value cannot pin a worker")
}

func TestPollUntilTerminal(t *testing.T) {
	fastAfter(t)
	cfg := getter.PollConfig{SuccessValues: []string{"succeeded"}, FailureValues: []string{"failed", "cancelled"}, MaxAttempts: 5}

	t.Run("reaches success after a few polls", func(t *testing.T) {
		seq := []string{"queued", "inProgress", "succeeded"}
		i := 0
		err := PollUntilTerminal(context.Background(), cfg, "op-1", func(context.Context, string) (string, error) {
			s := seq[i]
			i++
			return s, nil
		})
		require.NoError(t, err)
		assert.Equal(t, 3, i, "polled until the terminal success value")
	})

	t.Run("terminal failure is an error", func(t *testing.T) {
		err := PollUntilTerminal(context.Background(), cfg, "op-2", func(context.Context, string) (string, error) {
			return "failed", nil
		})
		require.Error(t, err)
		assert.ErrorContains(t, err, "terminal failure")
	})

	t.Run("never terminal exhausts attempts", func(t *testing.T) {
		calls := 0
		err := PollUntilTerminal(context.Background(), cfg, "op-3", func(context.Context, string) (string, error) {
			calls++
			return "inProgress", nil
		})
		require.Error(t, err)
		assert.ErrorContains(t, err, "did not reach a terminal status")
		assert.Equal(t, 5, calls, "polled exactly MaxAttempts times")
	})

	t.Run("case-insensitive status match", func(t *testing.T) {
		err := PollUntilTerminal(context.Background(), cfg, "op-4", func(context.Context, string) (string, error) {
			return "Succeeded", nil
		})
		require.NoError(t, err)
	})

	t.Run("cancelled context stops polling", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := PollUntilTerminal(ctx, cfg, "op-5", func(context.Context, string) (string, error) {
			return "inProgress", nil
		})
		require.Error(t, err)
		assert.ErrorContains(t, err, "cancelled")
	})
}
