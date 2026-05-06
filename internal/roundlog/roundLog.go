package roundlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	NotesBeginMarker = "<!-- mercurius:notes-begin -->"
	NotesEndMarker   = "<!-- mercurius:notes-end -->"
)

// ArtifactManifestEntry records one artifact snapshot in a round log.
type ArtifactManifestEntry struct {
	Name         string
	SourcePath   string
	SnapshotPath string
	Size         int64
	Hash         string
	Inline       bool
}

// ReviewerOutput records one reviewer's schema-valid round output.
type ReviewerOutput struct {
	Name       string
	Raw        json.RawMessage
	UsageNotes string
}

// Decision records one mutable human decision attached to a round.
type Decision struct {
	Ref         string
	Disposition string
	Note        string
}

// Entry contains the immutable content of one round log file.
type Entry struct {
	SessionID   string
	RoundNumber int
	OpenedAt    time.Time
	Verdict     string
	PromptPath  string
	Manifest    []ArtifactManifestEntry
	Reviewers   []ReviewerOutput
}

// WriteInitial writes a new immutable round log with placeholder notes.
func WriteInitial(path string, entry Entry) error {
	var b strings.Builder

	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("session_id: %s\n", entry.SessionID))
	b.WriteString(fmt.Sprintf("round_number: %d\n", entry.RoundNumber))
	b.WriteString(fmt.Sprintf("opened_at: %s\n", entry.OpenedAt.UTC().Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("verdict: %s\n", entry.Verdict))
	if entry.PromptPath != "" {
		b.WriteString(fmt.Sprintf("prompt_path: %s\n", entry.PromptPath))
	}
	b.WriteString("reviewers:\n")
	for _, reviewer := range entry.Reviewers {
		b.WriteString(fmt.Sprintf("  - %s\n", reviewer.Name))
	}
	b.WriteString("notes_recorded: false\n")
	b.WriteString("---\n\n")

	b.WriteString(fmt.Sprintf("# Round %02d\n\n", entry.RoundNumber))
	b.WriteString("## Artifact manifest\n\n")
	b.WriteString("| name | source_path | snapshot_path | size | hash |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, artifact := range entry.Manifest {
		sourcePath := artifact.SourcePath
		if artifact.Inline {
			sourcePath = "null"
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %d | %s |\n",
			escapeTableCell(artifact.Name),
			escapeTableCell(sourcePath),
			escapeTableCell(artifact.SnapshotPath),
			artifact.Size,
			escapeTableCell(artifact.Hash),
		))
	}
	b.WriteString("\n")

	b.WriteString("## Reviewer outputs\n\n")
	for _, reviewer := range entry.Reviewers {
		b.WriteString(fmt.Sprintf("### %s\n\n", reviewer.Name))
		b.WriteString(fmt.Sprintf("**Usage notes:** `%s`\n\n", escapeBackticks(reviewer.UsageNotes)))
		b.WriteString("```json\n")
		b.WriteString(prettyJSON(reviewer.Raw))
		b.WriteString("\n```\n\n")
	}

	b.WriteString(notesRegion("", nil))
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// UpdateNotes replaces the mutable notes region and flips notes_recorded.
func UpdateNotes(path string, commentary string, decisions []Decision) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(raw)

	if !strings.Contains(content, "notes_recorded: true") {
		if !strings.Contains(content, "notes_recorded: false") {
			return errors.New("round log missing notes_recorded frontmatter")
		}
		content = strings.Replace(content, "notes_recorded: false", "notes_recorded: true", 1)
	}

	begin := strings.Index(content, NotesBeginMarker)
	end := strings.Index(content, NotesEndMarker)
	if begin == -1 || end == -1 || end < begin {
		return errors.New("round log missing notes markers")
	}
	end += len(NotesEndMarker)

	content = content[:begin] + notesRegion(commentary, decisions) + content[end:]
	return os.WriteFile(path, []byte(content), 0o600)
}

func notesRegion(commentary string, decisions []Decision) string {
	var b strings.Builder
	b.WriteString(NotesBeginMarker)
	b.WriteString("\n\n")
	b.WriteString("## Commentary\n\n")
	if strings.TrimSpace(commentary) == "" {
		b.WriteString("_no commentary recorded yet_\n\n")
	} else {
		b.WriteString(strings.TrimRight(commentary, "\n"))
		b.WriteString("\n\n")
	}
	b.WriteString("## Decisions\n\n")
	if len(decisions) == 0 {
		b.WriteString("_no decisions recorded yet_\n\n")
	} else {
		for _, decision := range decisions {
			b.WriteString(fmt.Sprintf("- **%s** (ref: %s): %s.\n", decision.Disposition, decision.Ref, strings.TrimRight(decision.Note, ".")))
		}
		b.WriteString("\n")
	}
	b.WriteString(NotesEndMarker)
	b.WriteString("\n")
	return b.String()
}

func escapeTableCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", "<br>")
	return s
}

func escapeBackticks(s string) string {
	return strings.ReplaceAll(s, "`", "'")
}

func prettyJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		return string(raw)
	}
	return b.String()
}
