package pi

import (
	"context"
	"encoding/json"
	"errors"
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

func TestReviewerRunsPiWithArgsAndPrompt(t *testing.T) {
	helper := newFakePi(t, fakePiOptions{
		stdout: assistantStream(validReviewOutput()),
	})
	workingDir := t.TempDir()

	r := New(Options{
		BinaryPath: helper.binaryPath,
		Model:      "openai-codex/gpt-5.5",
		ExtraArgs:  []string{"--verbose"},
	})
	startedPID := 0
	resp, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review this prompt",
		Schema:     schema.ReviewOutputSchema(),
		WorkingDir: workingDir,
		Started:    func(pid int) { startedPID = pid },
	})
	if err != nil {
		t.Fatalf("review failed: %v", err)
	}
	if startedPID <= 0 {
		t.Fatalf("Started was not called with the reviewer's pid: %d", startedPID)
	}
	if string(resp.Raw) != validReviewOutput() {
		t.Fatalf("raw output mismatch:\n got: %s\nwant: %s", resp.Raw, validReviewOutput())
	}
	if resp.UsageNotes == "" {
		t.Fatal("expected usage notes")
	}

	args := helper.args(t)
	assertArgSequence(t, args, []string{
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
	})
	assertArgSequence(t, args, []string{"--model", "openai-codex/gpt-5.5", "--verbose"})

	last := args[len(args)-1]
	if !strings.HasPrefix(last, "@") {
		t.Fatalf("expected last arg to be an @file reference, got %q", last)
	}

	prompt := readFile(t, helper.promptPath)
	if prompt != "review this prompt" {
		t.Fatalf("prompt file mismatch: %q", prompt)
	}
	assertSameDir(t, readFile(t, helper.pwdPath), workingDir)
}

func TestReviewerOmitsModelWhenUnset(t *testing.T) {
	helper := newFakePi(t, fakePiOptions{stdout: assistantStream(validReviewOutput())})
	r := New(Options{BinaryPath: helper.binaryPath})
	if _, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review",
		WorkingDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("review failed: %v", err)
	}
	if slices.Contains(helper.args(t), "--model") {
		t.Fatalf("did not expect --model when model unset: %v", helper.args(t))
	}
}

func TestReviewerReturnsCommandFailureWithOutput(t *testing.T) {
	helper := newFakePi(t, fakePiOptions{
		exitCode: 1,
		stdout:   "not a json stream",
		stderr:   "failure details",
	})
	r := New(Options{BinaryPath: helper.binaryPath})

	_, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review",
		WorkingDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected review error")
	}
	if !strings.Contains(err.Error(), "pi reviewer failed") {
		t.Fatalf("expected error to name the pi failure, got: %v", err)
	}
	// the captured output travels on the error, not in its message.
	output := string(reviewer.CapturedOutput(err))
	for _, want := range []string{"failure details", "not a json stream"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected carried output to contain %q, got: %q", want, output)
		}
	}
	if strings.Contains(err.Error(), "failure details") {
		t.Fatalf("error message must not carry stderr: %v", err)
	}
}

// a clean exit whose stream holds no review object is a failure that holds
// output; the output must be carried on the error so the runner can keep it.
func TestReviewerCarriesMalformedOutput(t *testing.T) {
	helper := newFakePi(t, fakePiOptions{
		stdout: assistantStream("no json object in this answer"),
	})
	r := New(Options{BinaryPath: helper.binaryPath})

	_, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review",
		WorkingDir: t.TempDir(),
	})
	var outputErr *reviewer.OutputError
	if !errors.As(err, &outputErr) {
		t.Fatalf("expected an OutputError, got %T: %v", err, err)
	}
	if !strings.Contains(string(outputErr.Output), "no json object in this answer") {
		t.Fatalf("carried output missing the malformed stream: %q", outputErr.Output)
	}
}

// a reviewer that leaves a child holding its stdout and stderr open must not
// hold Review past the wait delay. without WaitDelay, Wait blocks until the
// child closes the pipes, and this test fails on its deadline.
func TestReviewerReturnsWhileChildHoldsPipes(t *testing.T) {
	previous := reviewer.WaitDelay
	reviewer.WaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { reviewer.WaitDelay = previous })
	helper := newFakePi(t, fakePiOptions{
		stdout:  assistantStream(validReviewOutput()),
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

func TestReviewerRecoversOnNonZeroExit(t *testing.T) {
	helper := newFakePi(t, fakePiOptions{
		exitCode: 143,
		stdout:   assistantStream(validReviewOutput()),
	})
	r := New(Options{BinaryPath: helper.binaryPath})

	resp, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review",
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("expected recovered output despite nonzero exit: %v", err)
	}
	if string(resp.Raw) != validReviewOutput() {
		t.Fatalf("raw output mismatch: %s", resp.Raw)
	}
}

func TestReviewerDoesNotValidateSchema(t *testing.T) {
	helper := newFakePi(t, fakePiOptions{
		stdout: assistantStream(`{"not":"a valid review output"}`),
	})
	r := New(Options{BinaryPath: helper.binaryPath})

	resp, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review",
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("expected json-valid output to be returned: %v", err)
	}
	if string(resp.Raw) != `{"not":"a valid review output"}` {
		t.Fatalf("raw output mismatch: %s", resp.Raw)
	}
	if err := schema.ValidateReviewOutput(resp.Raw); err == nil {
		t.Fatal("expected broker schema validation to reject this output")
	}
}

func TestReviewerRequiresWorkingDir(t *testing.T) {
	r := New(Options{BinaryPath: "pi"})

	_, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt: "review",
	})
	if err == nil || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("expected working directory error, got: %v", err)
	}
}

type fakePiOptions struct {
	stdout   string
	stderr   string
	exitCode int
	lingers  bool
}

type fakePi struct {
	binaryPath string
	argsPath   string
	pwdPath    string
	promptPath string
}

func newFakePi(t *testing.T, options fakePiOptions) fakePi {
	t.Helper()

	dir := t.TempDir()
	helper := fakePi{
		binaryPath: filepath.Join(dir, "pi"),
		argsPath:   filepath.Join(dir, "args.txt"),
		pwdPath:    filepath.Join(dir, "pwd.txt"),
		promptPath: filepath.Join(dir, "prompt-copy.md"),
	}

	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$FAKE_PI_ARGS"
pwd > "$FAKE_PI_PWD"
last=""
for a in "$@"; do last="$a"; done
case "$last" in
	@*) cp "${last#@}" "$FAKE_PI_PROMPT" ;;
esac
if [ -n "${FAKE_PI_STDERR:-}" ]; then
	printf '%s' "$FAKE_PI_STDERR" >&2
fi
if [ -n "${FAKE_PI_STDOUT:-}" ]; then
	printf '%s' "$FAKE_PI_STDOUT"
fi
if [ -n "${FAKE_PI_LINGER_PID:-}" ]; then
	sleep 60 &
	printf '%s' "$!" > "$FAKE_PI_LINGER_PID"
fi
exit "${FAKE_PI_EXIT_CODE:-0}"
`
	if err := os.WriteFile(helper.binaryPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}

	t.Setenv("FAKE_PI_ARGS", helper.argsPath)
	t.Setenv("FAKE_PI_PWD", helper.pwdPath)
	t.Setenv("FAKE_PI_PROMPT", helper.promptPath)
	t.Setenv("FAKE_PI_STDOUT", options.stdout)
	t.Setenv("FAKE_PI_STDERR", options.stderr)
	t.Setenv("FAKE_PI_EXIT_CODE", strconv.Itoa(options.exitCode))
	if options.lingers {
		pidPath := filepath.Join(dir, "linger.pid")
		t.Setenv("FAKE_PI_LINGER_PID", pidPath)
		t.Cleanup(func() { killPidFile(pidPath) })
	} else {
		t.Setenv("FAKE_PI_LINGER_PID", "")
	}

	return helper
}

func (f fakePi) args(t *testing.T) []string {
	t.Helper()

	raw := strings.TrimSpace(readFile(t, f.argsPath))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

// assistantStream builds a pi '--mode json' event stream whose final assistant
// message carries text (preceded by a thinking block, as a real run does).
func assistantStream(text string) string {
	lines := []string{
		`{"type":"session","version":3,"id":"x","cwd":"/tmp"}`,
		`{"type":"message_start","message":{"role":"assistant"}}`,
		assistantMessageEnd(text),
		`{"type":"agent_end","messages":[],"willRetry":false}`,
	}
	return strings.Join(lines, "\n")
}

func assistantMessageEnd(text string) string {
	ev := map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "thinking", "thinking": "reasoning"},
				map[string]any{"type": "text", "text": text},
			},
		},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		panic(err)
	}
	return string(raw)
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
