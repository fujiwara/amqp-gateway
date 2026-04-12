package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/fujiwara/amqp-gateway"

// setupOTelProviders initializes OpenTelemetry MeterProvider and TracerProvider
// if OTEL_EXPORTER_OTLP_ENDPOINT is set.
func setupOTelProviders(ctx context.Context) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }

	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return noop, nil
	}

	protocol := os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")

	var metricExporter sdkmetric.Exporter
	var traceExporter sdktrace.SpanExporter
	var err error

	switch protocol {
	case "grpc":
		metricExporter, err = otlpmetricgrpc.New(ctx)
		if err != nil {
			return noop, fmt.Errorf("failed to create OTLP metrics exporter: %w", err)
		}
		traceExporter, err = otlptracegrpc.New(ctx)
		if err != nil {
			return noop, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
		}
	case "http/protobuf", "":
		metricExporter, err = otlpmetrichttp.New(ctx)
		if err != nil {
			return noop, fmt.Errorf("failed to create OTLP metrics exporter: %w", err)
		}
		traceExporter, err = otlptracehttp.New(ctx)
		if err != nil {
			return noop, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
		}
	default:
		return noop, fmt.Errorf("unsupported OTEL_EXPORTER_OTLP_PROTOCOL: %s", protocol)
	}

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName("amqp-gateway"),
	)

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
	)
	otel.SetMeterProvider(meterProvider)

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExporter),
	)
	otel.SetTracerProvider(tracerProvider)

	// Report OTel SDK errors as warnings via slog
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Warn("OpenTelemetry error", "error", err)
	}))

	otel.SetTextMapPropagator(propagation.TraceContext{})

	if protocol == "" {
		protocol = "http/protobuf"
	}
	slog.Debug("OpenTelemetry enabled", "protocol", protocol)

	shutdown := func(ctx context.Context) error {
		if err := tracerProvider.Shutdown(ctx); err != nil {
			slog.Error("failed to shutdown tracer provider", "error", err)
		}
		return meterProvider.Shutdown(ctx)
	}
	return shutdown, nil
}

func newTracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// Metrics holds OpenTelemetry metric instruments.
type Metrics struct {
	httpRequestsTotal   metric.Int64Counter
	httpRequestDuration metric.Float64Histogram
	publishTotal        metric.Int64Counter
	rpcTotal            metric.Int64Counter
}

func newMetrics() (*Metrics, error) {
	meter := otel.Meter(tracerName)

	httpRequests, err := meter.Int64Counter("amqp_gateway.http.requests",
		metric.WithDescription("HTTP requests total"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create http.requests counter: %w", err)
	}

	httpDuration, err := meter.Float64Histogram("amqp_gateway.http.duration",
		metric.WithDescription("HTTP request duration"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create http.duration histogram: %w", err)
	}

	publishTotal, err := meter.Int64Counter("amqp_gateway.publish.total",
		metric.WithDescription("Publish operations total"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create publish.total counter: %w", err)
	}

	rpcTotal, err := meter.Int64Counter("amqp_gateway.rpc.total",
		metric.WithDescription("RPC operations total"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create rpc.total counter: %w", err)
	}

	return &Metrics{
		httpRequestsTotal:   httpRequests,
		httpRequestDuration: httpDuration,
		publishTotal:        publishTotal,
		rpcTotal:            rpcTotal,
	}, nil
}

// amqpTableCarrier adapts amqp.Table for OTel trace propagation.
type amqpTableCarrier amqp.Table

func (c amqpTableCarrier) Get(key string) string {
	if v, ok := c[key]; ok {
		switch val := v.(type) {
		case string:
			return val
		case int:
			return strconv.Itoa(val)
		}
	}
	return ""
}

func (c amqpTableCarrier) Set(key, val string) {
	c[key] = val
}

func (c amqpTableCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// injectTraceContext injects W3C trace context into AMQP headers table.
func injectTraceContext(ctx context.Context, headers amqp.Table) {
	if headers == nil {
		return
	}
	prop := propagation.TraceContext{}
	prop.Inject(ctx, amqpTableCarrier(headers))
}

// extractTraceContext extracts W3C trace context from AMQP headers table.
func extractTraceContext(ctx context.Context, headers amqp.Table) context.Context {
	if headers == nil {
		return ctx
	}
	prop := propagation.TraceContext{}
	return prop.Extract(ctx, amqpTableCarrier(headers))
}
