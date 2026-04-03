package gateway

import (
	"net/http"

	"go.opentelemetry.io/otel/trace/noop"
)

func testNewAMQPClient(url string) *AMQPClient {
	metrics, _ := newMetrics()
	tracer := noop.NewTracerProvider().Tracer("")
	cfg := &Config{AMQPURL: url}
	return NewAMQPClient(cfg, tracer, metrics)
}

func testNewServeMux(client *AMQPClient) http.Handler {
	return NewServeMux(client, nil)
}
