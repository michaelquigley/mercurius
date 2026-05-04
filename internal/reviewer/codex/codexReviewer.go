package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/michaelquigley/mercurius/internal/reviewer"
)

const defaultBinaryPath = "codex"

// Options configures the Codex subprocess reviewer.
type Options struct {
	BinaryPath string
	WorkingDir string
	Model      string
	ExtraArgs  []string
}

// Reviewer invokes codex exec for one structured review.
type Reviewer struct {
	options Options
}

// New returns a Codex subprocess reviewer.
func New(options Options) *Reviewer {
	if options.BinaryPath == "" {
		options.BinaryPath = defaultBinaryPath
	}
	options.ExtraArgs = append([]string(nil), options.ExtraArgs...)
	return &Reviewer{options: options}
}

// Review runs codex exec with the pre-assembled prompt and schema.
func (r *Reviewer) Review(ctx context.Context, req reviewer.ReviewRequest) (reviewer.ReviewResponse, error) {
	if err := ctx.Err(); err != nil {
		return reviewer.ReviewResponse{}, err
	}
	if r.options.WorkingDir == "" {
		return reviewer.ReviewResponse{}, errors.New("codex reviewer working directory is required")
	}
	if len(req.Schema) == 0 {
		return reviewer.ReviewResponse{}, errors.New("codex reviewer schema is required")
	}

	tempDir, err := os.MkdirTemp("", "mercurius-codex-*")
	if err != nil {
		return reviewer.ReviewResponse{}, fmt.Errorf("create codex temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	schemaPath := filepath.Join(tempDir, "schema.json")
	lastMessagePath := filepath.Join(tempDir, "last-message.json")
	if err := os.WriteFile(schemaPath, req.Schema, 0o600); err != nil {
		return reviewer.ReviewResponse{}, fmt.Errorf("write codex schema file: %w", err)
	}

	stdout, stderr, err := r.run(ctx, req.Prompt, schemaPath, lastMessagePath)
	if err != nil {
		return reviewer.ReviewResponse{}, err
	}

	output, err := os.ReadFile(lastMessagePath)
	if err != nil {
		return reviewer.ReviewResponse{}, fmt.Errorf("read codex last message file: %w", err)
	}
	raw, err := extractReviewOutput(output)
	if err != nil {
		return reviewer.ReviewResponse{}, err
	}

	return reviewer.ReviewResponse{
		Raw:        raw,
		UsageNotes: r.usageNotes(stdout, stderr),
	}, nil
}

func (r *Reviewer) run(ctx context.Context, prompt string, schemaPath string, lastMessagePath string) ([]byte, []byte, error) {
	args := r.args(schemaPath, lastMessagePath)
	cmd := exec.CommandContext(ctx, r.options.BinaryPath, args...)
	cmd.Stdin = strings.NewReader(prompt)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("codex reviewer failed: %w%s", err, commandOutputSuffix(stdout.Bytes(), stderr.Bytes()))
	}
	return stdout.Bytes(), stderr.Bytes(), nil
}

func (r *Reviewer) args(schemaPath string, lastMessagePath string) []string {
	args := []string{
		"exec",
		"-C", r.options.WorkingDir,
		"--ephemeral",
		"--sandbox", "read-only",
		"--output-schema", schemaPath,
		"--output-last-message", lastMessagePath,
	}
	if r.options.Model != "" {
		args = append(args, "-m", r.options.Model)
	}
	args = append(args, r.options.ExtraArgs...)
	return args
}

func (r *Reviewer) usageNotes(stdout []byte, stderr []byte) string {
	parts := []string{fmt.Sprintf("binary='%s'", r.options.BinaryPath)}
	if r.options.Model != "" {
		parts = append(parts, fmt.Sprintf("model='%s'", r.options.Model))
	}
	parts = append(parts, fmt.Sprintf("stdout_bytes='%d'", len(stdout)))
	parts = append(parts, fmt.Sprintf("stderr_bytes='%d'", len(stderr)))
	return strings.Join(parts, ", ")
}

func commandOutputSuffix(stdout []byte, stderr []byte) string {
	var parts []string
	if text := strings.TrimSpace(string(stderr)); text != "" {
		parts = append(parts, fmt.Sprintf("stderr: %s", text))
	}
	if text := strings.TrimSpace(string(stdout)); text != "" {
		parts = append(parts, fmt.Sprintf("stdout: %s", text))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, "; ")
}

func copyRaw(raw []byte) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}
