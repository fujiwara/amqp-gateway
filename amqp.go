package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// AMQPClient handles AMQP connections and operations.
type AMQPClient struct {
	baseURL string
	tracer  trace.Tracer
	metrics *Metrics
	pool    *connPool
}

// NewAMQPClient creates a new AMQPClient.
func NewAMQPClient(cfg *Config, tracer trace.Tracer, metrics *Metrics) *AMQPClient {
	return &AMQPClient{
		baseURL: cfg.AMQPURL,
		tracer:  tracer,
		metrics: metrics,
		pool:    newConnPool(cfg.MaxConnsPerUser, cfg.ConnTTL),
	}
}

// Close closes all pooled connections.
func (c *AMQPClient) Close() {
	c.pool.closeAll()
}

// dialURL builds the connection URL with the given credentials and vhost.
func (c *AMQPClient) dialURL(params *PublishParams) (string, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid amqp_url: %w", err)
	}
	u.User = url.UserPassword(params.Username, params.Password)
	// AMQP URI spec: default vhost "/" is represented as empty path segment.
	// amqp091-go does not decode %2F in the path, so we must not escape "/".
	if params.VHost == "/" {
		u.Path = "/"
	} else {
		u.Path = "/" + params.VHost
	}
	return u.String(), nil
}

// connHandle holds a connection and its creation time for pool return.
type connHandle struct {
	conn      *amqp.Connection
	createdAt time.Time
}

// getConn gets a connection from the pool or dials a new one.
func (c *AMQPClient) getConn(params *PublishParams) (*connHandle, error) {
	dialURL, err := c.dialURL(params)
	if err != nil {
		return nil, err
	}
	conn, err := c.pool.get(dialURL, params.Username, params.Password, params.VHost)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	return &connHandle{conn: conn, createdAt: time.Now()}, nil
}

// putConn returns a connection to the pool.
func (c *AMQPClient) putConn(h *connHandle, params *PublishParams) {
	c.pool.put(h.conn, params.Username, params.Password, params.VHost, h.createdAt)
}

func publishAttrs(params *PublishParams) trace.SpanStartOption {
	return trace.WithAttributes(
		semconv.MessagingSystemRabbitmq,
		semconv.MessagingDestinationName(params.Exchange),
		attribute.String("messaging.rabbitmq.routing_key", params.RoutingKey),
	)
}

// spanDo runs fn within a child span. If fn returns an error, the span records it.
func (c *AMQPClient) spanDo(ctx context.Context, name string, fn func(context.Context) error) error {
	ctx, span := c.tracer.Start(ctx, name)
	defer span.End()
	if err := fn(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

func (c *AMQPClient) recordError(ctx context.Context, span trace.Span, counter metric.Int64Counter, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
}

// Publish connects to RabbitMQ and publishes a message.
func (c *AMQPClient) Publish(ctx context.Context, params *PublishParams, body []byte) error {
	ctx, span := c.tracer.Start(ctx, "amqp.publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		publishAttrs(params),
	)
	defer span.End()

	// Inject trace context into AMQP headers
	injectTraceContext(ctx, params.Headers)

	var h *connHandle
	if err := c.spanDo(ctx, "amqp.connect", func(ctx context.Context) error {
		var err error
		h, err = c.getConn(params)
		return err
	}); err != nil {
		c.recordError(ctx, span, c.metrics.publishTotal, err)
		return err
	}
	returnConn := true
	defer func() {
		if returnConn {
			c.putConn(h, params)
		} else {
			c.pool.discard(h.conn)
		}
	}()

	var ch *amqp.Channel
	if err := c.spanDo(ctx, "amqp.channel", func(ctx context.Context) error {
		var err error
		ch, err = h.conn.Channel()
		if err != nil {
			returnConn = false
			return fmt.Errorf("failed to open channel: %w", err)
		}
		return nil
	}); err != nil {
		c.recordError(ctx, span, c.metrics.publishTotal, err)
		return err
	}
	defer ch.Close()

	pub := amqp.Publishing{
		DeliveryMode:  params.DeliveryMode,
		ContentType:   params.ContentType,
		MessageId:     params.MessageID,
		CorrelationId: params.CorrelationID,
		Expiration:    params.Expiration,
		Headers:       params.Headers,
		Body:          body,
	}

	if err := c.spanDo(ctx, "amqp.publish.send", func(ctx context.Context) error {
		return ch.PublishWithContext(ctx, params.Exchange, params.RoutingKey, false, false, pub)
	}); err != nil {
		returnConn = false
		err = fmt.Errorf("failed to publish message: %w", err)
		c.recordError(ctx, span, c.metrics.publishTotal, err)
		return err
	}

	slog.DebugContext(ctx, "message published",
		"exchange", params.Exchange,
		"routing_key", params.RoutingKey,
	)
	c.metrics.publishTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "success")))
	return nil
}

// RPC connects to RabbitMQ, publishes a message, and waits for a response.
func (c *AMQPClient) RPC(ctx context.Context, params *PublishParams, body []byte) (*amqp.Delivery, error) {
	ctx, span := c.tracer.Start(ctx, "amqp.rpc",
		trace.WithSpanKind(trace.SpanKindClient),
		publishAttrs(params),
	)
	defer span.End()

	// Inject trace context into AMQP headers
	injectTraceContext(ctx, params.Headers)

	var h *connHandle
	if err := c.spanDo(ctx, "amqp.connect", func(ctx context.Context) error {
		var err error
		h, err = c.getConn(params)
		return err
	}); err != nil {
		c.recordError(ctx, span, c.metrics.rpcTotal, err)
		return nil, err
	}
	returnConn := true
	defer func() {
		if returnConn {
			c.putConn(h, params)
		} else {
			c.pool.discard(h.conn)
		}
	}()

	var ch *amqp.Channel
	if err := c.spanDo(ctx, "amqp.channel", func(ctx context.Context) error {
		var err error
		ch, err = h.conn.Channel()
		if err != nil {
			returnConn = false
			return fmt.Errorf("failed to open channel: %w", err)
		}
		return nil
	}); err != nil {
		c.recordError(ctx, span, c.metrics.rpcTotal, err)
		return nil, err
	}
	defer ch.Close()

	var q amqp.Queue
	var msgs <-chan amqp.Delivery
	if err := c.spanDo(ctx, "amqp.rpc.setup", func(ctx context.Context) error {
		var err error
		q, err = ch.QueueDeclare("", false, true, true, false, nil)
		if err != nil {
			returnConn = false
			return fmt.Errorf("failed to declare reply queue: %w", err)
		}
		msgs, err = ch.Consume(q.Name, "", true, true, false, false, nil)
		if err != nil {
			returnConn = false
			return fmt.Errorf("failed to consume from reply queue: %w", err)
		}
		return nil
	}); err != nil {
		c.recordError(ctx, span, c.metrics.rpcTotal, err)
		return nil, err
	}

	correlationID := params.CorrelationID
	if correlationID == "" {
		correlationID = generateID()
	}

	pub := amqp.Publishing{
		DeliveryMode:  params.DeliveryMode,
		ContentType:   params.ContentType,
		MessageId:     params.MessageID,
		CorrelationId: correlationID,
		ReplyTo:       q.Name,
		Expiration:    params.Expiration,
		Headers:       params.Headers,
		Body:          body,
	}

	if err := c.spanDo(ctx, "amqp.rpc.send", func(ctx context.Context) error {
		return ch.PublishWithContext(ctx, params.Exchange, params.RoutingKey, false, false, pub)
	}); err != nil {
		returnConn = false
		err = fmt.Errorf("failed to publish RPC request: %w", err)
		c.recordError(ctx, span, c.metrics.rpcTotal, err)
		return nil, err
	}

	slog.DebugContext(ctx, "RPC request published, waiting for response",
		"exchange", params.Exchange,
		"routing_key", params.RoutingKey,
		"correlation_id", correlationID,
		"reply_to", q.Name,
	)

	// Wait for response with timeout
	_, waitSpan := c.tracer.Start(ctx, "amqp.rpc.wait")
	timeoutCtx, cancel := context.WithTimeout(ctx, params.Timeout)
	defer cancel()

	select {
	case msg, ok := <-msgs:
		waitSpan.End()
		if !ok {
			returnConn = false
			err := fmt.Errorf("reply queue channel closed unexpectedly")
			c.recordError(ctx, span, c.metrics.rpcTotal, err)
			return nil, err
		}
		if msg.CorrelationId != correlationID {
			err := fmt.Errorf("correlation ID mismatch: expected %s, got %s", correlationID, msg.CorrelationId)
			c.recordError(ctx, span, c.metrics.rpcTotal, err)
			return nil, err
		}
		c.metrics.rpcTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "success")))
		return &msg, nil
	case <-timeoutCtx.Done():
		waitSpan.End()
		err := &TimeoutError{Timeout: params.Timeout}
		c.recordError(ctx, span, c.metrics.rpcTotal, err)
		return nil, err
	}
}

// Ping tests the RabbitMQ connection using the base URL (no credentials).
func (c *AMQPClient) Ping(ctx context.Context) error {
	conn, err := amqp.Dial(c.baseURL)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// TimeoutError represents an RPC timeout.
type TimeoutError struct {
	Timeout fmt.Stringer
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("RPC timeout after %s", e.Timeout)
}

func generateID() string {
	return uuid.Must(uuid.NewV7()).String()
}
