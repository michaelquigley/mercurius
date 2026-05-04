package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/michaelquigley/df/dl"
	"github.com/michaelquigley/mercurius/internal/config"
	"github.com/michaelquigley/mercurius/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

func main() {
	configureLogging(false)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := newRootCommand()
	if err := root.ExecuteContext(ctx); err != nil {
		dl.Fatalf("mercurius failed: %v", err)
	}
}

func newRootCommand() *cobra.Command {
	var configPath string
	var verbose bool

	root := &cobra.Command{
		Use:           "mercurius",
		Short:         "run the Mercurius MCP server",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			configureLogging(verbose)

			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			server, _, err := mcpserver.New(cfg)
			if err != nil {
				return err
			}

			err = server.Run(cmd.Context(), &mcp.StdioTransport{})
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	root.Flags().StringVar(&configPath, "config", "./mercurius.yaml", "path to mercurius.yaml")
	root.Flags().BoolVar(&verbose, "verbose", false, "enable verbose stderr logging")
	return root
}

func configureLogging(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	dl.Init(dl.DefaultOptions().
		SetOutput(os.Stderr).
		SetTrimPrefix("github.com/michaelquigley/mercurius/").
		SetLevel(level))
}
