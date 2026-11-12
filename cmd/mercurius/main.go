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
	"github.com/michaelquigley/push/build"
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

			// the configuration is not read here. every tool call resolves it, so
			// the server starts in a project with no mercurius.yaml and answers
			// with a tool error naming the path instead of dying at launch.
			server, _, err := mcpserver.New(mcpserver.Options{
				ConfigPath:     configPath,
				UserConfigPath: config.UserConfigPath(),
			})
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
	root.PersistentFlags().StringVar(&configPath, "config", config.DefaultProjectPath, "path to mercurius.yaml")
	root.Flags().BoolVar(&verbose, "verbose", false, "enable verbose stderr logging")
	root.AddCommand(newMonitorCommand(&configPath))
	root.AddCommand(newBootstrapCommand())
	root.AddCommand(newRunRoundCommand())
	root.AddCommand(build.NewVersionCmd("mercurius"))
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
