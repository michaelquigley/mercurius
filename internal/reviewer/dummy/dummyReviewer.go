package dummy

import (
	"context"
	"encoding/json"

	"github.com/michaelquigley/mercurius/internal/reviewer"
)

// Reviewer returns a fixed valid response for tests and scaffolding.
type Reviewer struct{}

func New() *Reviewer {
	return &Reviewer{}
}

func (r *Reviewer) Review(ctx context.Context, req reviewer.ReviewRequest) (reviewer.ReviewResponse, error) {
	if err := ctx.Err(); err != nil {
		return reviewer.ReviewResponse{}, err
	}

	raw, err := json.Marshal(map[string]any{
		"verdict":        "ready_to_build",
		"summary":        "dummy reviewer found no concerns.",
		"concerns":       []any{},
		"questions":      []any{},
		"proposed_diffs": []any{},
	})
	if err != nil {
		return reviewer.ReviewResponse{}, err
	}

	return reviewer.ReviewResponse{
		Raw:        raw,
		UsageNotes: "dummy reviewer",
	}, nil
}
