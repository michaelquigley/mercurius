package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/michaelquigley/mercurius/internal/reviewer"
)

const defaultBinaryPath = "claude"

// Options configures the Claude Code subprocess reviewer.
type Options struct {
	BinaryPath string
	Model      string
	ExtraArgs  []string
}

// Reviewer invokes the Claude Code CLI in print mode for one structured review.
type Reviewer struct {
	options Options
}

// New returns a Claude Code subprocess reviewer.
func New(options Options) *Reviewer {
	if options.BinaryPath == "" {
		options.BinaryPath = defaultBinaryPath
	}
	options.ExtraArgs = append([]string(nil), options.ExtraArgs...)
	return &Reviewer{options: options}
}

// Review runs claude in print mode with the pre-assembled prompt and schema. The
// prompt already inlines the artifact content and the output schema, so claude is
// driven as a prompt-in / json-out call: read-only ('--permission-mode plan'),
// ephemeral ('--no-session-persistence'), with the schema enforced natively via
// '--json-schema'. The validated object is returned in the envelope's
// 'structured_output' field; when that is absent the object is recovered from the
// 'result' text. The broker validates the returned object against the schema.
func (r *Reviewer) Review(ctx context.Context, req reviewer.ReviewRequest) (reviewer.ReviewResponse, error) {
	if err := ctx.Err(); err != nil {
		return reviewer.ReviewResponse{}, err
	}
	if req.WorkingDir == "" {
		return reviewer.ReviewResponse{}, errors.New("claude reviewer working directory is required")
	}
	if len(req.Schema) == 0 {
		return reviewer.ReviewResponse{}, errors.New("claude reviewer schema is required")
	}

	stdout, stderr, runErr := r.run(ctx, req.WorkingDir, req.Prompt, req.Schema, req.Started)

	raw, env, parseErr := parseEnvelope(stdout)
	if parseErr == nil {
		return reviewer.ReviewResponse{Raw: raw, UsageNotes: r.usageNotes(env)}, nil
	}

	// when no envelope could be read and the process itself failed, the process
	// failure is the better message. an is_error envelope or a missing json object
	// is already descriptive, so prefer that message regardless of exit code.
	// either way the captured output is carried on the error, not in its message.
	err := parseErr
	if errors.Is(parseErr, errNotEnvelope) && runErr != nil {
		err = fmt.Errorf("claude reviewer failed: %w", runErr)
	}
	return reviewer.ReviewResponse{}, reviewer.NewOutputError(err,
		reviewer.OutputSection{Name: "stdout", Bytes: stdout},
		reviewer.OutputSection{Name: "stderr", Bytes: stderr},
	)
}

func (r *Reviewer) run(ctx context.Context, workingDir string, prompt string, schema json.RawMessage, started func(pid int)) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, r.options.BinaryPath, r.args(schema)...)
	cmd.Dir = workingDir
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = os.Environ()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := reviewer.RunCommand(cmd, started)
	return stdout.Bytes(), stderr.Bytes(), err
}

func (r *Reviewer) args(schema json.RawMessage) []string {
	args := []string{
		"-p",
		"--output-format", "json",
		"--json-schema", string(schema),
		"--permission-mode", "plan",
		"--no-session-persistence",
	}
	if r.options.Model != "" {
		args = append(args, "--model", r.options.Model)
	}
	// note: --bare is intentionally not set, so claude inherits the operator's
	// logged-in credentials. operators who want api-key-only auth can pass --bare
	// (and set ANTHROPIC_API_KEY) through extra_args.
	args = append(args, r.options.ExtraArgs...)
	return args
}

func (r *Reviewer) usageNotes(env envelope) string {
	parts := []string{fmt.Sprintf("binary='%s'", r.options.BinaryPath)}
	if r.options.Model != "" {
		parts = append(parts, fmt.Sprintf("model='%s'", r.options.Model))
	}
	if env.Subtype != "" {
		parts = append(parts, fmt.Sprintf("subtype='%s'", env.Subtype))
	}
	parts = append(parts, fmt.Sprintf("cost_usd='%.4f'", env.TotalCostUSD))
	if env.NumTurns > 0 {
		parts = append(parts, fmt.Sprintf("num_turns='%d'", env.NumTurns))
	}
	if env.DurationMS > 0 {
		parts = append(parts, fmt.Sprintf("duration_ms='%d'", env.DurationMS))
	}
	if env.SessionID != "" {
		parts = append(parts, fmt.Sprintf("session_id='%s'", env.SessionID))
	}
	return strings.Join(parts, ", ")
}
