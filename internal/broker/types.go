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
	Factory ReviewerFactory
}

// Options configures a broker instance.
type Options struct {
	LogDestination  string
	DefaultBudget   int
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
	SessionID  string
	SessionDir string
	OpenedAt   time.Time
	Budget     int
}

// ReviewRoundRequest runs a new round, optionally replacing artifacts.
type ReviewRoundRequest struct {
	SessionID string
	Artifacts []Artifact
}

// ReviewRoundResponse returns one successful round.
type ReviewRoundResponse struct {
	RoundNumber int
	LogPath     string
	Manifest    []ArtifactManifestEntry
	Reviewers   []ReviewerResult
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
	SessionID  string
	State      string
	Verdict    *string
	OpenedAt   time.Time
	ClosedAt   *time.Time
	Budget     int
	RoundsUsed int
	Rounds     []RoundStatus
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
