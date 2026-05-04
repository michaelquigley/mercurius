package dummy_test

import (
	"context"
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
