package broker

import (
	"encoding/json"
	"time"

	"github.com/michaelquigley/mercurius/internal/reviewer"
)

// ReviewerFactory creates a session-bound reviewer.
type ReviewerFactory func(sessionDir string) reviewer.Reviewer

// ReviewerSpec names one configured reviewer implementation.
type ReviewerSpec struct {
	Name    string
	Impl    string
	Model   string
	Factory ReviewerFactory
}

// Options configures a broker instance.
type Options struct {
	LogDestination  string
	ConfigPath      string
	DefaultBudget   int
	MaxFindings     int
	PromptOverrides string
	Reviewers       []ReviewerSpec
}

// Artifact identifies one source artifact at the broker boundary.
type Artifact struct {
	Name    string
	Path    string
	Content []byte
}

// OpenSessionRequest starts a new review session.
type OpenSessionRequest struct {
	Artifacts []Artifact
	Reviewers []string
	Budget    int
}

// OpenSessionResponse describes a newly opened session.
type OpenSessionResponse struct {
	SessionID       string
	SessionDir      string
	OpenedAt        time.Time
	Budget          int
	BudgetRemaining int
	MaxFindings     int
	RoundsUsed      int
	Reviewers       []ReviewerInfo
	Artifacts       []RegisteredArtifact
}

// ReviewRoundRequest runs a new round, optionally replacing artifacts.
type ReviewRoundRequest struct {
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

// ReviewRoundResponse returns one successful round.
type ReviewRoundResponse struct {
	RoundNumber int
	LogPath     string
	Manifest    []ArtifactManifestEntry
	Reviewers   []ReviewerResult
}

// RoundStatusRequest asks for a round job status.
type RoundStatusRequest struct {
	SessionID   string
	RoundNumber int
}

// RoundStatusResponse describes a running or terminal review job.
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
	Inline       bool
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
	Verdict   string
}

// CloseSessionResponse describes a closed session.
type CloseSessionResponse struct {
	SessionID string
	Verdict   string
	ClosedAt  time.Time
}

// SessionStatusResponse is a read-only session view.
type SessionStatusResponse struct {
	SessionID       string
	State           string
	Verdict         *string
	OpenedAt        time.Time
	ClosedAt        *time.Time
	Budget          int
	BudgetRemaining int
	MaxFindings     int
	RoundsUsed      int
	Reviewers       []ReviewerInfo
	Artifacts       []RegisteredArtifact
	LastError       *ErrorInfo
	ActiveRound     *RoundStatusResponse
	LastRoundJob    *RoundStatusResponse
	Rounds          []RoundStatus
}

// RoundStatus is a read-only round summary.
type RoundStatus struct {
	RoundNumber   int
	OpenedAt      time.Time
	LogPath       string
	HasNotes      bool
	DecisionCount int
}

// ListSessionsResponse enumerates sessions known to the broker.
type ListSessionsResponse struct {
	Sessions []SessionSummary
}

// SessionSummary is a compact read-only session summary.
type SessionSummary struct {
	SessionID  string
	State      string
	Verdict    *string
	OpenedAt   time.Time
	RoundsUsed int
}

// ReviewerInfo describes a configured or selected reviewer.
type ReviewerInfo struct {
	Name  string
	Impl  string
	Model string
}

// RegisteredArtifact describes one artifact registered with a session.
type RegisteredArtifact struct {
	Name       string
	SourcePath string
	Inline     bool
}

// ErrorInfo is a durable session-visible broker error summary.
type ErrorInfo struct {
	Code       string
	Message    string
	Details    map[string]any
	Retryable  bool
	NextAction string
	At         time.Time
}

// ListReviewersResponse enumerates configured reviewers.
type ListReviewersResponse struct {
	Reviewers []ReviewerInfo
}
