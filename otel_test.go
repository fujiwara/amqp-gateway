package gateway

import (
	"net/http"

	"go.opentelemetry.io/otel/trace/noop"
)

func testNewAMQPClient(url string) *AMQPClient {
	metrics, _ := newMetrics()
	tracer := noop.NewTracerProvider().Tracer("")
	return NewAMQPClient(url, tracer, metrics)
}

func testNewServeMux(client *AMQPClient) http.Handler {
	metrics, _ := newMetrics()
	tracer := noop.NewTracerProvider().Tracer("")
	return NewServeMux(client, metrics, tracer)
}
