package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/michaelquigley/df/dd"
	"github.com/michaelquigley/mercurius/internal/broker"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type tool[In, Out any] struct {
	name        string
	description string
	input       *jsonschema.Schema
	run         func(context.Context, In) (Out, error)
}

// register resolves the input schema once and installs a raw SDK handler.
func register[In, Out any](server *mcp.Server, t tool[In, Out]) error {
	input, err := t.input.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		return fmt.Errorf("resolve input schema for '%s': %w", t.name, err)
	}
	server.AddTool(&mcp.Tool{
		Name:        t.name,
		Description: t.description,
		InputSchema: t.input,
	}, newToolHandler(t.run, input))
	return nil
}

func newToolHandler[In, Out any](run func(context.Context, In) (Out, error), input *jsonschema.Resolved) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := ctx.Err(); err != nil {
			return nil, rpcError(err)
		}
		args := bytes.TrimSpace(req.Params.Arguments)
		if len(args) == 0 || bytes.Equal(args, []byte("null")) {
			args = []byte("{}")
		}
		decoded, err := dd.DecodeStrictJSON(args)
		if err != nil {
			return argumentError("malformed arguments", err)
		}
		schemaData, err := schemaValue(decoded)
		if err != nil {
			return argumentError("arguments failed schema validation", err)
		}
		if err := input.Validate(schemaData); err != nil {
			return argumentError("arguments failed schema validation", err)
		}
		var in In
		if err := dd.Bind(&in, decoded, dd.Strict()); err != nil {
			return argumentError("bind arguments", err)
		}

		out, err := run(ctx, in)
		if err != nil {
			return toolErrorResult(err)
		}
		return successResult(out)
	}
}

// schemaValue copies the JSON tree with native Go numbers for jsonschema-go,
// which classifies json.Number as a string. strict binding uses the original
// tree to check each number's spelling.
func schemaValue(value any) (any, error) {
	switch value := value.(type) {
	case map[string]any:
		copy := make(map[string]any, len(value))
		for key, entry := range value {
			converted, err := schemaValue(entry)
			if err != nil {
				return nil, err
			}
			copy[key] = converted
		}
		return copy, nil
	case []any:
		copy := make([]any, len(value))
		for i, entry := range value {
			converted, err := schemaValue(entry)
			if err != nil {
				return nil, err
			}
			copy[i] = converted
		}
		return copy, nil
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			return integer, nil
		}
		return value.Float64()
	default:
		return value, nil
	}
}

func argumentError(message string, cause error) (*mcp.CallToolResult, error) {
	return toolErrorResult(&broker.Error{Code: broker.CodeUserError, Message: message, Err: cause})
}

func successResult[Out any](out Out) (*mcp.CallToolResult, error) {
	data, err := dd.Unbind(out)
	if err != nil {
		return nil, rpcError(&broker.Error{Code: broker.CodeInternalError, Message: "unbind tool output", Err: err})
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, rpcError(&broker.Error{Code: broker.CodeInternalError, Message: "encode tool output", Err: err})
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(raw)}},
		StructuredContent: json.RawMessage(raw),
	}, nil
}

func toolErrorResult(err error) (*mcp.CallToolResult, error) {
	var brokerErr *broker.Error
	if !errors.As(err, &brokerErr) {
		return nil, rpcError(err)
	}

	output := errorOutput(broker.ErrorInfoFrom(err))
	structured, err := dd.Unbind(ToolErrorOutput{Error: output})
	if err != nil {
		return nil, rpcError(&broker.Error{Code: broker.CodeInternalError, Message: "unbind tool error", Err: err})
	}
	text := fmt.Sprintf("%s: %s", output.Code, output.Message)
	if cause, ok := output.Details["cause"].(string); ok && cause != "" {
		text += "\ncause: " + cause
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: structured,
		IsError:           true,
	}, nil
}

func rpcError(err error) error {
	var brokerErr *broker.Error
	if errors.As(err, &brokerErr) {
		code := int64(jsonrpc.CodeInvalidParams)
		if brokerErr.Code == broker.CodeInternalError {
			code = jsonrpc.CodeInternalError
		}
		return wireError(code, brokerErr.Code, brokerErr.Message, brokerErr.Details, brokerErr.Err)
	}
	return wireError(jsonrpc.CodeInternalError, broker.CodeInternalError, "internal error", nil, err)
}

func wireError(code int64, stableCode string, message string, details map[string]any, cause error) *jsonrpc.Error {
	payloadDetails := cloneDetails(details)
	if cause != nil {
		payloadDetails["cause"] = cause.Error()
	}

	data, err := dd.Unbind(ToolErrorOutput{
		Error: ErrorOutput{Code: stableCode, Message: message, Details: payloadDetails},
	})
	var raw []byte
	if err == nil {
		raw, err = json.Marshal(data)
	}
	if err != nil {
		raw = []byte(`{"error":{"code":"internal_error","message":"internal error","details":{"cause":"error payload marshal failed"}}}`)
	}
	return &jsonrpc.Error{
		Code:    code,
		Message: message,
		Data:    json.RawMessage(raw),
	}
}
