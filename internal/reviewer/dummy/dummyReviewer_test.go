package dummy_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/reviewer/dummy"
	"github.com/michaelquigley/mercurius/internal/schema"
)

func TestReviewerProducesValidReviewOutput(t *testing.T) {
	r := dummy.New()
	resp, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt: "review these artifacts",
		Artifacts: []reviewer.Artifact{
			{Name: "design", Path: "/tmp/design.md"},
		},
		Schema: schema.ReviewOutputSchema(),
		SessionMeta: reviewer.SessionMetadata{
			SessionID:   "s_test",
			RoundNumber: 1,
		},
	})
	if err != nil {
		t.Fatalf("review failed: %v", err)
	}
	if len(resp.Raw) == 0 {
		t.Fatal("expected raw output")
	}
	if resp.UsageNotes == "" {
		t.Fatal("expected usage notes")
	}
	if err := schema.ValidateReviewOutput(resp.Raw); err != nil {
		t.Fatalf("schema validation failed: %v", err)
	}
}

func TestReviewerCanBeConfiguredAndCapturesRequests(t *testing.T) {
	wantErr := errors.New("configured")
	r := dummy.New(dummy.Options{
		Raw:        json.RawMessage(`{"not":"used"}`),
		Err:        wantErr,
		UsageNotes: "configured usage",
	})
	_, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt: "prompt",
		Artifacts: []reviewer.Artifact{
			{Name: "design", Path: "/tmp/design.md", Content: []byte("content")},
		},
		Schema: json.RawMessage(`{"type":"object"}`),
		SessionMeta: reviewer.SessionMetadata{
			SessionID:   "s_test",
			RoundNumber: 2,
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected configured error, got %v", err)
	}

	requests := r.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if requests[0].Prompt != "prompt" || requests[0].SessionMeta.RoundNumber != 2 {
		t.Fatalf("unexpected captured request: %+v", requests[0])
	}
}
