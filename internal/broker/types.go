package broker

import (
	"time"

	"github.com/michaelquigley/mercurius/internal/runner"
)

// Options configures a broker. The broker holds no session state: every
// operation resolves the configuration from these paths and reads and writes
// the session directory, so any broker on the same log destination can serve
// any session.
type Options struct {
	// ConfigPath is the project mercurius.yaml.
	ConfigPath string
	// UserConfigPath is the user-level config, which holds the reviewer. It is
	// read on every call, alongside the project file.
	UserConfigPath string
	// Launcher starts a prepared round.
	Launcher runner.Launcher
}

// Artifact identifies one source artifact at the broker boundary.
type Artifact struct {
	Name string
	Path string
}

// OpenSessionRequest opens a new review session. It carries nothing: what
// open_session reports about the configuration is read at the moment of the call.
type OpenSessionRequest struct{}

// OpenSessionResponse describes a newly opened session. MaxFindings, the
// calibration presence flags, and Reviewer describe the configuration at the
// moment of the call, as information; none of them is a property of the
// session. Reviewer is nil when no reviewer is configured.
type OpenSessionResponse struct {
	SessionID            string
	SessionDir           string
	OpenedAt             time.Time
	MaxFindings          int
	ReviewContextPresent bool
	ReviewFocusPresent   bool
	Reviewer             *ReviewerInfo
}

// StartRoundRequest starts a new round in the named session. Calibration, the
// reviewer, and max_findings are resolved from configuration when the round
// starts and recorded on the round.
type StartRoundRequest struct {
	SessionID string
	Artifacts []Artifact
}

// StartReviewRoundResponse describes a round that has been handed to its runner.
type StartReviewRoundResponse struct {
	SessionID      string
	RoundNumber    int
	State          string
	Reviewer       ReviewerInfo
	StartedAt      time.Time
	StatusPath     string
	MonitorCommand string
	NextAction     string
}

// CollectedRoundResponse returns one completed round.
type CollectedRoundResponse struct {
	RoundNumber int
	LogPath     string
	Manifest    []ArtifactManifestEntry
	Reviewers   []ReviewerResult
}

// CollectRoundRequest fetches a round's outcome. RoundNumber zero means the
// session's latest round.
type CollectRoundRequest struct {
	SessionID   string
	RoundNumber int
}

// ArtifactManifestEntry records one artifact snapshot.
type ArtifactManifestEntry struct {
	Name         string
	SourcePath   string
	SnapshotPath string
	Size         int64
	Hash         string
}

// ReviewerResult records one reviewer response.
type ReviewerResult struct {
	ReviewerName string
	Raw          map[string]any `dd:",+opaque"`
	UsageNotes   string
}

// RecordRoundNotesRequest replaces commentary and decisions for a round.
type RecordRoundNotesRequest struct {
	SessionID   string
	RoundNumber int
	Commentary  string
	Decisions   []Decision
}

// Decision records one human disposition for a reviewer ref.
type Decision struct {
	Ref         string
	Disposition string
	Note        string
}

// RecordRoundNotesResponse describes updated notes.
type RecordRoundNotesResponse struct {
	RoundNumber        int
	LogPath            string
	CommentaryRecorded bool
	DecisionsRecorded  int
}

// CloseSessionRequest closes a session.
type CloseSessionRequest struct {
	SessionID string
}

// CloseSessionResponse describes a closed session. SynopsisPath points at the
// session-level _synopsis.md rendered from disk at close time.
type CloseSessionResponse struct {
	SessionID    string
	ClosedAt     time.Time
	SynopsisPath string
}

// SessionStatusResponse is a read-only session view. There is no session-level
// reviewer, limit, or error: those belong to rounds.
type SessionStatusResponse struct {
	SessionID  string
	State      string
	OpenedAt   time.Time
	ClosedAt   *time.Time
	RoundCount int
	Rounds     []RoundStatus
}

// RoundStatus is a read-only round summary. Every round that was started is
// listed, failed ones included.
type RoundStatus struct {
	RoundNumber   int
	OpenedAt      time.Time
	State         string
	Reviewer      ReviewerInfo
	MaxFindings   int
	StatePath     string
	LogPath       string
	CompletedAt   *time.Time
	HasNotes      bool
	DecisionCount int
	// OutlivedServer marks a round whose runner finished after the server that
	// started it was gone: a round the old in-memory design would have lost.
	OutlivedServer bool
	Error          *ErrorInfo
}

// ReviewerInfo describes a resolved reviewer block and where it came from:
// config.SourceUserConfig or config.SourceProjectOverride.
type ReviewerInfo struct {
	Name       string
	Impl       string
	Model      string
	BinaryPath string
	ExtraArgs  []string
	Timeout    time.Duration
	Source     string
}

// ErrorInfo is a tool-visible broker error summary.
type ErrorInfo struct {
	Code    string
	Message string
	Details map[string]any
	At      time.Time
}
