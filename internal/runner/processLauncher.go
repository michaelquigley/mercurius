//go:build unix

package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/michaelquigley/mercurius/internal/runlock"
	"github.com/michaelquigley/mercurius/internal/store"
)

// InheritedLockFD is the descriptor a runner process receives the lock on: the
// first entry in exec.Cmd.ExtraFiles.
const InheritedLockFD = 3

// ProcessLauncher runs each round in its own mercurius process, detached from
// the server, so the round finishes whether or not the server that started it
// is still there.
type ProcessLauncher struct {
	// Path is the executable to start.
	Path string
	// Args returns the arguments after the executable for a round directory.
	Args func(roundDir string) []string
	// Env is the runner's environment; nil inherits the server's.
	Env []string
}

// NewProcessLauncher returns the production launcher: this executable, run as
// 'run-round --round-dir <dir>'.
func NewProcessLauncher() (*ProcessLauncher, error) {
	path, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate the mercurius executable: %w", err)
	}
	return &ProcessLauncher{
		Path: path,
		Args: func(roundDir string) []string {
			return []string{"run-round", "--round-dir", roundDir}
		},
	}, nil
}

// Launch starts the runner in its own session and process group, so a harness
// taking down the server's group does not take the round with it. stdin is the
// null device, stdout and stderr go to the round's _runner.log, and the lock is
// passed as the one inherited descriptor. after the start the server closes its
// own copy of the lock: if it kept it, the lock would stay held for as long as
// the server lived. a goroutine waits on the runner, so a finished runner is
// reaped while its server is alive. on error nothing was started and the lock
// is still the caller's.
func (l *ProcessLauncher) Launch(roundDir string, lock *runlock.Lock) error {
	if lock.File() == nil {
		return errors.New("runner lock is not held")
	}
	logFile, err := os.OpenFile(filepath.Join(roundDir, store.RunnerLogFileName), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open runner log: %w", err)
	}
	defer logFile.Close()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open null device: %w", err)
	}
	defer devNull.Close()

	cmd := exec.Command(l.Path, l.Args(roundDir)...)
	cmd.Env = l.Env
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.ExtraFiles = []*os.File{lock.File()}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start runner: %w", err)
	}
	lock.Release()
	go func() { _ = cmd.Wait() }()
	return nil
}

// RunInherited is the body of a runner process. before anything else it marks
// the inherited lock descriptor close-on-exec: a descriptor passed through
// ExtraFiles arrives without that flag, and left clear the reviewer would
// inherit the lock. it then rebuilds the lock from the descriptor, locks the
// goroutine to its OS thread for the reviewer's parent-death signal, and runs
// the round.
func RunInherited(roundDir string, fd uintptr, build ReviewerFactory) error {
	syscall.CloseOnExec(int(fd))
	lock, err := runlock.FromFile(os.NewFile(fd, store.LockFileName))
	if err != nil {
		return fmt.Errorf("rebuild the runner lock from descriptor '%d': %w", fd, err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return Run(roundDir, lock, build)
}
