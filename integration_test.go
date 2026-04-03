package gateway

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const testRabbitMQURL = "amqp://guest:guest@localhost:5672/"

// requireRabbitMQ checks RabbitMQ connectivity.
// In CI, fails the test if RabbitMQ is not reachable.
// Locally, skips the test.
func requireRabbitMQ(t *testing.T) string {
	t.Helper()
	url := os.Getenv("RABBITMQ_URL")
	if url == "" {
		url = testRabbitMQURL
	}
	conn, err := amqp.Dial(url)
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("RabbitMQ required in CI: %v", err)
		}
		t.Skipf("RabbitMQ not available, skipping: %v", err)
	}
	conn.Close()
	return url
}

func testBasicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// setupTestQueue declares a queue and binds it to the default exchange for testing.
func setupTestQueue(t *testing.T, url, queueName string) {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("failed to open channel: %v", err)
	}
	defer ch.Close()
	_, err = ch.QueueDeclare(queueName, false, true, false, false, nil)
	if err != nil {
		t.Fatalf("failed to declare queue: %v", err)
	}
}

// consumeOne consumes one message from the queue.
func consumeOne(t *testing.T, url, queueName string, timeout time.Duration) *amqp.Delivery {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("failed to open channel: %v", err)
	}
	defer ch.Close()
	msgs, err := ch.Consume(queueName, "", true, false, false, false, nil)
	if err != nil {
		t.Fatalf("failed to consume: %v", err)
	}
	select {
	case msg := <-msgs:
		return &msg
	case <-time.After(timeout):
		t.Fatal("timed out waiting for message")
		return nil
	}
}

func TestIntegrationPublish(t *testing.T) {
	rmqURL := requireRabbitMQ(t)
	queueName := fmt.Sprintf("test-publish-%d", time.Now().UnixNano())
	setupTestQueue(t, rmqURL, queueName)

	client := NewAMQPClient(rmqURL)
	mux := NewServeMux(client)

	body := `{"hello":"world"}`
	req := httptest.NewRequest("POST", "/v1/publish", strings.NewReader(body))
	req.Header.Set("Authorization", testBasicAuth("guest", "guest"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Amqp-Routing-Key", queueName)
	req.Header.Set("Amqp-Message-Id", "test-msg-001")
	req.Header.Set("Amqp-Header-X-Custom", "custom-value")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("publish status: got %d, want %d, body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	// Verify the message arrived in the queue
	msg := consumeOne(t, rmqURL, queueName, 5*time.Second)
	if string(msg.Body) != body {
		t.Errorf("body: got %q, want %q", string(msg.Body), body)
	}
	if msg.ContentType != "application/json" {
		t.Errorf("content_type: got %q, want %q", msg.ContentType, "application/json")
	}
	if msg.MessageId != "test-msg-001" {
		t.Errorf("message_id: got %q, want %q", msg.MessageId, "test-msg-001")
	}
	if msg.DeliveryMode != 2 {
		t.Errorf("delivery_mode: got %d, want 2", msg.DeliveryMode)
	}
	if v, ok := msg.Headers["X-Custom"]; !ok || fmt.Sprintf("%v", v) != "custom-value" {
		t.Errorf("header X-Custom: got %v", msg.Headers["X-Custom"])
	}
}

func TestIntegrationPublishBadAuth(t *testing.T) {
	requireRabbitMQ(t)

	client := NewAMQPClient(testRabbitMQURL)
	mux := NewServeMux(client)

	req := httptest.NewRequest("POST", "/v1/publish", strings.NewReader("test"))
	req.Header.Set("Authorization", testBasicAuth("baduser", "badpass"))
	req.Header.Set("Amqp-Routing-Key", "some-queue")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("bad auth status: got %d, want %d, body: %s", w.Code, http.StatusUnauthorized, w.Body.String())
	}
}

func TestIntegrationRPC(t *testing.T) {
	rmqURL := requireRabbitMQ(t)
	queueName := fmt.Sprintf("test-rpc-%d", time.Now().UnixNano())
	setupTestQueue(t, rmqURL, queueName)

	// Start a goroutine that acts as an RPC server:
	// consume from the queue and reply
	rpcReady := make(chan struct{})
	go func() {
		conn, err := amqp.Dial(rmqURL)
		if err != nil {
			t.Errorf("rpc server: failed to connect: %v", err)
			return
		}
		defer conn.Close()
		ch, err := conn.Channel()
		if err != nil {
			t.Errorf("rpc server: failed to open channel: %v", err)
			return
		}
		defer ch.Close()
		msgs, err := ch.Consume(queueName, "", true, false, false, false, nil)
		if err != nil {
			t.Errorf("rpc server: failed to consume: %v", err)
			return
		}
		close(rpcReady)
		msg, ok := <-msgs
		if !ok {
			return
		}
		// Echo back the body with a prefix
		reply := amqp.Publishing{
			CorrelationId: msg.CorrelationId,
			ContentType:   "text/plain",
			Body:          []byte("reply:" + string(msg.Body)),
		}
		if err := ch.PublishWithContext(
			t.Context(),
			"", msg.ReplyTo, false, false, reply,
		); err != nil {
			t.Errorf("rpc server: failed to publish reply: %v", err)
		}
	}()
	<-rpcReady

	client := NewAMQPClient(rmqURL)
	mux := NewServeMux(client)

	req := httptest.NewRequest("POST", "/v1/rpc", strings.NewReader("ping"))
	req.Header.Set("Authorization", testBasicAuth("guest", "guest"))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Amqp-Routing-Key", queueName)
	req.Header.Set("Amqp-Timeout", "5000")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("rpc status: got %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	respBody, _ := io.ReadAll(w.Result().Body)
	if string(respBody) != "reply:ping" {
		t.Errorf("rpc response body: got %q, want %q", string(respBody), "reply:ping")
	}
	if ct := w.Result().Header.Get("Content-Type"); ct != "text/plain" {
		t.Errorf("rpc response content-type: got %q, want %q", ct, "text/plain")
	}
}

func TestIntegrationRPCTimeout(t *testing.T) {
	rmqURL := requireRabbitMQ(t)
	// Use a queue with no consumer, so RPC will timeout
	queueName := fmt.Sprintf("test-rpc-timeout-%d", time.Now().UnixNano())
	setupTestQueue(t, rmqURL, queueName)

	client := NewAMQPClient(rmqURL)
	mux := NewServeMux(client)

	req := httptest.NewRequest("POST", "/v1/rpc", strings.NewReader("ping"))
	req.Header.Set("Authorization", testBasicAuth("guest", "guest"))
	req.Header.Set("Amqp-Routing-Key", queueName)
	req.Header.Set("Amqp-Timeout", "500") // 500ms timeout
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusGatewayTimeout {
		t.Errorf("rpc timeout status: got %d, want %d, body: %s", w.Code, http.StatusGatewayTimeout, w.Body.String())
	}
}

func TestIntegrationReadyz(t *testing.T) {
	rmqURL := requireRabbitMQ(t)

	client := NewAMQPClient(rmqURL)
	mux := NewServeMux(client)

	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("readyz status: got %d, want %d", w.Code, http.StatusOK)
	}
}

func TestIntegrationReadyzUnavailable(t *testing.T) {
	// Point to a non-existent RabbitMQ
	client := NewAMQPClient("amqp://localhost:59999")
	mux := NewServeMux(client)

	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz unavailable status: got %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}
