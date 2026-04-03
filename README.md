# amqp-gateway

AMQP HTTP Gateway — Publish messages to RabbitMQ via HTTP.

For clients that cannot connect to RabbitMQ directly, this gateway provides HTTP endpoints to publish messages and perform RPC calls.

## Usage

```console
$ amqp-gateway -c config.jsonnet
```

### CLI Options

```
Usage: amqp-gateway -c <config> [flags] <command>

Flags:
  -c, --config       Config file path (Jsonnet/JSON) [required, env: AMQP_GATEWAY_CONFIG]
      --log-level    Log level (debug, info, warn, error) [default: info, env: AMQP_GATEWAY_LOG_LEVEL]
      --version      Show version

Commands:
  run        Run the gateway server (default)
  validate   Validate config
  render     Render config as JSON to stdout
```

### Configuration

Jsonnet or JSON format:

```jsonnet
{
  rabbitmq_url: "amqp://rabbitmq.internal:5672",
  listen_addr: ":8080",  // default: ":8080"
}
```

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
| `Amqp-Message-Id` | message_id | — |
| `Amqp-Correlation-Id` | correlation_id | auto-generated for RPC |
| `Amqp-Expiration` | expiration | — |
| `Amqp-Mandatory` | mandatory flag | `false` |
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

## Install

```console
$ go install github.com/fujiwara/amqp-gateway/cmd/amqp-gateway@latest
```

Or download from [Releases](https://github.com/fujiwara/amqp-gateway/releases).

## LICENSE

MIT

## Author

fujiwara
