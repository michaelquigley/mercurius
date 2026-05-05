package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/schema"
)

// Artifact is a snapshotted artifact rendered into the review prompt.
type Artifact struct {
	Name         string
	SourcePath   string
	SnapshotPath string
	Hash         string
	Content      []byte
	Inline       bool
}

// Request contains runtime values for the standard review prompt.
type Request struct {
	Artifacts       []Artifact
	PriorDecisions  []reviewer.PriorDecision
	ReviewContext   string
	DecisionsLog    string
	PromptOverrides string
	MaxFindings     int
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
	b.WriteString("Weight every finding against this review context. Suppress concerns that do not realistically apply under the stated deployment constraints, implementation model, locked decisions, or out-of-scope boundaries. When a point is useful but not readiness-blocking, put it in `advisory_notes`, not `concerns` or `questions`.\n\n")

	b.WriteString("## Review criteria\n\n")
	b.WriteString("For the design document, look for:\n")
	b.WriteString("- Decisions that are described but not actually made (handwaves like \"decided at scaffold time\").\n")
	b.WriteString("- Internal contradictions between sections.\n")
	b.WriteString("- Affordances claimed but not specified (e.g., \"the system supports X\" without saying how).\n")
	b.WriteString("- Architectural ambiguity that two implementers would resolve differently.\n\n")
	b.WriteString("For the work order, look for:\n")
	b.WriteString("- Scope items whose definition of done is not testable.\n")
	b.WriteString("- Dependencies between milestones that are not stated.\n")
	b.WriteString("- Concrete decisions deferred to implementation rather than settled.\n")
	b.WriteString("- Test coverage gaps for the architectural commitments in the design.\n\n")
	b.WriteString("For both, separate readiness blockers from polish. Only use `concerns` or `questions` for items that block shipping/building under the stated context; put non-blocking small improvements, missed opportunities, or downstream considerations in `advisory_notes`.\n\n")

	b.WriteString("## Project-specific guidance\n\n")
	if strings.TrimSpace(req.PromptOverrides) == "" {
		b.WriteString("(no project-specific guidance)\n\n")
	} else {
		b.WriteString(strings.TrimRight(req.PromptOverrides, "\n"))
		b.WriteString("\n\n")
	}

	b.WriteString("## Artifacts under review\n\n")
	b.WriteString("Read each artifact in full before forming a position.\n\n")
	for _, artifact := range req.Artifacts {
		writeArtifact(&b, artifact)
	}

	b.WriteString("## Prior decisions\n\n")
	if len(req.PriorDecisions) == 0 {
		b.WriteString("(No prior decisions; this is the first round of review for this session.)\n\n")
	} else {
		b.WriteString("The following decisions have been adjudicated in earlier rounds. Do not re-raise them unless there is a substantive new reason - for example, the artifacts have changed materially since the decision was made. If you do re-raise a prior decision, your `rationale` must reference the prior decision and explain why it should be revisited.\n\n")
		for _, decision := range req.PriorDecisions {
			b.WriteString(fmt.Sprintf("- Round %d, %s (%s): %s\n", decision.RoundNumber, decision.Ref, decision.Disposition, decision.Note))
		}
		b.WriteString("\n")
	}
	b.WriteString("Rendered decisions log:\n\n")
	if strings.TrimSpace(req.DecisionsLog) == "" {
		b.WriteString("(no decisions log has been recorded yet)\n\n")
	} else {
		b.WriteString(strings.TrimRight(strings.TrimSpace(req.DecisionsLog), "\n"))
		b.WriteString("\n\n")
	}
	b.WriteString("Treat accepted, rejected, and deferred decisions as adjudicated session context. Do not re-raise rejected or deferred items unless the artifacts now make the decision concretely broken or there is a genuinely new angle.\n\n")

	b.WriteString("## Verdict and severity\n\n")
	b.WriteString("Apply these definitions precisely.\n\n")
	b.WriteString("Verdict (the headline judgment for the whole review):\n")
	b.WriteString("- `ready_to_build`: an implementer could pick up this work order and produce code that satisfies the design under the stated constraints without further clarification. `ready_to_ship` must be true, and `concerns` plus `questions` must be empty.\n")
	b.WriteString("- `needs_changes`: at least one readiness-blocking concern exists; the artifacts must be revised before implementation.\n")
	b.WriteString("- `needs_discussion`: the artifacts are close, but at least one readiness-blocking question is open that the human and design agent should adjudicate before proceeding.\n\n")
	b.WriteString("Severity (per concern):\n")
	b.WriteString("- `blocker`: implementation cannot proceed without resolving this.\n")
	b.WriteString("- `major`: implementation could proceed but would produce something materially different from intent.\n")
	b.WriteString("- `minor`: a low-impact readiness issue. If it does not block readiness under the context, use `advisory_notes` instead.\n\n")
	b.WriteString("A `ready_to_ship` value of true requires `verdict: \"ready_to_build\"`, `concerns: []`, and `questions: []`. A `ready_to_ship` value of false requires at least one `concerns` or `questions` entry and a verdict other than `ready_to_build`. Advisory notes never make the artifact unready.\n\n")

	if req.MaxFindings > 0 {
		b.WriteString("## Finding budget\n\n")
		b.WriteString(fmt.Sprintf("Return at most %d total blocking findings across `concerns` and `questions` combined. Prioritize blockers and major concerns first, then the highest-leverage blocking questions. Do not pad the output to fill the budget. `advisory_notes` are outside this budget, but keep them concise and only include notes that are genuinely useful.\n\n", req.MaxFindings))
	}

	b.WriteString("## Output\n\n")
	b.WriteString("Respond with a single JSON object only. No prose before or after, no markdown fence, no commentary outside the object. Your response must conform exactly to this schema:\n\n")
	b.WriteString("```json\n")
	b.WriteString(prettySchema(req.MaxFindings))
	b.WriteString("\n```\n\n")
	b.WriteString("Required fields must be present even when empty (e.g., `concerns: []`, `advisory_notes: []`). Do not include fields not defined in the schema.\n")

	return b.String(), schema.ReviewOutputSchemaWithMaxFindings(req.MaxFindings)
}

func writeArtifact(b *strings.Builder, artifact Artifact) {
	sourcePath := artifact.SourcePath
	if artifact.Inline {
		sourcePath = "inline"
	}

	fence := dynamicFence(artifact.Content)
	b.WriteString(fmt.Sprintf("### %s\n\n", artifact.Name))
	b.WriteString(fmt.Sprintf("Snapshot path: %s\n", artifact.SnapshotPath))
	b.WriteString(fmt.Sprintf("Source path: %s\n", sourcePath))
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
