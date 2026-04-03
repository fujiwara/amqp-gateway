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
