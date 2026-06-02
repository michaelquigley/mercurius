package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/schema"
)

func TestReviewerRunsClaudeWithPromptSchemaAndArgs(t *testing.T) {
	helper := newFakeClaude(t, fakeClaudeOptions{
		stdout: successEnvelope(validReviewOutput()),
	})
	workingDir := t.TempDir()
	reqSchema := json.RawMessage(`{"type":"object"}`)

	r := New(Options{
		BinaryPath: helper.binaryPath,
		Model:      "sonnet",
		ExtraArgs:  []string{"--debug"},
	})
	resp, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review this prompt",
		Schema:     reqSchema,
		WorkingDir: workingDir,
	})
	if err != nil {
		t.Fatalf("review failed: %v", err)
	}
	if string(resp.Raw) != validReviewOutput() {
		t.Fatalf("raw output mismatch:\n got: %s\nwant: %s", resp.Raw, validReviewOutput())
	}
	if resp.UsageNotes == "" {
		t.Fatal("expected usage notes")
	}
	if !strings.Contains(resp.UsageNotes, "model='sonnet'") {
		t.Fatalf("usage notes missing model: %s", resp.UsageNotes)
	}

	args := helper.args(t)
	assertArgSequence(t, args, []string{
		"-p",
		"--output-format", "json",
		"--json-schema", `{"type":"object"}`,
		"--permission-mode", "plan",
		"--no-session-persistence",
	})
	assertArgSequence(t, args, []string{"--model", "sonnet", "--debug"})
	if slices.Contains(args, "--bare") {
		t.Fatalf("did not expect --bare in args: %v", args)
	}

	stdin := strings.TrimSpace(readFile(t, helper.stdinPath))
	if stdin != "review this prompt" {
		t.Fatalf("stdin mismatch: %q", stdin)
	}
	assertSameDir(t, readFile(t, helper.pwdPath), workingDir)
}

func TestReviewerOmitsModelWhenUnset(t *testing.T) {
	helper := newFakeClaude(t, fakeClaudeOptions{
		stdout: successEnvelope(validReviewOutput()),
	})
	r := New(Options{BinaryPath: helper.binaryPath})
	if _, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review",
		Schema:     json.RawMessage(`{"type":"object"}`),
		WorkingDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("review failed: %v", err)
	}
	if slices.Contains(helper.args(t), "--model") {
		t.Fatalf("did not expect --model when model unset: %v", helper.args(t))
	}
}

func TestReviewerSurfacesIsError(t *testing.T) {
	helper := newFakeClaude(t, fakeClaudeOptions{
		stdout: `{"is_error":true,"subtype":"success","result":"Not logged in · Please run /login"}`,
	})
	r := New(Options{BinaryPath: helper.binaryPath})

	_, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review",
		Schema:     json.RawMessage(`{"type":"object"}`),
		WorkingDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("expected not-logged-in error, got: %v", err)
	}
}

func TestReviewerReturnsCommandFailureWithOutput(t *testing.T) {
	helper := newFakeClaude(t, fakeClaudeOptions{
		exitCode: 1,
		stdout:   "boom",
		stderr:   "failure details",
	})
	r := New(Options{BinaryPath: helper.binaryPath})

	_, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review",
		Schema:     json.RawMessage(`{"type":"object"}`),
		WorkingDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected review error")
	}
	for _, want := range []string{"claude reviewer failed", "failure details"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got: %v", want, err)
		}
	}
}

func TestReviewerDoesNotValidateSchema(t *testing.T) {
	helper := newFakeClaude(t, fakeClaudeOptions{
		stdout: successEnvelope(`{"not":"a valid review output"}`),
	})
	r := New(Options{BinaryPath: helper.binaryPath})

	resp, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review",
		Schema:     json.RawMessage(`{"type":"object"}`),
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("expected structured output to be returned: %v", err)
	}
	if string(resp.Raw) != `{"not":"a valid review output"}` {
		t.Fatalf("raw output mismatch: %s", resp.Raw)
	}
	if err := schema.ValidateReviewOutput(resp.Raw); err == nil {
		t.Fatal("expected broker schema validation to reject this output")
	}
}

func TestReviewerRequiresWorkingDirAndSchema(t *testing.T) {
	r := New(Options{BinaryPath: "claude"})

	_, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Schema: json.RawMessage(`{"type":"object"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("expected working directory error, got: %v", err)
	}

	_, err = r.Review(context.Background(), reviewer.ReviewRequest{
		WorkingDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("expected schema error, got: %v", err)
	}
}

type fakeClaudeOptions struct {
	stdout   string
	stderr   string
	exitCode int
}

type fakeClaude struct {
	binaryPath string
	argsPath   string
	stdinPath  string
	pwdPath    string
}

func newFakeClaude(t *testing.T, options fakeClaudeOptions) fakeClaude {
	t.Helper()

	dir := t.TempDir()
	helper := fakeClaude{
		binaryPath: filepath.Join(dir, "claude"),
		argsPath:   filepath.Join(dir, "args.txt"),
		stdinPath:  filepath.Join(dir, "stdin.txt"),
		pwdPath:    filepath.Join(dir, "pwd.txt"),
	}

	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$FAKE_CLAUDE_ARGS"
pwd > "$FAKE_CLAUDE_PWD"
cat > "$FAKE_CLAUDE_STDIN"
if [ -n "${FAKE_CLAUDE_STDERR:-}" ]; then
	printf '%s' "$FAKE_CLAUDE_STDERR" >&2
fi
if [ -n "${FAKE_CLAUDE_STDOUT:-}" ]; then
	printf '%s' "$FAKE_CLAUDE_STDOUT"
fi
exit "${FAKE_CLAUDE_EXIT_CODE:-0}"
`
	if err := os.WriteFile(helper.binaryPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	t.Setenv("FAKE_CLAUDE_ARGS", helper.argsPath)
	t.Setenv("FAKE_CLAUDE_STDIN", helper.stdinPath)
	t.Setenv("FAKE_CLAUDE_PWD", helper.pwdPath)
	t.Setenv("FAKE_CLAUDE_STDOUT", options.stdout)
	t.Setenv("FAKE_CLAUDE_STDERR", options.stderr)
	t.Setenv("FAKE_CLAUDE_EXIT_CODE", strconv.Itoa(options.exitCode))

	return helper
}

func (f fakeClaude) args(t *testing.T) []string {
	t.Helper()

	raw := strings.TrimSpace(readFile(t, f.argsPath))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

func successEnvelope(structuredOutput string) string {
	return fmt.Sprintf(`{"is_error":false,"subtype":"success","result":"ok","structured_output":%s,"total_cost_usd":0.0123,"num_turns":2,"duration_ms":4200,"session_id":"abc123"}`, structuredOutput)
}

func validReviewOutput() string {
	raw, err := json.Marshal(map[string]any{
		"verdict":        "ready_to_build",
		"summary":        "ready",
		"concerns":       []any{},
		"questions":      []any{},
		"advisory_notes": []any{},
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func assertArgSequence(t *testing.T, args []string, sequence []string) {
	t.Helper()

	if len(sequence) == 0 {
		return
	}
	for i := 0; i <= len(args)-len(sequence); i++ {
		if slices.Equal(args[i:i+len(sequence)], sequence) {
			return
		}
	}
	t.Fatalf("expected args to contain sequence %v, got %v", sequence, args)
}

func assertSameDir(t *testing.T, got string, want string) {
	t.Helper()

	gotResolved, err := filepath.EvalSymlinks(strings.TrimSpace(got))
	if err != nil {
		t.Fatalf("resolve got dir: %v", err)
	}
	wantResolved, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatalf("resolve want dir: %v", err)
	}
	if gotResolved != wantResolved {
		t.Fatalf("working dir = %q, want %q", gotResolved, wantResolved)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
