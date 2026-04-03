# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

amqp-gateway is an HTTP-to-AMQP gateway that proxies HTTP requests to RabbitMQ. Clients authenticate via HTTP Basic auth, and credentials are passed through to RabbitMQ.

Module: `github.com/fujiwara/amqp-gateway`, Go 1.25+.

## Build & Test Commands

```bash
make amqp-gateway    # Build binary
make test            # Run all tests (go test -v ./...)
make install         # go install
make dist            # Build release with goreleaser
make clean           # Remove build artifacts
```

Run a single test:
```bash
go test -v -run TestName .
```

Before committing, always run:
```bash
go fmt ./...
go fix ./...
```

## Architecture

- **Root package (`gateway`)**: Core library containing all business logic.
  - `config.go` — Config loading via `fujiwara/jsonnet-armed` (Jsonnet/JSON), strict JSON parsing with `DisallowUnknownFields()`
  - `cli.go` — CLI using `alecthomas/kong` with subcommands: run (default), validate, render
  - `server.go` — HTTP server with `net/http` ServeMux, Basic auth parsing, AMQP error → HTTP status mapping
  - `header.go` — `AMQP-*` HTTP header → AMQP field mapping, `PublishParams` struct
  - `amqp.go` — Per-request AMQP connection/channel management, publish with confirm, RPC with temp queue
  - `version.go` — Version variable (set by goreleaser)
- **`cmd/amqp-gateway/`**: Entry point. Signal handling (platform-specific build tags) and `RunCLI()` call.

## Dependencies

Aligned with `fujiwara/mqsubscriber`:
- AMQP: `rabbitmq/amqp091-go`
- Config: `fujiwara/jsonnet-armed`
- CLI: `alecthomas/kong`

## CI

- `test.yml`: Runs `go test -race ./...` with RabbitMQ service container (Go 1.25/1.26 matrix)
- `tagpr-release.yml`: `Songmu/tagpr` + `goreleaser`
