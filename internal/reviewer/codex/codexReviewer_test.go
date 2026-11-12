package codex

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

func TestReviewerRunsCodexWithPromptSchemaAndOutputCapture(t *testing.T) {
	helper := newFakeCodex(t, fakeCodexOptions{
		output: validReviewOutput(),
	})
	workingDir := t.TempDir()
	reqSchema := schema.ReviewOutputSchema()

	r := New(Options{
		BinaryPath: helper.binaryPath,
		Model:      "gpt-test",
		ExtraArgs:  []string{"--ignore-user-config"},
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

	args := helper.args(t)
	assertArgSequence(t, args, []string{
		"exec",
		"-C", workingDir,
		"--ephemeral",
		"--skip-git-repo-check",
		"--sandbox", "read-only",
		"--output-schema",
	})
	assertArgSequence(t, args, []string{"--output-last-message"})
	assertArgSequence(t, args, []string{"-m", "gpt-test", "--ignore-user-config"})

	codexHome := strings.TrimSpace(readFile(t, helper.codexHomePath))
	if codexHome == "" || codexHome == helper.sourceCodexHome {
		t.Fatalf("codex home was not isolated: %q", codexHome)
	}
	if !strings.HasPrefix(codexHome, workingDir) {
		t.Fatalf("codex home = %q, want under %q", codexHome, workingDir)
	}

	stdin, err := os.ReadFile(helper.stdinPath)
	if err != nil {
		t.Fatalf("read captured stdin: %v", err)
	}
	if string(stdin) != "review this prompt" {
		t.Fatalf("stdin mismatch: %q", stdin)
	}

	writtenSchema, err := os.ReadFile(helper.schemaCopyPath)
	if err != nil {
		t.Fatalf("read copied schema: %v", err)
	}
	if string(writtenSchema) != string(reqSchema) {
		t.Fatal("schema written to subprocess did not match request schema")
	}

	schemaPath := strings.TrimSpace(readFile(t, helper.schemaPathPath))
	lastMessagePath := strings.TrimSpace(readFile(t, helper.lastMessagePathPath))
	if got := argAfter(t, args, "--output-schema"); got != schemaPath {
		t.Fatalf("schema arg mismatch: got %q want %q", got, schemaPath)
	}
	if got := argAfter(t, args, "--output-last-message"); got != lastMessagePath {
		t.Fatalf("last-message arg mismatch: got %q want %q", got, lastMessagePath)
	}
	if _, err := os.Stat(schemaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected schema temp file cleanup, got err=%v", err)
	}
	if _, err := os.Stat(lastMessagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected last-message temp file cleanup, got err=%v", err)
	}
}

func TestReviewerReturnsCommandFailureWithOutput(t *testing.T) {
	helper := newFakeCodex(t, fakeCodexOptions{
		exitCode: 42,
		stdout:   "progress output",
		stderr:   "failure details",
	})
	r := New(Options{
		BinaryPath: helper.binaryPath,
	})

	_, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review this prompt",
		Schema:     schema.ReviewOutputSchema(),
		WorkingDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected review error")
	}
	if !strings.Contains(err.Error(), "codex reviewer failed") {
		t.Fatalf("expected error to name the codex failure, got: %v", err)
	}
	// the captured output travels on the error, not in its message.
	output := string(reviewer.CapturedOutput(err))
	for _, want := range []string{"failure details", "progress output"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected carried output to contain %q, got: %q", want, output)
		}
		if strings.Contains(err.Error(), want) {
			t.Fatalf("error message must not carry output %q: %v", want, err)
		}
	}

	schemaPath := strings.TrimSpace(readFile(t, helper.schemaPathPath))
	lastMessagePath := strings.TrimSpace(readFile(t, helper.lastMessagePathPath))
	if _, err := os.Stat(schemaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected schema temp file cleanup after failure, got err=%v", err)
	}
	if _, err := os.Stat(lastMessagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected last-message temp file cleanup after failure, got err=%v", err)
	}
}

func TestReviewerRecoversLastMessageAfterCommandFailure(t *testing.T) {
	helper := newFakeCodex(t, fakeCodexOptions{
		output:                validReviewOutput(),
		exitCode:              143,
		stderr:                "terminated after final output",
		writeOutputBeforeExit: true,
	})
	r := New(Options{
		BinaryPath: helper.binaryPath,
	})

	resp, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review this prompt",
		Schema:     schema.ReviewOutputSchema(),
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("expected recovered review output: %v", err)
	}
	if string(resp.Raw) != validReviewOutput() {
		t.Fatalf("raw output mismatch:\n got: %s\nwant: %s", resp.Raw, validReviewOutput())
	}
	if !strings.Contains(resp.UsageNotes, "recovered_last_message_after_error='true'") {
		t.Fatalf("usage notes did not mark recovery: %s", resp.UsageNotes)
	}

	lastMessagePath := strings.TrimSpace(readFile(t, helper.lastMessagePathPath))
	if _, err := os.Stat(lastMessagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected last-message temp file cleanup after recovery, got err=%v", err)
	}
}

func TestReviewerDoesNotValidateSchema(t *testing.T) {
	helper := newFakeCodex(t, fakeCodexOptions{
		output: `{"not":"a valid review output"}`,
	})
	r := New(Options{
		BinaryPath: helper.binaryPath,
	})

	resp, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review this prompt",
		Schema:     schema.ReviewOutputSchema(),
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

func TestReviewerRequiresWorkingDirAndSchema(t *testing.T) {
	r := New(Options{BinaryPath: "codex"})

	_, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Schema: schema.ReviewOutputSchema(),
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

// a clean exit whose last message holds no json object is a failure that holds
// output; the output must be carried on the error so the runner can keep it.
func TestReviewerCarriesMalformedOutput(t *testing.T) {
	helper := newFakeCodex(t, fakeCodexOptions{
		output: "this is not a review object",
		stdout: "progress before the bad answer",
	})
	r := New(Options{BinaryPath: helper.binaryPath})

	_, err := r.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:     "review this prompt",
		Schema:     schema.ReviewOutputSchema(),
		WorkingDir: t.TempDir(),
	})
	var outputErr *reviewer.OutputError
	if !errors.As(err, &outputErr) {
		t.Fatalf("expected an OutputError, got %T: %v", err, err)
	}
	for _, want := range []string{"this is not a review object", "progress before the bad answer"} {
		if !strings.Contains(string(outputErr.Output), want) {
			t.Fatalf("carried output missing %q: %q", want, outputErr.Output)
		}
	}
}

// a reviewer that leaves a child holding its stdout and stderr open must not
// hold Review past the wait delay. without WaitDelay, Wait blocks until the
// child closes the pipes, and this test fails on its deadline.
func TestReviewerReturnsWhileChildHoldsPipes(t *testing.T) {
	setShortWaitDelay(t)
	helper := newFakeCodex(t, fakeCodexOptions{
		output:  validReviewOutput(),
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
			Prompt:     "review this prompt",
			Schema:     schema.ReviewOutputSchema(),
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

func setShortWaitDelay(t *testing.T) {
	t.Helper()
	previous := reviewer.WaitDelay
	reviewer.WaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { reviewer.WaitDelay = previous })
}

// killLingering kills the child a fake binary left running, by the pid it
// recorded.
func killLingering(t *testing.T, pidPath string) {
	t.Helper()
	t.Cleanup(func() {
		raw, err := os.ReadFile(pidPath)
		if err != nil {
			return
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
}

type fakeCodexOptions struct {
	output                string
	stdout                string
	stderr                string
	exitCode              int
	writeOutputBeforeExit bool
	lingers               bool
}

type fakeCodex struct {
	binaryPath          string
	argsPath            string
	stdinPath           string
	codexHomePath       string
	schemaCopyPath      string
	schemaPathPath      string
	lastMessagePathPath string
	sourceCodexHome     string
}

func newFakeCodex(t *testing.T, options fakeCodexOptions) fakeCodex {
	t.Helper()

	dir := t.TempDir()
	helper := fakeCodex{
		binaryPath:          filepath.Join(dir, "codex"),
		argsPath:            filepath.Join(dir, "args.txt"),
		stdinPath:           filepath.Join(dir, "stdin.txt"),
		codexHomePath:       filepath.Join(dir, "codex-home.txt"),
		schemaCopyPath:      filepath.Join(dir, "schema-copy.json"),
		schemaPathPath:      filepath.Join(dir, "schema-path.txt"),
		lastMessagePathPath: filepath.Join(dir, "last-message-path.txt"),
		sourceCodexHome:     filepath.Join(dir, "source-codex-home"),
	}
	if err := os.MkdirAll(helper.sourceCodexHome, 0o700); err != nil {
		t.Fatalf("create source codex home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(helper.sourceCodexHome, "auth.json"), []byte(`{"token":"test"}`), 0o600); err != nil {
		t.Fatalf("write source auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(helper.sourceCodexHome, "config.toml"), []byte(`model = "gpt-test"`), 0o600); err != nil {
		t.Fatalf("write source config: %v", err)
	}

	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$FAKE_CODEX_ARGS"
printf '%s' "${CODEX_HOME:-}" > "$FAKE_CODEX_HOME"
cat > "$FAKE_CODEX_STDIN"

schema_path=""
last_message_path=""
while [ "$#" -gt 0 ]; do
	case "$1" in
		--output-schema)
			schema_path="$2"
			shift 2
			;;
		--output-last-message)
			last_message_path="$2"
			shift 2
			;;
		*)
			shift
			;;
	esac
done

if [ -n "$schema_path" ]; then
	printf '%s' "$schema_path" > "$FAKE_CODEX_SCHEMA_PATH"
	cp "$schema_path" "$FAKE_CODEX_SCHEMA_COPY"
fi
if [ -n "$last_message_path" ]; then
	printf '%s' "$last_message_path" > "$FAKE_CODEX_LAST_MESSAGE_PATH"
fi
if [ -n "${FAKE_CODEX_STDOUT:-}" ]; then
	printf '%s\n' "$FAKE_CODEX_STDOUT"
fi
if [ -n "${FAKE_CODEX_STDERR:-}" ]; then
	printf '%s\n' "$FAKE_CODEX_STDERR" >&2
fi
if [ -n "$last_message_path" ]; then
	printf '%s' "$FAKE_CODEX_OUTPUT" > "$last_message_path"
fi
if [ -n "${FAKE_CODEX_LINGER_PID:-}" ]; then
	sleep 60 &
	printf '%s' "$!" > "$FAKE_CODEX_LINGER_PID"
fi
if [ "${FAKE_CODEX_EXIT_CODE:-0}" -ne 0 ]; then
	if [ "${FAKE_CODEX_WRITE_OUTPUT_BEFORE_EXIT:-0}" -eq 0 ] && [ -n "$last_message_path" ]; then
		rm -f "$last_message_path"
	fi
	exit "$FAKE_CODEX_EXIT_CODE"
fi
`
	if err := os.WriteFile(helper.binaryPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("FAKE_CODEX_ARGS", helper.argsPath)
	t.Setenv("FAKE_CODEX_STDIN", helper.stdinPath)
	t.Setenv("FAKE_CODEX_HOME", helper.codexHomePath)
	t.Setenv("FAKE_CODEX_SCHEMA_COPY", helper.schemaCopyPath)
	t.Setenv("FAKE_CODEX_SCHEMA_PATH", helper.schemaPathPath)
	t.Setenv("FAKE_CODEX_LAST_MESSAGE_PATH", helper.lastMessagePathPath)
	t.Setenv("FAKE_CODEX_OUTPUT", options.output)
	t.Setenv("FAKE_CODEX_STDOUT", options.stdout)
	t.Setenv("FAKE_CODEX_STDERR", options.stderr)
	t.Setenv("FAKE_CODEX_EXIT_CODE", strconv.Itoa(options.exitCode))
	if options.writeOutputBeforeExit {
		t.Setenv("FAKE_CODEX_WRITE_OUTPUT_BEFORE_EXIT", "1")
	} else {
		t.Setenv("FAKE_CODEX_WRITE_OUTPUT_BEFORE_EXIT", "0")
	}
	t.Setenv("CODEX_HOME", helper.sourceCodexHome)
	if options.lingers {
		pidPath := filepath.Join(dir, "linger.pid")
		t.Setenv("FAKE_CODEX_LINGER_PID", pidPath)
		killLingering(t, pidPath)
	} else {
		t.Setenv("FAKE_CODEX_LINGER_PID", "")
	}

	return helper
}

func (f fakeCodex) args(t *testing.T) []string {
	t.Helper()

	raw := strings.TrimSpace(readFile(t, f.argsPath))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
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

func argAfter(t *testing.T, args []string, flag string) string {
	t.Helper()

	for i, arg := range args {
		if arg == flag {
			if i+1 >= len(args) {
				t.Fatalf("flag %s has no value in args %v", flag, args)
			}
			return args[i+1]
		}
	}
	t.Fatalf("flag %s not found in args %v", flag, args)
	return ""
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
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
