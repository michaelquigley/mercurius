package broker

import (
	"encoding/json"
	"time"

	"github.com/michaelquigley/mercurius/internal/reviewer"
)

// Options configures a broker instance. ReviewContext and ReviewFocus are not
// cached on the broker; the MCP layer re-reads them from mercurius.yaml on each
// open_session call and passes them in via OpenSessionRequest, so calibration
// edits between sessions take effect without a server restart.
type Options struct {
	LogDestination string
	ConfigPath     string
	MaxFindings    int
	Reviewer       reviewer.Reviewer
	ReviewerInfo   ReviewerInfo
}

// Artifact identifies one source artifact at the broker boundary.
type Artifact struct {
	Name string
	Path string
}

// OpenSessionRequest starts a new review session. ReviewContext and
// ReviewFocus calibrate the round prompt for every round in this session; they
// are typically passed by the MCP layer after re-reading mercurius.yaml so
// edits between sessions are picked up without a server restart.
type OpenSessionRequest struct {
	ReviewContext string
	ReviewFocus   string
}

// OpenSessionResponse describes a newly opened session.
type OpenSessionResponse struct {
	SessionID            string
	SessionDir           string
	OpenedAt             time.Time
	MaxFindings          int
	ReviewContextPresent bool
	ReviewFocusPresent   bool
	Reviewer             ReviewerInfo
}

// StartRoundRequest runs a new round in the named session.
type StartRoundRequest struct {
	SessionID string
	Artifacts []Artifact
}

// StartReviewRoundResponse describes a background review round.
type StartReviewRoundResponse struct {
	SessionID      string
	RoundNumber    int
	State          string
	Reviewer       string
	StartedAt      time.Time
	StatusPath     string
	EventsPath     string
	MonitorCommand string
	NextAction     string
}

// CollectedRoundResponse returns one successful round.
type CollectedRoundResponse struct {
	RoundNumber int
	LogPath     string
	Manifest    []ArtifactManifestEntry
	Reviewers   []ReviewerResult
}

// RoundStatusResponse describes a running or terminal review job. Embedded in
// SessionStatusResponse; not exposed as an MCP tool.
type RoundStatusResponse struct {
	SessionID   string
	RoundNumber int
	State       string
	Reviewer    string
	StartedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
	LogPath     string
	StatusPath  string
	EventsPath  string
	Error       *ErrorInfo
}

// CollectRoundRequest fetches a completed round result.
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
	Raw          json.RawMessage
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

// CloseSessionResponse describes a closed session.
type CloseSessionResponse struct {
	SessionID string
	ClosedAt  time.Time
}

// SessionStatusResponse is a read-only session view.
type SessionStatusResponse struct {
	SessionID            string
	State                string
	OpenedAt             time.Time
	ClosedAt             *time.Time
	MaxFindings          int
	ReviewContextPresent bool
	ReviewFocusPresent   bool
	RoundCount           int
	Reviewer             ReviewerInfo
	LastError            *ErrorInfo
	ActiveRound          *RoundStatusResponse
	Rounds               []RoundStatus
}

// RoundStatus is a read-only round summary.
type RoundStatus struct {
	RoundNumber   int
	OpenedAt      time.Time
	LogPath       string
	HasNotes      bool
	DecisionCount int
}

// ReviewerInfo describes the configured reviewer.
type ReviewerInfo struct {
	Name  string
	Impl  string
	Model string
}

// ErrorInfo is a durable session-visible broker error summary.
type ErrorInfo struct {
	Code    string
	Message string
	Details map[string]any
	At      time.Time
}
