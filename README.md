# amqp-gateway

AMQP 0-9-1 HTTP Gateway — Publish messages to RabbitMQ via HTTP.

For clients that cannot connect to RabbitMQ directly, this gateway provides HTTP endpoints to publish messages and perform RPC calls over AMQP 0-9-1.

## Usage

### Server

```console
$ amqp-gateway -c config.jsonnet
```

### CLI Options

```
Usage: amqp-gateway [flags] <command>

Flags:
  -c, --config       Config file path (Jsonnet/JSON) [env: AMQP_GATEWAY_CONFIG]
      --log-level    Log level (debug, info, warn, error) [default: info, env: AMQP_GATEWAY_LOG_LEVEL]
      --version      Show version

Commands:
  run        Run the gateway server (default, requires --config)
  validate   Validate config (requires --config)
  render     Render config as JSON to stdout (requires --config)
  publish    Publish a message via HTTP
  rpc        Send an RPC request via HTTP
```

### Configuration

Config files are evaluated by [jsonnet-armed](https://github.com/fujiwara/jsonnet-armed), supporting Jsonnet and JSON format. You can use Jsonnet features such as variables, functions, and environment variable expansion.

```jsonnet
{
  amqp_url: "amqp://rabbitmq.internal:5672",
  listen_addr: ":8080",          // default: ":8080"
  shutdown_timeout: "30s",       // default: "30s"
  max_conns_per_user: 2,         // default: 2
  conn_ttl: "5m",                // default: "5m"
}
```

Environment variables can be referenced using `must_env` (fails if unset) or `env` (returns a default):

```jsonnet
local must_env = std.native('must_env');
local env = std.native('env');
{
  amqp_url: must_env('AMQP_URL'),
  listen_addr: env('LISTEN_ADDR', ':8080'),
}
```

| Key | Description | Default |
|---|---|---|
| `amqp_url` | AMQP server URL (credentials are per-request via Basic auth) | — (required) |
| `listen_addr` | HTTP listen address | `:8080` |
| `shutdown_timeout` | Graceful shutdown timeout | `30s` |
| `max_conns_per_user` | Max pooled AMQP connections per user/password/vhost | `2` |
| `conn_ttl` | Max lifetime of a pooled connection before it is discarded | `5m` |
| `aliases` | List of alias endpoint definitions (see below) | `[]` |

### Aliases

Aliases provide custom HTTP endpoints with pre-configured AMQP parameters. Clients can call these without knowing AMQP details or providing credentials.

```jsonnet
{
  amqp_url: "amqp://localhost:5672",
  aliases: [
    {
      path: "/api/send-email",
      method: "publish",
      username: "app-user",
      password: "app-pass",
      exchange: "notifications",
      routing_key: "email.send",
      content_type: "application/json",
    },
    {
      path: "/api/user-lookup",
      method: "rpc",
      username: "app-user",
      password: "app-pass",
      routing_key: "user.lookup",
      timeout: "10s",
    },
  ],
}
```

```console
# No auth needed — alias provides credentials
$ curl -X POST http://localhost:8080/api/send-email \
  -d '{"to": "user@example.com", "subject": "Hello"}'
# 202 Accepted

# RPC via alias
$ curl -X POST http://localhost:8080/api/user-lookup \
  -d '{"user_id": "123"}'
# 200 OK with response body
```

Alias fields:

| Field | Description | Required |
|---|---|---|
| `path` | HTTP path (e.g. `/api/send`) | yes |
| `method` | `"publish"` or `"rpc"` | yes |
| `username` | RabbitMQ username (if omitted, requires Basic auth in request) | no |
| `password` | RabbitMQ password (must be set together with username) | no |
| `exchange` | AMQP exchange | no |
| `routing_key` | AMQP routing key | no |
| `vhost` | AMQP vhost (default: `/`) | no |
| `delivery_mode` | 1=transient, 2=persistent (default: 2) | no |
| `content_type` | Default Content-Type | no |
| `timeout` | RPC timeout (default: `30s`) | no |
| `headers` | Default AMQP headers (key-value map) | no |
| `response` | Custom HTTP response (see below) | no |

**Response customization:**

| Field | Description |
|---|---|
| `response.status` | HTTP status code (overrides default: `202` for publish, `200` for rpc) |
| `response.content_type` | Content-Type header (default: `application/json`) |
| `response.body` | Response body. When `content_type` is `text/*`, must be a string and returned as-is. Otherwise JSON-encoded. |

When `response` is set on an RPC alias, the RPC reply from RabbitMQ is discarded and the configured response is returned instead.

HTTP request headers (`Amqp-*`, `Content-Type`) can override alias defaults.
If the client provides `Authorization: Basic ...`, it overrides the alias credentials.

### Client Subcommands

`publish` and `rpc` subcommands act as HTTP clients to the gateway. They do not require `--config`.

```console
# Publish a message
$ amqp-gateway publish http://localhost:8080 \
  -u guest -p guest \
  --routing-key my.queue \
  --content-type application/json \
  --body '{"hello":"world"}'

# Publish from file
$ amqp-gateway publish http://localhost:8080 \
  -u guest -p guest \
  --routing-key my.queue \
  --body-file message.json

# Publish from stdin
$ echo '{"hello":"world"}' | amqp-gateway publish http://localhost:8080 \
  -u guest -p guest \
  --routing-key my.queue \
  --body-file -

# RPC call
$ amqp-gateway rpc http://localhost:8080 \
  -u guest -p guest \
  --routing-key my.rpc.key \
  --timeout 5s \
  --body '{"request":"data"}'

# Custom AMQP headers
$ amqp-gateway publish http://localhost:8080 \
  -u guest -p guest \
  --routing-key my.queue \
  -H X-Request-Id=abc123 \
  --body test
```

`--body` and `--body-file` are mutually exclusive.

## API

### POST /v1/publish

Fire-and-forget publish with publisher confirm.

```console
$ curl -X POST http://localhost:8080/v1/publish \
  -u guest:guest \
  -H "Content-Type: application/json" \
  -H "Amqp-Exchange: my-exchange" \
  -H "Amqp-Routing-Key: my.routing.key" \
  -d '{"message": "hello"}'
# 202 Accepted
```

### POST /v1/rpc

RPC call — publishes a message and waits for a response on a temporary queue.

```console
$ curl -X POST http://localhost:8080/v1/rpc \
  -u guest:guest \
  -H "Content-Type: application/json" \
  -H "Amqp-Exchange: my-exchange" \
  -H "Amqp-Routing-Key: my.rpc.key" \
  -H "Amqp-Timeout: 5000" \
  -d '{"request": "data"}'
# 200 OK with response body
```

### GET /healthz

Service liveness check. Always returns `200 OK`.

### GET /readyz

RabbitMQ connectivity check. Returns `200 OK` if connected, `503` otherwise.

### HTTP Headers → AMQP Fields

| HTTP Header | AMQP Field | Default |
|---|---|---|
| `Amqp-Exchange` | exchange | `""` (default exchange) |
| `Amqp-Routing-Key` | routing key | `""` |
| `Amqp-Vhost` | vhost | `/` |
| `Amqp-Delivery-Mode` | delivery_mode | `2` (persistent) |
| `Amqp-Message-Id` | message_id | auto-generated (UUID v7) |
| `Amqp-Correlation-Id` | correlation_id | auto-generated for RPC |
| `Amqp-Expiration` | expiration | — |
| `Amqp-Timeout` | — | `30000` (RPC only, ms) |
| `Amqp-Header-*` | headers table | — |
| `Content-Type` | content_type | — |

### Authentication

HTTP Basic authentication. Credentials are passed through to RabbitMQ for connection authentication.

### Error Responses

| Code | Meaning |
|---|---|
| `400` | Validation error (invalid headers) |
| `401` | RabbitMQ authentication failure |
| `403` | Exchange permission denied |
| `404` | Exchange not found |
| `504` | RPC timeout |
| `503` | RabbitMQ unavailable |

## OpenTelemetry

amqp-gateway supports OpenTelemetry tracing and metrics. Enable by setting environment variables:

```console
$ export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
$ export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf  # or "grpc" (default: http/protobuf)
$ amqp-gateway -c config.jsonnet
```

When enabled, no code or config changes are needed — OTel is configured entirely via environment variables.

### Traces

Trace context (W3C `traceparent`) flows through the full request lifecycle:

```
HTTP client → amqp-gateway → RabbitMQ → consumer (e.g. mqsubscriber)
```

- **HTTP request**: Extracts `traceparent` from incoming HTTP headers
- **AMQP publish**: Injects `traceparent` into AMQP message headers, so downstream consumers (e.g. [mqsubscriber](https://github.com/fujiwara/mqsubscriber)) can continue the trace
- **RPC response**: Extracts `traceparent` from AMQP response and propagates it to the HTTP response

Spans created:
- `http.request` — HTTP request lifecycle
- `amqp.publish` — AMQP publish operation (SpanKind: Producer)
- `amqp.rpc` — RPC operation (SpanKind: Client)
- `amqp.rpc.wait` — Waiting for RPC response

### Metrics

| Metric | Type | Attributes |
|---|---|---|
| `amqp_gateway.http.requests` | Counter | method, path, status |
| `amqp_gateway.http.duration` | Histogram (s) | method, path |
| `amqp_gateway.publish.total` | Counter | result (success/error) |
| `amqp_gateway.rpc.total` | Counter | result (success/error/timeout) |
| `amqp_gateway.log.messages` | Counter | level |

### Log Correlation

When OTel is enabled, all structured logs (JSON) automatically include `trace_id` and `span_id` fields for correlation with traces.

## Install

### Homebrew

```console
$ brew install fujiwara/tap/amqp-gateway
```

### mise

```console
$ mise use github:fujiwara/amqp-gateway
```

### Go

```console
$ go install github.com/fujiwara/amqp-gateway/cmd/amqp-gateway@latest
```

### Binary

Download from [Releases](https://github.com/fujiwara/amqp-gateway/releases).

## LICENSE

MIT

## Author

fujiwara
