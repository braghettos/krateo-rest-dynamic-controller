package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/krateoplatformops/plumbing/env"
	"github.com/krateoplatformops/plumbing/kubeutil/event"
	"github.com/krateoplatformops/plumbing/kubeutil/eventrecorder"
	"github.com/krateoplatformops/plumbing/ptr"
	prettylog "github.com/krateoplatformops/plumbing/slogs/pretty"
	"github.com/krateoplatformops/unstructured-runtime/pkg/controller/builder"
	"github.com/krateoplatformops/unstructured-runtime/pkg/logging"
	"github.com/krateoplatformops/unstructured-runtime/pkg/metrics/server"
	"github.com/krateoplatformops/unstructured-runtime/pkg/telemetry"

	restResources "github.com/krateoplatformops/rest-dynamic-controller/internal/controllers"
	getter "github.com/krateoplatformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateoplatformops/unstructured-runtime/pkg/controller"
	"github.com/krateoplatformops/unstructured-runtime/pkg/pluralizer"
	"github.com/krateoplatformops/unstructured-runtime/pkg/workqueue"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	serviceName               = "rest-dynamic-controller"
	defaultOtelExportInterval = 30 * time.Second
)

var (
	Build string
)

func main() {
	// Flags
	kubeconfig := flag.String("kubeconfig",
		env.String("KUBECONFIG", ""),
		"absolute path to the kubeconfig file")
	debug := flag.Bool("debug",
		env.Bool("REST_CONTROLLER_DEBUG", false),
		"dump verbose output")
	prettyJSONDebug := flag.Bool("pretty-json-debug",
		env.Bool("REST_CONTROLLER_PRETTY_JSON_DEBUG", false),
		"globally enable pretty-print JSON formatting in HTTP debug output (response bodies)")
	workers := flag.Int("workers",
		env.Int("REST_CONTROLLER_WORKERS", 5),
		"number of workers")
	resyncInterval := flag.Duration("resync-interval",
		env.Duration("REST_CONTROLLER_RESYNC_INTERVAL", time.Minute*3),
		"resync interval")
	resourceGroup := flag.String("group",
		env.String("REST_CONTROLLER_GROUP", ""),
		"resource api group")
	resourceVersion := flag.String("version",
		env.String("REST_CONTROLLER_VERSION", ""),
		"resource api version")
	resourceName := flag.String("resource",
		env.String("REST_CONTROLLER_RESOURCE", ""),
		"resource plural name")
	namespace := flag.String("namespace",
		env.String("REST_CONTROLLER_NAMESPACE", ""),
		"namespace to watch, empty for all namespaces")
	maxErrorRetryInterval := flag.Duration("max-error-retry-interval",
		env.Duration("REST_CONTROLLER_MAX_ERROR_RETRY_INTERVAL", 90*time.Second),
		"The maximum interval between retries when an error occurs. This should be less than the half of the resync interval.")
	minErrorRetryInterval := flag.Duration("min-error-retry-interval",
		env.Duration("REST_CONTROLLER_MIN_ERROR_RETRY_INTERVAL", 1*time.Second),
		"The minimum interval between retries when an error occurs. This should be less than max-error-retry-interval.")
	maxErrorRetry := flag.Int("max-error-retries",
		env.Int("REST_CONTROLLER_MAX_ERROR_RETRIES", 5),
		"How many times to retry the processing of a resource when an error occurs before giving up and dropping the resource.")
	metricsServerPort := flag.Int("metrics-server-port",
		env.Int("REST_CONTROLLER_METRICS_SERVER_PORT", 0),
		"The address to bind the metrics server to. If empty, metrics server is disabled.")
	prettyLog := flag.Bool("pretty-log",
		env.Bool("REST_CONTROLLER_PRETTY_LOG", false),
		"emit human-readable colored logs instead of OTel-JSON logs (development only).")
	otelEnabled := flag.Bool("otel-enabled",
		env.Bool("OTEL_ENABLED", false),
		"Enable OTLP metrics export for provider-runtime reconcile telemetry.")
	otelTracingEnabled := flag.Bool("otel-tracing-enabled",
		env.Bool("OTEL_TRACING_ENABLED", false),
		"Enable OTLP trace export (distributed reconcile traces).")
	otelServiceName := flag.String("otel-service-name",
		env.String("OTEL_SERVICE_NAME", serviceName),
		"The service name attached to exported OTLP metrics/traces.")
	otelExportInterval := flag.Duration("otel-export-interval",
		env.Duration("OTEL_EXPORT_INTERVAL", defaultOtelExportInterval),
		"The interval used to export OTLP metrics.")
	deploymentName := flag.String("deployment-name",
		env.String("DEPLOYMENT_NAME", ""),
		"The deployment name for stable resource identification in metrics.")
	serviceVersion := env.String("SERVICE_VERSION", "") // image version, stamped as service.version on metrics/traces

	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		flag.PrintDefaults()
	}

	flag.Parse()

	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}

	// Default: JSON logs on stderr in the OTel log model (RFC3339Nano "timestamp",
	// SeverityText + SeverityNumber, trace_id/span_id when a reconcile span is in
	// context), compatible with logs-ingester. The shared handler lives in
	// unstructured-runtime (pkg/logging) so every dynamic controller is consistent;
	// "service" is kept alongside the OTel "service.name". The legacy human-readable
	// prettylog handler is retained behind --pretty-log for local development.
	var slogHandler slog.Handler
	if *prettyLog {
		slogHandler = prettylog.New(&slog.HandlerOptions{
			Level:     logLevel,
			AddSource: false,
		},
			prettylog.WithDestinationWriter(os.Stderr),
			prettylog.WithColor(),
			prettylog.WithOutputEmptyAttrs(),
		)
	} else {
		slogHandler = logging.NewOTelJSONHandler(logLevel, os.Stderr,
			slog.String("service.name", serviceName),
			slog.String("service", serviceName),
		)
	}

	log := logging.NewLogrLogger(logr.FromSlogHandler(slog.New(slogHandler).Handler()))

	// Kubernetes configuration
	var cfg *rest.Config
	var err error
	if len(*kubeconfig) > 0 {
		cfg, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		cfg, err = builder.GetConfig()
	}
	if err != nil {
		log.Error(err, "Building kubeconfig.")
		os.Exit(1)
	}

	var handler controller.ExternalClient

	pluralizer := pluralizer.New()
	var swg getter.Getter
	swg, err = getter.Dynamic(cfg, pluralizer)
	if err != nil {
		log.Error(err, "Creating definition getter.")
		os.Exit(1)
	}

	log.WithValues("build", Build).
		WithValues("debug", *debug).
		WithValues("prettyJSONDebug", *prettyJSONDebug).
		WithValues("resyncInterval", *resyncInterval).
		WithValues("group", *resourceGroup).
		WithValues("version", *resourceVersion).
		WithValues("resource", *resourceName).
		WithValues("namespace", *namespace).
		WithValues("maxErrorRetryInterval", *maxErrorRetryInterval).
		WithValues("minErrorRetryInterval", *minErrorRetryInterval).
		WithValues("maxErrorRetry", *maxErrorRetry).
		WithValues("workers", *workers).
		WithValues("prettyLog", *prettyLog).
		WithValues("otelEnabled", *otelEnabled).
		WithValues("otelTracingEnabled", *otelTracingEnabled).
		WithValues("otelServiceName", *otelServiceName).
		WithValues("otelExportInterval", *otelExportInterval).
		WithValues("deploymentName", *deploymentName).
		Info("Starting.", "serviceName", serviceName)

	ctx, cancel := signal.NotifyContext(context.Background(), []os.Signal{
		os.Interrupt,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGKILL,
		syscall.SIGHUP,
		syscall.SIGQUIT,
	}...)
	defer cancel()

	// OpenTelemetry: everything is gated OFF by default and is a no-op when the
	// OTEL_* env/flags are unset — no exporter, no provider swap, byte-identical
	// behavior. Tag this controller's telemetry resource with the GVR it manages so
	// metrics/traces are attributable to a specific dynamic CR type.
	gvrAttr := fmt.Sprintf("krateo.io/rest-gvr=%s/%s/%s", *resourceGroup, *resourceVersion, *resourceName)
	if existing := os.Getenv("OTEL_RESOURCE_ATTRIBUTES"); existing != "" {
		gvrAttr = existing + "," + gvrAttr
	}
	os.Setenv("OTEL_RESOURCE_ATTRIBUTES", gvrAttr)

	telemetryConfig := telemetry.Config{
		Enabled:        *otelEnabled,
		TracingEnabled: *otelTracingEnabled,
		ServiceName:    *otelServiceName,
		ExportInterval: *otelExportInterval,
		DeploymentName: *deploymentName,
		Version:        serviceVersion,
	}

	// OTLP reconcile-throughput metrics (provider_runtime.reconcile.* /
	// controller_reconcile_*). Disabled config returns a no-op recorder + shutdown.
	telemetryMetrics, telemetryShutdown, err := telemetry.Setup(context.Background(), log, telemetryConfig)
	if err != nil {
		log.Error(err, "Cannot initialize OpenTelemetry metrics")
		os.Exit(1)
	}
	defer telemetryShutdown(context.Background())

	// Distributed reconcile traces (gated OTEL_TRACING_ENABLED). Installs the W3C
	// propagator even when disabled so an inbound traceparent is honored; returns a
	// no-op shutdown otherwise.
	tracingShutdown, err := telemetry.SetupTracing(context.Background(), log, telemetryConfig)
	if err != nil {
		log.Error(err, "Cannot initialize OpenTelemetry tracing")
		os.Exit(1)
	}
	defer tracingShutdown(context.Background())

	// Kubernetes Event recorder for the reconciled dynamic CR (success/error
	// transitions on Create/Update/Delete).
	rec, err := eventrecorder.Create(ctx, cfg, serviceName, nil)
	if err != nil {
		log.Error(err, "Creating event recorder.")
		os.Exit(1)
	}
	apiRecorder := event.NewAPIRecorder(rec)

	handler = restResources.NewHandler(cfg, log, swg, *pluralizer, *prettyJSONDebug)
	if handler == nil {
		log.Error(fmt.Errorf("handler is nil"), "Creating handler for controller.")
		os.Exit(1)
	}
	if h, ok := handler.(interface {
		SetEventRecorder(event.Recorder)
	}); ok {
		h.SetEventRecorder(apiRecorder)
	}

	opts := []builder.FuncOption{
		builder.WithLogger(log),
		builder.WithNamespace(*namespace),
		builder.WithResyncInterval(*resyncInterval),
		builder.WithGlobalRateLimiter(workqueue.NewExponentialTimedFailureRateLimiter[any](*minErrorRetryInterval, *maxErrorRetryInterval)),
		builder.WithMaxRetries(*maxErrorRetry),
		builder.WithTelemetryMetrics(telemetryMetrics),
	}

	metricsServerBindAddress := ""
	if ptr.Deref(metricsServerPort, 0) != 0 {
		log.Info("Metrics server enabled", "bindAddress", fmt.Sprintf(":%d", *metricsServerPort))
		metricsServerBindAddress = fmt.Sprintf(":%d", *metricsServerPort)
		opts = append(opts, builder.WithMetrics(server.Options{
			BindAddress: metricsServerBindAddress,
		}))
	} else {
		log.Info("Metrics server disabled")
	}

	controller, err := builder.Build(ctx, builder.Configuration{
		Config: cfg,
		GVR: schema.GroupVersionResource{
			Group:    *resourceGroup,
			Version:  *resourceVersion,
			Resource: *resourceName,
		},
		ProviderName: serviceName,
	}, opts...)
	if err != nil {
		log.Error(err, "Building controller.")
		os.Exit(1)
	}
	controller.SetExternalClient(handler)

	err = controller.Run(ctx, *workers)
	if err != nil {
		log.Error(err, "Running controller.")
		os.Exit(1)
	}
}
