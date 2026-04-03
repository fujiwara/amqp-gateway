package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// RunServer starts the HTTP server.
func RunServer(ctx context.Context, cfg *Config) error {
	client := NewAMQPClient(cfg.RabbitMQURL)
	mux := NewServeMux(client)

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
		return server.Close()
	case err := <-errCh:
		return err
	}
}

// NewServeMux creates the HTTP handler with all routes.
func NewServeMux(client *AMQPClient) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/publish", handlePublish(client))
	mux.HandleFunc("POST /v1/rpc", handleRPC(client))
	mux.HandleFunc("GET /healthz", handleHealthz())
	mux.HandleFunc("GET /readyz", handleReadyz(client))
	return accessLog(mux)
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

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration", time.Since(start).String(),
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
		slog.Info("access", attrs...)
	})
}

func handlePublish(client *AMQPClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := parseBasicAuth(r)
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		params, err := ParsePublishParams(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		if err := client.Publish(r.Context(), username, password, params, body); err != nil {
			writeAMQPError(w, err)
			return
		}

		w.WriteHeader(http.StatusAccepted)
	}
}

func handleRPC(client *AMQPClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := parseBasicAuth(r)
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		params, err := ParsePublishParams(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		msg, err := client.RPC(r.Context(), username, password, params, body)
		if err != nil {
			writeAMQPError(w, err)
			return
		}

		writeRPCResponse(w, msg)
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

	var unroutableErr *UnroutableError
	if errors.As(err, &unroutableErr) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	var amqpErr *amqp.Error
	if errors.As(err, &amqpErr) {
		switch amqpErr.Code {
		case amqp.AccessRefused:
			// AccessRefused at connection level = auth failure (401)
			// AccessRefused at channel level = permission denied (403)
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

func writeRPCResponse(w http.ResponseWriter, msg *amqp.Delivery) {
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

	w.WriteHeader(http.StatusOK)
	w.Write(msg.Body)
}
