//go:build integration

package codex_test

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/reviewer/codex"
	"github.com/michaelquigley/mercurius/internal/schema"
)

func TestReviewerIntegration(t *testing.T) {
	binaryPath := os.Getenv("MERCURIUS_CODEX_BINARY")
	if binaryPath == "" {
		binaryPath = "codex"
	}
	resolvedBinaryPath, err := exec.LookPath(binaryPath)
	if err != nil {
		t.Skipf("codex binary %q not found: %v", binaryPath, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	workingDir := t.TempDir()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git binary not found: %v", err)
	}
	initCmd := exec.Command(gitPath, "init", workingDir)
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("initialize integration git repo: %v\n%s", err, output)
	}

	r := codex.New(codex.Options{
		BinaryPath: resolvedBinaryPath,
		WorkingDir: workingDir,
		Model:      os.Getenv("MERCURIUS_CODEX_MODEL"),
	})
	resp, err := r.Review(ctx, reviewer.ReviewRequest{
		Prompt: "Return one JSON object matching the supplied schema. Use ready_to_ship true, verdict ready_to_build, summary integration smoke, and empty concerns, questions, advisory_notes, and proposed_diffs arrays.",
		Schema: schema.ReviewOutputSchema(),
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
