package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateReviewOutputAcceptsMinimalValidOutput(t *testing.T) {
	raw := json.RawMessage(`{
		"verdict": "ready_to_build",
		"summary": "ready",
		"concerns": [],
		"questions": [],
		"proposed_diffs": []
	}`)

	if err := ValidateReviewOutput(raw); err != nil {
		t.Fatalf("expected valid output: %v", err)
	}
}

func TestValidateReviewOutputRejectsMalformedJSON(t *testing.T) {
	err := ValidateReviewOutput(json.RawMessage(`{"verdict":`))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "invalid review output json") {
		t.Fatalf("expected json error, got: %v", err)
	}
}

func TestValidateReviewOutputRejectsMissingRequiredField(t *testing.T) {
	raw := json.RawMessage(`{
		"verdict": "ready_to_build",
		"summary": "ready",
		"concerns": [],
		"questions": []
	}`)

	if err := ValidateReviewOutput(raw); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateReviewOutputRejectsUnknownTopLevelField(t *testing.T) {
	raw := json.RawMessage(`{
		"verdict": "ready_to_build",
		"summary": "ready",
		"concerns": [],
		"questions": [],
		"proposed_diffs": [],
		"extra": true
	}`)

	if err := ValidateReviewOutput(raw); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateReviewOutputRejectsUnknownNestedField(t *testing.T) {
	raw := json.RawMessage(`{
		"verdict": "needs_changes",
		"summary": "changes needed",
		"concerns": [
			{
				"id": "C-1",
				"severity": "major",
				"location": "work-order:M1",
				"claim": "missing test case",
				"rationale": "definition of done is not covered",
				"extra": true
			}
		],
		"questions": [],
		"proposed_diffs": []
	}`)

	if err := ValidateReviewOutput(raw); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateReviewOutputRejectsInvalidVerdict(t *testing.T) {
	raw := json.RawMessage(`{
		"verdict": "ready",
		"summary": "ready",
		"concerns": [],
		"questions": [],
		"proposed_diffs": []
	}`)

	if err := ValidateReviewOutput(raw); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateReviewOutputRejectsInvalidSeverity(t *testing.T) {
	raw := json.RawMessage(`{
		"verdict": "needs_changes",
		"summary": "changes needed",
		"concerns": [
			{
				"id": "C-1",
				"severity": "critical",
				"location": "design:section 5",
				"claim": "bad severity",
				"rationale": "severity enum is closed"
			}
		],
		"questions": [],
		"proposed_diffs": []
	}`)

	if err := ValidateReviewOutput(raw); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateReviewOutputConcernSuggestionVariants(t *testing.T) {
	tests := []struct {
		name       string
		suggestion string
		wantErr    bool
	}{
		{
			name:       "omitted",
			suggestion: "",
		},
		{
			name:       "null",
			suggestion: `,"suggestion": null`,
		},
		{
			name:       "non-empty",
			suggestion: `,"suggestion": "add a concrete acceptance test"`,
		},
		{
			name:       "empty",
			suggestion: `,"suggestion": ""`,
			wantErr:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := json.RawMessage(`{
				"verdict": "needs_changes",
				"summary": "changes needed",
				"concerns": [
					{
						"id": "C-1",
						"severity": "minor",
						"location": "design:section 5",
						"claim": "could be clearer",
						"rationale": "the implementer has to infer intent"` + test.suggestion + `
					}
				],
				"questions": [],
				"proposed_diffs": []
			}`)

			err := ValidateReviewOutput(raw)
			if test.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("expected valid output: %v", err)
			}
		})
	}
}

func TestParseReviewOutput(t *testing.T) {
	raw := json.RawMessage(`{
		"verdict": "needs_changes",
		"summary": "changes needed",
		"concerns": [
			{
				"id": "C-1",
				"severity": "major",
				"location": "design:section 5",
				"claim": "missing decision",
				"rationale": "two implementers would diverge"
			}
		],
		"questions": [
			{
				"id": "Q-1",
				"topic": "budget",
				"why_it_blocks": "cannot judge acceptance"
			}
		],
		"proposed_diffs": []
	}`)

	output, err := ParseReviewOutput(raw)
	if err != nil {
		t.Fatalf("parse review output: %v", err)
	}
	if output.Verdict != "needs_changes" || output.Concerns[0].ID != "C-1" || output.Questions[0].ID != "Q-1" {
		t.Fatalf("unexpected parsed output: %+v", output)
	}
}
