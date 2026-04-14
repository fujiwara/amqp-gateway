package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestParsePublishParams(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		check   func(t *testing.T, p *PublishParams)
		wantErr bool
	}{
		{
			name:    "defaults",
			headers: map[string]string{},
			check: func(t *testing.T, p *PublishParams) {
				t.Helper()
				if p.VHost != "/" {
					t.Errorf("VHost: got %q, want %q", p.VHost, "/")
				}
				if p.DeliveryMode != 2 {
					t.Errorf("DeliveryMode: got %d, want 2", p.DeliveryMode)
				}
				if p.Timeout != 30*time.Second {
					t.Errorf("Timeout: got %v, want 30s", p.Timeout)
				}
				// MessageID should be auto-generated as UUID v7
				parsed, err := uuid.Parse(p.MessageID)
				if err != nil {
					t.Fatalf("MessageID is not a valid UUID: %v", err)
				}
				if parsed.Version() != 7 {
					t.Errorf("MessageID UUID version: got %d, want 7", parsed.Version())
				}
			},
		},
		{
			name: "all headers",
			headers: map[string]string{
				"Amqp-Exchange":       "test-exchange",
				"Amqp-Routing-Key":    "test.key",
				"Amqp-Vhost":          "myvhost",
				"Amqp-Delivery-Mode":  "1",
				"Amqp-Message-Id":     "msg-123",
				"Amqp-Correlation-Id": "corr-456",
				"Amqp-Expiration":     "60000",
				"Amqp-Timeout":        "5000",
				"Content-Type":        "application/json",
			},
			check: func(t *testing.T, p *PublishParams) {
				t.Helper()
				if p.Exchange != "test-exchange" {
					t.Errorf("Exchange: got %q", p.Exchange)
				}
				if p.RoutingKey != "test.key" {
					t.Errorf("RoutingKey: got %q", p.RoutingKey)
				}
				if p.VHost != "myvhost" {
					t.Errorf("VHost: got %q", p.VHost)
				}
				if p.DeliveryMode != 1 {
					t.Errorf("DeliveryMode: got %d", p.DeliveryMode)
				}
				if p.MessageID != "msg-123" {
					t.Errorf("MessageID: got %q", p.MessageID)
				}
				if p.CorrelationID != "corr-456" {
					t.Errorf("CorrelationID: got %q", p.CorrelationID)
				}
				if p.Expiration != "60000" {
					t.Errorf("Expiration: got %q", p.Expiration)
				}
				if p.Timeout != 5*time.Second {
					t.Errorf("Timeout: got %v, want 5s", p.Timeout)
				}
				if p.ContentType != "application/json" {
					t.Errorf("ContentType: got %q", p.ContentType)
				}
			},
		},
		{
			name: "custom headers",
			headers: map[string]string{
				"Amqp-Header-X-Request-Id": "req-789",
				"Amqp-Header-Priority":     "high",
			},
			check: func(t *testing.T, p *PublishParams) {
				t.Helper()
				if v, ok := p.Headers["X-Request-Id"]; !ok || v != "req-789" {
					t.Errorf("Headers[X-Request-Id]: got %v", v)
				}
				if v, ok := p.Headers["Priority"]; !ok || v != "high" {
					t.Errorf("Headers[Priority]: got %v", v)
				}
			},
		},
		{
			name:    "invalid delivery mode",
			headers: map[string]string{"Amqp-Delivery-Mode": "abc"},
			wantErr: true,
		},
		{
			name:    "invalid timeout",
			headers: map[string]string{"Amqp-Timeout": "abc"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := http.NewRequest("POST", "/v1/publish", nil)
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}

			p, err := ParsePublishParams(r)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.check(t, p)
		})
	}
}
