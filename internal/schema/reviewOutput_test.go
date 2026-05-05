package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateReviewOutputAcceptsMinimalValidOutput(t *testing.T) {
	raw := json.RawMessage(`{
		"ready_to_ship": true,
		"verdict": "ready_to_build",
		"summary": "ready",
		"concerns": [],
		"questions": [],
		"advisory_notes": [],
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
		"ready_to_ship": true,
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
		"ready_to_ship": true,
		"verdict": "ready_to_build",
		"summary": "ready",
		"concerns": [],
		"questions": [],
		"advisory_notes": [],
		"proposed_diffs": [],
		"extra": true
	}`)

	if err := ValidateReviewOutput(raw); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateReviewOutputRejectsUnknownNestedField(t *testing.T) {
	raw := json.RawMessage(`{
		"ready_to_ship": false,
		"verdict": "needs_changes",
		"summary": "changes needed",
		"concerns": [
			{
				"id": "C-1",
				"severity": "major",
				"location": "work-order:M1",
				"claim": "missing test case",
				"rationale": "definition of done is not covered",
				"suggestion": null,
				"extra": true
			}
		],
		"questions": [],
		"advisory_notes": [],
		"proposed_diffs": []
	}`)

	if err := ValidateReviewOutput(raw); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateReviewOutputRejectsInvalidVerdict(t *testing.T) {
	raw := json.RawMessage(`{
		"ready_to_ship": true,
		"verdict": "ready",
		"summary": "ready",
		"concerns": [],
		"questions": [],
		"advisory_notes": [],
		"proposed_diffs": []
	}`)

	if err := ValidateReviewOutput(raw); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateReviewOutputRejectsInvalidSeverity(t *testing.T) {
	raw := json.RawMessage(`{
		"ready_to_ship": false,
		"verdict": "needs_changes",
		"summary": "changes needed",
		"concerns": [
			{
				"id": "C-1",
				"severity": "critical",
				"location": "design:section 5",
				"claim": "bad severity",
				"rationale": "severity enum is closed",
				"suggestion": null
			}
		],
		"questions": [],
		"advisory_notes": [],
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
			wantErr:    true,
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
				"ready_to_ship": false,
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
				"advisory_notes": [],
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
		"ready_to_ship": false,
		"verdict": "needs_changes",
		"summary": "changes needed",
		"concerns": [
			{
				"id": "C-1",
				"severity": "major",
				"location": "design:section 5",
				"claim": "missing decision",
				"rationale": "two implementers would diverge",
				"suggestion": null
			}
		],
		"questions": [
			{
				"id": "Q-1",
				"topic": "budget",
				"why_it_blocks": "cannot judge acceptance"
			}
		],
		"advisory_notes": [
			{
				"id": "A-1",
				"location": "design:section 6",
				"note": "example could be shorter",
				"rationale": "shorter text would be easier to scan",
				"suggestion": null
			}
		],
		"proposed_diffs": []
	}`)

	output, err := ParseReviewOutput(raw)
	if err != nil {
		t.Fatalf("parse review output: %v", err)
	}
	if output.Verdict != "needs_changes" || output.Concerns[0].ID != "C-1" || output.Questions[0].ID != "Q-1" || output.AdvisoryNotes[0].ID != "A-1" {
		t.Fatalf("unexpected parsed output: %+v", output)
	}
}

func TestReviewOutputSchemaWithMaxFindings(t *testing.T) {
	raw := ReviewOutputSchemaWithMaxFindings(2)

	var doc struct {
		Properties map[string]struct {
			MaxItems int `json:"maxItems"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if doc.Properties["concerns"].MaxItems != 2 {
		t.Fatalf("concerns maxItems = %d, want 2", doc.Properties["concerns"].MaxItems)
	}
	if doc.Properties["questions"].MaxItems != 2 {
		t.Fatalf("questions maxItems = %d, want 2", doc.Properties["questions"].MaxItems)
	}
	if doc.Properties["proposed_diffs"].MaxItems != 0 {
		t.Fatalf("proposed_diffs maxItems = %d, want unset", doc.Properties["proposed_diffs"].MaxItems)
	}
	if doc.Properties["advisory_notes"].MaxItems != 0 {
		t.Fatalf("advisory_notes maxItems = %d, want unset", doc.Properties["advisory_notes"].MaxItems)
	}
}

func TestValidateFindingLimit(t *testing.T) {
	output := ReviewOutput{
		Concerns:      []Concern{{ID: "C-1"}, {ID: "C-2"}},
		Questions:     []Question{{ID: "Q-1"}},
		AdvisoryNotes: []AdvisoryNote{{ID: "A-1"}, {ID: "A-2"}},
	}

	if err := ValidateFindingLimit(output, 3); err != nil {
		t.Fatalf("expected valid finding count: %v", err)
	}
	err := ValidateFindingLimit(output, 2)
	if err == nil {
		t.Fatal("expected finding limit error")
	}
	if !strings.Contains(err.Error(), "maximum is 2") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseReviewOutputRejectsReadinessContradictions(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{
			name: "ready with concern",
			raw: json.RawMessage(`{
				"ready_to_ship": true,
				"verdict": "ready_to_build",
				"summary": "contradiction",
				"concerns": [
					{
						"id": "C-1",
						"severity": "major",
						"location": "design",
						"claim": "missing decision",
						"rationale": "implementation would diverge",
						"suggestion": null
					}
				],
				"questions": [],
				"advisory_notes": [],
				"proposed_diffs": []
			}`),
			want: "ready_to_ship=true requires no concerns or questions",
		},
		{
			name: "not ready without blocking finding",
			raw: json.RawMessage(`{
				"ready_to_ship": false,
				"verdict": "needs_discussion",
				"summary": "only polish",
				"concerns": [],
				"questions": [],
				"advisory_notes": [
					{
						"id": "A-1",
						"location": "N/A",
						"note": "shorten wording",
						"rationale": "easier to scan",
						"suggestion": null
					}
				],
				"proposed_diffs": []
			}`),
			want: "ready_to_ship=false requires at least one concern or question",
		},
		{
			name: "not ready with ready verdict",
			raw: json.RawMessage(`{
				"ready_to_ship": false,
				"verdict": "ready_to_build",
				"summary": "contradiction",
				"concerns": [
					{
						"id": "C-1",
						"severity": "major",
						"location": "design",
						"claim": "missing decision",
						"rationale": "implementation would diverge",
						"suggestion": null
					}
				],
				"questions": [],
				"advisory_notes": [],
				"proposed_diffs": []
			}`),
			want: "ready_to_ship=false cannot use verdict ready_to_build",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseReviewOutput(test.raw)
			if err == nil {
				t.Fatal("expected consistency error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q in error, got %v", test.want, err)
			}
		})
	}
}
