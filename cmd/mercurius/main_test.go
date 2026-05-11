package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	monitorpkg "github.com/michaelquigley/mercurius/internal/monitor"
	"github.com/spf13/cobra"
)

func TestPrintStatusOmitsRemovedFields(t *testing.T) {
	cmd, out := testCommand()
	now := time.Date(2026, 5, 5, 19, 38, 10, 0, time.UTC)

	printStatus(cmd, monitorpkg.SessionStatus{
		SessionID:            "s_test",
		State:                "active",
		RoundCount:           1,
		ReviewContextPresent: true,
		ReviewFocusPresent:   true,
		ActiveRound: &monitorpkg.RoundJob{
			SessionID:   "s_test",
			RoundNumber: 1,
			State:       "running",
			Reviewer:    "codex",
			StartedAt:   now,
			StatusPath:  "/tmp/status.json",
		},
	})

	got := out.String()
	for _, want := range []string{
		"session 's_test' active",
		"rounds: 1",
		"review context: present",
		"review focus: present",
		"active round: 1 running reviewer='codex'",
		"monitor file: status='/tmp/status.json'",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected monitor status to contain %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"budget", "convergence", "verdict", "events"} {
		if strings.Contains(got, banned) {
			t.Fatalf("status should not contain %q:\n%s", banned, got)
		}
	}
}

func TestMonitorRejectsMissingLogDestination(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "mercurius.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
log_destination: ./reviews
reviewer:
  name: dummy
  impl: dummy
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	root := newRootCommand()
	_, out := testCommand()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"monitor", "--config", cfgPath, "--all"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected monitor against missing log_destination to error")
	}
	if !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("expected not-exist error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "reviews")); !os.IsNotExist(err) {
		t.Fatalf("monitor created log_destination as a side effect: stat err=%v", err)
	}
}

func testCommand() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	return cmd, &out
}
