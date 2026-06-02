//go:build integration

package pi_test

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/michaelquigley/mercurius/internal/prompt"
	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/reviewer/pi"
	"github.com/michaelquigley/mercurius/internal/schema"
)

func TestReviewerIntegration(t *testing.T) {
	binaryPath := os.Getenv("MERCURIUS_PI_BINARY")
	if binaryPath == "" {
		binaryPath = "pi"
	}
	resolvedBinaryPath, err := exec.LookPath(binaryPath)
	if err != nil {
		t.Skipf("pi binary %q not found: %v", binaryPath, err)
	}

	model := os.Getenv("MERCURIUS_PI_MODEL")
	if model == "" {
		model = "openai-codex/gpt-5.5"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// build a real review prompt so the schema and output instruction reach pi the
	// same way the broker delivers them.
	promptText, reqSchema := prompt.Build(prompt.Request{
		Artifacts: []prompt.Artifact{{
			Name:    "note.md",
			Content: []byte("# Note\n\nThis is a trivially complete artifact for an integration smoke test.\n"),
		}},
		MaxFindings: 6,
	})

	r := pi.New(pi.Options{
		BinaryPath: resolvedBinaryPath,
		Model:      model,
	})
	resp, err := r.Review(ctx, reviewer.ReviewRequest{
		Prompt:     promptText,
		Schema:     reqSchema,
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
