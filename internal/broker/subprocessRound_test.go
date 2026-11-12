package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/runner"
	"github.com/michaelquigley/mercurius/internal/store"
)

// these tests run rounds through the production reviewers, built by
// runner.BuildReviewer from the round's _run.json, against fake binaries named
// through binary_path.

// one fake binary per reviewer impl. each writes FAKE_REVIEW where its real
// counterpart puts its answer, and when FAKE_LINGER_PID is set it leaves a
// child holding stdout and stderr open and records the child's pid.
var fakeReviewerScripts = map[string]string{
	"codex": `#!/bin/sh
cat > /dev/null
last=""
while [ "$#" -gt 0 ]; do
	case "$1" in
		--output-last-message) last="$2"; shift 2 ;;
		*) shift ;;
	esac
done
printf '%s' "$FAKE_STDOUT"
printf '%s' "$FAKE_REVIEW" > "$last"
if [ -n "${FAKE_LINGER_PID:-}" ]; then
	sleep 60 &
	printf '%s' "$!" > "$FAKE_LINGER_PID"
fi
exit 0
`,
	"claude": `#!/bin/sh
cat > /dev/null
printf '%s' "$FAKE_REVIEW"
if [ -n "${FAKE_LINGER_PID:-}" ]; then
	sleep 60 &
	printf '%s' "$!" > "$FAKE_LINGER_PID"
fi
exit 0
`,
	"pi": `#!/bin/sh
printf '%s' "$FAKE_REVIEW"
if [ -n "${FAKE_LINGER_PID:-}" ]; then
	sleep 60 &
	printf '%s' "$!" > "$FAKE_LINGER_PID"
fi
exit 0
`,
}

// wrapAnswer puts a reviewer's answer text into the form the impl's binary
// emits it in.
func wrapAnswer(t *testing.T, impl string, answer string) string {
	t.Helper()
	switch impl {
	case "codex":
		return answer
	case "claude":
		raw, err := json.Marshal(map[string]any{"type": "result", "is_error": false, "result": answer})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	case "pi":
		end, err := json.Marshal(map[string]any{
			"type":    "message_end",
			"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": answer}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return `{"type":"message_start","message":{"role":"assistant"}}` + "\n" + string(end) + "\n"
	}
	t.Fatalf("unknown impl %q", impl)
	return ""
}

// a clean exit with malformed output fails the round, and every byte of what
// the reviewer produced is kept in _raw.txt.
func TestSubprocessMalformedOutputLandsInRaw(t *testing.T) {
	for _, impl := range []string{"codex", "claude", "pi"} {
		t.Run(impl, func(t *testing.T) {
			const malformed = "this answer holds no review object"
			h := newSubprocessHarness(t, impl)
			t.Setenv("FAKE_REVIEW", wrapAnswer(t, impl, malformed))
			t.Setenv("FAKE_STDOUT", "progress from "+impl)
			t.Setenv("FAKE_LINGER_PID", "")

			open, err := h.broker().OpenSession(context.Background(), OpenSessionRequest{})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			_, err = h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}})
			assertBrokerCode(t, err, CodeReviewerFailed)

			raw := readFile(t, filepath.Join(store.RoundDir(open.SessionDir, 1), store.RawFileName))
			if !strings.Contains(raw, malformed) {
				t.Fatalf("_raw.txt does not hold the malformed output:\n%s", raw)
			}
		})
	}
}

// a reviewer that prints a valid result and exits, leaving a child that holds
// its output pipes open, completes its round within the wait delay and
// releases the lock.
func TestSubprocessLingeringChildDoesNotHoldRound(t *testing.T) {
	previous := reviewer.WaitDelay
	reviewer.WaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { reviewer.WaitDelay = previous })

	for _, impl := range []string{"codex", "claude", "pi"} {
		t.Run(impl, func(t *testing.T) {
			h := newSubprocessHarness(t, impl)
			pidPath := filepath.Join(t.TempDir(), "linger.pid")
			t.Cleanup(func() { killRecordedChild(pidPath) })
			t.Setenv("FAKE_REVIEW", wrapAnswer(t, impl, string(validReviewOutput("ready_to_build"))))
			t.Setenv("FAKE_STDOUT", "")
			t.Setenv("FAKE_LINGER_PID", pidPath)

			open, err := h.broker().OpenSession(context.Background(), OpenSessionRequest{})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			started, err := h.broker().StartReviewRound(context.Background(), StartRoundRequest{
				SessionID: open.SessionID,
				Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
			})
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			h.waitDone(filepath.Dir(started.StatusPath))
			if _, err := h.broker().CollectRound(context.Background(), CollectRoundRequest{SessionID: open.SessionID, RoundNumber: 1}); err != nil {
				t.Fatalf("the round must complete: %v", err)
			}
			if _, err := os.Stat(pidPath); err != nil {
				t.Fatalf("the fake binary never left its child behind: %v", err)
			}
			assertLockFree(t, h.logDest)
		})
	}
}

func newSubprocessHarness(t *testing.T, impl string) *harness {
	t.Helper()
	binDir := t.TempDir()
	binaryPath := filepath.Join(binDir, impl)
	if err := os.WriteFile(binaryPath, []byte(fakeReviewerScripts[impl]), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", impl, err)
	}
	if impl == "codex" {
		codexHome := t.TempDir()
		for _, name := range []string{"auth.json", "config.toml"} {
			if err := os.WriteFile(filepath.Join(codexHome, name), []byte("{}"), 0o600); err != nil {
				t.Fatalf("write codex home: %v", err)
			}
		}
		t.Setenv("CODEX_HOME", codexHome)
	}

	h := newHarness(t, nil, "log_destination: reviews\nreviewer:\n  name: "+impl+"\n  impl: "+impl+"\n  binary_path: "+binaryPath+"\n  timeout: 30s\n")
	h.launcher = runner.NewInProcess(runner.BuildReviewer)
	t.Cleanup(h.launcher.Wait)
	return h
}

func killRecordedChild(pidPath string) {
	raw, err := os.ReadFile(pidPath)
	if errors.Is(err, os.ErrNotExist) || err != nil {
		return
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
