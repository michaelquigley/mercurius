package schema

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ReviewOutput is the canonical structured response returned by a reviewer.
type ReviewOutput struct {
	ReadyToShip   bool           `json:"ready_to_ship"`
	Verdict       string         `json:"verdict"`
	Summary       string         `json:"summary"`
	Concerns      []Concern      `json:"concerns"`
	Questions     []Question     `json:"questions"`
	AdvisoryNotes []AdvisoryNote `json:"advisory_notes"`
	ProposedDiffs []ProposedDiff `json:"proposed_diffs"`
}

// Concern identifies a problem or improvement found during review.
type Concern struct {
	ID         string  `json:"id"`
	Severity   string  `json:"severity"`
	Location   string  `json:"location"`
	Claim      string  `json:"claim"`
	Rationale  string  `json:"rationale"`
	Suggestion *string `json:"suggestion"`
}

// Question records a clarification needed before the reviewer can decide.
type Question struct {
	ID          string `json:"id"`
	Topic       string `json:"topic"`
	WhyItBlocks string `json:"why_it_blocks"`
}

// AdvisoryNote records non-blocking polish or downstream considerations.
type AdvisoryNote struct {
	ID         string  `json:"id"`
	Location   string  `json:"location"`
	Note       string  `json:"note"`
	Rationale  string  `json:"rationale"`
	Suggestion *string `json:"suggestion"`
}

// ProposedDiff records concrete text or patch suggested by the reviewer.
type ProposedDiff struct {
	ID     string `json:"id"`
	Target string `json:"target"`
	Patch  string `json:"patch"`
}

//go:embed reviewOutputSchema.json
var schemaFS embed.FS

var (
	compileReviewOutputOnce sync.Once
	compiledReviewOutput    *jsonschema.Schema
	compiledReviewOutputErr error
)

// ReviewOutputSchema returns the canonical structured review output schema.
func ReviewOutputSchema() json.RawMessage {
	return append(json.RawMessage(nil), reviewOutputSchemaBytes()...)
}

// ReviewOutputSchemaWithMaxFindings returns the review schema with per-array hints.
func ReviewOutputSchemaWithMaxFindings(maxFindings int) json.RawMessage {
	raw := ReviewOutputSchema()
	if maxFindings <= 0 {
		return raw
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw
	}
	properties, ok := doc["properties"].(map[string]any)
	if !ok {
		return raw
	}
	for _, name := range []string{"concerns", "questions"} {
		array, ok := properties[name].(map[string]any)
		if !ok {
			return raw
		}
		array["maxItems"] = maxFindings
	}
	limited, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return limited
}

// ValidateReviewOutput validates raw reviewer output against the canonical schema.
func ValidateReviewOutput(raw json.RawMessage) error {
	compiled, err := reviewOutputSchema()
	if err != nil {
		return err
	}

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("invalid review output json: %w", err)
	}
	if err := compiled.Validate(inst); err != nil {
		return fmt.Errorf("review output schema violation: %w", err)
	}
	return nil
}

// ParseReviewOutput validates and unmarshals raw reviewer output.
func ParseReviewOutput(raw json.RawMessage) (ReviewOutput, error) {
	if err := ValidateReviewOutput(raw); err != nil {
		return ReviewOutput{}, err
	}

	var output ReviewOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		return ReviewOutput{}, fmt.Errorf("parse review output: %w", err)
	}
	if err := ValidateReviewConsistency(output); err != nil {
		return ReviewOutput{}, err
	}
	return output, nil
}

// FindingCount returns the number of concern and question findings.
func FindingCount(output ReviewOutput) int {
	return len(output.Concerns) + len(output.Questions)
}

// ValidateFindingLimit checks the configured total findings cap.
func ValidateFindingLimit(output ReviewOutput, maxFindings int) error {
	if maxFindings <= 0 {
		return nil
	}
	count := FindingCount(output)
	if count <= maxFindings {
		return nil
	}
	return fmt.Errorf("review output has %d findings (concerns=%d, questions=%d), maximum is %d", count, len(output.Concerns), len(output.Questions), maxFindings)
}

// ValidateReviewConsistency checks cross-field readiness invariants.
func ValidateReviewConsistency(output ReviewOutput) error {
	blockingFindings := FindingCount(output)
	if output.ReadyToShip {
		if output.Verdict != "ready_to_build" {
			return fmt.Errorf("review output contradiction: ready_to_ship=true requires verdict ready_to_build")
		}
		if blockingFindings != 0 {
			return fmt.Errorf("review output contradiction: ready_to_ship=true requires no concerns or questions")
		}
		return nil
	}

	if output.Verdict == "ready_to_build" {
		return fmt.Errorf("review output contradiction: ready_to_ship=false cannot use verdict ready_to_build")
	}
	if blockingFindings == 0 {
		return fmt.Errorf("review output contradiction: ready_to_ship=false requires at least one concern or question")
	}
	return nil
}

func reviewOutputSchema() (*jsonschema.Schema, error) {
	compileReviewOutputOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(reviewOutputSchemaBytes()))
		if err != nil {
			compiledReviewOutputErr = fmt.Errorf("invalid embedded review output schema: %w", err)
			return
		}

		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("reviewOutputSchema.json", doc); err != nil {
			compiledReviewOutputErr = fmt.Errorf("register review output schema: %w", err)
			return
		}

		compiledReviewOutput, compiledReviewOutputErr = compiler.Compile("reviewOutputSchema.json")
		if compiledReviewOutputErr != nil {
			compiledReviewOutputErr = fmt.Errorf("compile review output schema: %w", compiledReviewOutputErr)
		}
	})

	return compiledReviewOutput, compiledReviewOutputErr
}

func reviewOutputSchemaBytes() []byte {
	data, err := schemaFS.ReadFile("reviewOutputSchema.json")
	if err != nil {
		panic(fmt.Sprintf("read embedded review output schema: %v", err))
	}
	return data
}
