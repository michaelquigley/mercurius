package prompt

import (
	"strings"
	"testing"

	"github.com/michaelquigley/mercurius/internal/reviewer"
)

func TestBuildIncludesRequiredSectionsInOrder(t *testing.T) {
	prompt, schema := Build(Request{
		Artifacts: []Artifact{
			{
				Name:         "design",
				SourcePath:   "/tmp/design.md",
				SnapshotPath: "/tmp/session/snapshots/round-01/design",
				Hash:         "sha256:abc",
				Content:      []byte("design content"),
			},
		},
	})
	if len(schema) == 0 {
		t.Fatal("expected schema")
	}

	sections := []string{
		"You are reviewing project artifacts",
		"## Review context",
		"## What to flag",
		"## Fix sizing",
		"## Project-specific focus",
		"In addition to the universal what-to-flag criteria above",
		"## Artifacts under review",
		"## Prior decisions",
		"## Verdict and severity",
		"## Output",
	}
	last := -1
	for _, section := range sections {
		index := strings.Index(prompt, section)
		if index == -1 {
			t.Fatalf("missing section %q", section)
		}
		if index <= last {
			t.Fatalf("section %q is out of order", section)
		}
		last = index
	}
	if !strings.Contains(prompt, "(no project-specific focus)") {
		t.Fatal("expected empty focus placeholder")
	}
}

func TestBuildRendersReviewFocusAndPriorDecisions(t *testing.T) {
	prompt, _ := Build(Request{
		ReviewContext: "deployment: personal one-shot",
		DecisionsLog:  "# session decisions log\n\n## round 2\n- C-1 (accepted): fix it.\n",
		ReviewFocus:   "flag ad-hoc logging.",
		PriorDecisions: []reviewer.PriorDecision{
			{RoundNumber: 2, Ref: "C-1", Disposition: "accepted", Note: "fix it."},
		},
	})

	for _, want := range []string{
		"deployment: personal one-shot",
		"Rendered decisions log:",
		"flag ad-hoc logging.",
		"- Round 2, C-1 (accepted): fix it.",
		"- C-1 (accepted): fix it.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q", want)
		}
	}
}

func TestBuildIncludesFindingBudget(t *testing.T) {
	prompt, schema := Build(Request{MaxFindings: 3})

	for _, want := range []string{
		"## Finding budget",
		"Return at most 3 total blocking findings",
		"`advisory_notes` are outside this budget",
		`"maxItems": 3`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q", want)
		}
	}
	if !strings.Contains(string(schema), `"maxItems":3`) {
		t.Fatalf("expected schema to contain maxItems, got %s", string(schema))
	}
}

func TestBuildUsesDynamicArtifactFences(t *testing.T) {
	prompt, _ := Build(Request{
		Artifacts: []Artifact{
			{
				Name:         "design",
				SourcePath:   "/tmp/design.md",
				SnapshotPath: "/tmp/session/snapshots/round-01/design",
				Hash:         "sha256:abc",
				Content:      []byte("```\ninside\n```"),
			},
		},
	})

	if !strings.Contains(prompt, "````\n```\ninside\n```\n````") {
		t.Fatalf("expected four-backtick wrapper, got:\n%s", prompt)
	}
}

func TestBuildRendersInlineSource(t *testing.T) {
	prompt, _ := Build(Request{
		Artifacts: []Artifact{
			{
				Name:         "context",
				SnapshotPath: "/tmp/session/snapshots/round-01/context",
				Hash:         "sha256:def",
				Content:      []byte("inline content"),
				Inline:       true,
			},
		},
	})

	if !strings.Contains(prompt, "Source path: inline") {
		t.Fatal("expected inline source path")
	}
}
