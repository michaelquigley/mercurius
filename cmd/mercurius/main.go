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
			// Load() is now side-effect-free; the server startup path is the
			// caller responsible for creating log_destination before any
			// session opens.
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
	root.AddCommand(newPreviewCommand(&configPath))
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
	if len(events) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "events:")
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
					printRoundTerminalAction(cmd, event)
					return nil
				}
			}
			seen = len(events)
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
		if status.LastError.NextAction != "" {
			fmt.Fprintf(out, "next: %s\n", status.LastError.NextAction)
		}
	}
	if status.ActiveRound != nil {
		fmt.Fprintf(out, "active round: %d %s reviewer='%s' started=%s\n", status.ActiveRound.RoundNumber, status.ActiveRound.State, status.ActiveRound.Reviewer, formatMonitorTime(status.ActiveRound.StartedAt))
		fmt.Fprintf(out, "monitor files: status='%s' events='%s'\n", status.ActiveRound.StatusPath, status.ActiveRound.EventsPath)
	} else if status.LastRoundJob != nil {
		fmt.Fprintf(out, "last round: %d %s reviewer='%s' updated=%s\n", status.LastRoundJob.RoundNumber, status.LastRoundJob.State, status.LastRoundJob.Reviewer, formatMonitorTime(status.LastRoundJob.UpdatedAt))
		if status.LastRoundJob.LogPath != "" {
			fmt.Fprintf(out, "log: '%s'\n", status.LastRoundJob.LogPath)
		}
		if status.LastRoundJob.Error != nil {
			fmt.Fprintf(out, "round error: %s - %s\n", status.LastRoundJob.Error.Code, status.LastRoundJob.Error.Message)
		}
	}
}

func printEvent(cmd *cobra.Command, event monitorpkg.Event) {
	out := cmd.OutOrStdout()
	line := fmt.Sprintf("  %s  %s", formatMonitorTime(event.At), event.Event)
	if event.RoundNumber != 0 {
		line += fmt.Sprintf(" round=%d", event.RoundNumber)
	}
	if event.Reviewer != "" {
		line += " reviewer='" + event.Reviewer + "'"
	}
	if event.State != "" {
		line += " state=" + event.State
	}
	if event.LogPath != "" {
		line += " log='" + event.LogPath + "'"
	}
	if event.Error != nil {
		line += " error=" + event.Error.Code
	}
	fmt.Fprintln(out, line)
}

func printRoundTerminalAction(cmd *cobra.Command, event monitorpkg.Event) {
	out := cmd.OutOrStdout()
	switch event.Event {
	case "round_completed":
		fmt.Fprintf(out, "\nround %d completed\n", event.RoundNumber)
		if event.LogPath != "" {
			fmt.Fprintf(out, "log: '%s'\n", event.LogPath)
		}
		if event.SessionID != "" {
			fmt.Fprintf(out, "next: ask the design agent to call collect_round for session '%s' round %d\n", event.SessionID, event.RoundNumber)
		} else {
			fmt.Fprintf(out, "next: ask the design agent to call collect_round for round %d\n", event.RoundNumber)
		}
	case "round_failed":
		fmt.Fprintf(out, "\nround %d failed\n", event.RoundNumber)
		if event.Error != nil {
			fmt.Fprintf(out, "error: %s - %s\n", event.Error.Code, event.Error.Message)
			if event.Error.NextAction != "" {
				fmt.Fprintf(out, "next: %s\n", event.Error.NextAction)
				return
			}
		}
		fmt.Fprintln(out, "next: inspect session_status.last_error, then decide whether to retry")
	}
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
