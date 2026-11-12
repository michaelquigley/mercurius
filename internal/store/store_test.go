package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidSessionID(t *testing.T) {
	for _, id := range []string{"s_abcdefABCD12"} {
		if !ValidSessionID(id) {
			t.Fatalf("%q should be valid", id)
		}
	}
	for _, id := range []string{"", "../x", "s_short", "s_abcdefABCD123", "s_abcdef/BCD12", "x_abcdefABCD12", "s_abcdefABCD1\n"} {
		if ValidSessionID(id) {
			t.Fatalf("%q should be rejected", id)
		}
	}
}

func TestDurationRoundTrip(t *testing.T) {
	raw, err := json.Marshal(RunReviewer{Name: "x", Impl: "dummy", Timeout: Duration(20 * time.Minute)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"timeout":"20m0s"`) {
		t.Fatalf("timeout not written as a duration string: %s", raw)
	}
	var back RunReviewer
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if time.Duration(back.Timeout) != 20*time.Minute {
		t.Fatalf("timeout = %s", time.Duration(back.Timeout))
	}
}

func TestNewStoredErrorCapsCauseAndDropsRaw(t *testing.T) {
	stored := NewStoredError(CodeReviewerFailed, "reviewer failed", strings.Repeat("x", MaxCauseBytes+10), map[string]any{"raw": "big", "reviewer": "r"}, time.Now())
	cause := stored.Details["cause"].(string)
	if len(cause) > MaxCauseBytes+64 || !strings.Contains(cause, "[cut: 10 more bytes not stored]") {
		t.Fatalf("cause not capped with a marker: %d bytes, tail %q", len(cause), cause[len(cause)-40:])
	}
	if _, ok := stored.Details["raw"]; ok {
		t.Fatal("raw must never be stored")
	}
	if stored.Details["reviewer"] != "r" {
		t.Fatalf("other details must be kept: %+v", stored.Details)
	}
}

func TestReadSessionDistinguishesMissingFromNotASession(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadSession(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir = %v", err)
	}
	if _, err := ReadSession(dir); !errors.Is(err, ErrNotSession) {
		t.Fatalf("dir without session.json = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, SessionFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	var parseErr *ParseError
	if _, err := ReadSession(dir); !errors.As(err, &parseErr) || parseErr.Path != filepath.Join(dir, SessionFileName) {
		t.Fatalf("corrupt session.json = %v", err)
	}
}

func TestRoundsSkipsLeftoverDirectories(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []int{1, 2, 10} {
		if err := os.Mkdir(RoundDir(dir, n), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteState(RoundDir(dir, 1), State{State: RoundCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := WriteState(RoundDir(dir, 10), State{State: RoundFailed}); err != nil {
		t.Fatal(err)
	}
	dirs, err := RoundDirs(dir)
	if err != nil || len(dirs) != 3 || dirs[2] != 10 {
		t.Fatalf("round dirs = %v err=%v", dirs, err)
	}
	rounds, err := Rounds(dir)
	if err != nil || len(rounds) != 2 || rounds[0] != 1 || rounds[1] != 10 {
		t.Fatalf("rounds = %v err=%v", rounds, err)
	}
}
