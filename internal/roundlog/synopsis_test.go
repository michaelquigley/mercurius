package roundlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelquigley/mercurius/internal/schema"
)

func TestWriteSynopsisRendersAllSections(t *testing.T) {
	suggestion := "clarify the work order"
	advisorySuggestion := "trim the second paragraph"

	entry := SynopsisEntry{
		SessionID: "s_synopsis_test",
		OpenedAt:  time.Date(2026, 5, 12, 17, 51, 9, 0, time.UTC),
		ClosedAt:  time.Date(2026, 5, 12, 18, 30, 0, 0, time.UTC),
		Rounds: []SynopsisRound{
			{
				Number:               1,
				OpenedAt:             time.Date(2026, 5, 12, 17, 51, 16, 0, time.UTC),
				State:                "completed",
				Reviewer:             SynopsisReviewer{Name: "codex", Impl: "codex", Model: "gpt-5.5"},
				ReviewContextPresent: true,
				ReviewFocusPresent:   true,
				LogPath:              "round-01/_round.md",
				NotesPath:            "round-01/_notes.md",
				HasNotes:             true,
				Verdict:              "needs_changes",
				ReviewerSummary:      "the artifacts are close but not yet ship-ready.",
				Concerns: []schema.Concern{{
					ID:         "C-1",
					Severity:   "major",
					Location:   "work-order:M3",
					Claim:      "missing detail",
					Rationale:  "implementation would diverge",
					Suggestion: &suggestion,
				}},
				Questions: []schema.Question{{
					ID:          "Q-1",
					Topic:       "budget",
					WhyItBlocks: "cannot judge loop length",
				}},
				AdvisoryNotes: []schema.AdvisoryNote{{
					ID:         "A-1",
					Location:   "docs/design.md",
					Note:       "shorten one paragraph",
					Rationale:  "easier to scan",
					Suggestion: &advisorySuggestion,
				}},
				Decisions: []Decision{
					{Ref: "C-1", Disposition: "fixed", Note: "addressed in commit abc1234"},
					{Ref: "Q-1", Disposition: "deferred", Note: "revisit after v1.1 cut"},
				},
				Commentary: "the reviewer's framing lined up with what we already suspected.",
			},
			{
				Number:   2,
				OpenedAt: time.Date(2026, 5, 12, 18, 10, 0, 0, time.UTC),
				State:    "failed",
				Reviewer: SynopsisReviewer{Name: "codex", Impl: "codex", Model: "gpt-5.5"},
				Failure:  &SynopsisError{Code: "reviewer_failed", Message: "reviewer output failed schema validation"},
				RawPath:  "round-02/_raw.txt",
			},
			{
				Number:         3,
				OpenedAt:       time.Date(2026, 5, 12, 18, 25, 11, 0, time.UTC),
				State:          "completed",
				Reviewer:       SynopsisReviewer{Name: "codex", Impl: "codex", Model: "gpt-5.5"},
				OutlivedServer: true,
				LogPath:        "round-03/_round.md",
				Verdict:        "ready_to_build",
			},
		},
	}

	path := filepath.Join(t.TempDir(), "_synopsis.md")
	if err := WriteSynopsis(path, entry); err != nil {
		t.Fatalf("write synopsis: %v", err)
	}
	content := readFile(t, path)

	assertFrontmatter(t, frontmatterOf(t, content), map[string]string{
		"session_id":            "s_synopsis_test",
		"opened_at":             "2026-05-12T17:51:09Z",
		"closed_at":             "2026-05-12T18:30:00Z",
		"round_count":           "3",
		"failed_round_count":    "1",
		"outlived_server_count": "1",
	})
	for _, want := range []string{
		"- Outlived its server: yes",
		"# Session synopsis\n",
		"## Summary\n",
		"Closed 3 rounds (1 failed) with reviewer 'codex'.",
		"Latest verdict: 'ready_to_build'.",
		"Decisions across all rounds: 1 fixed / 1 deferred.",
		"1 advisory note recorded across the arc.",
		"## Round outcomes\n",
		"| round | opened_at | state | reviewer | verdict | concerns | questions | advisory | decisions | notes |",
		"| 01 | 2026-05-12T17:51:16Z | completed | codex | needs_changes | 1 | 1 | 1 | 1 fixed / 1 deferred | yes |",
		"| 02 | 2026-05-12T18:10:00Z | failed | codex | - | - | - | - | - | - |",
		"| 03 | 2026-05-12T18:25:11Z | completed | codex | ready_to_build | 0 | 0 | 0 | - | - |",
		"## Round detail\n",
		"### Round 01\n",
		"- Reviewer: 'codex' (impl 'codex', model 'gpt-5.5')\n",
		"- Calibration: review_context present, review_focus present\n",
		"### Round 02\n",
		"- State: failed\n",
		"- Failure: reviewer_failed - reviewer output failed schema validation\n",
		"- Raw output: round-02/_raw.txt\n",
		"### Round 03\n",
		"- Calibration: review_context absent, review_focus absent\n",
		"**Reviewer summary:** the artifacts are close but not yet ship-ready",
		"**Concerns**",
		"- **C-1** (major, 'work-order:M3'): missing detail — implementation would diverge — _suggestion_: clarify the work order",
		"**Questions**",
		"- **Q-1** ('budget'): cannot judge loop length",
		"**Advisory notes**",
		"- **A-1** ('docs/design.md'): shorten one paragraph — easier to scan — _suggestion_: trim the second paragraph",
		"**Decisions**",
		"- **fixed** (C-1): addressed in commit abc1234.",
		"- **deferred** (Q-1): revisit after v1.1 cut.",
		"**Commentary**",
		"> the reviewer's framing lined up with what we already suspected.",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("synopsis missing %q\n---\n%s", want, content)
		}
	}

	// the later rounds had no findings, decisions, or commentary - their detail
	// blocks must not include those headings.
	later := content[strings.Index(content, "### Round 02"):]
	for _, unwanted := range []string{"**Concerns**", "**Questions**", "**Advisory notes**", "**Decisions**", "**Commentary**"} {
		if strings.Contains(later, unwanted) {
			t.Errorf("rounds 02 and 03 detail must not contain %q\n---\n%s", unwanted, later)
		}
	}
	// there is no session-level reviewer or calibration any more.
	for _, unwanted := range []string{"reviewer:\n", "review_context_present:", "last_error:"} {
		if strings.Contains(content, unwanted) {
			t.Errorf("synopsis frontmatter must not contain %q\n---\n%s", unwanted, content)
		}
	}

	// the synopsis is its own rendering - it must not copy the _notes.md
	// "no decisions recorded" placeholder.
	if strings.Contains(content, "_no decisions recorded_") {
		t.Errorf("synopsis must not include notes-file placeholder; content:\n%s", content)
	}
}

func TestWriteSynopsisEmptyRounds(t *testing.T) {
	entry := SynopsisEntry{
		SessionID: "s_empty",
		OpenedAt:  time.Date(2026, 5, 12, 17, 0, 0, 0, time.UTC),
		ClosedAt:  time.Date(2026, 5, 12, 17, 5, 0, 0, time.UTC),
	}

	path := filepath.Join(t.TempDir(), "_synopsis.md")
	if err := WriteSynopsis(path, entry); err != nil {
		t.Fatalf("write synopsis: %v", err)
	}
	content := readFile(t, path)

	// the counts are always written, zero included.
	assertFrontmatter(t, frontmatterOf(t, content), map[string]string{
		"round_count":           "0",
		"failed_round_count":    "0",
		"outlived_server_count": "0",
	})
	if !strings.Contains(content, "Session closed with no rounds.") {
		t.Errorf("expected empty-rounds summary; got:\n%s", content)
	}
	for _, unwanted := range []string{"## Round outcomes", "## Round detail", "| round |"} {
		if strings.Contains(content, unwanted) {
			t.Errorf("empty-rounds synopsis must not contain %q\n---\n%s", unwanted, content)
		}
	}
}

func TestWriteSynopsisUnparseableRound(t *testing.T) {
	entry := SynopsisEntry{
		SessionID: "s_unparseable",
		OpenedAt:  time.Date(2026, 5, 12, 17, 0, 0, 0, time.UTC),
		ClosedAt:  time.Date(2026, 5, 12, 17, 10, 0, 0, time.UTC),
		Rounds: []SynopsisRound{{
			Number:      1,
			OpenedAt:    time.Date(2026, 5, 12, 17, 1, 0, 0, time.UTC),
			State:       "completed",
			Reviewer:    SynopsisReviewer{Name: "codex"},
			LogPath:     "round-01/_round.md",
			Verdict:     "needs_changes",
			Unparseable: true,
		}},
	}

	path := filepath.Join(t.TempDir(), "_synopsis.md")
	if err := WriteSynopsis(path, entry); err != nil {
		t.Fatalf("write synopsis: %v", err)
	}
	content := readFile(t, path)

	if !strings.Contains(content, "_reviewer output unparseable; see round-01/_review.json for raw JSON_") {
		t.Errorf("expected unparseable fallback; got:\n%s", content)
	}
	// the row in the outcomes table should still appear, with dashes for the
	// per-finding counts since the parse failed.
	if !strings.Contains(content, "| 01 | 2026-05-12T17:01:00Z | completed | codex | needs_changes | - | - | - | - | - |") {
		t.Errorf("expected outcomes row with dashes for unparseable round; got:\n%s", content)
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
