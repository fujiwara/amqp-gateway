package gateway

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func basicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func TestHandleHealthz(t *testing.T) {
	handler := handleHealthz()
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandlePublishNoAuth(t *testing.T) {
	client := testNewAMQPClient("amqp://localhost:5672")
	handler := handlePublish(client)

	req := httptest.NewRequest("POST", "/v1/publish", strings.NewReader("test body"))
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleRPCNoAuth(t *testing.T) {
	client := testNewAMQPClient("amqp://localhost:5672")
	handler := handleRPC(client)

	req := httptest.NewRequest("POST", "/v1/rpc", strings.NewReader("test body"))
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandlePublishBadHeader(t *testing.T) {
	client := testNewAMQPClient("amqp://localhost:5672")
	handler := handlePublish(client)

	req := httptest.NewRequest("POST", "/v1/publish", strings.NewReader("test body"))
	req.Header.Set("Authorization", basicAuth("guest", "guest"))
	req.Header.Set("Amqp-Delivery-Mode", "invalid")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestPublishMessageIDResponseHeader(t *testing.T) {
	client := testNewAMQPClient("amqp://localhost:5672")
	handler := handlePublish(client)

	t.Run("explicit message_id", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/publish", strings.NewReader("test"))
		req.Header.Set("Authorization", basicAuth("guest", "guest"))
		req.Header.Set("Amqp-Message-Id", "explicit-id-123")
		w := httptest.NewRecorder()
		handler(w, req)

		got := w.Header().Get("Amqp-Message-Id")
		if got != "explicit-id-123" {
			t.Errorf("Amqp-Message-Id header: got %q, want %q", got, "explicit-id-123")
		}
	})

	t.Run("auto-generated message_id", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/publish", strings.NewReader("test"))
		req.Header.Set("Authorization", basicAuth("guest", "guest"))
		// No Amqp-Message-Id header set
		w := httptest.NewRecorder()
		handler(w, req)

		got := w.Header().Get("Amqp-Message-Id")
		if got == "" {
			t.Error("Amqp-Message-Id header should be set with auto-generated ID")
		}
	})
}

func TestRPCMessageIDResponseHeader(t *testing.T) {
	client := testNewAMQPClient("amqp://localhost:5672")
	handler := handleRPC(client)

	t.Run("explicit message_id", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/rpc", strings.NewReader("test"))
		req.Header.Set("Authorization", basicAuth("guest", "guest"))
		req.Header.Set("Amqp-Message-Id", "rpc-id-456")
		req.Header.Set("Amqp-Timeout", "100")
		w := httptest.NewRecorder()
		handler(w, req)

		got := w.Header().Get("Amqp-Message-Id")
		if got != "rpc-id-456" {
			t.Errorf("Amqp-Message-Id header: got %q, want %q", got, "rpc-id-456")
		}
	})

	t.Run("auto-generated message_id", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/rpc", strings.NewReader("test"))
		req.Header.Set("Authorization", basicAuth("guest", "guest"))
		req.Header.Set("Amqp-Timeout", "100")
		w := httptest.NewRecorder()
		handler(w, req)

		got := w.Header().Get("Amqp-Message-Id")
		if got == "" {
			t.Error("Amqp-Message-Id header should be set with auto-generated ID")
		}
	})
}

func TestParseBasicAuth(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		wantUser string
		wantPass string
		wantOk   bool
	}{
		{
			name:     "valid",
			header:   basicAuth("user", "pass"),
			wantUser: "user",
			wantPass: "pass",
			wantOk:   true,
		},
		{
			name:   "empty",
			header: "",
			wantOk: false,
		},
		{
			name:   "not basic",
			header: "Bearer token",
			wantOk: false,
		},
		{
			name:   "invalid base64",
			header: "Basic !!!invalid!!!",
			wantOk: false,
		},
		{
			name:   "no colon",
			header: "Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon")),
			wantOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if tt.header != "" {
				r.Header.Set("Authorization", tt.header)
			}
			user, pass, ok := parseBasicAuth(r)
			if ok != tt.wantOk {
				t.Errorf("ok: got %v, want %v", ok, tt.wantOk)
			}
			if ok {
				if user != tt.wantUser {
					t.Errorf("user: got %q, want %q", user, tt.wantUser)
				}
				if pass != tt.wantPass {
					t.Errorf("pass: got %q, want %q", pass, tt.wantPass)
				}
			}
		})
	}
}

func TestNewServeMux(t *testing.T) {
	client := testNewAMQPClient("amqp://localhost:5672")
	mux := testNewServeMux(client)

	// Test healthz is registered
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("healthz status: got %d, want %d", w.Code, http.StatusOK)
	}

	// Test publish requires auth
	req = httptest.NewRequest("POST", "/v1/publish", strings.NewReader(""))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("publish without auth: got %d, want %d", w.Code, http.StatusUnauthorized)
	}

	// Test method not allowed
	req = httptest.NewRequest("GET", "/v1/publish", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Error("GET /v1/publish should not return 200")
	}
}
