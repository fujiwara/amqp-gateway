package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	armed "github.com/fujiwara/jsonnet-armed"
	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	defaultShutdownTimeout = 30 * time.Second

	// AliasMethodPublish and AliasMethodRPC are the allowed values for AliasConfig.Method.
	AliasMethodPublish = "publish"
	AliasMethodRPC     = "rpc"
)

// Config represents the application configuration.
type Config struct {
	AMQPURL         string        `json:"amqp_url"`
	ListenAddr      string        `json:"listen_addr"`
	ShutdownTimeout time.Duration `json:"shutdown_timeout"`
	MaxConnsPerUser int           `json:"max_conns_per_user"`
	ConnTTL         time.Duration `json:"conn_ttl"`
	Aliases         []AliasConfig `json:"aliases"`
}

// AliasConfig defines a custom HTTP path that maps to a publish or rpc call
// with pre-configured AMQP parameters.
type AliasConfig struct {
	Path   string `json:"path"`
	Method string `json:"method"` // "publish" or "rpc"

	// AMQP parameters — same fields as PublishParams.
	// HTTP request headers can override these if provided.
	Username     string            `json:"username"`
	Password     string            `json:"password"`
	Exchange     string            `json:"exchange"`
	RoutingKey   string            `json:"routing_key"`
	VHost        string            `json:"vhost"`
	DeliveryMode *uint8            `json:"delivery_mode"`
	ContentType  string            `json:"content_type"`
	Timeout      time.Duration     `json:"timeout"`
	Headers      map[string]string `json:"headers"`

	// Response customization for alias endpoints.
	Response *AliasResponseConfig `json:"response,omitempty"`
}

// AliasResponseConfig customizes the HTTP response for alias endpoints.
type AliasResponseConfig struct {
	Status      int    `json:"status,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Body        any    `json:"body,omitempty"`
}

// toPublishParams converts AliasConfig to PublishParams as defaults.
func (a *AliasConfig) toPublishParams() *PublishParams {
	p := &PublishParams{
		Username:     a.Username,
		Password:     a.Password,
		Exchange:     a.Exchange,
		RoutingKey:   a.RoutingKey,
		VHost:        a.VHost,
		ContentType:  a.ContentType,
		Timeout:      a.Timeout,
		DeliveryMode: defaultDeliveryMode,
		Headers:      amqp.Table{},
	}
	if p.VHost == "" {
		p.VHost = defaultVHost
	}
	if p.Timeout == 0 {
		p.Timeout = defaultTimeout * time.Millisecond
	}
	if a.DeliveryMode != nil {
		p.DeliveryMode = *a.DeliveryMode
	}
	for k, v := range a.Headers {
		p.Headers[k] = v
	}
	return p
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
	if c.AMQPURL == "" {
		return fmt.Errorf("amqp_url is required")
	}
	paths := make(map[string]bool)
	for i, a := range c.Aliases {
		if a.Path == "" {
			return fmt.Errorf("aliases[%d]: path is required", i)
		}
		if a.Method != AliasMethodPublish && a.Method != AliasMethodRPC {
			return fmt.Errorf("aliases[%d]: method must be \"publish\" or \"rpc\"", i)
		}
		if (a.Username == "") != (a.Password == "") {
			return fmt.Errorf("aliases[%d]: username and password must both be set or both be empty", i)
		}
		if a.Response != nil {
			if s := a.Response.Status; s != 0 && (s < 100 || s > 599) {
				return fmt.Errorf("aliases[%d]: response.status must be between 100 and 599", i)
			}
			if strings.HasPrefix(a.Response.ContentType, "text/") && a.Response.Body != nil {
				if _, ok := a.Response.Body.(string); !ok {
					return fmt.Errorf("aliases[%d]: response.body must be a string when content_type is text/*", i)
				}
			}
		}
		if paths[a.Path] {
			return fmt.Errorf("aliases[%d]: duplicate path %q", i, a.Path)
		}
		paths[a.Path] = true
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
