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
	if err := gateway.RunCLI(ctx); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}
