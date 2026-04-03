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
}

// NewAMQPClient creates a new AMQPClient with the given base URL.
func NewAMQPClient(baseURL string, tracer trace.Tracer, metrics *Metrics) *AMQPClient {
	return &AMQPClient{baseURL: baseURL, tracer: tracer, metrics: metrics}
}

// dialURL builds the connection URL with the given credentials and vhost.
func (c *AMQPClient) dialURL(username, password, vhost string) (string, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid rabbitmq_url: %w", err)
	}
	u.User = url.UserPassword(username, password)
	// AMQP URI spec: default vhost "/" is represented as empty path segment.
	// amqp091-go does not decode %2F in the path, so we must not escape "/".
	if vhost == "/" {
		u.Path = "/"
	} else {
		u.Path = "/" + vhost
	}
	return u.String(), nil
}

func publishAttrs(params *PublishParams) trace.SpanStartOption {
	return trace.WithAttributes(
		semconv.MessagingSystemRabbitmq,
		semconv.MessagingDestinationName(params.Exchange),
		attribute.String("messaging.rabbitmq.routing_key", params.RoutingKey),
	)
}

// Publish connects to RabbitMQ, publishes a message, and waits for confirm.
func (c *AMQPClient) Publish(ctx context.Context, username, password string, params *PublishParams, body []byte) error {
	ctx, span := c.tracer.Start(ctx, "amqp.publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		publishAttrs(params),
	)
	defer span.End()

	// Inject trace context into AMQP headers
	injectTraceContext(ctx, params.Headers)

	dialURL, err := c.dialURL(username, password, params.VHost)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.metrics.publishTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
		return err
	}

	conn, err := amqp.Dial(dialURL)
	if err != nil {
		err = fmt.Errorf("failed to connect to RabbitMQ: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.metrics.publishTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
		return err
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		err = fmt.Errorf("failed to open channel: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.metrics.publishTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
		return err
	}
	defer ch.Close()

	if err := ch.Confirm(false); err != nil {
		err = fmt.Errorf("failed to enable confirm mode: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.metrics.publishTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
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
		err = fmt.Errorf("failed to publish message: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.metrics.publishTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
		return err
	}

	if !confirm.Wait() {
		err := fmt.Errorf("publish was not confirmed by broker")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.metrics.publishTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
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
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			c.metrics.publishTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
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
func (c *AMQPClient) RPC(ctx context.Context, username, password string, params *PublishParams, body []byte) (*amqp.Delivery, error) {
	ctx, span := c.tracer.Start(ctx, "amqp.rpc",
		trace.WithSpanKind(trace.SpanKindClient),
		publishAttrs(params),
	)
	defer span.End()

	// Inject trace context into AMQP headers
	injectTraceContext(ctx, params.Headers)

	dialURL, err := c.dialURL(username, password, params.VHost)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.metrics.rpcTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
		return nil, err
	}

	conn, err := amqp.Dial(dialURL)
	if err != nil {
		err = fmt.Errorf("failed to connect to RabbitMQ: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.metrics.rpcTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
		return nil, err
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		err = fmt.Errorf("failed to open channel: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.metrics.rpcTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
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
		err = fmt.Errorf("failed to declare reply queue: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.metrics.rpcTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
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
		err = fmt.Errorf("failed to consume from reply queue: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.metrics.rpcTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
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
		err = fmt.Errorf("failed to publish RPC request: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.metrics.rpcTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
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
			err := fmt.Errorf("reply queue channel closed unexpectedly")
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			c.metrics.rpcTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
			return nil, err
		}
		if msg.CorrelationId != correlationID {
			err := fmt.Errorf("correlation ID mismatch: expected %s, got %s", correlationID, msg.CorrelationId)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			c.metrics.rpcTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error")))
			return nil, err
		}
		c.metrics.rpcTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "success")))
		return &msg, nil
	case <-timeoutCtx.Done():
		waitSpan.End()
		err := &TimeoutError{Timeout: params.Timeout}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.metrics.rpcTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "timeout")))
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
