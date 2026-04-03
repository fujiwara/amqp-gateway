package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/alecthomas/kong"
)

// CLI represents the command-line interface.
type CLI struct {
	Config   string           `kong:"required,short='c',env='AMQP_GATEWAY_CONFIG',help='Config file path (Jsonnet/JSON)'"`
	LogLevel string           `kong:"default='info',enum='debug,info,warn,error',env='AMQP_GATEWAY_LOG_LEVEL',help='Log level'"`
	Run      RunCmd           `cmd:"" default:"1" help:"Run the gateway server"`
	Validate ValidateCmd      `cmd:"" help:"Validate config"`
	Render   RenderCmd        `cmd:"" help:"Render config as JSON to stdout"`
	Version  kong.VersionFlag `help:"Show version"`
}

// RunCmd runs the gateway server.
type RunCmd struct{}

func (cmd *RunCmd) Run(cli *CLI) error {
	ctx := context.Background()
	cfg, err := LoadConfig(ctx, cli.Config)
	if err != nil {
		return err
	}
	return RunServer(ctx, cfg)
}

// ValidateCmd validates the configuration file.
type ValidateCmd struct{}

func (cmd *ValidateCmd) Run(cli *CLI) error {
	ctx := context.Background()
	_, err := LoadConfig(ctx, cli.Config)
	if err != nil {
		return err
	}
	slog.Info("config is valid")
	return nil
}

// RenderCmd renders the configuration as JSON.
type RenderCmd struct{}

func (cmd *RenderCmd) Run(cli *CLI) error {
	ctx := context.Background()
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
	)
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
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lv})
	slog.SetDefault(slog.New(handler))
}
