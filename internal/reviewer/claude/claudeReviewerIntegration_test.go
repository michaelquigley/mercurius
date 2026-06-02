//go:build integration

package claude_test

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/reviewer/claude"
	"github.com/michaelquigley/mercurius/internal/schema"
)

func TestReviewerIntegration(t *testing.T) {
	binaryPath := os.Getenv("MERCURIUS_CLAUDE_BINARY")
	if binaryPath == "" {
		binaryPath = "claude"
	}
	resolvedBinaryPath, err := exec.LookPath(binaryPath)
	if err != nil {
		t.Skipf("claude binary %q not found: %v", binaryPath, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	r := claude.New(claude.Options{
		BinaryPath: resolvedBinaryPath,
		Model:      os.Getenv("MERCURIUS_CLAUDE_MODEL"),
	})
	resp, err := r.Review(ctx, reviewer.ReviewRequest{
		Prompt:     "Return one JSON object matching the supplied schema. Use verdict ready_to_build, summary integration smoke, and empty concerns, questions, and advisory_notes arrays.",
		Schema:     schema.ReviewOutputSchema(),
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("review failed: %v", err)
	}
	if len(resp.Raw) == 0 {
		t.Fatal("expected raw output")
	}
	if err := schema.ValidateReviewOutput(resp.Raw); err != nil {
		t.Fatalf("schema validation failed: %v\nraw: %s", err, resp.Raw)
	}
}
