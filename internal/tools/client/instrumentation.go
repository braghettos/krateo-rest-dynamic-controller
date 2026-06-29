package restclient

import (
	"context"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// meterName is the instrumentation scope for the outbound-REST metrics owned by
// rest-dynamic-controller (distinct from the runtime's unstructured_runtime.* scope).
const meterName = "github.com/krateoplatformops/rest-dynamic-controller"

// Outbound-REST metric instruments. These are registered against the GLOBAL
// MeterProvider (otel.Meter). unstructured-runtime installs a real
// MeterProvider only when OTEL_ENABLED is set; otherwise the global provider is
// the OTel no-op, so these instruments record nothing and the behavior is
// byte-identical to the un-instrumented controller (default-off).
var (
	outboundMetricsOnce sync.Once

	// rest_dynamic_controller.external.request.count — outbound REST calls made
	// to the managed external API, attributed by method/status/operation.
	outboundRequestCount metric.Int64Counter
	// rest_dynamic_controller.external.request.duration_seconds — wall-clock
	// duration of each outbound REST call (cli.Do), same attributes.
	outboundRequestDuration metric.Float64Histogram
)

// initOutboundMetrics lazily creates the outbound-REST instruments off the
// global MeterProvider. Safe to call repeatedly; the instruments are no-ops
// until the runtime sets a real provider (OTEL_ENABLED).
func initOutboundMetrics() {
	outboundMetricsOnce.Do(func() {
		meter := otel.Meter(meterName)
		outboundRequestCount, _ = meter.Int64Counter(
			"rest_dynamic_controller.external.request.count",
			metric.WithDescription("Number of outbound REST calls to the managed external API."),
		)
		outboundRequestDuration, _ = meter.Float64Histogram(
			"rest_dynamic_controller.external.request.duration_seconds",
			metric.WithDescription("Duration of outbound REST calls to the managed external API, in seconds."),
			metric.WithUnit("s"),
		)
	})
}

// instrumentTransport wraps base with otelhttp so every outbound call to the
// managed external API gets a client span and a W3C traceparent injected
// (continuing the reconcile span carried on the request context). The global
// TracerProvider + propagator are installed by unstructured-runtime; when
// tracing is disabled they are the OTel no-op, so this stays default-off and
// byte-identical. A nil base falls back to http.DefaultTransport inside
// otelhttp.
func instrumentTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base)
}

// recordOutboundCall emits the owned outbound-REST metrics for a single
// cli.Do. op is the target operation/path; statusCode is 0 on transport error.
// No-op until the global MeterProvider is real (OTEL_ENABLED).
func recordOutboundCall(ctx context.Context, method, op string, statusCode int, start time.Time) {
	initOutboundMetrics()
	if outboundRequestCount == nil || outboundRequestDuration == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("http.request.method", method),
		attribute.String("rest.operation.path", op),
	}
	if statusCode != 0 {
		attrs = append(attrs, attribute.Int("http.response.status_code", statusCode))
	}
	set := metric.WithAttributes(attrs...)
	outboundRequestCount.Add(ctx, 1, set)
	outboundRequestDuration.Record(ctx, time.Since(start).Seconds(), set)
}
