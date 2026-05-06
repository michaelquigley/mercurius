package dummy

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/michaelquigley/mercurius/internal/reviewer"
)

// Options configures a dummy reviewer.
type Options struct {
	Raw        json.RawMessage
	Err        error
	UsageNotes string
}

// Reviewer returns a fixed valid response for tests and scaffolding.
type Reviewer struct {
	mu       sync.Mutex
	raw      json.RawMessage
	err      error
	usage    string
	requests []reviewer.ReviewRequest
}

func New(options ...Options) *Reviewer {
	option := Options{
		Raw:        defaultRaw(),
		UsageNotes: "dummy reviewer",
	}
	if len(options) > 0 {
		option = options[0]
		if len(option.Raw) == 0 {
			option.Raw = defaultRaw()
		}
		if option.UsageNotes == "" {
			option.UsageNotes = "dummy reviewer"
		}
	}
	return &Reviewer{
		raw:   append(json.RawMessage(nil), option.Raw...),
		err:   option.Err,
		usage: option.UsageNotes,
	}
}

func (r *Reviewer) Review(ctx context.Context, req reviewer.ReviewRequest) (reviewer.ReviewResponse, error) {
	if err := ctx.Err(); err != nil {
		return reviewer.ReviewResponse{}, err
	}

	r.mu.Lock()
	r.requests = append(r.requests, cloneRequest(req))
	raw := append(json.RawMessage(nil), r.raw...)
	err := r.err
	usage := r.usage
	r.mu.Unlock()

	if err != nil {
		return reviewer.ReviewResponse{}, err
	}

	return reviewer.ReviewResponse{
		Raw:        raw,
		UsageNotes: usage,
	}, nil
}

// Requests returns the requests captured by this reviewer.
func (r *Reviewer) Requests() []reviewer.ReviewRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	requests := make([]reviewer.ReviewRequest, 0, len(r.requests))
	for _, req := range r.requests {
		requests = append(requests, cloneRequest(req))
	}
	return requests
}

func defaultRaw() json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"verdict":        "ready_to_build",
		"summary":        "dummy reviewer found no concerns.",
		"concerns":       []any{},
		"questions":      []any{},
		"advisory_notes": []any{},
		"proposed_diffs": []any{},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func cloneRequest(req reviewer.ReviewRequest) reviewer.ReviewRequest {
	req.Artifacts = append([]reviewer.Artifact(nil), req.Artifacts...)
	for i := range req.Artifacts {
		req.Artifacts[i].Content = append([]byte(nil), req.Artifacts[i].Content...)
	}
	req.Schema = append(json.RawMessage(nil), req.Schema...)
	req.SessionMeta.PriorDecisions = append([]reviewer.PriorDecision(nil), req.SessionMeta.PriorDecisions...)
	return req
}
