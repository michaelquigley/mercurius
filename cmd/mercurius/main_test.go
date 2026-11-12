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

	"github.com/michaelquigley/mercurius/internal/runlock"
	"github.com/michaelquigley/mercurius/internal/store"
	"github.com/spf13/cobra"
)

func TestPrintMonitorView(t *testing.T) {
	cmd, out := testCommand()
	now := time.Date(2026, 5, 5, 19, 38, 10, 0, time.UTC)

	printMonitorView(cmd.OutOrStdout(), monitorView{
		SessionID: "s_test",
		State:     "active",
		Rounds: []monitorRound{
			{Number: 1, State: "failed", Reviewer: "codex", StartedAt: now, Error: &store.StoredError{Code: "reviewer_failed", Message: "reviewer failed"}},
			{Number: 2, State: "completed", Reviewer: "codex", StartedAt: now},
			{Number: 3, State: "running", Reviewer: "codex", StartedAt: now, StatePath: "/tmp/round-03/_state.json"},
		},
	})

	got := out.String()
	for _, want := range []string{
		"session 's_test' active",
		"rounds: 3",
		"round 1 failed reviewer='codex' started=2026-05-05T19:38:10Z",
		"  error: reviewer_failed - reviewer failed",
		"round 2 completed reviewer='codex'",
		"round 3 running reviewer='codex'",
		"  monitor file: state='/tmp/round-03/_state.json'",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected monitor output to contain %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"budget", "convergence", "verdict", "events", "status.json"} {
		if strings.Contains(got, banned) {
			t.Fatalf("monitor output should not contain %q:\n%s", banned, got)
		}
	}
}

// the monitor only reads. Against read-only directories it still reports a lost
// round, which would fail a monitor that needed to write.
func TestMonitorReportsLostRoundOnReadOnlyDirectories(t *testing.T) {
	project, logDest, sessionID := monitorFixture(t, true)
	setTreeWritable(t, logDest, false)
	t.Cleanup(func() { setTreeWritable(t, logDest, true) })

	out, err := runMonitor(t, project, "--session", sessionID)
	if err != nil {
		t.Fatalf("monitor on read-only directories: %v\n%s", err, out)
	}
	if !strings.Contains(out, "round 1 lost") {
		t.Fatalf("expected the lost round to be reported:\n%s", out)
	}
}

// the read-only run alone would pass a monitor that tried to write and ignored
// the error. Against a writable log destination, every entry and every file's
// contents must be the same afterward, and no lock file may appear.
func TestMonitorLeavesLogDestinationUnchanged(t *testing.T) {
	project, logDest, sessionID := monitorFixture(t, false)
	before := snapshotTree(t, logDest)

	for _, args := range [][]string{{"--session", sessionID}, {"--all"}, {"--session", sessionID, "--wait"}} {
		out, err := runMonitor(t, project, args...)
		if err != nil {
			t.Fatalf("monitor %v: %v\n%s", args, err, out)
		}
		if !strings.Contains(out, "lost") {
			t.Fatalf("monitor %v did not report the lost round:\n%s", args, out)
		}
	}
	after := snapshotTree(t, logDest)
	if len(before) != len(after) {
		t.Fatalf("entries changed:\nbefore %v\nafter  %v", keys(before), keys(after))
	}
	for path, content := range before {
		if after[path] != content {
			t.Fatalf("'%s' changed", path)
		}
	}
}

// --wait watches the latest round's state until it reaches an outcome.
func TestMonitorWaitsForRoundOutcome(t *testing.T) {
	previous := monitorPollInterval
	monitorPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { monitorPollInterval = previous })

	project, logDest, sessionID := monitorFixture(t, true)
	lock, err := runlock.TakeOnce(logDest)
	if err != nil {
		t.Fatalf("hold the lock as a live runner would: %v", err)
	}
	roundDir := store.RoundDir(store.SessionDir(logDest, sessionID), 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		state, _ := store.ReadState(roundDir)
		now := time.Now().UTC()
		state.State = store.RoundCompleted
		state.CompletedAt = &now
		_ = store.WriteState(roundDir, state)
		lock.Release()
	}()

	out, err := runMonitor(t, project, "--session", sessionID, "--wait")
	if err != nil {
		t.Fatalf("monitor --wait: %v\n%s", err, out)
	}
	if !strings.Contains(out, "round 1 running") || !strings.Contains(out, "round 1 completed") {
		t.Fatalf("expected the running round and then its completion:\n%s", out)
	}
	if !strings.Contains(out, "collect_round for session '"+sessionID+"' round 1") {
		t.Fatalf("expected the next step to name the round:\n%s", out)
	}
}

func TestMonitorRejectsUnsafeSessionID(t *testing.T) {
	project, _, _ := monitorFixture(t, false)
	out, err := runMonitor(t, project, "--session", "../x")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found for an unsafe id, got err=%v\n%s", err, out)
	}
}

// monitorFixture writes a project whose log destination holds one session with
// round 1 marked running and the lock free: a lost round. withLockFile leaves a
// runner.lock behind, as a dead runner would.
func monitorFixture(t *testing.T, withLockFile bool) (string, string, string) {
	t.Helper()
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "mercurius.yaml"), []byte("log_destination: reviews\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	logDest := filepath.Join(project, "reviews")
	sessionID := "s_monitorTest1"
	sessionDir := store.SessionDir(logDest, sessionID)
	roundDir := store.RoundDir(sessionDir, 1)
	if err := os.MkdirAll(roundDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	now := time.Now().UTC()
	if err := store.WriteSession(sessionDir, store.Session{SessionID: sessionID, OpenedAt: now, State: store.SessionActive}); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := store.WriteJSON(filepath.Join(roundDir, store.RunFileName), store.Run{SessionID: sessionID, RoundNumber: 1, Reviewer: store.RunReviewer{Name: "codex", Impl: "codex"}}); err != nil {
		t.Fatalf("write run: %v", err)
	}
	if err := store.WriteState(roundDir, store.State{State: store.RoundRunning, StartedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if withLockFile {
		lock, err := runlock.TakeOnce(logDest)
		if err != nil {
			t.Fatalf("take lock: %v", err)
		}
		if err := lock.WriteNote(runlock.Note{SessionID: sessionID, RoundNumber: 1, StartedAt: now}); err != nil {
			t.Fatalf("write note: %v", err)
		}
		lock.Release()
	}
	return project, logDest, sessionID
}

func runMonitor(t *testing.T, project string, args ...string) (string, error) {
	t.Helper()
	root := newRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"monitor", "--config", filepath.Join(project, "mercurius.yaml")}, args...))
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

func setTreeWritable(t *testing.T, root string, writable bool) {
	t.Helper()
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	mode := os.FileMode(0o500)
	if writable {
		mode = 0o700
	}
	for _, dir := range dirs {
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatalf("chmod %s: %v", dir, err)
		}
	}
}

// snapshotTree maps every entry under root to its type, mode, and contents.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := info.Mode().String()
		if !d.IsDir() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry += "\n" + string(raw)
		}
		out[path] = entry
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return out
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestMonitorRejectsMissingLogDestination(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "mercurius.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
log_destination: ./reviews
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
	if got := out.String(); !strings.Contains(got, "agent-guide.md") {
		t.Fatalf("expected success message to point at the agent guide, got %q", got)
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
