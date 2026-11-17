package mcpserver

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/michaelquigley/mercurius/internal/broker"
	"github.com/michaelquigley/mercurius/internal/runlock"
	"github.com/michaelquigley/mercurius/internal/schema"
	"github.com/michaelquigley/mercurius/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRawToolHandlerNormalizesEmptyArguments(t *testing.T) {
	openSchema, err := objectSchema(nil).Resolve(nil)
	if err != nil {
		t.Fatalf("resolve open session schema: %v", err)
	}
	statusSchema, err := sessionIDInputSchema().Resolve(nil)
	if err != nil {
		t.Fatalf("resolve session status schema: %v", err)
	}
	tests := []struct {
		name string
		args json.RawMessage
	}{
		{name: "absent"},
		{name: "null", args: json.RawMessage(" null ")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: test.args}}
			open := newToolHandler(func(context.Context, OpenSessionInput) (OpenSessionOutput, error) {
				return OpenSessionOutput{SessionID: "s_abcdefghijkl"}, nil
			}, openSchema)
			result, err := open(t.Context(), req)
			if err != nil || result.IsError || result.StructuredContent == nil {
				t.Fatalf("expected empty open_session arguments to succeed, got %+v, err=%v", result, err)
			}

			status := newToolHandler(func(context.Context, SessionStatusInput) (SessionStatusOutput, error) {
				t.Fatal("session_status handler ran without a session id")
				return SessionStatusOutput{}, nil
			}, statusSchema)
			result, err = status(t.Context(), req)
			if err != nil {
				t.Fatalf("handle session_status: %v", err)
			}
			checkWireArgumentAnswer(t, result, wireArgumentAnswer{
				message: "arguments failed schema validation",
				cause:   `validating root: required: missing properties: ["session_id"]`,
			})
		})
	}
}

func TestSuccessResultPreservesRawReviewNumbers(t *testing.T) {
	review := map[string]any{
		"decimal": json.Number("1.50"),
		"integer": json.Number("9007199254740993"),
	}
	result, err := successResult(ReviewerOutput{Raw: review})
	if err != nil {
		t.Fatalf("encode reviewer result: %v", err)
	}
	raw, ok := result.StructuredContent.(json.RawMessage)
	if !ok {
		t.Fatalf("expected encoded structured content, got %T", result.StructuredContent)
	}
	data := decodeWireJSON(t, raw).(map[string]any)
	if !reflect.DeepEqual(data["raw"], review) {
		t.Fatalf("raw review = %#v, want %#v", data["raw"], review)
	}
	if content, ok := result.Content[0].(*mcp.TextContent); !ok || content.Text != string(raw) {
		t.Fatalf("text and structured content differ: %+v, %s", result.Content[0], raw)
	}
}

func TestProjectPayloadsHaveNoJSONTags(t *testing.T) {
	roots := []any{
		store.Session{}, store.Run{}, store.State{}, store.Review{}, store.Notes{}, runlock.Note{},
		schema.ReviewOutput{}, broker.ReviewerResult{},
		OpenSessionInput{}, StartRoundInput{}, CollectRoundInput{}, RecordRoundNotesInput{}, CloseSessionInput{}, SessionStatusInput{},
		OpenSessionOutput{}, StartReviewRoundOutput{}, CollectedRoundOutput{}, RecordRoundNotesOutput{}, CloseSessionOutput{}, SessionStatusOutput{},
		ToolErrorOutput{},
	}
	seen := make(map[reflect.Type]bool)
	var check func(reflect.Type)
	check = func(typ reflect.Type) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			check(typ.Elem())
		case reflect.Map:
			check(typ.Key())
			check(typ.Elem())
		case reflect.Struct:
			if !strings.HasPrefix(typ.PkgPath(), "github.com/michaelquigley/mercurius/internal/") {
				return
			}
			for i := range typ.NumField() {
				field := typ.Field(i)
				if tag, exists := field.Tag.Lookup("json"); exists {
					t.Errorf("%s.%s carries a json tag '%s'", typ, field.Name, tag)
				}
				check(field.Type)
			}
		}
	}
	for _, root := range roots {
		check(reflect.TypeOf(root))
	}
}
