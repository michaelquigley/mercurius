package claude

import (
	"errors"
	"strings"
	"testing"
)

func TestParseEnvelope(t *testing.T) {
	tests := []struct {
		name        string
		stdout      string
		wantRaw     string
		wantErr     bool
		errContains string
		errIsNotEnv bool
	}{
		{
			name:    "structured output present",
			stdout:  `{"is_error":false,"subtype":"success","result":"42","structured_output":{"verdict":"ready_to_build"},"total_cost_usd":0.1}`,
			wantRaw: `{"verdict":"ready_to_build"}`,
		},
		{
			name:    "structured output absent falls back to result text",
			stdout:  `{"is_error":false,"subtype":"success","result":"here you go {\"verdict\":\"needs_changes\"} done"}`,
			wantRaw: `{"verdict":"needs_changes"}`,
		},
		{
			name:    "structured output as array falls back to result",
			stdout:  `{"is_error":false,"structured_output":[1,2],"result":"{\"verdict\":\"ready_to_build\"}"}`,
			wantRaw: `{"verdict":"ready_to_build"}`,
		},
		{
			name:        "is_error not logged in",
			stdout:      `{"is_error":true,"subtype":"success","result":"Not logged in · Please run /login"}`,
			wantErr:     true,
			errContains: "Not logged in",
		},
		{
			name:        "is_error names subtype",
			stdout:      `{"is_error":true,"subtype":"error_max_structured_output_retries","result":"failed to produce schema-valid output"}`,
			wantErr:     true,
			errContains: "error_max_structured_output_retries",
		},
		{
			name:        "non envelope output",
			stdout:      "not json at all",
			wantErr:     true,
			errIsNotEnv: true,
		},
		{
			name:        "empty output",
			stdout:      "   \n\t ",
			wantErr:     true,
			errIsNotEnv: true,
		},
		{
			name:        "success but no json object anywhere",
			stdout:      `{"is_error":false,"subtype":"success","result":"sorry, no structured answer"}`,
			wantErr:     true,
			errContains: "no json object",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, _, err := parseEnvelope([]byte(test.stdout))
			if test.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if test.errContains != "" && !strings.Contains(err.Error(), test.errContains) {
					t.Fatalf("error %q does not contain %q", err, test.errContains)
				}
				if test.errIsNotEnv && !errors.Is(err, errNotEnvelope) {
					t.Fatalf("expected errNotEnvelope, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(raw) != test.wantRaw {
				t.Fatalf("raw mismatch: got %s want %s", raw, test.wantRaw)
			}
		})
	}
}
