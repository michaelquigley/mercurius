package prompt

import (
	"strings"
	"testing"
)

func TestBuildIncludesRequiredSectionsInOrder(t *testing.T) {
	prompt, schema := Build(Request{
		Artifacts: []Artifact{
			{
				Name:         "design",
				SourcePath:   "/tmp/design.md",
				SnapshotPath: "/tmp/session/round-01/design",
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

func TestBuildRendersReviewContextAndFocus(t *testing.T) {
	prompt, _ := Build(Request{
		ReviewContext: "deployment: personal one-shot",
		ReviewFocus:   "flag ad-hoc logging.",
	})

	for _, want := range []string{
		"deployment: personal one-shot",
		"flag ad-hoc logging.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q", want)
		}
	}
}

func TestBuildRendersSettledDecisionsBlock(t *testing.T) {
	prompt, _ := Build(Request{
		SettledDecisions: []SettledDecision{
			{ID: "recall-deferred", DoNotFlag: "the absence of a 'recall' concept"},
			{ID: "obs-out-of-scope", DoNotFlag: "missing production-grade observability"},
		},
	})

	for _, want := range []string{
		"## Settled decisions (do not re-raise)",
		"decisions already made and out of scope",
		"the absence of a 'recall' concept",
		"missing production-grade observability",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q; got:\n%s", want, prompt)
		}
	}
	// the operator-side id is a handle for editing guards and must never reach
	// the reviewer.
	for _, id := range []string{"recall-deferred", "obs-out-of-scope"} {
		if strings.Contains(prompt, id) {
			t.Fatalf("settled-decision id %q must not be rendered into the prompt", id)
		}
	}
	// the block stands apart from calibration and ahead of what-to-flag.
	if strings.Index(prompt, "## Settled decisions") >= strings.Index(prompt, "## What to flag") {
		t.Fatal("settled-decisions block should render before the what-to-flag section")
	}
}

func TestBuildOmitsSettledDecisionsWhenEmpty(t *testing.T) {
	// no entries, and an entry whose do_not_flag is blank, both render nothing.
	for _, req := range []Request{
		{},
		{SettledDecisions: []SettledDecision{{ID: "blank", DoNotFlag: "   "}}},
	} {
		prompt, _ := Build(req)
		if strings.Contains(prompt, "## Settled decisions") {
			t.Fatalf("expected no settled-decisions block; got:\n%s", prompt)
		}
	}
}

func TestBuildCalibrationDropsLockedDecisions(t *testing.T) {
	prompt, _ := Build(Request{ReviewContext: "personal tool"})
	if strings.Contains(prompt, "locked decisions") {
		t.Fatal("calibration weighting sentence must no longer mention locked decisions; suppression-by-prior-decision is the settled-decisions block's job")
	}
}

func TestBuildOmitsPriorDecisionsSection(t *testing.T) {
	prompt, _ := Build(Request{})
	if strings.Contains(prompt, "Prior decisions") {
		t.Fatal("rounds are single-shot now; the prompt should not mention prior decisions")
	}
	if strings.Contains(prompt, "decisions log") {
		t.Fatal("rounds are single-shot now; the prompt should not mention a decisions log")
	}
}

func TestBuildIncludesCrossArrayUniquenessInstruction(t *testing.T) {
	prompt, _ := Build(Request{})
	want := "every id appearing in `concerns`, `questions`, or `advisory_notes` must be unique across all three arrays"
	if !strings.Contains(prompt, want) {
		t.Fatalf("expected uniqueness instruction in prompt; got:\n%s", prompt)
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
				SnapshotPath: "/tmp/session/round-01/design",
				Hash:         "sha256:abc",
				Content:      []byte("```\ninside\n```"),
			},
		},
	})

	if !strings.Contains(prompt, "````\n```\ninside\n```\n````") {
		t.Fatalf("expected four-backtick wrapper, got:\n%s", prompt)
	}
}
