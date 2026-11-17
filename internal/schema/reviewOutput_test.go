package schema

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/michaelquigley/df/dd"
)

func TestValidateReviewOutputAcceptsMinimalValidOutput(t *testing.T) {
	raw := json.RawMessage(`{
		"verdict": "ready_to_build",
		"summary": "ready",
		"concerns": [],
		"questions": [],
		"advisory_notes": []
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
		"advisory_notes": [],
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
				"suggestion": null,
				"extra": true
			}
		],
		"questions": [],
		"advisory_notes": []
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
		"advisory_notes": []
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
				"rationale": "severity enum is closed",
				"suggestion": null
			}
		],
		"questions": [],
		"advisory_notes": []
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
				"advisory_notes": []
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
		]
	}`)

	output, err := ParseReviewOutput(raw)
	if err != nil {
		t.Fatalf("parse review output: %v", err)
	}
	if output.Verdict != "needs_changes" || output.Concerns[0].ID != "C-1" || output.Questions[0].ID != "Q-1" || output.AdvisoryNotes[0].ID != "A-1" {
		t.Fatalf("unexpected parsed output: %+v", output)
	}

	decoded, err := dd.DecodeStrictJSON(raw)
	if err != nil {
		t.Fatalf("decode review output: %v", err)
	}
	bound, err := BindReviewOutput(decoded)
	if err != nil {
		t.Fatalf("bind decoded review output: %v", err)
	}
	if !reflect.DeepEqual(output, bound) {
		t.Fatalf("bound review output = %#v, want %#v", bound, output)
	}
}

func TestParseReviewOutputSuggestionVariants(t *testing.T) {
	const review = `{
		"verdict": "needs_changes",
		"summary": "changes needed",
		"concerns": [{
			"id": "C-1",
			"severity": "major",
			"location": "design",
			"claim": "scope is ambiguous",
			"rationale": "implementations would diverge"{{concern}}
		}],
		"questions": [],
		"advisory_notes": [{
			"id": "A-1",
			"location": "design",
			"note": "shorten the introduction",
			"rationale": "easier to scan"{{advisory}}
		}]
	}`
	const null = `,"suggestion": null`
	suggestion := "revise the design"
	const text = `,"suggestion": "revise the design"`
	tests := []struct {
		name     string
		concern  string
		advisory string
		want     *string
		wantErr  bool
	}{
		{name: "null", concern: null, advisory: null},
		{name: "string", concern: text, advisory: text, want: &suggestion},
		{name: "omitted concern suggestion", advisory: null, wantErr: true},
		{name: "omitted advisory suggestion", concern: null, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := json.RawMessage(strings.NewReplacer("{{concern}}", test.concern, "{{advisory}}", test.advisory).Replace(review))
			output, err := ParseReviewOutput(raw)
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), "review output schema violation") || !strings.Contains(err.Error(), "suggestion") {
					t.Fatalf("expected schema violation for missing suggestion, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse review output: %v", err)
			}
			if len(output.Concerns) != 1 || len(output.AdvisoryNotes) != 1 {
				t.Fatalf("unexpected parsed output: %+v", output)
			}
			if !reflect.DeepEqual(output.Concerns[0].Suggestion, test.want) ||
				!reflect.DeepEqual(output.AdvisoryNotes[0].Suggestion, test.want) {
				t.Fatalf("unexpected concern or advisory suggestion: %+v", output)
			}
		})
	}
}

func TestParseReviewOutputRejectsDuplicateKey(t *testing.T) {
	raw := json.RawMessage(`{
		"verdict": "ready_to_build",
		"summary": "ready",
		"concerns": [],
		"questions": [],
		"advisory_notes": [{
			"id": "A-1",
			"location": "design",
			"note": "shorten the introduction",
			"rationale": "easier to scan",
			"suggestion": null,
			"suggestion": "remove the last paragraph"
		}]
	}`)

	_, err := ParseReviewOutput(raw)
	var duplicate *dd.DuplicateKeyError
	if !errors.As(err, &duplicate) || duplicate.Key != "suggestion" {
		t.Fatalf("expected duplicate suggestion key error, got %v", err)
	}
}

func TestBindReviewOutputRejectsUnknownField(t *testing.T) {
	_, err := BindReviewOutput(map[string]any{"extra": true})
	var unknown *dd.UnknownFieldError
	if !errors.As(err, &unknown) || unknown.Key != "extra" {
		t.Fatalf("expected unknown field error, got %v", err)
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

func TestValidateUniqueIDsRejectsDuplicates(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{
			name: "duplicate within concerns",
			raw: json.RawMessage(`{
				"verdict": "needs_changes",
				"summary": "x",
				"concerns": [
					{"id": "C-1", "severity": "major", "location": "x", "claim": "a", "rationale": "b", "suggestion": null},
					{"id": "C-1", "severity": "minor", "location": "y", "claim": "c", "rationale": "d", "suggestion": null}
				],
				"questions": [],
				"advisory_notes": []
			}`),
			want: "within concerns",
		},
		{
			name: "duplicate across concerns and advisory_notes",
			raw: json.RawMessage(`{
				"verdict": "needs_changes",
				"summary": "x",
				"concerns": [
					{"id": "X-1", "severity": "major", "location": "x", "claim": "a", "rationale": "b", "suggestion": null}
				],
				"questions": [],
				"advisory_notes": [
					{"id": "X-1", "location": "y", "note": "polish", "rationale": "tone", "suggestion": null}
				]
			}`),
			want: "across concerns and advisory_notes",
		},
		{
			name: "duplicate across questions and advisory_notes",
			raw: json.RawMessage(`{
				"verdict": "needs_changes",
				"summary": "x",
				"concerns": [],
				"questions": [
					{"id": "X-1", "topic": "scope", "why_it_blocks": "z"}
				],
				"advisory_notes": [
					{"id": "X-1", "location": "y", "note": "polish", "rationale": "tone", "suggestion": null}
				]
			}`),
			want: "across questions and advisory_notes",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseReviewOutput(test.raw)
			if err == nil {
				t.Fatal("expected uniqueness error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q in error, got %v", test.want, err)
			}
		})
	}
}

func TestValidateUniqueIDsAcceptsAllUnique(t *testing.T) {
	output := ReviewOutput{
		Concerns:      []Concern{{ID: "C-1"}, {ID: "C-2"}},
		Questions:     []Question{{ID: "Q-1"}},
		AdvisoryNotes: []AdvisoryNote{{ID: "A-1"}},
	}
	if err := ValidateUniqueIDs(output); err != nil {
		t.Fatalf("expected unique-id pass: %v", err)
	}
}

func TestParseReviewOutputRejectsReadinessContradictions(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{
			name: "ready_to_build with concern",
			raw: json.RawMessage(`{
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
				"advisory_notes": []
			}`),
			want: "verdict ready_to_build requires no concerns or questions",
		},
		{
			name: "needs_discussion without blocking finding",
			raw: json.RawMessage(`{
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
				]
			}`),
			want: "verdict needs_discussion requires at least one concern or question",
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
