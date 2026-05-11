package roundlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// ArtifactManifestEntry records one artifact snapshot in a round log.
type ArtifactManifestEntry struct {
	Name         string
	SourcePath   string
	SnapshotPath string
	Size         int64
	Hash         string
}

// ReviewerOutput records one reviewer's schema-valid round output.
type ReviewerOutput struct {
	Name       string
	Raw        json.RawMessage
	UsageNotes string
}

// Decision records one human decision attached to a round.
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

// WriteInitial writes the immutable round log. There is no mutable region;
// commentary and decisions land in a sibling file via WriteNotes.
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
	b.WriteString("---\n\n")

	b.WriteString(fmt.Sprintf("# Round %02d\n\n", entry.RoundNumber))
	b.WriteString("## Artifact manifest\n\n")
	b.WriteString("| name | source_path | snapshot_path | size | hash |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, artifact := range entry.Manifest {
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %d | %s |\n",
			escapeTableCell(artifact.Name),
			escapeTableCell(artifact.SourcePath),
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
		b.WriteString("\n```\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// WriteNotes writes a sibling notes file containing commentary and decisions
// for a completed round. The original round log is untouched.
func WriteNotes(path string, commentary string, decisions []Decision) error {
	var b strings.Builder

	b.WriteString("# Notes\n\n")
	b.WriteString("## Commentary\n\n")
	if strings.TrimSpace(commentary) == "" {
		b.WriteString("_no commentary recorded_\n\n")
	} else {
		b.WriteString(strings.TrimRight(commentary, "\n"))
		b.WriteString("\n\n")
	}
	b.WriteString("## Decisions\n\n")
	if len(decisions) == 0 {
		b.WriteString("_no decisions recorded_\n")
	} else {
		for _, decision := range decisions {
			b.WriteString(fmt.Sprintf("- **%s** (ref: %s): %s.\n", decision.Disposition, decision.Ref, strings.TrimRight(decision.Note, ".")))
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
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
