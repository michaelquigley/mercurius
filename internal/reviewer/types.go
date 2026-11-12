package reviewer

import (
	"context"
	"encoding/json"
)

type Reviewer interface {
	Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error)
}

// ReviewRequest is the broker-owned payload handed to a reviewer.
type ReviewRequest struct {
	Prompt      string
	Artifacts   []Artifact
	Schema      json.RawMessage
	WorkingDir  string
	SessionMeta SessionMetadata
	// Started, when set, is called with the reviewer process's pid once it has
	// started. The runner records it for the operator; Mercurius never signals a
	// process by a recorded pid.
	Started func(pid int)
}

// Artifact identifies one design artifact under review.
type Artifact struct {
	Name    string
	Path    string
	Content []byte
}

// SessionMetadata carries round context into a reviewer call.
type SessionMetadata struct {
	SessionID   string
	RoundNumber int
}

// ReviewResponse contains the reviewer's structured output and diagnostics.
type ReviewResponse struct {
	Raw        json.RawMessage
	UsageNotes string
}
