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
	root.PersistentFlags().StringVar(&configPath, "config", "./mercurius.yaml", "path to mercurius.yaml")
	root.Flags().BoolVar(&verbose, "verbose", false, "enable verbose stderr logging")
	root.AddCommand(newMonitorCommand(&configPath))
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
	cmd.Flags().BoolVar(&wait, "wait", false, "stream events until the active round completes or fails")
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
	eventsPath := monitorpkg.EventsPath(sessionDir)
	status, err := monitorpkg.ReadStatus(statusPath)
	if err != nil {
		return err
	}
	printStatus(cmd, status)

	events, err := monitorpkg.ReadEvents(eventsPath)
	if err != nil {
		return err
	}
	for _, event := range events {
		printEvent(cmd, event)
	}
	if !wait {
		return nil
	}
	waitRound := 0
	if status.ActiveRound != nil {
		waitRound = status.ActiveRound.RoundNumber
	} else {
		return nil
	}

	seen := len(events)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-cmd.Context().Done():
			return nil
		case <-ticker.C:
			events, err := monitorpkg.ReadEvents(eventsPath)
			if err != nil {
				return err
			}
			for _, event := range events[seen:] {
				printEvent(cmd, event)
				if event.RoundNumber == waitRound && (event.Event == "round_completed" || event.Event == "round_failed") {
					return nil
				}
			}
			seen = len(events)
		}
	}
}

func printStatus(cmd *cobra.Command, status monitorpkg.SessionStatus) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "session %s state=%s rounds_used=%d budget_remaining=%d\n", status.SessionID, status.State, status.RoundsUsed, status.BudgetRemaining)
	if status.ReviewContextSource != "" {
		fmt.Fprintf(out, "review_context source=%s present=%t\n", status.ReviewContextSource, status.ReviewContextPresent)
	}
	if status.Convergence.Signal != "" {
		fmt.Fprintf(out, "convergence signal=%s latest_blocking=%d previous_blocking=%d accepted_decisions=%d declined_or_deferred_decisions=%d\n", status.Convergence.Signal, status.Convergence.LatestBlockingFindings, status.Convergence.PreviousBlockingFindings, status.Convergence.AcceptedDecisions, status.Convergence.DeclinedOrDeferredDecisions)
		if status.Convergence.Message != "" {
			fmt.Fprintf(out, "convergence_message=%s\n", status.Convergence.Message)
		}
	}
	if status.ActiveRound != nil {
		fmt.Fprintf(out, "active round %d state=%s reviewer=%s started=%s\n", status.ActiveRound.RoundNumber, status.ActiveRound.State, status.ActiveRound.Reviewer, formatMonitorTime(status.ActiveRound.StartedAt))
		fmt.Fprintf(out, "status=%s events=%s\n", status.ActiveRound.StatusPath, status.ActiveRound.EventsPath)
	} else if status.LastRoundJob != nil {
		fmt.Fprintf(out, "last round %d state=%s reviewer=%s updated=%s\n", status.LastRoundJob.RoundNumber, status.LastRoundJob.State, status.LastRoundJob.Reviewer, formatMonitorTime(status.LastRoundJob.UpdatedAt))
		if status.LastRoundJob.LogPath != "" {
			fmt.Fprintf(out, "log=%s\n", status.LastRoundJob.LogPath)
		}
	}
}

func printEvent(cmd *cobra.Command, event monitorpkg.Event) {
	out := cmd.OutOrStdout()
	line := fmt.Sprintf("[%s] %s", formatMonitorTime(event.At), event.Event)
	if event.SessionID != "" {
		line += " session=" + event.SessionID
	}
	if event.RoundNumber != 0 {
		line += fmt.Sprintf(" round=%d", event.RoundNumber)
	}
	if event.Reviewer != "" {
		line += " reviewer=" + event.Reviewer
	}
	if event.State != "" {
		line += " state=" + event.State
	}
	if event.LogPath != "" {
		line += " log=" + event.LogPath
	}
	if event.Error != nil {
		line += " error=" + event.Error.Code
	}
	fmt.Fprintln(out, line)
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
