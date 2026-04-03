package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"

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
		baseURL: cfg.RabbitMQURL,
		tracer:  tracer,
		metrics: metrics,
		pool:    newConnPool(cfg.MaxConnsPerUser),
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
		return "", fmt.Errorf("invalid rabbitmq_url: %w", err)
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

// getConn gets a connection from the pool or dials a new one.
func (c *AMQPClient) getConn(params *PublishParams) (*amqp.Connection, error) {
	dialURL, err := c.dialURL(params)
	if err != nil {
		return nil, err
	}
	conn, err := c.pool.get(dialURL, params.Username, params.Password, params.VHost)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	return conn, nil
}

// putConn returns a connection to the pool.
func (c *AMQPClient) putConn(conn *amqp.Connection, params *PublishParams) {
	c.pool.put(conn, params.Username, params.Password, params.VHost)
}

func publishAttrs(params *PublishParams) trace.SpanStartOption {
	return trace.WithAttributes(
		semconv.MessagingSystemRabbitmq,
		semconv.MessagingDestinationName(params.Exchange),
		attribute.String("messaging.rabbitmq.routing_key", params.RoutingKey),
	)
}

func (c *AMQPClient) recordError(ctx context.Context, span trace.Span, counter metric.Int64Counter, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
}

// Publish connects to RabbitMQ, publishes a message, and waits for confirm.
func (c *AMQPClient) Publish(ctx context.Context, params *PublishParams, body []byte) error {
	ctx, span := c.tracer.Start(ctx, "amqp.publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		publishAttrs(params),
	)
	defer span.End()

	// Inject trace context into AMQP headers
	injectTraceContext(ctx, params.Headers)

	conn, err := c.getConn(params)
	if err != nil {
		c.recordError(ctx, span, c.metrics.publishTotal, err)
		return err
	}
	returnConn := true
	defer func() {
		if returnConn {
			c.putConn(conn, params)
		} else {
			c.pool.discard(conn)
		}
	}()

	ch, err := conn.Channel()
	if err != nil {
		returnConn = false
		err = fmt.Errorf("failed to open channel: %w", err)
		c.recordError(ctx, span, c.metrics.publishTotal, err)
		return err
	}
	defer ch.Close()

	if err := ch.Confirm(false); err != nil {
		returnConn = false
		err = fmt.Errorf("failed to enable confirm mode: %w", err)
		c.recordError(ctx, span, c.metrics.publishTotal, err)
		return err
	}

	// Listen for basic.return (unroutable messages when mandatory=true)
	returnCh := ch.NotifyReturn(make(chan amqp.Return, 1))

	pub := amqp.Publishing{
		DeliveryMode:  params.DeliveryMode,
		ContentType:   params.ContentType,
		MessageId:     params.MessageID,
		CorrelationId: params.CorrelationID,
		Expiration:    params.Expiration,
		Headers:       params.Headers,
		Body:          body,
	}

	confirm, err := ch.PublishWithDeferredConfirmWithContext(
		ctx,
		params.Exchange,
		params.RoutingKey,
		params.Mandatory,
		false, // immediate
		pub,
	)
	if err != nil {
		returnConn = false
		err = fmt.Errorf("failed to publish message: %w", err)
		c.recordError(ctx, span, c.metrics.publishTotal, err)
		return err
	}

	if !confirm.Wait() {
		returnConn = false
		err := fmt.Errorf("publish was not confirmed by broker")
		c.recordError(ctx, span, c.metrics.publishTotal, err)
		return err
	}

	// Check if the message was returned (no matching queue)
	select {
	case ret, ok := <-returnCh:
		if ok {
			err := &UnroutableError{
				ReplyCode:  ret.ReplyCode,
				ReplyText:  ret.ReplyText,
				Exchange:   ret.Exchange,
				RoutingKey: ret.RoutingKey,
			}
			// Unroutable is not a connection error, keep the connection
			c.recordError(ctx, span, c.metrics.publishTotal, err)
			return err
		}
	default:
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

	conn, err := c.getConn(params)
	if err != nil {
		c.recordError(ctx, span, c.metrics.rpcTotal, err)
		return nil, err
	}
	returnConn := true
	defer func() {
		if returnConn {
			c.putConn(conn, params)
		} else {
			c.pool.discard(conn)
		}
	}()

	ch, err := conn.Channel()
	if err != nil {
		returnConn = false
		err = fmt.Errorf("failed to open channel: %w", err)
		c.recordError(ctx, span, c.metrics.rpcTotal, err)
		return nil, err
	}
	defer ch.Close()

	// Create exclusive temporary queue for reply
	q, err := ch.QueueDeclare(
		"",    // auto-generated name
		false, // durable
		true,  // auto-delete
		true,  // exclusive
		false, // no-wait
		nil,
	)
	if err != nil {
		returnConn = false
		err = fmt.Errorf("failed to declare reply queue: %w", err)
		c.recordError(ctx, span, c.metrics.rpcTotal, err)
		return nil, err
	}

	msgs, err := ch.Consume(
		q.Name, // queue
		"",     // consumer
		true,   // auto-ack
		true,   // exclusive
		false,  // no-local
		false,  // no-wait
		nil,
	)
	if err != nil {
		returnConn = false
		err = fmt.Errorf("failed to consume from reply queue: %w", err)
		c.recordError(ctx, span, c.metrics.rpcTotal, err)
		return nil, err
	}

	// Set correlation ID if not provided
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

	if err := ch.PublishWithContext(
		ctx,
		params.Exchange,
		params.RoutingKey,
		params.Mandatory,
		false, // immediate
		pub,
	); err != nil {
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
		// Timeout is not a connection error, keep the connection
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

// UnroutableError represents a message that could not be routed (basic.return).
type UnroutableError struct {
	ReplyCode  uint16
	ReplyText  string
	Exchange   string
	RoutingKey string
}

func (e *UnroutableError) Error() string {
	return fmt.Sprintf("message unroutable: %d %s (exchange=%q, routing_key=%q)",
		e.ReplyCode, e.ReplyText, e.Exchange, e.RoutingKey)
}

func generateID() string {
	return uuid.New().String()
}
