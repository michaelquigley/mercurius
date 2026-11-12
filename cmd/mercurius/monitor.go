package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/michaelquigley/mercurius/internal/config"
	"github.com/michaelquigley/mercurius/internal/runlock"
	"github.com/michaelquigley/mercurius/internal/store"
	"github.com/spf13/cobra"
)

// monitorPollInterval is how often --wait re-reads a round's state.
var monitorPollInterval = time.Second

func newMonitorCommand(configPath *string) *cobra.Command {
	var sessionID string
	var all bool
	var wait bool

	cmd := &cobra.Command{
		Use:          "monitor",
		Short:        "monitor Mercurius review sessions",
		Long:         "Print Mercurius sessions and rounds from the log destination. The monitor only reads: a round marked running whose runner is gone is printed as lost and left for a server to record.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if wait && all {
				return errors.New("--wait requires a single --session")
			}
			if !all && sessionID == "" {
				return errors.New("either --session or --all is required")
			}
			resolved, err := config.Resolve(*configPath, config.UserConfigPath())
			if err != nil {
				return err
			}
			logDestination := resolved.Project.LogDestination
			if all {
				return monitorAll(cmd.OutOrStdout(), logDestination)
			}
			return monitorSession(cmd, logDestination, sessionID, wait)
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "session id to monitor")
	cmd.Flags().BoolVar(&all, "all", false, "show all sessions")
	cmd.Flags().BoolVar(&wait, "wait", false, "poll the latest round's _state.json until it completes or fails")
	return cmd
}

// monitorView is what the monitor knows about one session, read from disk.
type monitorView struct {
	SessionID string
	State     string
	Rounds    []monitorRound
}

type monitorRound struct {
	Number    int
	State     string
	Lost      bool
	Reviewer  string
	StartedAt time.Time
	StatePath string
	LogPath   string
	Error     *store.StoredError
}

func monitorAll(out io.Writer, logDestination string) error {
	ids, err := store.Sessions(logDestination)
	if err != nil {
		return err
	}
	for _, id := range ids {
		view, err := readMonitorView(logDestination, id)
		if err != nil {
			return err
		}
		printMonitorView(out, view)
	}
	return nil
}

func monitorSession(cmd *cobra.Command, logDestination string, sessionID string, wait bool) error {
	out := cmd.OutOrStdout()
	view, err := readMonitorView(logDestination, sessionID)
	if err != nil {
		return err
	}
	printMonitorView(out, view)
	if !wait || len(view.Rounds) == 0 {
		return nil
	}
	latest := view.Rounds[len(view.Rounds)-1]
	if latest.State != store.RoundRunning || latest.Lost {
		return nil
	}

	roundDir := filepath.Dir(latest.StatePath)
	ticker := time.NewTicker(monitorPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-cmd.Context().Done():
			return nil
		case <-ticker.C:
			round, err := readMonitorRound(logDestination, roundDir, latest.Number, true)
			if err != nil {
				return err
			}
			if round.State == store.RoundRunning && !round.Lost {
				continue
			}
			printWaitedRound(out, sessionID, round)
			return nil
		}
	}
}

// readMonitorView reads a session for display. It never writes: a round marked
// running whose lock is free is reported as lost and left as it is.
func readMonitorView(logDestination string, sessionID string) (monitorView, error) {
	if !store.ValidSessionID(sessionID) {
		return monitorView{}, fmt.Errorf("session '%s' not found", sessionID)
	}
	sessionDir := store.SessionDir(logDestination, sessionID)
	session, err := store.ReadSession(sessionDir)
	if errors.Is(err, store.ErrNotSession) {
		return monitorView{}, fmt.Errorf("session '%s' not found: '%s' has no %s", sessionID, sessionDir, store.SessionFileName)
	}
	if errors.Is(err, os.ErrNotExist) {
		return monitorView{}, fmt.Errorf("session '%s' not found", sessionID)
	}
	if err != nil {
		return monitorView{}, err
	}
	numbers, err := store.Rounds(sessionDir)
	if err != nil {
		return monitorView{}, err
	}
	view := monitorView{SessionID: session.SessionID, State: session.State}
	for i, n := range numbers {
		round, err := readMonitorRound(logDestination, store.RoundDir(sessionDir, n), n, i == len(numbers)-1)
		if err != nil {
			return monitorView{}, err
		}
		view.Rounds = append(view.Rounds, round)
	}
	return view, nil
}

func readMonitorRound(logDestination string, roundDir string, number int, latest bool) (monitorRound, error) {
	state, err := store.ReadState(roundDir)
	if err != nil {
		return monitorRound{}, err
	}
	round := monitorRound{
		Number:    number,
		State:     state.State,
		StartedAt: state.StartedAt,
		StatePath: filepath.Join(roundDir, store.StateFileName),
		Error:     state.Error,
	}
	if run, err := store.ReadRun(roundDir); err == nil {
		round.Reviewer = run.Reviewer.Name
	}
	if state.State == store.RoundCompleted {
		round.LogPath = filepath.Join(roundDir, store.RoundLogFileName)
	}
	if latest && state.State == store.RoundRunning {
		fresh, lost, err := runnerGone(logDestination, roundDir, state)
		if err != nil {
			return monitorRound{}, err
		}
		round.Lost = lost
		round.State = fresh.State
		round.Error = fresh.Error
		if fresh.State == store.RoundCompleted {
			round.LogPath = filepath.Join(roundDir, store.RoundLogFileName)
		}
	}
	return round, nil
}

// runnerGone makes one read-only attempt at the runner lock. If it gets the
// lock, it re-reads the round's state while holding it, since the runner may
// have finished between the first read and the attempt, and reports the round
// lost when the state still says running. It writes nothing and creates
// nothing; a missing lock file means the lock is free.
func runnerGone(logDestination string, roundDir string, state store.State) (store.State, bool, error) {
	lock, err := runlock.TakeOnceReadOnly(logDestination)
	if errors.Is(err, runlock.ErrHeld) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	defer lock.Release()
	fresh, err := store.ReadState(roundDir)
	if err != nil {
		return state, false, err
	}
	return fresh, fresh.State == store.RoundRunning, nil
}

func printMonitorView(out io.Writer, view monitorView) {
	fmt.Fprintf(out, "session '%s' %s\n", view.SessionID, view.State)
	fmt.Fprintf(out, "rounds: %d\n", len(view.Rounds))
	for _, round := range view.Rounds {
		state := round.State
		if round.Lost {
			state = "lost"
		}
		fmt.Fprintf(out, "round %d %s reviewer='%s' started=%s\n", round.Number, state, round.Reviewer, formatMonitorTime(round.StartedAt))
		switch {
		case round.Lost:
			fmt.Fprintln(out, "  marked running, but its runner is gone; the next server call on this session records it failed")
		case round.State == store.RoundRunning:
			fmt.Fprintf(out, "  monitor file: state='%s'\n", round.StatePath)
		case round.State == store.RoundFailed && round.Error != nil:
			fmt.Fprintf(out, "  error: %s - %s\n", round.Error.Code, round.Error.Message)
		}
	}
}

func printWaitedRound(out io.Writer, sessionID string, round monitorRound) {
	switch {
	case round.Lost:
		fmt.Fprintf(out, "\nround %d lost\n", round.Number)
		fmt.Fprintln(out, "the round is marked running but its runner is gone")
		fmt.Fprintf(out, "next: ask the design agent to call collect_round for session '%s' round %d, then decide whether to retry\n", sessionID, round.Number)
	case round.State == store.RoundCompleted:
		fmt.Fprintf(out, "\nround %d completed\n", round.Number)
		if round.LogPath != "" {
			fmt.Fprintf(out, "log: '%s'\n", round.LogPath)
		}
		fmt.Fprintf(out, "next: ask the design agent to call collect_round for session '%s' round %d\n", sessionID, round.Number)
	default:
		fmt.Fprintf(out, "\nround %d %s\n", round.Number, round.State)
		if round.Error != nil {
			fmt.Fprintf(out, "error: %s - %s\n", round.Error.Code, round.Error.Message)
		}
		fmt.Fprintf(out, "next: ask the design agent to call collect_round for session '%s' round %d, then decide whether to retry\n", sessionID, round.Number)
	}
}

func formatMonitorTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
