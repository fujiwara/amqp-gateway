package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestAliasPublish(t *testing.T) {
	rmqURL := requireRabbitMQ(t)
	queueName := fmt.Sprintf("test-alias-publish-%d", time.Now().UnixNano())
	setupTestQueue(t, rmqURL, queueName)

	client := testNewAMQPClient(rmqURL)
	aliases := []AliasConfig{
		{
			Path:       "/api/send",
			Method:     "publish",
			Username:   "guest",
			Password:   "guest",
			RoutingKey: queueName,
		},
	}
	mux := NewServeMux(client, aliases)

	req := httptest.NewRequest("POST", "/api/send", strings.NewReader(`{"msg":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d, body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	msg := consumeOne(t, rmqURL, queueName, 5*time.Second)
	if string(msg.Body) != `{"msg":"hello"}` {
		t.Errorf("body: got %q", string(msg.Body))
	}
	if msg.ContentType != "application/json" {
		t.Errorf("content_type: got %q", msg.ContentType)
	}
}

func TestAliasRPC(t *testing.T) {
	rmqURL := requireRabbitMQ(t)
	queueName := fmt.Sprintf("test-alias-rpc-%d", time.Now().UnixNano())
	setupTestQueue(t, rmqURL, queueName)

	// RPC responder
	rpcReady := make(chan struct{})
	go func() {
		conn, err := amqp.Dial(rmqURL)
		if err != nil {
			t.Errorf("rpc responder: dial: %v", err)
			return
		}
		defer conn.Close()
		ch, err := conn.Channel()
		if err != nil {
			t.Errorf("rpc responder: channel: %v", err)
			return
		}
		defer ch.Close()
		msgs, err := ch.Consume(queueName, "", true, false, false, false, nil)
		if err != nil {
			t.Errorf("rpc responder: consume: %v", err)
			return
		}
		close(rpcReady)
		msg, ok := <-msgs
		if !ok {
			return
		}
		reply := amqp.Publishing{
			CorrelationId: msg.CorrelationId,
			ContentType:   "text/plain",
			Body:          []byte("reply:" + string(msg.Body)),
		}
		ch.PublishWithContext(t.Context(), "", msg.ReplyTo, false, false, reply)
	}()
	<-rpcReady

	client := testNewAMQPClient(rmqURL)
	aliases := []AliasConfig{
		{
			Path:       "/api/lookup",
			Method:     "rpc",
			Username:   "guest",
			Password:   "guest",
			RoutingKey: queueName,
			Timeout:    5 * time.Second,
		},
	}
	mux := NewServeMux(client, aliases)

	req := httptest.NewRequest("POST", "/api/lookup", strings.NewReader("ping"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	respBody, _ := io.ReadAll(w.Result().Body)
	if string(respBody) != "reply:ping" {
		t.Errorf("body: got %q, want %q", string(respBody), "reply:ping")
	}
}

func TestAliasHeaderOverride(t *testing.T) {
	rmqURL := requireRabbitMQ(t)
	overrideQueue := fmt.Sprintf("test-alias-override-%d", time.Now().UnixNano())
	setupTestQueue(t, rmqURL, overrideQueue)

	client := testNewAMQPClient(rmqURL)
	aliases := []AliasConfig{
		{
			Path:       "/api/flexible",
			Method:     "publish",
			Username:   "guest",
			Password:   "guest",
			RoutingKey: "default-queue",
		},
	}
	mux := NewServeMux(client, aliases)

	// Override routing key via HTTP header
	req := httptest.NewRequest("POST", "/api/flexible", strings.NewReader("test"))
	req.Header.Set("Amqp-Routing-Key", overrideQueue)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d, body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	msg := consumeOne(t, rmqURL, overrideQueue, 5*time.Second)
	if string(msg.Body) != "test" {
		t.Errorf("body: got %q", string(msg.Body))
	}
}

func TestAliasNoCredentials(t *testing.T) {
	rmqURL := requireRabbitMQ(t)
	queueName := fmt.Sprintf("test-alias-noauth-%d", time.Now().UnixNano())
	setupTestQueue(t, rmqURL, queueName)

	client := testNewAMQPClient(rmqURL)
	aliases := []AliasConfig{
		{
			Path:       "/api/open",
			Method:     "publish",
			RoutingKey: queueName,
		},
	}
	mux := NewServeMux(client, aliases)

	// Without Basic auth → 401
	req := httptest.NewRequest("POST", "/api/open", strings.NewReader("no-auth"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no auth: got %d, want %d", w.Code, http.StatusUnauthorized)
	}

	// With Basic auth → 202
	req = httptest.NewRequest("POST", "/api/open", strings.NewReader("with-auth"))
	req.Header.Set("Authorization", testBasicAuth("guest", "guest"))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("with auth: got %d, want %d, body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}
}

func TestAliasCustomResponse(t *testing.T) {
	rmqURL := requireRabbitMQ(t)
	queueName := fmt.Sprintf("test-alias-response-%d", time.Now().UnixNano())
	setupTestQueue(t, rmqURL, queueName)

	client := testNewAMQPClient(rmqURL)

	t.Run("custom status and body", func(t *testing.T) {
		aliases := []AliasConfig{
			{
				Path:       "/api/custom",
				Method:     "publish",
				Username:   "guest",
				Password:   "guest",
				RoutingKey: queueName,
				Response: &AliasResponseConfig{
					Status: http.StatusOK,
					Body:   map[string]string{"result": "ok"},
				},
			},
		}
		mux := NewServeMux(client, aliases)

		req := httptest.NewRequest("POST", "/api/custom", strings.NewReader("test"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status: got %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type: got %q, want application/json", ct)
		}
		want := `{"result":"ok"}`
		if got := strings.TrimSpace(w.Body.String()); got != want {
			t.Errorf("body: got %q, want %q", got, want)
		}
	})

	t.Run("custom status only", func(t *testing.T) {
		aliases := []AliasConfig{
			{
				Path:       "/api/status-only",
				Method:     "publish",
				Username:   "guest",
				Password:   "guest",
				RoutingKey: queueName,
				Response: &AliasResponseConfig{
					Status: http.StatusCreated,
				},
			},
		}
		mux := NewServeMux(client, aliases)

		req := httptest.NewRequest("POST", "/api/status-only", strings.NewReader("test"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("status: got %d, want %d", w.Code, http.StatusCreated)
		}
		if w.Body.Len() != 0 {
			t.Errorf("body: got %q, want empty", w.Body.String())
		}
	})

	t.Run("body only (default status)", func(t *testing.T) {
		aliases := []AliasConfig{
			{
				Path:       "/api/body-only",
				Method:     "publish",
				Username:   "guest",
				Password:   "guest",
				RoutingKey: queueName,
				Response: &AliasResponseConfig{
					Body: []string{"queued"},
				},
			},
		}
		mux := NewServeMux(client, aliases)

		req := httptest.NewRequest("POST", "/api/body-only", strings.NewReader("test"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusAccepted {
			t.Fatalf("status: got %d, want %d", w.Code, http.StatusAccepted)
		}
		want := `["queued"]`
		if got := strings.TrimSpace(w.Body.String()); got != want {
			t.Errorf("body: got %q, want %q", got, want)
		}
	})

	t.Run("text content type", func(t *testing.T) {
		aliases := []AliasConfig{
			{
				Path:       "/api/text",
				Method:     "publish",
				Username:   "guest",
				Password:   "guest",
				RoutingKey: queueName,
				Response: &AliasResponseConfig{
					ContentType: "text/plain",
					Body:        "accepted",
				},
			},
		}
		mux := NewServeMux(client, aliases)

		req := httptest.NewRequest("POST", "/api/text", strings.NewReader("test"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusAccepted {
			t.Fatalf("status: got %d, want %d", w.Code, http.StatusAccepted)
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/plain" {
			t.Errorf("content-type: got %q, want text/plain", ct)
		}
		if got := w.Body.String(); got != "accepted" {
			t.Errorf("body: got %q, want %q", got, "accepted")
		}
	})

	t.Run("custom content type for json", func(t *testing.T) {
		aliases := []AliasConfig{
			{
				Path:       "/api/custom-ct",
				Method:     "publish",
				Username:   "guest",
				Password:   "guest",
				RoutingKey: queueName,
				Response: &AliasResponseConfig{
					ContentType: "application/vnd.api+json",
					Body:        map[string]string{"status": "ok"},
				},
			},
		}
		mux := NewServeMux(client, aliases)

		req := httptest.NewRequest("POST", "/api/custom-ct", strings.NewReader("test"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		if w.Code != http.StatusAccepted {
			t.Fatalf("status: got %d, want %d", w.Code, http.StatusAccepted)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/vnd.api+json" {
			t.Errorf("content-type: got %q, want application/vnd.api+json", ct)
		}
		want := `{"status":"ok"}`
		if got := strings.TrimSpace(w.Body.String()); got != want {
			t.Errorf("body: got %q, want %q", got, want)
		}
	})
}

func TestAliasConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		aliases []AliasConfig
		wantErr bool
	}{
		{
			name: "valid",
			aliases: []AliasConfig{
				{Path: "/api/send", Method: "publish", Username: "u", Password: "p"},
			},
		},
		{
			name: "missing path",
			aliases: []AliasConfig{
				{Method: "publish", Username: "u", Password: "p"},
			},
			wantErr: true,
		},
		{
			name: "invalid method",
			aliases: []AliasConfig{
				{Path: "/api/send", Method: "consume", Username: "u", Password: "p"},
			},
			wantErr: true,
		},
		{
			name: "no credentials (valid, requires Basic auth at request time)",
			aliases: []AliasConfig{
				{Path: "/api/send", Method: "publish"},
			},
		},
		{
			name: "username without password",
			aliases: []AliasConfig{
				{Path: "/api/send", Method: "publish", Username: "u"},
			},
			wantErr: true,
		},
		{
			name: "password without username",
			aliases: []AliasConfig{
				{Path: "/api/send", Method: "publish", Password: "p"},
			},
			wantErr: true,
		},
		{
			name: "duplicate path",
			aliases: []AliasConfig{
				{Path: "/api/send", Method: "publish", Username: "u", Password: "p"},
				{Path: "/api/send", Method: "rpc", Username: "u", Password: "p"},
			},
			wantErr: true,
		},
		{
			name: "valid response config",
			aliases: []AliasConfig{
				{Path: "/api/send", Method: "publish", Username: "u", Password: "p", Response: &AliasResponseConfig{Status: 200, Body: "ok"}},
			},
		},
		{
			name: "invalid response status",
			aliases: []AliasConfig{
				{Path: "/api/send", Method: "publish", Username: "u", Password: "p", Response: &AliasResponseConfig{Status: 999}},
			},
			wantErr: true,
		},
		{
			name: "text content type with string body",
			aliases: []AliasConfig{
				{Path: "/api/send", Method: "publish", Username: "u", Password: "p", Response: &AliasResponseConfig{ContentType: "text/plain", Body: "ok"}},
			},
		},
		{
			name: "text content type with non-string body",
			aliases: []AliasConfig{
				{Path: "/api/send", Method: "publish", Username: "u", Password: "p", Response: &AliasResponseConfig{ContentType: "text/html", Body: 123}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				AMQPURL: "amqp://localhost:5672",
				Aliases: tt.aliases,
			}
			cfg.applyDefaults()
			err := cfg.validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
