package gateway

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	ctx := context.Background()
	cfg, err := LoadConfig(ctx, "testdata/config.jsonnet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AMQPURL != "amqp://localhost:5672" {
		t.Errorf("amqp_url: got %q, want %q", cfg.AMQPURL, "amqp://localhost:5672")
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("listen_addr: got %q, want %q", cfg.ListenAddr, ":8080")
	}
}

func TestLoadConfigMinimal(t *testing.T) {
	ctx := context.Background()
	cfg, err := LoadConfig(ctx, "testdata/config_minimal.jsonnet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("default listen_addr: got %q, want %q", cfg.ListenAddr, ":8080")
	}
}

func TestLoadConfigMissingAMQPURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonnet")
	if err := os.WriteFile(path, []byte(`{ listen_addr: ":9090" }`), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err := LoadConfig(ctx, path)
	if err == nil {
		t.Fatal("expected error for missing amqp_url")
	}
}

func TestLoadConfigUnknownField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonnet")
	content := `{ amqp_url: "amqp://localhost:5672", unknown_field: "value" }`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err := LoadConfig(ctx, path)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestRenderConfig(t *testing.T) {
	ctx := context.Background()
	out, err := RenderConfig(ctx, "testdata/config.jsonnet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty output")
	}
}
