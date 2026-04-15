package gateway

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestSetupLogger(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			setupLogger(level)
			handler := slog.Default().Handler()
			if handler == nil {
				t.Fatal("expected non-nil handler")
			}
		})
	}
}

func TestSetupLoggerMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { provider.Shutdown(context.Background()) })

	meter := provider.Meter(tracerName)
	logCounter, err := meter.Int64Counter("amqp_gateway.log.messages")
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	// Use otelmetrics handler directly to test counter increments
	handler := newTraceHandler(
		newMetricsHandler(jsonHandler, logCounter, slog.LevelDebug),
	)
	logger := slog.New(handler)

	logger.Info("test info message")
	logger.Warn("test warn message")
	logger.Error("test error message")

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "amqp_gateway.log.messages" {
				found = true
				sum, ok := m.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("expected Sum[int64], got %T", m.Data)
				}
				var total int64
				for _, dp := range sum.DataPoints {
					total += dp.Value
				}
				if total != 3 {
					t.Errorf("expected 3 log messages counted, got %d", total)
				}
			}
		}
	}
	if !found {
		t.Error("amqp_gateway.log.messages metric not found")
	}
}
