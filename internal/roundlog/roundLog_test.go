package roundlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelquigley/df/dd"
)

// frontmatterOf returns the yaml block that opens a document, decoded, so a
// test asserts the values and not the layout they happen to be written in.
func frontmatterOf(t *testing.T, content string) map[string]any {
	t.Helper()
	parts := strings.SplitN(content, "---\n", 3)
	if len(parts) != 3 || parts[0] != "" {
		t.Fatalf("document does not open with a frontmatter block:\n%s", content)
	}
	values, err := dd.DecodeStrictYAML([]byte(parts[1]))
	if err != nil {
		t.Fatalf("frontmatter is not valid yaml: %v\n%s", err, parts[1])
	}
	return values
}

// assertFrontmatter compares decoded frontmatter values by their printed form.
func assertFrontmatter(t *testing.T, values map[string]any, want map[string]string) {
	t.Helper()
	for key, wantValue := range want {
		got, ok := values[key]
		if !ok {
			t.Errorf("frontmatter has no '%s'; got %v", key, values)
			continue
		}
		if fmt.Sprint(got) != wantValue {
			t.Errorf("frontmatter '%s' = %v, want %s", key, got, wantValue)
		}
	}
}

// a reviewer's name is free-form text from config. written into the
// frontmatter by hand, a name with yaml punctuation in it changes what the
// block means; through dd it comes back as the name it was.
func TestRoundLogFrontmatterSurvivesYAMLPunctuation(t *testing.T) {
	names := []string{"codex: fast", "#1 reviewer", "[draft]", "plain"}
	entry := Entry{
		SessionID:   "s_test",
		RoundNumber: 1,
		OpenedAt:    time.Date(2026, 5, 5, 19, 38, 10, 0, time.UTC),
		Verdict:     "ready_to_build",
		PromptPath:  "_prompt.md",
	}
	for _, name := range names {
		entry.Reviewers = append(entry.Reviewers, ReviewerOutput{Name: name, Raw: json.RawMessage(`{}`)})
	}
	logPath := filepath.Join(t.TempDir(), "_round.md")
	if err := WriteInitial(logPath, entry); err != nil {
		t.Fatalf("write initial: %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	values := frontmatterOf(t, string(raw))
	got, ok := values["reviewers"].([]any)
	if !ok || len(got) != len(names) {
		t.Fatalf("frontmatter reviewers = %#v, want %d names", values["reviewers"], len(names))
	}
	for i, name := range names {
		if got[i] != name {
			t.Errorf("frontmatter reviewer %d = %#v, want %q", i, got[i], name)
		}
	}
	assertFrontmatter(t, values, map[string]string{
		"session_id":   "s_test",
		"round_number": "1",
		"opened_at":    "2026-05-05T19:38:10Z",
		"verdict":      "ready_to_build",
		"prompt_path":  "_prompt.md",
	})
}

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
