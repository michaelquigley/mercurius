package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/michaelquigley/df/dl"
	"github.com/michaelquigley/mercurius/internal/config"
	"github.com/michaelquigley/mercurius/internal/mcpserver"
	monitorpkg "github.com/michaelquigley/mercurius/internal/monitor"
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

			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			if err := cfg.EnsureLogDestination(); err != nil {
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
	root.PersistentFlags().StringVar(&configPath, "config", "./mercurius.yaml", "path to mercurius.yaml")
	root.Flags().BoolVar(&verbose, "verbose", false, "enable verbose stderr logging")
	root.AddCommand(newMonitorCommand(&configPath))
	root.AddCommand(newBootstrapCommand())
	root.AddCommand(build.NewVersionCmd("mercurius"))
	return root
}

func newMonitorCommand(configPath *string) *cobra.Command {
	var sessionID string
	var all bool
	var wait bool

	cmd := &cobra.Command{
		Use:          "monitor",
		Short:        "monitor Mercurius review sessions",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if wait && all {
				return errors.New("--wait requires a single --session")
			}
			if !all && sessionID == "" {
				return errors.New("either --session or --all is required")
			}
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			if all {
				return monitorAll(cmd, cfg.LogDestination)
			}
			return monitorSession(cmd, cfg.LogDestination, sessionID, wait)
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "session id to monitor")
	cmd.Flags().BoolVar(&all, "all", false, "show all sessions with status files")
	cmd.Flags().BoolVar(&wait, "wait", false, "poll status.json until the active round completes or fails")
	return cmd
}

func monitorAll(cmd *cobra.Command, logDestination string) error {
	entries, err := os.ReadDir(logDestination)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		statusPath := monitorpkg.StatusPath(monitorpkg.SessionDir(logDestination, entry.Name()))
		status, err := monitorpkg.ReadStatus(statusPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		printStatus(cmd, status)
	}
	return nil
}

func monitorSession(cmd *cobra.Command, logDestination string, sessionID string, wait bool) error {
	sessionDir := monitorpkg.SessionDir(logDestination, sessionID)
	statusPath := monitorpkg.StatusPath(sessionDir)
	status, err := monitorpkg.ReadStatus(statusPath)
	if err != nil {
		return err
	}
	printStatus(cmd, status)
	if !wait || status.ActiveRound == nil {
		return nil
	}

	waitRound := status.ActiveRound.RoundNumber
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-cmd.Context().Done():
			return nil
		case <-ticker.C:
			status, err := monitorpkg.ReadStatus(statusPath)
			if err != nil {
				return err
			}
			if status.ActiveRound != nil && status.ActiveRound.RoundNumber == waitRound {
				continue
			}
			// active round either advanced or cleared; pick up the round
			// status from the rounds list or the lingering active job.
			printWaitedRoundTerminal(cmd, status, waitRound)
			return nil
		}
	}
}

func printStatus(cmd *cobra.Command, status monitorpkg.SessionStatus) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "session '%s' %s\n", status.SessionID, status.State)
	fmt.Fprintf(out, "rounds: %d\n", status.RoundCount)
	if status.ReviewContextPresent {
		fmt.Fprintln(out, "review context: present")
	}
	if status.ReviewFocusPresent {
		fmt.Fprintln(out, "review focus: present")
	}
	if status.LastError != nil {
		fmt.Fprintf(out, "last error: %s - %s\n", status.LastError.Code, status.LastError.Message)
	}
	if status.ActiveRound != nil {
		fmt.Fprintf(out, "active round: %d %s reviewer='%s' started=%s\n", status.ActiveRound.RoundNumber, status.ActiveRound.State, status.ActiveRound.Reviewer, formatMonitorTime(status.ActiveRound.StartedAt))
		fmt.Fprintf(out, "monitor file: status='%s'\n", status.ActiveRound.StatusPath)
	}
}

func printWaitedRoundTerminal(cmd *cobra.Command, status monitorpkg.SessionStatus, roundNumber int) {
	out := cmd.OutOrStdout()
	// look up the completed round in the rounds list (success path) or
	// fall through to last-error reporting if the round failed.
	for _, r := range status.Rounds {
		if r.RoundNumber == roundNumber {
			fmt.Fprintf(out, "\nround %d completed\n", r.RoundNumber)
			if r.LogPath != "" {
				fmt.Fprintf(out, "log: '%s'\n", r.LogPath)
			}
			fmt.Fprintf(out, "next: ask the design agent to call collect_round for session '%s' round %d\n", status.SessionID, r.RoundNumber)
			return
		}
	}
	fmt.Fprintf(out, "\nround %d failed\n", roundNumber)
	if status.LastError != nil {
		fmt.Fprintf(out, "error: %s - %s\n", status.LastError.Code, status.LastError.Message)
	}
	fmt.Fprintln(out, "next: inspect session_status.last_error, then decide whether to retry")
}

func formatMonitorTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
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
