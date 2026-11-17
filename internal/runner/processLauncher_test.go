//go:build linux

package runner_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/michaelquigley/mercurius/internal/broker"
	"github.com/michaelquigley/mercurius/internal/runlock"
	"github.com/michaelquigley/mercurius/internal/runner"
	"github.com/michaelquigley/mercurius/internal/store"
)

// these tests run rounds in real processes. The test binary re-executes itself
// as the runner, as a stand-in server, and as a sleeping helper; the reviewer
// is a shell script standing in for the claude CLI, named through binary_path.

const roleEnv = "MERCURIUS_TEST_ROLE"

func TestMain(m *testing.M) {
	switch os.Getenv(roleEnv) {
	case "runner":
		if err := runner.RunInherited(os.Args[len(os.Args)-1], runner.InheritedLockFD, runner.BuildReviewer); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case "server":
		os.Exit(standInServer())
	case "sleeper":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// standInServer prepares one round, starts its runner, prints the round
// directory, and exits at once, without waiting for the runner to come up.
func standInServer() int {
	b := broker.New(broker.Options{
		ConfigPath:     os.Getenv("MERCURIUS_TEST_CONFIG"),
		UserConfigPath: os.Getenv("MERCURIUS_TEST_USER_CONFIG"),
		Launcher:       testLauncher(),
	})
	started, err := b.StartReviewRound(context.Background(), broker.StartRoundRequest{
		SessionID: os.Getenv("MERCURIUS_TEST_SESSION"),
		Artifacts: []broker.Artifact{{Name: "design", Path: os.Getenv("MERCURIUS_TEST_ARTIFACT")}},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Print(filepath.Dir(started.StatusPath))
	return 0
}

// testLauncher starts this test binary as the runner.
func testLauncher() *runner.ProcessLauncher {
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return &runner.ProcessLauncher{
		Path: path,
		Args: func(roundDir string) []string { return []string{roundDir} },
		Env:  withRole("runner"),
	}
}

func withRole(role string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, roleEnv+"=") {
			env = append(env, kv)
		}
	}
	return append(env, roleEnv+"="+role)
}

// the round outlives its server: a stand-in server starts the round and exits
// at once, the round completes anyway, and the runner records that it outlived
// the server that started it.
func TestRoundOutlivesItsServer(t *testing.T) {
	p := newProject(t, "sleep 1\n", "")
	sessionID := p.openSession()

	helper := exec.Command(selfPath(t))
	helper.Env = append(withRole("server"),
		"MERCURIUS_TEST_CONFIG="+p.configPath,
		"MERCURIUS_TEST_USER_CONFIG="+p.userPath,
		"MERCURIUS_TEST_SESSION="+sessionID,
		"MERCURIUS_TEST_ARTIFACT="+p.artifact,
	)
	var stdout, stderr bytes.Buffer
	helper.Stdout = &stdout
	helper.Stderr = &stderr
	if err := helper.Run(); err != nil {
		t.Fatalf("stand-in server: %v\n%s", err, stderr.String())
	}
	roundDir := stdout.String()
	if state, err := store.ReadState(roundDir); err != nil || state.State != store.RoundRunning {
		t.Fatalf("the server must be gone while the round still runs: state=%+v err=%v", state, err)
	}
	// with the server gone, the detached runner is the lock's only holder. A
	// free lock here would let a second launch start beside a running round.
	// the check waits until the reviewer has started, so the runner is inside
	// Run with the review under way: checked any earlier, the lock is held by the
	// descriptor the runner inherited before its own code has run at all.
	if state := waitFor(t, roundDir, 30*time.Second, func(s store.State) bool { return s.ReviewerPID > 0 }); state.State != store.RoundRunning {
		t.Fatalf("the round must still be running while the reviewer works: %+v", state)
	}
	if lock, err := runlock.TakeOnce(p.logDest); !errors.Is(err, runlock.ErrHeld) {
		lock.Release()
		t.Fatalf("the detached runner must hold the runner lock while its round runs; take = %v", err)
	}

	state := waitDone(t, roundDir, 30*time.Second)
	if state.State != store.RoundCompleted {
		t.Fatalf("the round must complete without its server: %+v\n%s", state, readRunnerLog(roundDir))
	}
	if !state.OutlivedServer {
		t.Fatalf("outlived_server must be true: %+v", state)
	}
	if state.RunnerPID <= 0 || state.RunnerPID == helper.ProcessState.Pid() || state.ReviewerPID <= 0 {
		t.Fatalf("pids not recorded as the runner's and the reviewer's: %+v (server %d)", state, helper.ProcessState.Pid())
	}
	waitGone(t, state.RunnerPID, 5*time.Second)
}

// the server lets go of its copy of the lock after the handoff. The same
// process starts a round, waits for it, and starts another; if the server kept
// its copy, the lock would stay held and the second start would be refused.
func TestLockIsLetGo(t *testing.T) {
	p := newProject(t, "", "")
	sessionID := p.openSession()
	b := p.broker()

	first := p.start(b, sessionID)
	state := waitDone(t, first, 30*time.Second)
	if state.State != store.RoundCompleted || state.OutlivedServer {
		t.Fatalf("round 1 = %+v\n%s", state, readRunnerLog(first))
	}
	waitGone(t, state.RunnerPID, 5*time.Second)

	second := p.start(b, sessionID)
	if state := waitDone(t, second, 30*time.Second); state.State != store.RoundCompleted {
		t.Fatalf("round 2 = %+v", state)
	}
}

// the runner marks its inherited lock descriptor close-on-exec before it
// starts the reviewer. The reviewer lists its own open descriptors; none of
// them may be runner.lock.
func TestReviewerDoesNotHoldTheLock(t *testing.T) {
	fdList := filepath.Join(t.TempDir(), "fds.txt")
	p := newProject(t, "ls -l /proc/$$/fd > '"+fdList+"'\n", "")
	sessionID := p.openSession()

	roundDir := p.start(p.broker(), sessionID)
	if state := waitDone(t, roundDir, 30*time.Second); state.State != store.RoundCompleted {
		t.Fatalf("round = %+v\n%s", state, readRunnerLog(roundDir))
	}
	listing, err := os.ReadFile(fdList)
	if err != nil {
		t.Fatalf("the reviewer did not list its descriptors: %v", err)
	}
	if !strings.Contains(string(listing), "->") {
		t.Fatalf("the descriptor listing is empty:\n%s", listing)
	}
	if strings.Contains(string(listing), store.LockFileName) {
		t.Fatalf("the reviewer inherited the runner lock:\n%s", listing)
	}
}

// the reviewer dies with its runner: the kernel kills it when the runner is
// killed, and the round is then recorded as runner_lost.
func TestReviewerDiesWithTheRunner(t *testing.T) {
	p := newProject(t, "exec sleep 60\n", "")
	sessionID := p.openSession()

	roundDir := p.start(p.broker(), sessionID)
	state := waitFor(t, roundDir, 30*time.Second, func(s store.State) bool { return s.ReviewerPID > 0 })
	t.Cleanup(func() { _ = syscall.Kill(state.ReviewerPID, syscall.SIGKILL) })

	if err := syscall.Kill(state.RunnerPID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill the runner: %v", err)
	}
	waitGone(t, state.RunnerPID, 5*time.Second)
	waitGone(t, state.ReviewerPID, 2*time.Second)

	status, err := p.broker().SessionStatus(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	round := status.Rounds[0]
	if round.State != store.RoundFailed || round.Error == nil || round.Error.Details["reason"] != store.ReasonRunnerLost {
		t.Fatalf("the round must be marked runner_lost: %+v", round)
	}
}

// mercurius never signals a process by a pid it read from disk. A dead round
// names a live helper, leading its own process group, as its reviewer and the
// test process as its runner; every tool runs against it; both stay alive.
func TestRecordedPidsAreNeverSignalled(t *testing.T) {
	p := newProject(t, "", "")
	sessionID := p.openSession()

	helper := exec.Command(selfPath(t))
	helper.Env = withRole("sleeper")
	helper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		_ = helper.Process.Kill()
		_, _ = helper.Process.Wait()
	})
	helperPID := helper.Process.Pid

	sessionDir := store.SessionDir(p.logDest, sessionID)
	roundDir := store.RoundDir(sessionDir, 1)
	if err := os.Mkdir(roundDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	now := time.Now().UTC()
	if err := store.WriteJSON(filepath.Join(roundDir, store.RunFileName), store.Run{
		SessionID: sessionID, RoundNumber: 1, OpenedAt: now, ServerPID: os.Getpid(),
		Reviewer: store.RunReviewer{Name: "fake", Impl: "claude", Timeout: time.Minute}, MaxFindings: 6,
	}); err != nil {
		t.Fatalf("write run: %v", err)
	}
	if err := store.WriteState(roundDir, store.State{
		State: store.RoundRunning, StartedAt: now, UpdatedAt: now,
		RunnerPID: os.Getpid(), ReviewerPID: helperPID,
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}
	lock, err := runlock.Take(p.logDest)
	if err != nil {
		t.Fatalf("take lock: %v", err)
	}
	if err := lock.WriteNote(runlock.Note{SessionID: sessionID, RoundNumber: 1, ServerPID: os.Getpid(), StartedAt: now}); err != nil {
		t.Fatalf("write note: %v", err)
	}
	lock.Release()

	ctx := context.Background()
	b := p.broker()
	if _, err := b.OpenSession(ctx, broker.OpenSessionRequest{}); err != nil {
		t.Fatalf("open_session: %v", err)
	}
	if _, err := b.CollectRound(ctx, broker.CollectRoundRequest{SessionID: sessionID, RoundNumber: 1}); broker.ErrorCode(err) != broker.CodeReviewerFailed {
		t.Fatalf("collect_round on the dead round: %v", err)
	}
	if _, err := b.SessionStatus(ctx, sessionID); err != nil {
		t.Fatalf("session_status: %v", err)
	}
	if _, err := b.RecordRoundNotes(ctx, broker.RecordRoundNotesRequest{SessionID: sessionID, RoundNumber: 1, Commentary: "x"}); broker.ErrorCode(err) != broker.CodeConflict {
		t.Fatalf("record_round_notes on the failed round: %v", err)
	}
	next := p.start(b, sessionID)
	if state := waitDone(t, next, 30*time.Second); state.State != store.RoundCompleted {
		t.Fatalf("round 2 = %+v", state)
	}
	if _, err := b.CloseSession(ctx, broker.CloseSessionRequest{SessionID: sessionID}); err != nil {
		t.Fatalf("close_session: %v", err)
	}

	if !processAlive(helperPID) {
		t.Fatal("the helper named as the dead round's reviewer was signalled")
	}
	if !processAlive(os.Getpid()) {
		t.Fatal("unreachable: the test process named as the runner is gone")
	}
}

// the timeout kills the reviewer's whole process group, from inside the runner
// that started it: the reviewer's own child dies with it.
func TestTimeoutKillsTheGroup(t *testing.T) {
	pids := t.TempDir()
	selfFile := filepath.Join(pids, "self.pid")
	childFile := filepath.Join(pids, "child.pid")
	p := newProject(t, "sleep 60 &\necho $! > '"+childFile+"'\necho $$ > '"+selfFile+"'\nsleep 60\n", "1s")
	sessionID := p.openSession()

	roundDir := p.start(p.broker(), sessionID)
	state := waitDone(t, roundDir, 30*time.Second)
	self, child := readPid(t, selfFile), readPid(t, childFile)
	t.Cleanup(func() {
		_ = syscall.Kill(self, syscall.SIGKILL)
		_ = syscall.Kill(child, syscall.SIGKILL)
	})
	if state.State != store.RoundFailed || state.Error == nil || state.Error.Details["reason"] != store.ReasonTimeout {
		t.Fatalf("the round must fail with reason timeout: %+v\n%s", state, readRunnerLog(roundDir))
	}
	waitGone(t, self, 2*time.Second)
	waitGone(t, child, 2*time.Second)
}

// a runner that finishes while its server is alive is reaped, not left a
// zombie.
func TestFinishedRunnerIsReaped(t *testing.T) {
	p := newProject(t, "", "")
	sessionID := p.openSession()

	roundDir := p.start(p.broker(), sessionID)
	state := waitDone(t, roundDir, 30*time.Second)
	if state.State != store.RoundCompleted || state.RunnerPID <= 0 {
		t.Fatalf("round = %+v", state)
	}
	waitGone(t, state.RunnerPID, 5*time.Second)
	if _, err := os.Stat(filepath.Join(roundDir, store.RunnerLogFileName)); err != nil {
		t.Fatalf("the runner's log must land in the round directory: %v", err)
	}
}

// project is a temporary project whose reviewer is a fake claude script: the
// script runs its body and then prints a valid review envelope.
type project struct {
	t          *testing.T
	dir        string
	configPath string
	userPath   string
	logDest    string
	artifact   string
}

func newProject(t *testing.T, body string, timeout string) *project {
	t.Helper()
	dir := t.TempDir()
	p := &project{
		t:          t,
		dir:        dir,
		configPath: filepath.Join(dir, "mercurius.yaml"),
		userPath:   filepath.Join(dir, "user-config.yaml"),
		logDest:    filepath.Join(dir, "reviews"),
		artifact:   filepath.Join(dir, "design.md"),
	}
	if err := os.WriteFile(p.artifact, []byte("# design\n"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	script := filepath.Join(dir, "fake-claude")
	envelope := `{"type":"result","is_error":false,"result":"","structured_output":{"verdict":"ready_to_build","summary":"ok","concerns":[],"questions":[],"advisory_notes":[]}}`
	content := "#!/bin/sh\ncat > /dev/null\n" + body + "cat <<'EOF'\n" + envelope + "\nEOF\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake reviewer: %v", err)
	}
	user := "reviewer:\n  name: fake\n  impl: claude\n  binary_path: " + script + "\n"
	if timeout != "" {
		user += "  timeout: " + timeout + "\n"
	}
	if err := os.WriteFile(p.userPath, []byte(user), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	if err := os.WriteFile(p.configPath, []byte("log_destination: reviews\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func (p *project) broker() *broker.Broker {
	return broker.New(broker.Options{ConfigPath: p.configPath, UserConfigPath: p.userPath, Launcher: testLauncher()})
}

func (p *project) openSession() string {
	p.t.Helper()
	open, err := p.broker().OpenSession(context.Background(), broker.OpenSessionRequest{})
	if err != nil {
		p.t.Fatalf("open session: %v", err)
	}
	return open.SessionID
}

func (p *project) start(b *broker.Broker, sessionID string) string {
	p.t.Helper()
	started, err := b.StartReviewRound(context.Background(), broker.StartRoundRequest{
		SessionID: sessionID,
		Artifacts: []broker.Artifact{{Name: "design", Path: p.artifact}},
	})
	if err != nil {
		p.t.Fatalf("start round: %v", err)
	}
	return filepath.Dir(started.StatusPath)
}

func waitDone(t *testing.T, roundDir string, timeout time.Duration) store.State {
	t.Helper()
	return waitFor(t, roundDir, timeout, func(s store.State) bool { return s.State != store.RoundRunning })
}

func waitFor(t *testing.T, roundDir string, timeout time.Duration, done func(store.State) bool) store.State {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		state, err := store.ReadState(roundDir)
		if err == nil && done(state) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("round '%s' did not get there; last state=%+v err=%v\n%s", roundDir, state, err, readRunnerLog(roundDir))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// processAlive reports whether pid names a live process. A zombie is not alive.
func processAlive(pid int) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 || end+2 >= len(raw) {
		return false
	}
	return raw[end+2] != 'Z'
}

// waitGone fails unless pid stops naming a live process within the timeout. It
// also fails if the pid lingers as a zombie: an unreaped child is not gone.
func waitGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			state := "alive"
			if !processAlive(pid) {
				state = "a zombie"
			}
			t.Fatalf("pid %d is still %s after %s", pid, state, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return pid
}

func readRunnerLog(roundDir string) string {
	raw, _ := os.ReadFile(filepath.Join(roundDir, store.RunnerLogFileName))
	return "runner log:\n" + string(raw)
}

func selfPath(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	return path
}
