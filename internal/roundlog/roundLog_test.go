package roundlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteInitialAndWriteNotes(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "_round.md")
	entry := Entry{
		SessionID:   "s_test",
		RoundNumber: 1,
		OpenedAt:    time.Date(2026, 5, 5, 19, 38, 10, 0, time.UTC),
		Verdict:     "ready_to_build",
		PromptPath:  "_prompt.md",
		Manifest: []ArtifactManifestEntry{
			{Name: "design", SourcePath: "/tmp/design.md", SnapshotPath: filepath.Join(dir, "design"), Size: 10, Hash: "sha256:abc"},
		},
		Reviewers: []ReviewerOutput{
			{Name: "dummy", Raw: json.RawMessage(`{"verdict":"ready_to_build","summary":"ok","concerns":[],"questions":[],"advisory_notes":[]}`), UsageNotes: "dummy"},
		},
	}

	if err := WriteInitial(logPath, entry); err != nil {
		t.Fatalf("write initial: %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	content := string(raw)
	for _, want := range []string{
		"session_id: s_test",
		"round_number: 1",
		"verdict: ready_to_build",
		"prompt_path: _prompt.md",
		"- dummy",
		"## Artifact manifest",
		"| design |",
		"## Reviewer outputs",
		"### dummy",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("log missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "notes_recorded") {
		t.Fatalf("log should not carry notes_recorded frontmatter: %s", content)
	}

	notesPath := filepath.Join(dir, "_notes.md")
	if err := WriteNotes(notesPath, "commentary text", []Decision{
		{Ref: "C-1", Disposition: "fixed", Note: "fix landed"},
	}); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	notes, err := os.ReadFile(notesPath)
	if err != nil {
		t.Fatalf("read notes: %v", err)
	}
	notesContent := string(notes)
	for _, want := range []string{
		"# Notes",
		"## Commentary",
		"commentary text",
		"## Decisions",
		"- **fixed** (ref: C-1): fix landed.",
	} {
		if !strings.Contains(notesContent, want) {
			t.Fatalf("notes missing %q:\n%s", want, notesContent)
		}
	}
}

func TestWriteNotesEmptyDecisions(t *testing.T) {
	notesPath := filepath.Join(t.TempDir(), "_notes.md")
	if err := WriteNotes(notesPath, "just commentary", nil); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	raw, err := os.ReadFile(notesPath)
	if err != nil {
		t.Fatalf("read notes: %v", err)
	}
	if !strings.Contains(string(raw), "_no decisions recorded_") {
		t.Fatalf("expected empty-decisions placeholder:\n%s", string(raw))
	}
}
