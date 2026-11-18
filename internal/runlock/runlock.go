//go:build unix

// Package runlock is the project-wide runner lock: a kernel flock(2) on
// '<log_destination>/runner.lock'. a held lock means a round is running; a free
// lock beside a round still marked running means that round's runner is gone.
// the kernel releases the lock when the last descriptor sharing it is closed,
// however the holder exits, so nothing here probes pids or writes heartbeats.
//
// the lock file is never renamed or replaced: the lock belongs to the file's
// inode, so a note written by rename would leave the lock behind on the old one.
// the note is rewritten in place through the held descriptor.
package runlock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/michaelquigley/df/dd"
	"github.com/michaelquigley/mercurius/internal/store"
)

// ErrHeld is returned when another holder has the lock.
var ErrHeld = errors.New("runner lock is held")

// retry policy for Take. a server marking a dead round, or a monitor checking
// one, holds the lock for a moment; the retry rides over that. package
// variables so tests can shorten them.
var (
	Retries       = 5
	RetryInterval = 50 * time.Millisecond
)

// Note is the content of runner.lock: a description of the current holder. the
// lock itself is the kernel's; the note is only what the holder said about
// itself when it took the lock.
type Note struct {
	SessionID   string
	RoundNumber int
	ServerPID   int
	StartedAt   time.Time
}

// Lock is a held runner lock.
type Lock struct {
	file *os.File
}

// Take acquires the lock for a log destination, creating the lock file if
// needed. it makes a non-blocking attempt and retries it up to Retries times,
// RetryInterval apart, before returning ErrHeld.
func Take(logDestination string) (*Lock, error) {
	for attempt := 0; ; attempt++ {
		lock, err := TakeOnce(logDestination)
		if !errors.Is(err, ErrHeld) || attempt >= Retries {
			return lock, err
		}
		time.Sleep(RetryInterval)
	}
}

// TakeOnce makes one non-blocking attempt at the lock, creating the lock file
// if needed.
func TakeOnce(logDestination string) (*Lock, error) {
	file, err := os.OpenFile(store.LockPath(logDestination), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open runner lock: %w", err)
	}
	return flockOrClose(file)
}

// TakeOnceReadOnly makes one non-blocking attempt at the lock without creating
// or writing anything. a missing lock file means the lock is free, and returns
// a nil lock with no error. the monitor uses this so it never writes.
func TakeOnceReadOnly(logDestination string) (*Lock, error) {
	file, err := os.Open(store.LockPath(logDestination))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open runner lock: %w", err)
	}
	return flockOrClose(file)
}

func flockOrClose(file *os.File) (*Lock, error) {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, fmt.Errorf("flock runner lock: %w", err)
	}
	return &Lock{file: file}, nil
}

// FromFile rebuilds a held lock from a descriptor inherited across exec. the
// descriptor shares the open file description that holds the lock, so taking
// it again succeeds at once; if the description does not hold the lock and
// another one does, this answers ErrHeld instead of pretending.
func FromFile(file *os.File) (*Lock, error) {
	if file == nil {
		return nil, errors.New("runner lock descriptor is missing")
	}
	return flockOrClose(file)
}

// Release lets go of the lock by closing this descriptor. safe on a nil lock
// and safe to call more than once.
func (l *Lock) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = l.file.Close()
	l.file = nil
}

// File returns the descriptor holding the lock, for handing to another process.
func (l *Lock) File() *os.File {
	if l == nil {
		return nil
	}
	return l.file
}

// WriteNote replaces the lock file's note in place through the held descriptor.
func (l *Lock) WriteNote(note Note) error {
	if l == nil || l.file == nil {
		return errors.New("runner lock is not held")
	}
	raw, err := dd.UnbindJSON(note)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	if _, err := l.file.WriteAt(raw, 0); err != nil {
		return err
	}
	return nil
}

// ReadNote reads the note in a log destination's lock file. it does not take
// the lock. a missing or empty lock file returns an error matching
// os.ErrNotExist.
func ReadNote(logDestination string) (Note, error) {
	raw, err := os.ReadFile(store.LockPath(logDestination))
	if err != nil {
		return Note{}, err
	}
	if len(raw) == 0 {
		return Note{}, os.ErrNotExist
	}
	var note Note
	if err := dd.BindJSON(&note, raw, dd.Strict()); err != nil {
		return Note{}, fmt.Errorf("parse runner lock note: %w", err)
	}
	return note, nil
}
