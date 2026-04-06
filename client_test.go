package gateway

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClientOptionsBuildRequest(t *testing.T) {
	opts := ClientOptions{
		URL:           "http://localhost:8080",
		User:          "guest",
		Password:      "guest",
		Exchange:      "my-exchange",
		RoutingKey:    "my.key",
		VHost:         "myvhost",
		DeliveryMode:  "1",
		MessageID:     "msg-1",
		CorrelationID: "corr-1",
		Expiration:    "60000",
		ContentType:   "application/json",
		Timeout:       5 * time.Second,
		Headers:       map[string]string{"X-Foo": "bar"},
		Body:          `{"hello":"world"}`,
	}

	req, err := opts.buildRequest("POST", "/v1/publish")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if req.URL.String() != "http://localhost:8080/v1/publish" {
		t.Errorf("url: got %q", req.URL.String())
	}
	user, pass, ok := req.BasicAuth()
	if !ok || user != "guest" || pass != "guest" {
		t.Errorf("basic auth: got %q/%q/%v", user, pass, ok)
	}

	checks := map[string]string{
		"Amqp-Exchange":       "my-exchange",
		"Amqp-Routing-Key":    "my.key",
		"Amqp-Vhost":          "myvhost",
		"Amqp-Delivery-Mode":  "1",
		"Amqp-Message-Id":     "msg-1",
		"Amqp-Correlation-Id": "corr-1",
		"Amqp-Expiration":     "60000",
		"Content-Type":        "application/json",
		"Amqp-Timeout":        "5000", // 5s → 5000ms
		"Amqp-Header-X-Foo":   "bar",
	}
	for k, want := range checks {
		if got := req.Header.Get(k); got != want {
			t.Errorf("header %s: got %q, want %q", k, got, want)
		}
	}
}

func TestClientOptionsBuildRequestTrailingSlash(t *testing.T) {
	opts := ClientOptions{URL: "http://localhost:8080/"}
	req, err := opts.buildRequest("POST", "/v1/publish")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.URL.String() != "http://localhost:8080/v1/publish" {
		t.Errorf("url: got %q", req.URL.String())
	}
}

func TestClientOptionsReadBody(t *testing.T) {
	t.Run("body string", func(t *testing.T) {
		opts := ClientOptions{Body: "hello"}
		body, err := opts.readBody()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if body != "hello" {
			t.Errorf("body: got %q", body)
		}
	})

	t.Run("body file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "body.txt")
		if err := os.WriteFile(path, []byte("from file"), 0644); err != nil {
			t.Fatal(err)
		}
		opts := ClientOptions{BodyFile: path}
		body, err := opts.readBody()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if body != "from file" {
			t.Errorf("body: got %q", body)
		}
	})

	t.Run("empty", func(t *testing.T) {
		opts := ClientOptions{}
		body, err := opts.readBody()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if body != "" {
			t.Errorf("body: got %q", body)
		}
	})
}
