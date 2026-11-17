package mcpserver

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/michaelquigley/mercurius/internal/broker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type wireArgumentAnswer struct {
	round   int
	text    string
	code    string
	details map[string]any
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
	sdkError := func(text string) wireArgumentAnswer {
		return wireArgumentAnswer{text: `validating "arguments": ` + text}
	}
	unknownKey := sdkError(`validating root: unexpected additional properties ["unknown"]`)
	missingSession := wireArgumentAnswer{
		text:    "not_found: session not found",
		code:    broker.CodeNotFound,
		details: map[string]any{"session_id": ""},
	}
	missingRound := wireArgumentAnswer{
		text:    "not_found: round not found",
		code:    broker.CodeNotFound,
		details: map[string]any{"round_number": float64(0)},
	}
	nullArtifacts := wireArgumentAnswer{
		text:    "user_error: invalid artifacts\ncause: artifact list is empty",
		code:    broker.CodeUserError,
		details: map[string]any{"cause": "artifact list is empty"},
	}
	sessionWrongType := sdkError(`validating root: validating /properties/session_id: type: 5 has type "integer", want "string"`)
	sessionNull := sdkError(`validating root: validating /properties/session_id: type: <invalid reflect.Value> has type "null", want "string"`)
	roundWrongType := sdkError(`validating root: validating /properties/round_number: type: 1 has type "string", want "integer"`)

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
			{"wrong_type", `[]`, sdkError(`unmarshaling arguments: json: cannot unmarshal "[]" into Go value of type map[string]interface {}`)},
			{"null_arguments", `null`, wireArgumentAnswer{}},
		}},
		{"start_review_round", []argumentCase{
			{"unknown_key", `{"session_id":{{session}},"artifacts":[{"name":"design.md","path":{{artifact}}}],"unknown":true}`, unknownKey},
			{"wrong_type", `{"session_id":{{session}},"artifacts":"invalid"}`, sdkError(`validating root: validating /properties/artifacts: type: invalid has type "string", want one of "null, array"`)},
			{"missing_session_id", `{"artifacts":[{"name":"design.md","path":{{artifact}}}]}`, missingSession},
			{"explicit_null", `{"session_id":{{session}},"artifacts":null}`, nullArtifacts},
		}},
		{"collect_round", []argumentCase{
			{"unknown_key", `{"session_id":{{session}},"round_number":1,"unknown":true}`, unknownKey},
			{"wrong_type", `{"session_id":{{session}},"round_number":"1"}`, roundWrongType},
			{"missing_session_id", `{"round_number":1}`, missingSession},
			{"missing_round_number", `{"session_id":{{session}}}`, wireArgumentAnswer{round: 2}},
			{"explicit_null", `{"session_id":{{session}},"round_number":null}`, sdkError(`validating root: validating /properties/round_number: type: <invalid reflect.Value> has type "null", want "integer"`)},
			{"zero_round_number", `{"session_id":{{session}},"round_number":0}`, wireArgumentAnswer{round: 2}},
			{"decimal_round_number", `{"session_id":{{session}},"round_number":1.0}`, wireArgumentAnswer{round: 1}},
			{"exponent_round_number", `{"session_id":{{session}},"round_number":1e0}`, wireArgumentAnswer{round: 1}},
		}},
		{"record_round_notes", []argumentCase{
			{"unknown_key", `{"session_id":{{session}},"round_number":1,"commentary":"reviewed","unknown":true}`, wireArgumentAnswer{round: 1}},
			{"wrong_type", `{"session_id":{{session}},"round_number":"1","commentary":"reviewed"}`, roundWrongType},
			{"missing_session_id", `{"round_number":1,"commentary":"reviewed"}`, missingSession},
			{"missing_round_number", `{"session_id":{{session}},"commentary":"reviewed"}`, missingRound},
			{"explicit_null", `{"session_id":{{session}},"round_number":1,"commentary":null}`, sdkError(`validating root: validating /properties/commentary: type: <invalid reflect.Value> has type "null", want "string"`)},
			{"zero_round_number", `{"session_id":{{session}},"round_number":0,"commentary":"reviewed"}`, missingRound},
			{"decimal_round_number", `{"session_id":{{session}},"round_number":1.0,"commentary":"reviewed"}`, wireArgumentAnswer{round: 1}},
			{"exponent_round_number", `{"session_id":{{session}},"round_number":1e0,"commentary":"reviewed"}`, wireArgumentAnswer{round: 1}},
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
	if want.text == "" {
		if result.IsError || result.StructuredContent == nil {
			t.Fatalf("expected successful tool result, got %+v", result)
		}
		if want.round > 0 {
			requireWireRound(t, result, want.round)
		}
		return
	}

	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("expected one tool error message, got %+v", result)
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok || content.Text != want.text {
		t.Fatalf("tool error content = %+v, want '%s'", result.Content[0], want.text)
	}
	if want.code == "" {
		if result.StructuredContent != nil {
			t.Fatalf("expected a text-only sdk error, got structured content %+v", result.StructuredContent)
		}
		return
	}
	payload := assertToolError(t, result, nil, want.code)
	if !reflect.DeepEqual(payload.Details, want.details) {
		t.Fatalf("tool error details = %+v, want %+v", payload.Details, want.details)
	}
}
