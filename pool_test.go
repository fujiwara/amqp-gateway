package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConnPoolGetPut(t *testing.T) {
	rmqURL := requireRabbitMQ(t)

	pool := newConnPool(2)
	defer pool.closeAll()

	conn1, err := pool.get(rmqURL, "guest", "guest", "/")
	if err != nil {
		t.Fatalf("get conn1: %v", err)
	}
	if conn1.IsClosed() {
		t.Fatal("conn1 should be open")
	}

	pool.put(conn1, "guest", "guest", "/")

	// Should reuse the pooled connection
	conn2, err := pool.get(rmqURL, "guest", "guest", "/")
	if err != nil {
		t.Fatalf("get conn2: %v", err)
	}
	if conn1 != conn2 {
		t.Error("expected to reuse the same connection")
	}

	pool.put(conn2, "guest", "guest", "/")
}

func TestConnPoolMaxConns(t *testing.T) {
	rmqURL := requireRabbitMQ(t)

	pool := newConnPool(1)
	defer pool.closeAll()

	conn1, err := pool.get(rmqURL, "guest", "guest", "/")
	if err != nil {
		t.Fatalf("get conn1: %v", err)
	}
	conn2, err := pool.get(rmqURL, "guest", "guest", "/")
	if err != nil {
		t.Fatalf("get conn2: %v", err)
	}

	pool.put(conn1, "guest", "guest", "/")
	pool.put(conn2, "guest", "guest", "/") // pool full, conn2 should be closed

	if !conn2.IsClosed() {
		t.Error("conn2 should be closed (pool full)")
	}
	if conn1.IsClosed() {
		t.Error("conn1 should still be open (in pool)")
	}
}

func TestConnPoolStaleConnection(t *testing.T) {
	rmqURL := requireRabbitMQ(t)

	pool := newConnPool(2)
	defer pool.closeAll()

	conn, err := pool.get(rmqURL, "guest", "guest", "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	conn.Close() // simulate server-side disconnect
	pool.put(conn, "guest", "guest", "/")

	// Should skip stale and dial new
	conn2, err := pool.get(rmqURL, "guest", "guest", "/")
	if err != nil {
		t.Fatalf("get after stale: %v", err)
	}
	if conn2.IsClosed() {
		t.Error("new connection should be open")
	}
	pool.put(conn2, "guest", "guest", "/")
}

func TestConnPoolCloseAll(t *testing.T) {
	rmqURL := requireRabbitMQ(t)

	pool := newConnPool(2)

	conn, err := pool.get(rmqURL, "guest", "guest", "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	pool.put(conn, "guest", "guest", "/")
	pool.closeAll()

	if !conn.IsClosed() {
		t.Error("connection should be closed after closeAll")
	}
}

func TestIntegrationPoolReuse(t *testing.T) {
	rmqURL := requireRabbitMQ(t)
	queueName := fmt.Sprintf("test-pool-%d", time.Now().UnixNano())
	setupTestQueue(t, rmqURL, queueName)

	client := testNewAMQPClient(rmqURL)
	mux := testNewServeMux(client)

	// Publish 3 times — connections should be reused from pool
	for i := range 3 {
		req := httptest.NewRequest("POST", "/v1/publish", strings.NewReader(fmt.Sprintf("msg-%d", i)))
		req.Header.Set("Authorization", testBasicAuth("guest", "guest"))
		req.Header.Set("Amqp-Routing-Key", queueName)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("publish %d: got %d, body: %s", i, w.Code, w.Body.String())
		}
	}
}
