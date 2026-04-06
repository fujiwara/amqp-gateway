package gateway

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ClientOptions holds common options for publish and rpc client commands.
type ClientOptions struct {
	URL           string            `kong:"required,arg,help='Gateway URL (e.g. http://localhost:8080)'"`
	User          string            `kong:"short='u',env='AMQP_GATEWAY_USER',help='Username for Basic auth'"`
	Password      string            `kong:"short='p',env='AMQP_GATEWAY_PASSWORD',help='Password for Basic auth'"`
	Exchange      string            `kong:"name='exchange',help='AMQP exchange'"`
	RoutingKey    string            `kong:"name='routing-key',help='AMQP routing key'"`
	VHost         string            `kong:"name='vhost',help='AMQP vhost'"`
	DeliveryMode  string            `kong:"name='delivery-mode',help='AMQP delivery mode (1=transient, 2=persistent)'"`
	MessageID     string            `kong:"name='message-id',help='AMQP message ID'"`
	CorrelationID string            `kong:"name='correlation-id',help='AMQP correlation ID'"`
	Expiration    string            `kong:"name='expiration',help='AMQP expiration'"`
	ContentType   string            `kong:"name='content-type',help='Content-Type header'"`
	Timeout       time.Duration     `kong:"name='timeout',help='RPC timeout (e.g. 5s, 500ms)'"`
	Headers       map[string]string `kong:"name='header',short='H',help='Custom AMQP headers (key=value)'"`
	Body          string            `kong:"name='body',xor='body',help='Message body string'"`
	BodyFile      string            `kong:"name='body-file',xor='body',help='Read message body from file (- for stdin)'"`
}

func (o *ClientOptions) buildRequest(method, path string) (*http.Request, error) {
	body, err := o.readBody()
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(o.URL, "/") + path
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if o.User != "" {
		req.SetBasicAuth(o.User, o.Password)
	}
	if o.Exchange != "" {
		req.Header.Set("Amqp-Exchange", o.Exchange)
	}
	if o.RoutingKey != "" {
		req.Header.Set("Amqp-Routing-Key", o.RoutingKey)
	}
	if o.VHost != "" {
		req.Header.Set("Amqp-Vhost", o.VHost)
	}
	if o.DeliveryMode != "" {
		req.Header.Set("Amqp-Delivery-Mode", o.DeliveryMode)
	}
	if o.MessageID != "" {
		req.Header.Set("Amqp-Message-Id", o.MessageID)
	}
	if o.CorrelationID != "" {
		req.Header.Set("Amqp-Correlation-Id", o.CorrelationID)
	}
	if o.Expiration != "" {
		req.Header.Set("Amqp-Expiration", o.Expiration)
	}
	if o.ContentType != "" {
		req.Header.Set("Content-Type", o.ContentType)
	}
	if o.Timeout > 0 {
		req.Header.Set("Amqp-Timeout", strconv.FormatInt(o.Timeout.Milliseconds(), 10))
	}
	for k, v := range o.Headers {
		req.Header.Set("Amqp-Header-"+k, v)
	}

	return req, nil
}

func (o *ClientOptions) readBody() (string, error) {
	if o.BodyFile != "" {
		var r io.Reader
		if o.BodyFile == "-" {
			r = os.Stdin
		} else {
			f, err := os.Open(o.BodyFile)
			if err != nil {
				return "", fmt.Errorf("failed to open body file: %w", err)
			}
			defer f.Close()
			r = f
		}
		b, err := io.ReadAll(r)
		if err != nil {
			return "", fmt.Errorf("failed to read body: %w", err)
		}
		return string(b), nil
	}
	return o.Body, nil
}

// ClientPublishCmd publishes a message via the gateway HTTP API.
type ClientPublishCmd struct {
	ClientOptions `kong:"embed"`
}

func (cmd *ClientPublishCmd) Run(cli *CLI) error {
	req, err := cmd.buildRequest("POST", "/v1/publish")
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("publish failed: %s %s", resp.Status, string(body))
	}
	fmt.Fprintln(os.Stderr, resp.Status)
	return nil
}

// ClientRPCCmd sends an RPC request via the gateway HTTP API.
type ClientRPCCmd struct {
	ClientOptions `kong:"embed"`
}

func (cmd *ClientRPCCmd) Run(cli *CLI) error {
	req, err := cmd.buildRequest("POST", "/v1/rpc")
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("rpc failed: %s %s", resp.Status, string(body))
	}

	// Print response headers with AMQP- prefix to stderr
	for key, values := range resp.Header {
		if strings.HasPrefix(key, "Amqp-") {
			fmt.Fprintf(os.Stderr, "%s: %s\n", key, values[0])
		}
	}
	// Print response body to stdout
	if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	return nil
}
