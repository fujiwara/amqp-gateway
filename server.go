package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
)

// RunServer starts the HTTP server.
func RunServer(ctx context.Context, cfg *Config) error {
	metrics, err := newMetrics()
	if err != nil {
		return fmt.Errorf("failed to create metrics: %w", err)
	}
	tracer := newTracer()

	client := NewAMQPClient(cfg, tracer, metrics)
	defer client.Close()
	mux := NewServeMux(client, cfg.Aliases)

	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("starting server", "addr", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// NewServeMux creates the HTTP handler with all routes.
func NewServeMux(client *AMQPClient, aliases []AliasConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/publish", handlePublish(client))
	mux.HandleFunc("POST /v1/rpc", handleRPC(client))
	mux.HandleFunc("GET /healthz", handleHealthz())
	mux.HandleFunc("GET /readyz", handleReadyz(client))
	for _, a := range aliases {
		mux.HandleFunc("POST "+a.Path, handleAlias(client, a))
	}
	return otelhttp.NewHandler(accessLog(mux, client.metrics), "amqp-gateway")
}

// statusResponseWriter wraps http.ResponseWriter to capture the status code.
type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func accessLog(next http.Handler, metrics *Metrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		elapsed := time.Since(start)

		ctx := r.Context()

		// Record metrics
		metricAttrs := metric.WithAttributes(
			attribute.String("method", r.Method),
			attribute.String("path", r.URL.Path),
		)
		metrics.httpRequestsTotal.Add(ctx, 1, metric.WithAttributes(
			attribute.String("method", r.Method),
			attribute.String("path", r.URL.Path),
			attribute.String("status", strconv.Itoa(sw.status)),
		))
		metrics.httpRequestDuration.Record(ctx, elapsed.Seconds(), metricAttrs)

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration", elapsed.String(),
			"remote_addr", r.RemoteAddr,
		}
		if user, _, ok := parseBasicAuth(r); ok {
			attrs = append(attrs, "user", user)
		}
		if v := r.Header.Get(headerVHost); v != "" {
			attrs = append(attrs, "vhost", v)
		}
		if v := r.Header.Get(headerExchange); v != "" {
			attrs = append(attrs, "exchange", v)
		}
		if v := r.Header.Get(headerRoutingKey); v != "" {
			attrs = append(attrs, "routing_key", v)
		}
		slog.InfoContext(ctx, "access", attrs...)
	})
}

func handlePublish(client *AMQPClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params, err := parseRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if params == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		if err := client.Publish(r.Context(), params, body); err != nil {
			writeAMQPError(w, err)
			return
		}

		w.WriteHeader(http.StatusAccepted)
	}
}

func handleRPC(client *AMQPClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params, err := parseRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if params == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		msg, err := client.RPC(r.Context(), params, body)
		if err != nil {
			writeAMQPError(w, err)
			return
		}

		writeRPCResponse(r.Context(), w, msg)
	}
}

func handleAlias(client *AMQPClient, alias AliasConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Start with alias defaults
		params := alias.toPublishParams()

		// Allow HTTP request headers to override
		if user, pass, ok := parseBasicAuth(r); ok {
			params.Username = user
			params.Password = pass
		}
		if params.Username == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		applyHeaderOverrides(r, params)

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		switch alias.Method {
		case AliasMethodPublish:
			if err := client.Publish(r.Context(), params, body); err != nil {
				writeAMQPError(w, err)
				return
			}
			writeAliasResponse(w, alias.Response, http.StatusAccepted)
		case AliasMethodRPC:
			msg, err := client.RPC(r.Context(), params, body)
			if err != nil {
				writeAMQPError(w, err)
				return
			}
			if alias.Response != nil {
				writeAliasResponse(w, alias.Response, http.StatusOK)
			} else {
				writeRPCResponse(r.Context(), w, msg)
			}
		}
	}
}

// applyHeaderOverrides overrides PublishParams fields from HTTP request headers if present.
func applyHeaderOverrides(r *http.Request, p *PublishParams) {
	if v := r.Header.Get(headerExchange); v != "" {
		p.Exchange = v
	}
	if v := r.Header.Get(headerRoutingKey); v != "" {
		p.RoutingKey = v
	}
	if v := r.Header.Get(headerVHost); v != "" {
		p.VHost = v
	}
	if v := r.Header.Get(headerMessageID); v != "" {
		p.MessageID = v
	}
	if v := r.Header.Get(headerCorrelation); v != "" {
		p.CorrelationID = v
	}
	if v := r.Header.Get(headerExpiration); v != "" {
		p.Expiration = v
	}
	if v := r.Header.Get("Content-Type"); v != "" {
		p.ContentType = v
	}
	// Inject any custom AMQP-Header-* from the request
	for key, values := range r.Header {
		if headerName, ok := strings.CutPrefix(key, headerCustomPrefix); ok && headerName != "" {
			p.Headers[headerName] = values[0]
		}
	}
}

func handleHealthz() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	}
}

func handleReadyz(client *AMQPClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := client.Ping(r.Context()); err != nil {
			http.Error(w, "RabbitMQ unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	}
}

// parseRequest parses Basic auth and AMQP headers from the request.
// Returns nil params (no error) if auth is missing.
func parseRequest(r *http.Request) (*PublishParams, error) {
	username, password, ok := parseBasicAuth(r)
	if !ok {
		return nil, nil
	}
	params, err := ParsePublishParams(r)
	if err != nil {
		return nil, err
	}
	params.Username = username
	params.Password = password
	return params, nil
}

func parseBasicAuth(r *http.Request) (string, string, bool) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", "", false
	}
	const prefix = "Basic "
	if !strings.HasPrefix(auth, prefix) {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func writeAMQPError(w http.ResponseWriter, err error) {
	var timeoutErr *TimeoutError
	if errors.As(err, &timeoutErr) {
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}

	var amqpErr *amqp.Error
	if errors.As(err, &amqpErr) {
		switch amqpErr.Code {
		case amqp.AccessRefused:
			if strings.Contains(err.Error(), "username or password") {
				http.Error(w, err.Error(), http.StatusUnauthorized)
			} else {
				http.Error(w, err.Error(), http.StatusForbidden)
			}
			return
		case amqp.NotFound:
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
	}

	slog.Error("AMQP error", "error", err)
	http.Error(w, "RabbitMQ unavailable", http.StatusServiceUnavailable)
}

func writeAliasResponse(w http.ResponseWriter, resp *AliasResponseConfig, defaultStatus int) {
	status := defaultStatus
	if resp != nil && resp.Status != 0 {
		status = resp.Status
	}
	if resp != nil && resp.Body != nil {
		ct := resp.ContentType
		if strings.HasPrefix(ct, "text/") {
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(status)
			fmt.Fprint(w, resp.Body)
		} else {
			if ct == "" {
				ct = "application/json"
			}
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(resp.Body)
		}
		return
	}
	if resp != nil && resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}
	w.WriteHeader(status)
}

func writeRPCResponse(ctx context.Context, w http.ResponseWriter, msg *amqp.Delivery) {
	// Set AMQP response headers
	if msg.ContentType != "" {
		w.Header().Set("Content-Type", msg.ContentType)
	}
	if msg.MessageId != "" {
		w.Header().Set(headerMessageID, msg.MessageId)
	}
	if msg.CorrelationId != "" {
		w.Header().Set(headerCorrelation, msg.CorrelationId)
	}
	if msg.Expiration != "" {
		w.Header().Set(headerExpiration, msg.Expiration)
	}

	// Set custom headers from AMQP headers table
	for key, value := range msg.Headers {
		w.Header().Set(headerCustomPrefix+key, fmt.Sprintf("%v", value))
	}

	// Propagate trace context from AMQP response to HTTP response
	if msg.Headers != nil {
		respCtx := extractTraceContext(ctx, amqp.Table(msg.Headers))
		prop := propagation.TraceContext{}
		prop.Inject(respCtx, propagation.HeaderCarrier(w.Header()))
	}

	w.WriteHeader(http.StatusOK)
	w.Write(msg.Body)
}
