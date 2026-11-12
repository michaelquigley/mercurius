package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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
	if !strings.Contains(err.Error(), "claude reviewer failed") {
		t.Fatalf("expected error to name the claude failure, got: %v", err)
	}
	// the captured output travels on the error, not in its message.
	output := string(reviewer.CapturedOutput(err))
	for _, want := range []string{"failure details", "boom"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected carried output to contain %q, got: %q", want, output)
		}
	}
	if strings.Contains(err.Error(), "failure details") {
		t.Fatalf("error message must not carry stderr: %v", err)
	}
}

// a clean exit with output that is not an envelope is a failure that holds
// output; the output must be carried on the error so the runner can keep it.
func TestReviewerCarriesMalformedOutput(t *testing.T) {
	helper := newFakeClaude(t, fakeClaudeOptions{
		stdout: "this is not an envelope",
	})
	r := New(Options{BinaryPath: helper.binaryPath})

	_, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review",
		Schema:     json.RawMessage(`{"type":"object"}`),
		WorkingDir: t.TempDir(),
	})
	var outputErr *reviewer.OutputError
	if !errors.As(err, &outputErr) {
		t.Fatalf("expected an OutputError, got %T: %v", err, err)
	}
	if !strings.Contains(string(outputErr.Output), "this is not an envelope") {
		t.Fatalf("carried output missing the malformed stdout: %q", outputErr.Output)
	}
}

// a reviewer that leaves a child holding its stdout and stderr open must not
// hold Review past the wait delay. without WaitDelay, Wait blocks until the
// child closes the pipes, and this test fails on its deadline.
func TestReviewerReturnsWhileChildHoldsPipes(t *testing.T) {
	previous := reviewer.WaitDelay
	reviewer.WaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { reviewer.WaitDelay = previous })
	helper := newFakeClaude(t, fakeClaudeOptions{
		stdout:  successEnvelope(validReviewOutput()),
		lingers: true,
	})
	r := New(Options{BinaryPath: helper.binaryPath})

	type result struct {
		resp reviewer.ReviewResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := r.Review(context.Background(), reviewer.ReviewRequest{
			Prompt:     "review",
			Schema:     json.RawMessage(`{"type":"object"}`),
			WorkingDir: t.TempDir(),
		})
		done <- result{resp, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("expected the result despite the lingering child: %v", got.err)
		}
		if string(got.resp.Raw) != validReviewOutput() {
			t.Fatalf("raw output mismatch: %s", got.resp.Raw)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("review did not return while a child held its output pipes open")
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
	lingers  bool
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
if [ -n "${FAKE_CLAUDE_LINGER_PID:-}" ]; then
	sleep 60 &
	printf '%s' "$!" > "$FAKE_CLAUDE_LINGER_PID"
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
	if options.lingers {
		pidPath := filepath.Join(dir, "linger.pid")
		t.Setenv("FAKE_CLAUDE_LINGER_PID", pidPath)
		t.Cleanup(func() { killPidFile(pidPath) })
	} else {
		t.Setenv("FAKE_CLAUDE_LINGER_PID", "")
	}

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

// killPidFile kills the child a fake binary left running, by the pid it
// recorded.
func killPidFile(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
