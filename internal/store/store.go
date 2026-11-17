// Package store holds the on-disk shapes of sessions and rounds and the helpers
// that read and write them. It is the only session state Mercurius has: every
// broker operation reads what it needs from here and writes what it changes back,
// so any server process can serve any session. The package holds no process
// logic; locking lives in internal/runlock and round execution in
// internal/runner.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/michaelquigley/df/dd"
)

// file names inside a log destination, a session directory, and a round
// directory.
const (
	LockFileName      = "runner.lock"
	SessionFileName   = "session.json"
	SynopsisFileName  = "_synopsis.md"
	StateFileName     = "_state.json"
	RunFileName       = "_run.json"
	ConfigFileName    = "_config.yaml"
	PromptFileName    = "_prompt.md"
	ReviewFileName    = "_review.json"
	RoundLogFileName  = "_round.md"
	RawFileName       = "_raw.txt"
	RunnerLogFileName = "_runner.log"
	NotesJSONFileName = "_notes.json"
	NotesFileName     = "_notes.md"
)

// session states.
const (
	SessionActive = "active"
	SessionClosed = "closed"
)

// round states.
const (
	RoundRunning   = "running"
	RoundCompleted = "completed"
	RoundFailed    = "failed"
)

// stable error codes. they are defined here, beside the stored error that
// carries them, because the runner records failures without depending on the
// broker; the broker re-exports them.
const (
	CodeUserError      = "user_error"
	CodeNotFound       = "not_found"
	CodeConflict       = "conflict"
	CodeReviewerFailed = "reviewer_failed"
	CodeInternalError  = "internal_error"
)

// failure reasons recorded in a stored error's details under 'reason'.
const (
	ReasonTimeout    = "timeout"
	ReasonRunnerLost = "runner_lost"
)

// MaxCauseBytes bounds a stored error's details.cause. Reviewer output never
// belongs in an error; it goes to _raw.txt. The cap is a backstop so a long
// cause cannot bloat _state.json.
const MaxCauseBytes = 8 * 1024

var (
	sessionIDPattern = regexp.MustCompile(`^s_[A-Za-z0-9]{12}$`)
	roundDirPattern  = regexp.MustCompile(`^round-([0-9]+)$`)
)

// ErrNotSession marks a directory that matches the session id form but holds
// no session.json, such as a session written under the old layout.
var ErrNotSession = errors.New("directory is not a session in this layout")

// Session is session.json: the session's own facts and nothing else.
type Session struct {
	SessionID string
	OpenedAt  time.Time
	State     string
	ClosedAt  *time.Time
}

// Run is _run.json: the inputs of one round, written once by the server that
// prepared it and never changed.
type Run struct {
	SessionID            string
	RoundNumber          int
	OpenedAt             time.Time
	ServerPID            int
	Reviewer             RunReviewer
	ReviewerSource       string
	MaxFindings          int
	ReviewContextPresent bool
	ReviewFocusPresent   bool
	Manifest             []ManifestEntry
}

// RunReviewer is the fully resolved reviewer block a round runs with.
type RunReviewer struct {
	Name       string
	Impl       string
	Model      string `dd:",+omitempty"`
	BinaryPath string `dd:",+omitempty"`
	ExtraArgs  []string
	Timeout    time.Duration
}

// ManifestEntry records one artifact snapshot.
type ManifestEntry struct {
	Name         string
	SourcePath   string
	SnapshotPath string
	Size         int64
	Hash         string
}

// State is _state.json: where a round is in its life. It is written last in any
// step, so a round that says completed has its outcome on disk. Only a holder of
// the runner lock writes it. RunnerPID and ReviewerPID are for the operator to
// read; Mercurius never signals a process by a pid it read from disk.
type State struct {
	State       string
	StartedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
	RunnerPID   int `dd:",+omitempty"`
	ReviewerPID int `dd:",+omitempty"`
	// OutlivedServer is set by the runner at finish: the runner was a separate
	// process and the server that prepared the round was no longer its parent.
	OutlivedServer bool
	Error          *StoredError
}

// StoredError is a round failure as recorded in _state.json.
type StoredError struct {
	Code    string
	Message string
	Details map[string]any
	At      time.Time
}

// Review is _review.json: the outcome of a completed round. Reviewers is a list
// from the start so a multi-reviewer round needs no new shape.
type Review struct {
	Reviewers []ReviewerResult
}

// ReviewerResult holds one reviewer's decoded, schema-valid output.
type ReviewerResult struct {
	ReviewerName string
	Raw          map[string]any `dd:",+opaque"`
	UsageNotes   string
}

// Notes is _notes.json: the triage recorded for a completed round.
type Notes struct {
	Commentary string
	Decisions  []Decision
	RecordedAt time.Time
}

// Decision is one human disposition for a reviewer ref.
type Decision struct {
	Ref         string
	Disposition string
	Note        string
}

// ValidSessionID reports whether id has the form of a session id. Ids arrive
// from tool calls and the monitor flag and are joined into paths, so anything
// else must be rejected before it reaches the filesystem.
func ValidSessionID(id string) bool {
	return sessionIDPattern.MatchString(id)
}

// SessionDir returns the session directory for a validated id.
func SessionDir(logDestination string, sessionID string) string {
	return filepath.Join(logDestination, sessionID)
}

// RoundDir returns the round directory for a round number.
func RoundDir(sessionDir string, roundNumber int) string {
	return filepath.Join(sessionDir, fmt.Sprintf("round-%02d", roundNumber))
}

// LockPath returns the runner lock path for a log destination.
func LockPath(logDestination string) string {
	return filepath.Join(logDestination, LockFileName)
}

// WriteJSON writes v as indented JSON through a temporary file in the target's
// directory renamed over the target, so a reader never sees a partial file.
func WriteJSON(path string, v any) error {
	raw, err := dd.UnbindJSON(v)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return WriteFileAtomic(path, raw)
}

// WriteFileAtomic writes raw through a temporary file renamed over path.
func WriteFileAtomic(path string, raw []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// ReadJSON strictly binds the machine file at path into v. A missing file
// returns an error matching os.ErrNotExist; invalid content returns a ParseError.
func ReadJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := dd.BindJSON(v, raw, dd.Strict()); err != nil {
		return &ParseError{Path: path, Err: err}
	}
	return nil
}

// ParseError is a machine file that exists and cannot be parsed.
type ParseError struct {
	Path string
	Err  error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse '%s': %v", e.Path, e.Err)
}

func (e *ParseError) Unwrap() error {
	return e.Err
}

// ReadSession reads a session's session.json. A directory with no session.json
// returns ErrNotSession; a missing directory returns os.ErrNotExist.
func ReadSession(sessionDir string) (Session, error) {
	var session Session
	err := ReadJSON(filepath.Join(sessionDir, SessionFileName), &session)
	if errors.Is(err, os.ErrNotExist) {
		if info, statErr := os.Stat(sessionDir); statErr == nil && info.IsDir() {
			return Session{}, ErrNotSession
		}
		return Session{}, os.ErrNotExist
	}
	return session, err
}

// WriteSession writes a session's session.json.
func WriteSession(sessionDir string, session Session) error {
	return WriteJSON(filepath.Join(sessionDir, SessionFileName), session)
}

// ReadState reads a round's _state.json.
func ReadState(roundDir string) (State, error) {
	var state State
	err := ReadJSON(filepath.Join(roundDir, StateFileName), &state)
	return state, err
}

// WriteState writes a round's _state.json. The caller must hold the runner lock.
func WriteState(roundDir string, state State) error {
	return WriteJSON(filepath.Join(roundDir, StateFileName), state)
}

// ReadRun reads a round's _run.json.
func ReadRun(roundDir string) (Run, error) {
	var run Run
	err := ReadJSON(filepath.Join(roundDir, RunFileName), &run)
	return run, err
}

// ReadReview reads a round's _review.json.
func ReadReview(roundDir string) (Review, error) {
	var review Review
	err := ReadJSON(filepath.Join(roundDir, ReviewFileName), &review)
	return review, err
}

// ReadNotes reads a round's _notes.json. A round with no notes returns an error
// matching os.ErrNotExist.
func ReadNotes(roundDir string) (Notes, error) {
	var notes Notes
	err := ReadJSON(filepath.Join(roundDir, NotesJSONFileName), &notes)
	return notes, err
}

// RoundDirs lists the round directories in a session by number, ascending,
// whether or not they hold a _state.json.
func RoundDirs(sessionDir string) ([]int, error) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil, err
	}
	var numbers []int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		match := roundDirPattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		n, err := strconv.Atoi(match[1])
		if err != nil || n <= 0 {
			continue
		}
		numbers = append(numbers, n)
	}
	slices.Sort(numbers)
	return numbers, nil
}

// Rounds lists the rounds that exist in a session, ascending. A round exists
// once its _state.json exists; a round directory without one is a leftover from
// a server that died while preparing it and is not a round.
func Rounds(sessionDir string) ([]int, error) {
	dirs, err := RoundDirs(sessionDir)
	if err != nil {
		return nil, err
	}
	var rounds []int
	for _, n := range dirs {
		if RoundExists(RoundDir(sessionDir, n)) {
			rounds = append(rounds, n)
		}
	}
	return rounds, nil
}

// RoundExists reports whether a round directory holds a _state.json.
func RoundExists(roundDir string) bool {
	_, err := os.Stat(filepath.Join(roundDir, StateFileName))
	return err == nil
}

// Sessions lists the directories in a log destination that hold a session.json,
// sorted by name.
func Sessions(logDestination string) ([]string, error) {
	entries, err := os.ReadDir(logDestination)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() || !ValidSessionID(entry.Name()) {
			continue
		}
		if _, err := os.Stat(filepath.Join(logDestination, entry.Name(), SessionFileName)); err == nil {
			ids = append(ids, entry.Name())
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// NewStoredError builds a stored error with its cause capped at MaxCauseBytes.
// details is copied; 'raw' is never stored and is dropped if present.
func NewStoredError(code string, message string, cause string, details map[string]any, at time.Time) *StoredError {
	out := map[string]any{}
	for key, value := range details {
		if key == "raw" {
			continue
		}
		out[key] = value
	}
	if cause != "" {
		out["cause"] = CapCause(cause)
	}
	return &StoredError{Code: code, Message: message, Details: out, At: at}
}

// CapCause cuts a cause to MaxCauseBytes and says so.
func CapCause(cause string) string {
	if len(cause) <= MaxCauseBytes {
		return cause
	}
	return cause[:MaxCauseBytes] + fmt.Sprintf("\n[cut: %d more bytes not stored]", len(cause)-MaxCauseBytes)
}
