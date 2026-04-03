package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

// AMQPClient handles AMQP connections and operations.
type AMQPClient struct {
	baseURL string
}

// NewAMQPClient creates a new AMQPClient with the given base URL.
func NewAMQPClient(baseURL string) *AMQPClient {
	return &AMQPClient{baseURL: baseURL}
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

// Publish connects to RabbitMQ, publishes a message, and waits for confirm.
func (c *AMQPClient) Publish(ctx context.Context, username, password string, params *PublishParams, body []byte) error {
	dialURL, err := c.dialURL(username, password, params.VHost)
	if err != nil {
		return err
	}

	conn, err := amqp.Dial(dialURL)
	if err != nil {
		return fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open channel: %w", err)
	}
	defer ch.Close()

	if err := ch.Confirm(false); err != nil {
		return fmt.Errorf("failed to enable confirm mode: %w", err)
	}

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
		return fmt.Errorf("failed to publish message: %w", err)
	}

	if !confirm.Wait() {
		return fmt.Errorf("publish was not confirmed by broker")
	}

	slog.Debug("message published",
		"exchange", params.Exchange,
		"routing_key", params.RoutingKey,
	)
	return nil
}

// RPC connects to RabbitMQ, publishes a message, and waits for a response.
func (c *AMQPClient) RPC(ctx context.Context, username, password string, params *PublishParams, body []byte) (*amqp.Delivery, error) {
	dialURL, err := c.dialURL(username, password, params.VHost)
	if err != nil {
		return nil, err
	}

	conn, err := amqp.Dial(dialURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open channel: %w", err)
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
		return nil, fmt.Errorf("failed to declare reply queue: %w", err)
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
		return nil, fmt.Errorf("failed to consume from reply queue: %w", err)
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
		return nil, fmt.Errorf("failed to publish RPC request: %w", err)
	}

	slog.Debug("RPC request published, waiting for response",
		"exchange", params.Exchange,
		"routing_key", params.RoutingKey,
		"correlation_id", correlationID,
		"reply_to", q.Name,
	)

	// Wait for response with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, params.Timeout)
	defer cancel()

	select {
	case msg, ok := <-msgs:
		if !ok {
			return nil, fmt.Errorf("reply queue channel closed unexpectedly")
		}
		if msg.CorrelationId != correlationID {
			return nil, fmt.Errorf("correlation ID mismatch: expected %s, got %s", correlationID, msg.CorrelationId)
		}
		return &msg, nil
	case <-timeoutCtx.Done():
		return nil, &TimeoutError{Timeout: params.Timeout}
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
	return uuid.New().String()
}
