package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/bridges/otellogrus"
	promotel "go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// otlpEndpoint resolves the OTLP/gRPC collector URL (scheme required, e.g. http://alloy:4317).
// OTEL_EXPORTER_OTLP_ENDPOINT is the standard variable; COLLECTOR_SERVICE_ADDR (host:port) is a
// legacy fallback. An empty result means telemetry is disabled.
func otlpEndpoint() string {
	if os.Getenv("ENABLE_TRACING") == "0" {
		return ""
	}
	if ep := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); ep != "" {
		return ep
	}
	if addr := os.Getenv("COLLECTOR_SERVICE_ADDR"); addr != "" {
		return "http://" + addr
	}
	return ""
}

// initTelemetry pushes traces, metrics and logs over OTLP/gRPC to the collector.
//
//   - traces:  OpenTelemetry SDK (spans come from the otelgrpc / otelhttp handlers)
//   - metrics: the Prometheus default registry (go-grpc-prometheus, Go runtime, custom metrics)
//     is bridged into OTel and pushed, so existing metric names are preserved
//   - logs:    every logger passed in gets a logrus hook that mirrors records over OTLP
//     (stdout output is unchanged)
//
// It returns a shutdown function that flushes all three pipelines.
func initTelemetry(ctx context.Context, defaultServiceName string, loggers ...*logrus.Logger) (func(context.Context) error, error) {
	endpoint := otlpEndpoint()
	if endpoint == "" {
		return func(context.Context) error { return nil }, errors.New("OTEL_EXPORTER_OTLP_ENDPOINT not set")
	}

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = defaultServiceName
	}
	instanceID := os.Getenv("HOSTNAME") // pod name in Kubernetes
	if instanceID == "" {
		instanceID, _ = os.Hostname()
	}

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
			semconv.ServiceInstanceIDKey.String(instanceID),
		),
	)
	if err != nil {
		return nil, err
	}

	// --- traces ---
	traceExp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(endpoint))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	// --- metrics (Prometheus registry bridged into OTLP) ---
	metricExp, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpointURL(endpoint))
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(metricInterval()),
			sdkmetric.WithProducer(promotel.NewMetricProducer()),
		)),
	)

	// --- logs ---
	logExp, err := otlploggrpc.New(ctx, otlploggrpc.WithEndpointURL(endpoint))
	if err != nil {
		return nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
	)
	otel.SetLoggerProvider(lp)
	for _, l := range loggers {
		l.AddHook(otellogrus.NewHook(serviceName, otellogrus.WithLoggerProvider(lp)))
	}

	return func(ctx context.Context) error {
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx), lp.Shutdown(ctx))
	}, nil
}

func metricInterval() time.Duration {
	if v := os.Getenv("OTEL_METRIC_EXPORT_INTERVAL"); v != "" {
		if ms, err := time.ParseDuration(v + "ms"); err == nil && ms > 0 {
			return ms
		}
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 15 * time.Second
}
