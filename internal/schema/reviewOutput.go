package schema

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/michaelquigley/df/dd"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ReviewOutput is the canonical structured response returned by a reviewer.
type ReviewOutput struct {
	Verdict       string
	Summary       string
	Concerns      []Concern
	Questions     []Question
	AdvisoryNotes []AdvisoryNote
}

// Concern identifies a problem or improvement found during review.
type Concern struct {
	ID         string
	Severity   string
	Location   string
	Claim      string
	Rationale  string
	Suggestion *string `dd:",+nullable"`
}

// Question records a clarification needed before the reviewer can decide.
type Question struct {
	ID          string
	Topic       string
	WhyItBlocks string
}

// AdvisoryNote records non-blocking polish or downstream considerations.
type AdvisoryNote struct {
	ID         string
	Location   string
	Note       string
	Rationale  string
	Suggestion *string `dd:",+nullable"`
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
	_, err := validateReviewOutputSchema(raw)
	return err
}

// ReviewVerdict validates the JSON schema and returns the verdict for staging
// the round log. the strict decode and cross-field checks still have to pass
// before the log is published.
func ReviewVerdict(raw json.RawMessage) (string, error) {
	value, err := validateReviewOutputSchema(raw)
	if err != nil {
		return "", err
	}
	return value.(map[string]any)["verdict"].(string), nil
}

func validateReviewOutputSchema(raw json.RawMessage) (any, error) {
	compiled, err := reviewOutputSchema()
	if err != nil {
		return nil, err
	}

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid review output json: %w", err)
	}
	if err := compiled.Validate(inst); err != nil {
		return nil, fmt.Errorf("review output schema violation: %w", err)
	}
	return inst, nil
}

// ParseReviewOutput validates and strictly binds raw reviewer output.
func ParseReviewOutput(raw json.RawMessage) (ReviewOutput, error) {
	if err := ValidateReviewOutput(raw); err != nil {
		return ReviewOutput{}, err
	}

	value, err := dd.DecodeStrictJSON(raw)
	if err != nil {
		return ReviewOutput{}, fmt.Errorf("parse review output: %w", err)
	}
	return BindAndValidateReviewOutput(value)
}

// BindAndValidateReviewOutput strictly binds a schema-validated and strictly
// decoded object, then checks finding IDs and verdict consistency.
func BindAndValidateReviewOutput(value map[string]any) (ReviewOutput, error) {
	output, err := BindReviewOutput(value)
	if err != nil {
		return ReviewOutput{}, err
	}
	if err := ValidateUniqueIDs(output); err != nil {
		return ReviewOutput{}, err
	}
	if err := ValidateReviewConsistency(output); err != nil {
		return ReviewOutput{}, err
	}
	return output, nil
}

// BindReviewOutput strictly binds an already-decoded review that the caller
// has validated. it does not repeat schema or cross-field checks.
func BindReviewOutput(value map[string]any) (ReviewOutput, error) {
	var output ReviewOutput
	if err := dd.Bind(&output, value, dd.Strict()); err != nil {
		return ReviewOutput{}, fmt.Errorf("bind review output: %w", err)
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

// ValidateUniqueIDs rejects reviewer output that reuses an id within or across
// the concerns, questions, and advisory_notes arrays. JSON Schema cannot express
// cross-array uniqueness, so the broker enforces it here. duplicate ids would
// otherwise make ref-kind classification ambiguous and let reviewer drift slip
// past validation silently. an empty id is treated like any other: the schema
// refuses one at round time, and two of them here are a reuse, not an exemption.
func ValidateUniqueIDs(output ReviewOutput) error {
	seen := make(map[string]string, len(output.Concerns)+len(output.Questions)+len(output.AdvisoryNotes))
	check := func(kind string, id string) error {
		if previous, ok := seen[id]; ok {
			if previous == kind {
				return fmt.Errorf("review output reuses id '%s' within %s", id, kind)
			}
			return fmt.Errorf("review output reuses id '%s' across %s and %s", id, previous, kind)
		}
		seen[id] = kind
		return nil
	}
	for _, concern := range output.Concerns {
		if err := check("concerns", concern.ID); err != nil {
			return err
		}
	}
	for _, question := range output.Questions {
		if err := check("questions", question.ID); err != nil {
			return err
		}
	}
	for _, note := range output.AdvisoryNotes {
		if err := check("advisory_notes", note.ID); err != nil {
			return err
		}
	}
	return nil
}

// ValidateReviewConsistency checks cross-field readiness invariants.
func ValidateReviewConsistency(output ReviewOutput) error {
	blockingFindings := FindingCount(output)
	if output.Verdict == "ready_to_build" {
		if blockingFindings != 0 {
			return fmt.Errorf("review output contradiction: verdict ready_to_build requires no concerns or questions")
		}
		return nil
	}
	if blockingFindings == 0 {
		return fmt.Errorf("review output contradiction: verdict %s requires at least one concern or question", output.Verdict)
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
