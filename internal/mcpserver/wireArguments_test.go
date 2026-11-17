package mcpserver

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/michaelquigley/mercurius/internal/broker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type wireArgumentAnswer struct {
	round   int
	message string
	cause   string
}

func TestWireArguments(t *testing.T) {
	ctx, client, _ := newTestClientWith(t, fixedReviewer{raw: json.RawMessage(wireReviewWithoutFindings)}, baseTestConfig)
	sessionID, artifactPath := openTestSession(t, ctx, client)
	startAndCollectRoundTool(t, ctx, client, sessionID, artifactPath)
	startAndCollectRoundTool(t, ctx, client, sessionID, artifactPath)

	replacer := strings.NewReplacer(
		"{{session}}", string(marshalWireJSON(t, sessionID)),
		"{{artifact}}", string(marshalWireJSON(t, artifactPath)),
	)
	schemaError := func(cause string) wireArgumentAnswer {
		return wireArgumentAnswer{message: "arguments failed schema validation", cause: cause}
	}
	roundBindError := func(inputType string, lexeme string) wireArgumentAnswer {
		field := inputType + ".RoundNumber"
		return wireArgumentAnswer{
			message: "bind arguments",
			cause:   fmt.Sprintf(`binding field %s from key "round_number": %s: number %s is not an integer`, field, field, lexeme),
		}
	}
	unknownKey := schemaError(`validating root: unexpected additional properties ["unknown"]`)
	missingSession := schemaError(`validating root: required: missing properties: ["session_id"]`)
	missingRound := schemaError(`validating root: required: missing properties: ["round_number"]`)
	zeroRound := schemaError(`validating root: validating /properties/round_number: minimum: 0/1 is less than 1.000000`)
	nullArtifacts := schemaError(`validating root: validating /properties/artifacts: type: <invalid reflect.Value> has type "null", want "array"`)
	sessionWrongType := schemaError(`validating root: validating /properties/session_id: type: 5 has type "integer", want "string"`)
	sessionNull := schemaError(`validating root: validating /properties/session_id: type: <invalid reflect.Value> has type "null", want "string"`)
	roundWrongType := schemaError(`validating root: validating /properties/round_number: type: 1 has type "string", want "integer"`)

	type argumentCase struct {
		name string
		args string
		want wireArgumentAnswer
	}
	sessionCases := []argumentCase{
		{"unknown_key", `{"session_id":{{session}},"unknown":true}`, unknownKey},
		{"wrong_type", `{"session_id":5}`, sessionWrongType},
		{"missing_session_id", `{}`, missingSession},
		{"explicit_null", `{"session_id":null}`, sessionNull},
	}
	tools := []struct {
		name  string
		cases []argumentCase
	}{
		{"open_session", []argumentCase{
			{"unknown_key", `{"unknown":true}`, unknownKey},
			{"wrong_type", `[]`, wireArgumentAnswer{message: "malformed arguments", cause: "$: expected object at top level, got []interface {}"}},
			{"null_arguments", `null`, wireArgumentAnswer{}},
		}},
		{"start_review_round", []argumentCase{
			{"unknown_key", `{"session_id":{{session}},"artifacts":[{"name":"design.md","path":{{artifact}}}],"unknown":true}`, unknownKey},
			{"wrong_type", `{"session_id":{{session}},"artifacts":"invalid"}`, schemaError(`validating root: validating /properties/artifacts: type: invalid has type "string", want "array"`)},
			{"missing_session_id", `{"artifacts":[{"name":"design.md","path":{{artifact}}}]}`, missingSession},
			{"explicit_null", `{"session_id":{{session}},"artifacts":null}`, nullArtifacts},
		}},
		{"collect_round", []argumentCase{
			{"unknown_key", `{"session_id":{{session}},"round_number":1,"unknown":true}`, unknownKey},
			{"wrong_type", `{"session_id":{{session}},"round_number":"1"}`, roundWrongType},
			{"missing_session_id", `{"round_number":1}`, missingSession},
			{"missing_round_number", `{"session_id":{{session}}}`, wireArgumentAnswer{round: 2}},
			{"explicit_null", `{"session_id":{{session}},"round_number":null}`, schemaError(`validating root: validating /properties/round_number: type: <invalid reflect.Value> has type "null", want "integer"`)},
			{"zero_round_number", `{"session_id":{{session}},"round_number":0}`, zeroRound},
			{"integer_round_number", `{"session_id":{{session}},"round_number":1}`, wireArgumentAnswer{round: 1}},
			{"decimal_round_number", `{"session_id":{{session}},"round_number":1.0}`, roundBindError("CollectRoundInput", "1.0")},
			{"exponent_round_number", `{"session_id":{{session}},"round_number":1e0}`, roundBindError("CollectRoundInput", "1e0")},
		}},
		{"record_round_notes", []argumentCase{
			{"unknown_key", `{"session_id":{{session}},"round_number":1,"commentary":"reviewed","unknown":true}`, unknownKey},
			{"wrong_type", `{"session_id":{{session}},"round_number":"1","commentary":"reviewed"}`, roundWrongType},
			{"missing_session_id", `{"round_number":1,"commentary":"reviewed"}`, missingSession},
			{"missing_round_number", `{"session_id":{{session}},"commentary":"reviewed"}`, missingRound},
			{"explicit_null", `{"session_id":{{session}},"round_number":1,"commentary":null}`, schemaError(`validating root: validating /properties/commentary: type: <invalid reflect.Value> has type "null", want "string"`)},
			{"zero_round_number", `{"session_id":{{session}},"round_number":0,"commentary":"reviewed"}`, zeroRound},
			{"integer_round_number", `{"session_id":{{session}},"round_number":1,"commentary":"reviewed"}`, wireArgumentAnswer{round: 1}},
			{"decimal_round_number", `{"session_id":{{session}},"round_number":1.0,"commentary":"reviewed"}`, roundBindError("RecordRoundNotesInput", "1.0")},
			{"exponent_round_number", `{"session_id":{{session}},"round_number":1e0,"commentary":"reviewed"}`, roundBindError("RecordRoundNotesInput", "1e0")},
		}},
		{"close_session", sessionCases},
		{"session_status", sessionCases},
	}

	for _, tool := range tools {
		t.Run(tool.name, func(t *testing.T) {
			for _, tc := range tool.cases {
				t.Run(tc.name, func(t *testing.T) {
					// raw arguments keep '1.0' and '1e0' on the client side of the wire.
					args := json.RawMessage(replacer.Replace(tc.args))
					result := callWireTool(t, ctx, client, tool.name, args)
					checkWireArgumentAnswer(t, result, tc.want)
				})
			}
		})
	}
}

func checkWireArgumentAnswer(t *testing.T, result *mcp.CallToolResult, want wireArgumentAnswer) {
	t.Helper()
	if want.message == "" {
		if result.IsError || result.StructuredContent == nil {
			t.Fatalf("expected successful tool result, got %+v", result)
		}
		if want.round > 0 {
			requireWireRound(t, result, want.round)
		}
		return
	}

	payload := assertToolError(t, result, nil, broker.CodeUserError)
	wantDetails := map[string]any{"cause": want.cause}
	if payload.Message != want.message || !reflect.DeepEqual(payload.Details, wantDetails) {
		t.Fatalf("tool error = %+v, want message '%s' and details %+v", payload, want.message, wantDetails)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected one tool error message, got %+v", result)
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	wantText := "user_error: " + want.message + "\ncause: " + want.cause
	if !ok || content.Text != wantText {
		t.Fatalf("tool error content = %+v, want '%s'", result.Content[0], wantText)
	}
}
