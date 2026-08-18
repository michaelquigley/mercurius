package pi

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
	"github.com/michaelquigley/mercurius/internal/reviewer/jsonout"
)

const defaultBinaryPath = "pi"

// Options configures the pi.dev subprocess reviewer.
type Options struct {
	BinaryPath string
	Model      string
	ExtraArgs  []string
}

// Reviewer invokes the pi coding agent in print mode for one structured review.
type Reviewer struct {
	options Options
}

// New returns a pi subprocess reviewer.
func New(options Options) *Reviewer {
	if options.BinaryPath == "" {
		options.BinaryPath = defaultBinaryPath
	}
	options.ExtraArgs = append([]string(nil), options.ExtraArgs...)
	return &Reviewer{options: options}
}

// Review runs pi in print mode with the pre-assembled prompt. Unlike codex and
// claude, pi has no native json-schema enforcement, so the schema is conveyed only
// by the prompt (which already inlines the artifact content, the output schema, and
// a "single json object only" instruction). pi runs read-only ('--tools
// read,grep,find,ls'), ephemeral ('--no-session'), and with project context files
// suppressed ('--no-context-files') so the review is not coloured by the AGENTS.md
// or CLAUDE.md that sit above the round directory.
//
// The run is also hermetically sealed off from the host's development-flavoured
// pi configuration. The same pi install often serves double duty as an
// interactive coding harness with a richer setup — extensions (which is how MCP
// servers arrive; pi core has no native MCP), skills, prompt templates, and
// project-local '.pi/' directories trusted in the agent dir's trust store. None
// of that belongs in a review subprocess: '--no-extensions', '--no-skills', and
// '--no-prompt-templates' disable discovery outright, and '--no-approve' forces
// the working directory untrusted for the run so a directory trusted for
// development can't execute its project extensions inside a review. '--offline'
// rounds it out by disabling startup network operations (version check, managed
// binary and package installs, model catalog refresh); the model API call and
// oauth token refresh are unaffected.
//
// The final assistant message is
// parsed out of pi's '--mode json' event stream and the json object is extracted
// from it; the broker validates that object against the schema.
func (r *Reviewer) Review(ctx context.Context, req reviewer.ReviewRequest) (reviewer.ReviewResponse, error) {
	if err := ctx.Err(); err != nil {
		return reviewer.ReviewResponse{}, err
	}
	if req.WorkingDir == "" {
		return reviewer.ReviewResponse{}, errors.New("pi reviewer working directory is required")
	}

	stdout, stderr, runErr := r.run(ctx, req.WorkingDir, req.Prompt)

	raw, err := extractReviewObject(stdout)
	if err == nil {
		return reviewer.ReviewResponse{Raw: raw, UsageNotes: r.usageNotes(stdout, stderr)}, nil
	}

	// no usable object. if the process itself failed, the captured output is the
	// most useful diagnostic; otherwise surface the extraction failure directly.
	if runErr != nil {
		return reviewer.ReviewResponse{}, fmt.Errorf("pi reviewer failed: %w%s", runErr, commandOutputSuffix(stdout, stderr))
	}
	return reviewer.ReviewResponse{}, err
}

// extractReviewObject pulls the final assistant message out of the pi event stream
// and returns the json object embedded in it.
func extractReviewObject(stdout []byte) (json.RawMessage, error) {
	text, err := finalMessage(stdout)
	if err != nil {
		return nil, err
	}
	raw, err := jsonout.Object([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("pi reviewer final message contained no json object: %w", err)
	}
	return raw, nil
}

func (r *Reviewer) run(ctx context.Context, workingDir string, prompt string) ([]byte, []byte, error) {
	tempDir, err := os.MkdirTemp("", "mercurius-pi-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create pi temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// the prompt is delivered as a file reference ('@<path>') rather than a
	// positional argument: it inlines the artifact content and can be large, and
	// pi reads '@file' content into the message.
	promptPath := filepath.Join(tempDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return nil, nil, fmt.Errorf("write pi prompt file: %w", err)
	}

	cmd := exec.CommandContext(ctx, r.options.BinaryPath, r.args(promptPath)...)
	cmd.Dir = workingDir
	// stdin is left nil so the child reads from the null device. pi consumes stdin
	// as additional input; an open stdin with no EOF would hang the run.
	cmd.Env = os.Environ()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// args builds the pi command line. flags verified against pi v0.80.7; a version
// bump is the place to re-check '--mode json' event shape and the read-only,
// context-suppression, and discovery/trust/offline flags. the prompt is passed
// last as an '@file' reference.
func (r *Reviewer) args(promptPath string) []string {
	args := []string{
		"-p",
		"--mode", "json",
		"--no-session",
		"--no-context-files",
		"--tools", "read,grep,find,ls",
		"--no-extensions",
		"--no-skills",
		"--no-prompt-templates",
		"--no-approve",
		"--offline",
	}
	if r.options.Model != "" {
		args = append(args, "--model", r.options.Model)
	}
	args = append(args, r.options.ExtraArgs...)
	args = append(args, "@"+promptPath)
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
