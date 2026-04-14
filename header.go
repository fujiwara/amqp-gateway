package gateway

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	headerExchange     = "Amqp-Exchange"
	headerRoutingKey   = "Amqp-Routing-Key"
	headerVHost        = "Amqp-Vhost"
	headerDeliveryMode = "Amqp-Delivery-Mode"
	headerMessageID    = "Amqp-Message-Id"
	headerCorrelation  = "Amqp-Correlation-Id"
	headerExpiration   = "Amqp-Expiration"
	headerTimeout      = "Amqp-Timeout"
	headerCustomPrefix = "Amqp-Header-"

	defaultDeliveryMode = 2 // persistent
	defaultVHost        = "/"
	defaultTimeout      = 30000 // ms
)

// PublishParams holds the parsed AMQP parameters from HTTP headers.
type PublishParams struct {
	Username      string
	Password      string
	Exchange      string
	RoutingKey    string
	VHost         string
	DeliveryMode  uint8
	MessageID     string
	CorrelationID string
	Expiration    string
	ContentType   string
	Headers       amqp.Table
	Timeout       time.Duration
}

// ParsePublishParams extracts AMQP parameters from HTTP request headers.
func ParsePublishParams(r *http.Request) (*PublishParams, error) {
	p := &PublishParams{
		Exchange:      r.Header.Get(headerExchange),
		RoutingKey:    r.Header.Get(headerRoutingKey),
		VHost:         r.Header.Get(headerVHost),
		MessageID:     r.Header.Get(headerMessageID),
		CorrelationID: r.Header.Get(headerCorrelation),
		Expiration:    r.Header.Get(headerExpiration),
		ContentType:   r.Header.Get("Content-Type"),
		DeliveryMode:  defaultDeliveryMode,
		Headers:       amqp.Table{},
	}

	if p.MessageID == "" {
		p.MessageID = uuid.Must(uuid.NewV7()).String()
	}

	if p.VHost == "" {
		p.VHost = defaultVHost
	}

	if v := r.Header.Get(headerDeliveryMode); v != "" {
		mode, err := strconv.ParseUint(v, 10, 8)
		if err != nil {
			return nil, &ValidationError{Field: headerDeliveryMode, Message: "must be a valid integer"}
		}
		p.DeliveryMode = uint8(mode)
	}

	// Parse timeout (default 30000ms)
	p.Timeout = defaultTimeout * time.Millisecond
	if v := r.Header.Get(headerTimeout); v != "" {
		ms, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, &ValidationError{Field: headerTimeout, Message: "must be a valid integer (milliseconds)"}
		}
		p.Timeout = time.Duration(ms) * time.Millisecond
	}

	// Extract custom headers (AMQP-Header-* → headers table)
	for key, values := range r.Header {
		if headerName, ok := strings.CutPrefix(key, headerCustomPrefix); ok && headerName != "" {
			p.Headers[headerName] = values[0]
		}
	}

	return p, nil
}

// ValidationError represents a header validation error.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}
