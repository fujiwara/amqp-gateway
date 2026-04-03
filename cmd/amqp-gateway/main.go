package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"

	gateway "github.com/fujiwara/amqp-gateway"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), signals()...)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	return gateway.Run(ctx)
}
