package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	armed "github.com/fujiwara/jsonnet-armed"
)

const defaultShutdownTimeout = 30 * time.Second

// Config represents the application configuration.
type Config struct {
	RabbitMQURL     string        `json:"rabbitmq_url"`
	ListenAddr      string        `json:"listen_addr"`
	ShutdownTimeout time.Duration `json:"shutdown_timeout"`
}

func (c *Config) applyDefaults() {
	if c.ListenAddr == "" {
		c.ListenAddr = ":8080"
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = defaultShutdownTimeout
	}
}

func (c *Config) validate() error {
	if c.RabbitMQURL == "" {
		return fmt.Errorf("rabbitmq_url is required")
	}
	return nil
}

// LoadConfig loads and parses a configuration file (Jsonnet or JSON).
func LoadConfig(ctx context.Context, path string) (*Config, error) {
	jsonBytes, err := evaluateJsonnet(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate config: %w", err)
	}
	return parseConfig(jsonBytes)
}

func evaluateJsonnet(ctx context.Context, path string) ([]byte, error) {
	var buf bytes.Buffer
	cli := &armed.CLI{Filename: path}
	cli.SetWriter(&buf)
	if err := cli.Run(ctx); err != nil {
		return nil, fmt.Errorf("failed to evaluate jsonnet %q: %w", path, err)
	}
	return buf.Bytes(), nil
}

func parseConfig(data []byte) (*Config, error) {
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &cfg, nil
}

// RenderConfig evaluates the config file and returns pretty-printed JSON.
func RenderConfig(ctx context.Context, path string) ([]byte, error) {
	jsonBytes, err := evaluateJsonnet(ctx, path)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, jsonBytes, "", "  "); err != nil {
		return nil, fmt.Errorf("failed to format JSON: %w", err)
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}
