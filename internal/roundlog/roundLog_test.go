package roundlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteInitialAndUpdateNotes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "round-01.md")
	entry := Entry{
		SessionID:   "s_test",
		RoundNumber: 1,
		OpenedAt:    time.Date(2026, 5, 4, 18, 32, 14, 0, time.UTC),
		Verdict:     "ready_to_build",
		PromptPath:  "snapshots/round-01/_prompt.md",
		Manifest: []ArtifactManifestEntry{
			{Name: "design", SourcePath: "/tmp/design.md", SnapshotPath: "/tmp/session/snapshots/round-01/design", Size: 10, Hash: "sha256:abc"},
			{Name: "context", SnapshotPath: "/tmp/session/snapshots/round-01/context", Size: 6, Hash: "sha256:def", Inline: true},
		},
		Reviewers: []ReviewerOutput{
			{Name: "dummy", Raw: json.RawMessage(`{"verdict":"ready_to_build","summary":"ok","concerns":[],"questions":[],"advisory_notes":[],"proposed_diffs":[]}`), UsageNotes: "dummy"},
		},
	}

	if err := WriteInitial(path, entry); err != nil {
		t.Fatalf("write initial: %v", err)
	}
	content := readLog(t, path)
	for _, want := range []string{
		"session_id: s_test",
		"round_number: 1",
		"prompt_path: snapshots/round-01/_prompt.md",
		"notes_recorded: false",
		"| context | null |",
		"### dummy",
		NotesBeginMarker,
		NotesEndMarker,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected log to contain %q\n%s", want, content)
		}
	}

	if err := UpdateNotes(path, "integrated feedback", []Decision{{Ref: "C-1", Disposition: "accepted", Note: "fixed."}}); err != nil {
		t.Fatalf("update notes: %v", err)
	}
	updated := readLog(t, path)
	for _, want := range []string{
		"notes_recorded: true",
		"integrated feedback",
		"- **accepted** (ref: C-1): fixed.",
	} {
		if !strings.Contains(updated, want) {
			t.Fatalf("expected updated log to contain %q\n%s", want, updated)
		}
	}
	if strings.Count(updated, NotesBeginMarker) != 1 || strings.Count(updated, NotesEndMarker) != 1 {
		t.Fatal("expected exactly one notes region")
	}

	if err := UpdateNotes(path, "", nil); err != nil {
		t.Fatalf("replace notes: %v", err)
	}
	replaced := readLog(t, path)
	if strings.Contains(replaced, "integrated feedback") || strings.Contains(replaced, "accepted") {
		t.Fatal("expected previous notes to be replaced")
	}
	if !strings.Contains(replaced, "_no commentary recorded yet_") || !strings.Contains(replaced, "_no decisions recorded yet_") {
		t.Fatal("expected placeholders after replacement")
	}
}

func readLog(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return string(raw)
}
