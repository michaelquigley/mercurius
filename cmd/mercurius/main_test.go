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

func TestBootstrapCommandWritesConfig(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	root := newRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"bootstrap"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	written := filepath.Join(dir, "mercurius.yaml")
	if _, err := os.Stat(written); err != nil {
		t.Fatalf("expected mercurius.yaml at %s: %v", written, err)
	}
	if got := out.String(); !strings.Contains(got, "wrote '") || !strings.Contains(got, "mercurius.yaml") {
		t.Fatalf("expected wrote-confirmation in output, got %q", got)
	}

	// re-running without --force refuses to clobber and leaves the file alone.
	before, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("read first write: %v", err)
	}
	root2 := newRootCommand()
	var out2 bytes.Buffer
	root2.SetOut(&out2)
	root2.SetErr(&out2)
	root2.SetArgs([]string{"bootstrap"})
	if err := root2.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected bootstrap to refuse overwrite")
	}
	if got := out2.String(); !strings.Contains(got, "already exists") {
		t.Fatalf("expected 'already exists' message, got %q", got)
	}
	after, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("read after refused overwrite: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("refused overwrite still mutated the file")
	}

	// --force succeeds and replaces the file.
	if err := os.WriteFile(written, []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	root3 := newRootCommand()
	var out3 bytes.Buffer
	root3.SetOut(&out3)
	root3.SetErr(&out3)
	root3.SetArgs([]string{"bootstrap", "--force"})
	if err := root3.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("bootstrap --force: %v", err)
	}
	got, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("read after force: %v", err)
	}
	if string(got) == "stale" {
		t.Fatal("force did not overwrite stale content")
	}
}

func testCommand() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	return cmd, &out
}
