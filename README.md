# amqp-gateway

AMQP HTTP Gateway — Publish messages to RabbitMQ via HTTP.

For clients that cannot connect to RabbitMQ directly, this gateway provides HTTP endpoints to publish messages and perform RPC calls.

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

Jsonnet or JSON format:

```jsonnet
{
  rabbitmq_url: "amqp://rabbitmq.internal:5672",
  listen_addr: ":8080",  // default: ":8080"
}
```

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

When `Amqp-Mandatory: true` is set and the message cannot be routed to any queue, the server returns `404 Not Found`.

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
| `404` | Exchange not found / message unroutable (mandatory) |
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
