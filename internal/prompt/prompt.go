package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/michaelquigley/mercurius/internal/schema"
)

// Artifact is a snapshotted artifact rendered into the review prompt.
type Artifact struct {
	Name         string
	SourcePath   string
	SnapshotPath string
	Hash         string
	Content      []byte
}

// SettledDecision is a guard rendered into the review prompt: a decision already
// made that the reviewer must not re-raise. Only DoNotFlag is rendered; the
// operator-side id never reaches the reviewer.
type SettledDecision struct {
	ID        string
	DoNotFlag string
}

// Request contains runtime values for the standard review prompt.
type Request struct {
	Artifacts        []Artifact
	ReviewContext    string
	ReviewFocus      string
	SettledDecisions []SettledDecision
	MaxFindings      int
}

// Build assembles the standard review prompt and schema payload.
func Build(req Request) (string, json.RawMessage) {
	var b strings.Builder

	b.WriteString("You are reviewing project artifacts before implementation begins. Your job is to decide whether the artifacts are ready to ship or build under the stated constraints. You are not the implementer; you are the reviewer.\n\n")
	b.WriteString("Answer three questions: Would you ship/build this under the stated constraints? If not, what specifically blocks readiness? Separately, what polish opportunities are advisory only and should not block readiness?\n\n")
	b.WriteString("## Review context\n\n")
	if strings.TrimSpace(req.ReviewContext) == "" {
		b.WriteString("(no review context provided)\n\n")
	} else {
		b.WriteString(strings.TrimRight(strings.TrimSpace(req.ReviewContext), "\n"))
		b.WriteString("\n\n")
	}
	b.WriteString("Weight every finding against this review context. Suppress concerns that do not realistically apply under the stated deployment constraints, implementation model, or out-of-scope boundaries. When a point is useful but not readiness-blocking, put it in `advisory_notes`, not `concerns` or `questions`.\n\n")

	if block := settledDecisionsBlock(req.SettledDecisions); block != "" {
		b.WriteString(block)
	}

	b.WriteString("## What to flag\n\n")
	b.WriteString("You are looking for SUBTLE issues — things an implementer (LLM or human) would not catch on their own through normal implementation friction. The implementer will discover obvious problems through compile errors, runtime errors, failing tests, and fail-fast preconditions. Your value is finding what those normal feedback loops will miss.\n\n")
	b.WriteString("Flag an issue if it would:\n")
	b.WriteString("- silently produce wrong numbers, wrong attribution, or wrong visibility\n")
	b.WriteString("- let two implementers land on incompatible interpretations of the same spec without either hitting a clear error\n")
	b.WriteString("- violate a non-trivial invariant that is not obvious from the surrounding schema, code, or prose\n")
	b.WriteString("- defer work in a way that stays silently absent until a specific deployment moment fails\n")
	b.WriteString("- claim an affordance that is not actually specified, such that the implementer would have to invent the missing detail\n\n")
	b.WriteString("Do NOT flag an issue if any of these are true:\n")
	b.WriteString("- it would cause a compile error, codegen failure, or type mismatch\n")
	b.WriteString("- it would fail with a clear, directed runtime error during testing\n")
	b.WriteString("- a fail-fast precondition or assertion already documented in the spec catches it\n")
	b.WriteString("- it is a stale reference that other prose in the same doc contradicts\n")
	b.WriteString("- it is a wording bug that does not change implementation behavior\n")
	b.WriteString("- it is a missing entry on a list whose absence would be immediately apparent when the implementer wires the feature\n\n")
	b.WriteString("Common locations for subtle issues:\n")
	b.WriteString("- Design: decisions described but not actually made (handwaves like \"decided at scaffold time\"), internal contradictions between sections, architectural ambiguity that two implementers would resolve differently.\n")
	b.WriteString("- Work order: scope items whose definition of done is not testable, dependencies between milestones that are not stated, concrete decisions deferred to implementation rather than settled, test coverage gaps for architectural commitments in the design.\n\n")
	b.WriteString("Prefer fewer, more material findings over comprehensive checklists. Any review can always find more to say. The question is whether saying it adds value over the implementer's own attention.\n\n")
	b.WriteString("Separate readiness blockers from polish. Use `concerns` or `questions` only for items that block shipping or building under the stated context; put non-blocking small improvements, missed opportunities, or downstream considerations in `advisory_notes`. Emitting fewer than the finding budget is normal and expected. Emitting zero blocking findings when the artifacts are genuinely implementation-ready is a valid, useful result.\n\n")

	b.WriteString("## Fix sizing\n\n")
	b.WriteString("When proposing a fix or suggestion, prefer the smallest-shaped fix that resolves the subtle issue. Do not propose new schemas, new mechanisms, or new work units when an existing pattern can absorb the fix. Over-engineered fixes are themselves a subtle harm — they ship as permanent doc surface that constrains future work.\n\n")

	b.WriteString("## Project-specific focus\n\n")
	b.WriteString("In addition to the universal what-to-flag criteria above, give particular attention to:\n\n")
	if strings.TrimSpace(req.ReviewFocus) == "" {
		b.WriteString("(no project-specific focus)\n\n")
	} else {
		b.WriteString(strings.TrimRight(req.ReviewFocus, "\n"))
		b.WriteString("\n\n")
	}

	b.WriteString("## Artifacts under review\n\n")
	b.WriteString("Read each artifact in full before forming a position.\n\n")
	for _, artifact := range req.Artifacts {
		writeArtifact(&b, artifact)
	}

	b.WriteString("## Verdict and severity\n\n")
	b.WriteString("Apply these definitions precisely.\n\n")
	b.WriteString("Verdict (the headline judgment for the whole review):\n")
	b.WriteString("- `ready_to_build`: an implementer could pick up this work order and produce code that satisfies the design under the stated constraints without further clarification. `concerns` plus `questions` must be empty.\n")
	b.WriteString("- `needs_changes`: at least one readiness-blocking concern exists; the artifacts must be revised before implementation.\n")
	b.WriteString("- `needs_discussion`: the artifacts are close, but at least one readiness-blocking question is open that the human and design agent should adjudicate before proceeding.\n\n")
	b.WriteString("Severity (per concern):\n")
	b.WriteString("- `blocker`: implementation cannot proceed without resolving this.\n")
	b.WriteString("- `major`: implementation could proceed but would produce something materially different from intent.\n")
	b.WriteString("- `minor`: a low-impact readiness issue. If it does not block readiness under the context, use `advisory_notes` instead.\n\n")

	if req.MaxFindings > 0 {
		b.WriteString("## Finding budget\n\n")
		b.WriteString(fmt.Sprintf("Return at most %d total blocking findings across `concerns` and `questions` combined. Prioritize blockers and major concerns first, then the highest-leverage blocking questions. Do not pad the output to fill the budget. `advisory_notes` are outside this budget, but keep them concise and only include notes that are genuinely useful.\n\n", req.MaxFindings))
	}

	b.WriteString("## Output\n\n")
	b.WriteString("Respond with a single JSON object only. No prose before or after, no markdown fence, no commentary outside the object. Your response must conform exactly to this schema:\n\n")
	b.WriteString("```json\n")
	b.WriteString(prettySchema(req.MaxFindings))
	b.WriteString("\n```\n\n")
	b.WriteString("Required fields must be present even when empty (e.g., `concerns: []`, `advisory_notes: []`). Do not include fields not defined in the schema.\n\n")
	b.WriteString("Within a single review output, every id appearing in `concerns`, `questions`, or `advisory_notes` must be unique across all three arrays. Never reuse an id - not within a single array, and not across arrays.\n")

	return b.String(), schema.ReviewOutputSchemaWithMaxFindings(req.MaxFindings)
}

// settledDecisionsBlock renders the settled-decisions section. It is kept as its
// own section, distinct from the calibration block, so that suppressing it for a
// single round (the deferred fresh-reader round) stays a cheap change. Entries
// whose do_not_flag is empty after trimming are skipped, and the whole block is
// omitted when no entry has load-bearing text; the operator-side id is never
// rendered.
func settledDecisionsBlock(decisions []SettledDecision) string {
	var items []string
	for _, d := range decisions {
		text := strings.TrimSpace(d.DoNotFlag)
		if text == "" {
			continue
		}
		items = append(items, text)
	}
	if len(items) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Settled decisions (do not re-raise)\n\n")
	b.WriteString("The following are decisions already made and out of scope for this review. Do not raise them as concerns, questions, or advisory notes, and do not suggest revisiting them:\n\n")
	for _, item := range items {
		b.WriteString("- ")
		b.WriteString(item)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

func writeArtifact(b *strings.Builder, artifact Artifact) {
	fence := dynamicFence(artifact.Content)
	b.WriteString(fmt.Sprintf("### %s\n\n", artifact.Name))
	b.WriteString(fmt.Sprintf("Snapshot path: %s\n", artifact.SnapshotPath))
	b.WriteString(fmt.Sprintf("Source path: %s\n", artifact.SourcePath))
	b.WriteString(fmt.Sprintf("SHA-256: %s\n\n", artifact.Hash))
	b.WriteString(fence)
	b.WriteString("\n")
	b.Write(artifact.Content)
	if len(artifact.Content) == 0 || artifact.Content[len(artifact.Content)-1] != '\n' {
		b.WriteString("\n")
	}
	b.WriteString(fence)
	b.WriteString("\n\n")
}

func dynamicFence(content []byte) string {
	longest := 0
	current := 0
	for _, b := range content {
		if b == '`' {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	if longest < 3 {
		longest = 2
	}
	return strings.Repeat("`", longest+1)
}

func prettySchema(maxFindings int) string {
	raw := schema.ReviewOutputSchemaWithMaxFindings(maxFindings)
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}
