package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	monitorpkg "github.com/michaelquigley/mercurius/internal/monitor"
	"github.com/spf13/cobra"
)

func TestPrintStatusSuppressesEmptyConvergence(t *testing.T) {
	cmd, out := testCommand()
	now := time.Date(2026, 5, 5, 19, 38, 10, 0, time.UTC)

	printStatus(cmd, monitorpkg.SessionStatus{
		SessionID:            "s_test",
		State:                "active",
		Budget:               4,
		BudgetRemaining:      4,
		ReviewContextSource:  "config",
		ReviewContextPresent: true,
		Convergence:          monitorpkg.Convergence{Signal: "none", Message: "No convergence signal yet."},
		ActiveRound: &monitorpkg.RoundJob{
			SessionID:   "s_test",
			RoundNumber: 1,
			State:       "running",
			Reviewer:    "codex",
			StartedAt:   now,
			StatusPath:  "/tmp/status.json",
			EventsPath:  "/tmp/events.ndjson",
		},
	})

	got := out.String()
	for _, want := range []string{
		"session 's_test' active",
		"review context: config",
		"active round: 1 running reviewer='codex'",
		"monitor files: status='/tmp/status.json' events='/tmp/events.ndjson'",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected monitor status to contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "convergence") {
		t.Fatalf("empty convergence should be suppressed:\n%s", got)
	}
}

func TestPrintRoundTerminalActionCompleted(t *testing.T) {
	cmd, out := testCommand()

	printRoundTerminalAction(cmd, monitorpkg.Event{
		Event:       "round_completed",
		SessionID:   "s_test",
		RoundNumber: 2,
		LogPath:     "/tmp/round-02.md",
	})

	got := out.String()
	for _, want := range []string{
		"round 2 completed",
		"log: '/tmp/round-02.md'",
		"next: ask the design agent to call collect_round for session 's_test' round 2",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected terminal action to contain %q:\n%s", want, got)
		}
	}
}

func testCommand() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	return cmd, &out
}
