//go:build unix

package runlock

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/michaelquigley/df/dd"
	"github.com/michaelquigley/mercurius/internal/store"
)

func TestTakeIsExclusiveAndReleases(t *testing.T) {
	dir := t.TempDir()
	shortRetry(t)

	first, err := Take(dir)
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	if _, err := Take(dir); !errors.Is(err, ErrHeld) {
		t.Fatalf("second take = %v, want ErrHeld", err)
	}
	if _, err := TakeOnceReadOnly(dir); !errors.Is(err, ErrHeld) {
		t.Fatalf("read-only take = %v, want ErrHeld", err)
	}
	first.Release()
	first.Release()
	second, err := TakeOnce(dir)
	if err != nil {
		t.Fatalf("take after release: %v", err)
	}
	second.Release()
}

// the note is rewritten in place. a note written by rename would leave the lock
// on the old inode, and the next taker would get it while the holder lives.
func TestWriteNoteKeepsTheLock(t *testing.T) {
	dir := t.TempDir()
	shortRetry(t)

	lock, err := Take(dir)
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	defer lock.Release()
	want := Note{SessionID: "s_abcdefghijkl", RoundNumber: 3, ServerPID: 42, StartedAt: time.Now().UTC().Truncate(time.Second)}
	if err := lock.WriteNote(Note{SessionID: "s_longerfirstnote", RoundNumber: 100, ServerPID: 1234567}); err != nil {
		t.Fatalf("write first note: %v", err)
	}
	if err := lock.WriteNote(want); err != nil {
		t.Fatalf("write note: %v", err)
	}
	got, err := ReadNote(dir)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	if got != want {
		t.Fatalf("note = %+v, want %+v", got, want)
	}
	if _, err := TakeOnce(dir); !errors.Is(err, ErrHeld) {
		t.Fatalf("take after note write = %v, want ErrHeld", err)
	}
}

func TestTakeOnceReadOnlyCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	lock, err := TakeOnceReadOnly(dir)
	if err != nil || lock != nil {
		t.Fatalf("missing lock file must read as free: lock=%v err=%v", lock, err)
	}
	if _, err := os.Stat(store.LockPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only take created the lock file: %v", err)
	}
	if _, err := ReadNote(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read note with no lock file = %v, want not-exist", err)
	}
}

func TestReadNoteRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{"session_id":"s_abcdefghijkl","round_number":1,"server_pid":42,"started_at":"2026-01-01T12:00:00Z","extra":true}`)
	if err := os.WriteFile(store.LockPath(dir), raw, 0o600); err != nil {
		t.Fatalf("write lock note: %v", err)
	}
	_, err := ReadNote(dir)
	var unknown *dd.UnknownFieldError
	if !errors.As(err, &unknown) || unknown.Key != "extra" {
		t.Fatalf("expected unknown key error, got %v", err)
	}
}

func shortRetry(t *testing.T) {
	t.Helper()
	previous := RetryInterval
	RetryInterval = time.Millisecond
	t.Cleanup(func() { RetryInterval = previous })
}
