package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/fujiwara/sloghandler/otelmetrics"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// CLI represents the command-line interface.
type CLI struct {
	Config   string           `kong:"short='c',env='AMQP_GATEWAY_CONFIG',help='Config file path (Jsonnet/JSON)'"`
	LogLevel string           `kong:"default='info',enum='debug,info,warn,error',env='AMQP_GATEWAY_LOG_LEVEL',help='Log level'"`
	Run      RunCmd           `cmd:"" default:"1" help:"Run the gateway server"`
	Validate ValidateCmd      `cmd:"" help:"Validate config"`
	Render   RenderCmd        `cmd:"" help:"Render config as JSON to stdout"`
	Publish  ClientPublishCmd `cmd:"" help:"Publish a message via HTTP"`
	RPC      ClientRPCCmd     `cmd:"" help:"Send an RPC request via HTTP"`
	Version  kong.VersionFlag `help:"Show version"`
}

// RunCmd runs the gateway server.
type RunCmd struct{}

func (cmd *RunCmd) Run(ctx context.Context, cli *CLI) error {
	if cli.Config == "" {
		return fmt.Errorf("--config is required for run command")
	}
	cfg, err := LoadConfig(ctx, cli.Config)
	if err != nil {
		return err
	}
	return RunServer(ctx, cfg)
}

// ValidateCmd validates the configuration file.
type ValidateCmd struct{}

func (cmd *ValidateCmd) Run(ctx context.Context, cli *CLI) error {
	if cli.Config == "" {
		return fmt.Errorf("--config is required for validate command")
	}
	_, err := LoadConfig(ctx, cli.Config)
	if err != nil {
		return err
	}
	slog.Info("config is valid")
	return nil
}

// RenderCmd renders the configuration as JSON.
type RenderCmd struct{}

func (cmd *RenderCmd) Run(ctx context.Context, cli *CLI) error {
	if cli.Config == "" {
		return fmt.Errorf("--config is required for render command")
	}
	out, err := RenderConfig(ctx, cli.Config)
	if err != nil {
		return err
	}
	fmt.Print(string(out))
	return nil
}

// RunCLI parses command-line arguments and executes the appropriate command.
func RunCLI(ctx context.Context) error {
	var cli CLI
	kctx := kong.Parse(&cli,
		kong.Name("amqp-gateway"),
		kong.Description("AMQP HTTP Gateway - Publish messages to RabbitMQ via HTTP"),
		kong.Vars{"version": Version},
		kong.BindTo(ctx, (*context.Context)(nil)),
	)
	serviceName := "amqp-gateway"
	cmd := kctx.Command()
	if strings.HasPrefix(cmd, "publish") || strings.HasPrefix(cmd, "rpc") {
		serviceName = "amqp-gateway-client"
	}
	shutdownOTel, err := setupOTelProviders(ctx, serviceName)
	if err != nil {
		return fmt.Errorf("failed to setup OpenTelemetry providers: %w", err)
	}
	defer shutdownOTel(context.WithoutCancel(ctx))
	setupLogger(cli.LogLevel)
	return kctx.Run(&cli)
}

func setupLogger(level string) {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	jsonHandler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lv})

	meter := otel.Meter(tracerName)
	logCounter, err := meter.Int64Counter("amqp_gateway.log.messages",
		metric.WithDescription("Log messages total by level"),
	)
	if err != nil {
		slog.Error("failed to create log messages counter", "error", err)
		slog.SetDefault(slog.New(newTraceHandler(jsonHandler)))
		return
	}
	slog.SetDefault(slog.New(newTraceHandler(newMetricsHandler(jsonHandler, logCounter, lv))))
}

func newMetricsHandler(base slog.Handler, counter metric.Int64Counter, minLevel slog.Level) slog.Handler {
	return otelmetrics.NewHandlerWithOptions(base, counter, &otelmetrics.Options{
		MinLevel: minLevel,
	})
}
