package roundlog

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/michaelquigley/mercurius/internal/schema"
)

// SynopsisEntry is the shape WriteSynopsis renders into a session-level
// _synopsis.md. The broker assembles one from the session directory at
// close_session time. There is no session-level reviewer: the reviewer and
// calibration are per round.
type SynopsisEntry struct {
	SessionID string
	OpenedAt  time.Time
	ClosedAt  time.Time
	Rounds    []SynopsisRound
}

// SynopsisReviewer describes the reviewer one round ran with.
type SynopsisReviewer struct {
	Name  string
	Impl  string
	Model string
}

// SynopsisError captures a failed round's stored error.
type SynopsisError struct {
	Code    string
	Message string
}

// SynopsisRound is the per-round material rendered in the outcomes table and
// the detail section. A failed round carries Failure and gets a short detail
// block. When Unparseable is true on a completed round the writer falls back to
// a stable placeholder pointing at the round's _review.json; concerns,
// questions, advisory notes, decisions, and commentary are ignored in that case.
type SynopsisRound struct {
	Number               int
	OpenedAt             time.Time
	State                string
	Reviewer             SynopsisReviewer
	ReviewContextPresent bool
	ReviewFocusPresent   bool
	Failure              *SynopsisError
	RawPath              string
	LogPath              string
	NotesPath            string
	HasNotes             bool
	Verdict              string
	ReviewerSummary      string
	Unparseable          bool
	Concerns             []schema.Concern
	Questions            []schema.Question
	AdvisoryNotes        []schema.AdvisoryNote
	Decisions            []Decision
	Commentary           string
}

const (
	synopsisStateCompleted = "completed"
	synopsisStateFailed    = "failed"
)

// WriteSynopsis writes a session-level synopsis markdown file. It is meant to
// be the single durable artifact a reader (human or future agent) opens to
// understand the arc of a closed session, failed rounds included.
func WriteSynopsis(path string, entry SynopsisEntry) error {
	var b strings.Builder

	writeSynopsisFrontmatter(&b, entry)
	b.WriteString("# Session synopsis\n\n")
	writeSynopsisSummary(&b, entry)
	writeSynopsisRoundOutcomes(&b, entry)
	writeSynopsisRoundDetail(&b, entry)

	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func writeSynopsisFrontmatter(b *strings.Builder, entry SynopsisEntry) {
	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("session_id: %s\n", entry.SessionID))
	b.WriteString(fmt.Sprintf("opened_at: %s\n", entry.OpenedAt.UTC().Format(time.RFC3339)))
	if !entry.ClosedAt.IsZero() {
		b.WriteString(fmt.Sprintf("closed_at: %s\n", entry.ClosedAt.UTC().Format(time.RFC3339)))
	}
	b.WriteString(fmt.Sprintf("round_count: %d\n", len(entry.Rounds)))
	if failed := countFailed(entry.Rounds); failed > 0 {
		b.WriteString(fmt.Sprintf("failed_round_count: %d\n", failed))
	}
	b.WriteString("---\n\n")
}

func writeSynopsisSummary(b *strings.Builder, entry SynopsisEntry) {
	b.WriteString("## Summary\n\n")
	if len(entry.Rounds) == 0 {
		b.WriteString("Session closed with no rounds.\n\n")
		return
	}

	latestVerdict := "unknown"
	for _, round := range entry.Rounds {
		if round.State == synopsisStateCompleted && round.Verdict != "" {
			latestVerdict = round.Verdict
		}
	}

	totals := aggregateDispositions(entry.Rounds)
	advisoryCount := 0
	for _, round := range entry.Rounds {
		if !round.Unparseable {
			advisoryCount += len(round.AdvisoryNotes)
		}
	}

	b.WriteString(fmt.Sprintf("Closed %s", pluralRounds(len(entry.Rounds))))
	if failed := countFailed(entry.Rounds); failed > 0 {
		b.WriteString(fmt.Sprintf(" (%d failed)", failed))
	}
	if names := reviewerNames(entry.Rounds); len(names) == 1 {
		b.WriteString(fmt.Sprintf(" with reviewer '%s'", names[0]))
	} else if len(names) > 1 {
		b.WriteString(fmt.Sprintf(" with reviewers '%s'", strings.Join(names, "', '")))
	}
	b.WriteString(fmt.Sprintf(". Latest verdict: '%s'.", latestVerdict))
	if totals.total() > 0 {
		b.WriteString(fmt.Sprintf(" Decisions across all rounds: %s.", summarizeDispositions(totals)))
	}
	if advisoryCount > 0 {
		b.WriteString(fmt.Sprintf(" %s recorded across the arc.", pluralAdvisory(advisoryCount)))
	}
	b.WriteString("\n\n")
}

func writeSynopsisRoundOutcomes(b *strings.Builder, entry SynopsisEntry) {
	if len(entry.Rounds) == 0 {
		return
	}
	b.WriteString("## Round outcomes\n\n")
	b.WriteString("| round | opened_at | state | reviewer | verdict | concerns | questions | advisory | decisions | notes |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, round := range entry.Rounds {
		concerns, questions, advisory := "-", "-", "-"
		if round.State == synopsisStateCompleted && !round.Unparseable {
			concerns = fmt.Sprintf("%d", len(round.Concerns))
			questions = fmt.Sprintf("%d", len(round.Questions))
			advisory = fmt.Sprintf("%d", len(round.AdvisoryNotes))
		}
		decisions := "-"
		if len(round.Decisions) > 0 {
			decisions = summarizeDispositions(countDispositions(round.Decisions))
		}
		notes := "-"
		if round.HasNotes {
			notes = "yes"
		}
		verdict := round.Verdict
		if verdict == "" {
			verdict = "-"
		}
		b.WriteString(fmt.Sprintf("| %02d | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			round.Number,
			round.OpenedAt.UTC().Format(time.RFC3339),
			escapeTableCell(emptyDash(round.State)),
			escapeTableCell(emptyDash(round.Reviewer.Name)),
			escapeTableCell(verdict),
			concerns,
			questions,
			advisory,
			escapeTableCell(decisions),
			notes,
		))
	}
	b.WriteString("\n")
}

func writeSynopsisRoundDetail(b *strings.Builder, entry SynopsisEntry) {
	if len(entry.Rounds) == 0 {
		return
	}
	b.WriteString("## Round detail\n\n")
	for _, round := range entry.Rounds {
		writeSynopsisRound(b, round)
	}
}

func writeSynopsisRound(b *strings.Builder, round SynopsisRound) {
	b.WriteString(fmt.Sprintf("### Round %02d\n\n", round.Number))
	b.WriteString(fmt.Sprintf("- Opened: %s\n", round.OpenedAt.UTC().Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("- State: %s\n", emptyDash(round.State)))
	b.WriteString(fmt.Sprintf("- Reviewer: %s\n", formatSynopsisReviewer(round.Reviewer)))
	b.WriteString(fmt.Sprintf("- Calibration: review_context %s, review_focus %s\n", presence(round.ReviewContextPresent), presence(round.ReviewFocusPresent)))
	if round.Verdict != "" {
		b.WriteString(fmt.Sprintf("- Verdict: %s\n", round.Verdict))
	}
	if round.Failure != nil {
		b.WriteString(fmt.Sprintf("- Failure: %s - %s\n", round.Failure.Code, round.Failure.Message))
	}
	if round.RawPath != "" {
		b.WriteString(fmt.Sprintf("- Raw output: %s\n", round.RawPath))
	}
	if round.LogPath != "" {
		b.WriteString(fmt.Sprintf("- Log: %s\n", round.LogPath))
	}
	if round.HasNotes && round.NotesPath != "" {
		b.WriteString(fmt.Sprintf("- Notes: %s\n", round.NotesPath))
	}
	b.WriteString("\n")

	if round.State != synopsisStateCompleted {
		return
	}
	if round.Unparseable {
		b.WriteString(fmt.Sprintf("_reviewer output unparseable; see round-%02d/_review.json for raw JSON_\n\n", round.Number))
		return
	}

	if strings.TrimSpace(round.ReviewerSummary) != "" {
		b.WriteString("**Reviewer summary:** ")
		b.WriteString(strings.TrimSpace(round.ReviewerSummary))
		b.WriteString("\n\n")
	}
	if len(round.Concerns) > 0 {
		b.WriteString("**Concerns**\n\n")
		for _, concern := range round.Concerns {
			b.WriteString(formatConcernBullet(concern))
		}
		b.WriteString("\n")
	}
	if len(round.Questions) > 0 {
		b.WriteString("**Questions**\n\n")
		for _, question := range round.Questions {
			b.WriteString(formatQuestionBullet(question))
		}
		b.WriteString("\n")
	}
	if len(round.AdvisoryNotes) > 0 {
		b.WriteString("**Advisory notes**\n\n")
		for _, note := range round.AdvisoryNotes {
			b.WriteString(formatAdvisoryBullet(note))
		}
		b.WriteString("\n")
	}
	if len(round.Decisions) > 0 {
		b.WriteString("**Decisions**\n\n")
		for _, decision := range round.Decisions {
			b.WriteString(formatDecisionBullet(decision))
		}
		b.WriteString("\n")
	}
	if strings.TrimSpace(round.Commentary) != "" {
		b.WriteString("**Commentary**\n\n")
		for _, line := range strings.Split(strings.TrimRight(round.Commentary, "\n"), "\n") {
			b.WriteString("> ")
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
}

func formatConcernBullet(concern schema.Concern) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("- **%s** (%s, '%s'): %s",
		concern.ID,
		emptyDash(concern.Severity),
		concern.Location,
		strings.TrimRight(concern.Claim, "."),
	))
	if rationale := strings.TrimSpace(concern.Rationale); rationale != "" {
		b.WriteString(" — ")
		b.WriteString(strings.TrimRight(rationale, "."))
	}
	if concern.Suggestion != nil {
		if suggestion := strings.TrimSpace(*concern.Suggestion); suggestion != "" {
			b.WriteString(" — _suggestion_: ")
			b.WriteString(strings.TrimRight(suggestion, "."))
		}
	}
	b.WriteString("\n")
	return b.String()
}

func formatQuestionBullet(question schema.Question) string {
	return fmt.Sprintf("- **%s** ('%s'): %s\n",
		question.ID,
		question.Topic,
		strings.TrimRight(question.WhyItBlocks, "."),
	)
}

func formatAdvisoryBullet(note schema.AdvisoryNote) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("- **%s** ('%s'): %s",
		note.ID,
		note.Location,
		strings.TrimRight(note.Note, "."),
	))
	if rationale := strings.TrimSpace(note.Rationale); rationale != "" {
		b.WriteString(" — ")
		b.WriteString(strings.TrimRight(rationale, "."))
	}
	if note.Suggestion != nil {
		if suggestion := strings.TrimSpace(*note.Suggestion); suggestion != "" {
			b.WriteString(" — _suggestion_: ")
			b.WriteString(strings.TrimRight(suggestion, "."))
		}
	}
	b.WriteString("\n")
	return b.String()
}

func formatDecisionBullet(decision Decision) string {
	if strings.TrimSpace(decision.Note) == "" {
		return fmt.Sprintf("- **%s** (%s)\n", decision.Disposition, decision.Ref)
	}
	return fmt.Sprintf("- **%s** (%s): %s.\n",
		decision.Disposition,
		decision.Ref,
		strings.TrimRight(decision.Note, "."),
	)
}

// dispositionCounts tallies the three allowed Decision dispositions.
type dispositionCounts struct {
	fixed    int
	deferred int
	rejected int
}

func (c dispositionCounts) total() int { return c.fixed + c.deferred + c.rejected }

func countDispositions(decisions []Decision) dispositionCounts {
	var c dispositionCounts
	for _, decision := range decisions {
		switch decision.Disposition {
		case "fixed":
			c.fixed++
		case "deferred":
			c.deferred++
		case "rejected":
			c.rejected++
		}
	}
	return c
}

func aggregateDispositions(rounds []SynopsisRound) dispositionCounts {
	var c dispositionCounts
	for _, round := range rounds {
		rc := countDispositions(round.Decisions)
		c.fixed += rc.fixed
		c.deferred += rc.deferred
		c.rejected += rc.rejected
	}
	return c
}

// summarizeDispositions returns "X fixed / Y deferred / Z rejected", skipping
// zero-count categories. Returns "0" when all counts are zero (the caller
// usually checks total() first).
func summarizeDispositions(c dispositionCounts) string {
	parts := make([]string, 0, 3)
	if c.fixed > 0 {
		parts = append(parts, fmt.Sprintf("%d fixed", c.fixed))
	}
	if c.deferred > 0 {
		parts = append(parts, fmt.Sprintf("%d deferred", c.deferred))
	}
	if c.rejected > 0 {
		parts = append(parts, fmt.Sprintf("%d rejected", c.rejected))
	}
	if len(parts) == 0 {
		return "0"
	}
	return strings.Join(parts, " / ")
}

func pluralRounds(n int) string {
	if n == 1 {
		return "1 round"
	}
	return fmt.Sprintf("%d rounds", n)
}

func pluralAdvisory(n int) string {
	if n == 1 {
		return "1 advisory note"
	}
	return fmt.Sprintf("%d advisory notes", n)
}

func countFailed(rounds []SynopsisRound) int {
	n := 0
	for _, round := range rounds {
		if round.State == synopsisStateFailed {
			n++
		}
	}
	return n
}

// reviewerNames returns the distinct reviewer names across the rounds, in the
// order they first ran.
func reviewerNames(rounds []SynopsisRound) []string {
	var names []string
	for _, round := range rounds {
		if round.Reviewer.Name == "" || slices.Contains(names, round.Reviewer.Name) {
			continue
		}
		names = append(names, round.Reviewer.Name)
	}
	return names
}

func formatSynopsisReviewer(r SynopsisReviewer) string {
	if r.Name == "" {
		return "-"
	}
	parts := []string{}
	if r.Impl != "" {
		parts = append(parts, fmt.Sprintf("impl '%s'", r.Impl))
	}
	if r.Model != "" {
		parts = append(parts, fmt.Sprintf("model '%s'", r.Model))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("'%s'", r.Name)
	}
	return fmt.Sprintf("'%s' (%s)", r.Name, strings.Join(parts, ", "))
}

func presence(present bool) string {
	if present {
		return "present"
	}
	return "absent"
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
