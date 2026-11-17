package mcpserver

import "github.com/google/jsonschema-go/jsonschema"

func objectSchema(properties map[string]*jsonschema.Schema, required ...string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:                 "object",
		Properties:           properties,
		Required:             required,
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}
}

func startRoundInputSchema() *jsonschema.Schema {
	return objectSchema(map[string]*jsonschema.Schema{
		"session_id": {Type: "string"},
		"artifacts": {
			Type:     "array",
			MinItems: jsonschema.Ptr(1),
			Items: objectSchema(map[string]*jsonschema.Schema{
				"name": {Type: "string"},
				"path": {Type: "string"},
			}, "name", "path"),
		},
	}, "session_id", "artifacts")
}

func collectRoundInputSchema() *jsonschema.Schema {
	return objectSchema(map[string]*jsonschema.Schema{
		"session_id":   {Type: "string"},
		"round_number": {Type: "integer", Minimum: jsonschema.Ptr(1.0)},
	}, "session_id")
}

func recordRoundNotesInputSchema() *jsonschema.Schema {
	return objectSchema(map[string]*jsonschema.Schema{
		"session_id": {
			Type:        "string",
			Description: "the session id returned by open_session.",
		},
		"round_number": {
			Type:        "integer",
			Minimum:     jsonschema.Ptr(1.0),
			Description: "the round number whose notes are being recorded.",
		},
		"commentary": {
			Type:        "string",
			Description: "free-form human commentary for the round.",
		},
		"decisions": {
			Type:        "array",
			Description: "one entry per reviewer finding triaged this round; include the full array on the first call rather than a later one.",
			Items: objectSchema(map[string]*jsonschema.Schema{
				"ref": {
					Type:        "string",
					Description: "the reviewer finding id this decision is about (a concern, question, or advisory id from the round).",
				},
				"disposition": {
					Type:        "string",
					Enum:        []any{"fixed", "rejected", "deferred"},
					Description: "how the finding was handled: 'fixed' (addressed in the artifacts), 'rejected' (declined, e.g. out of scope), or 'deferred' (acknowledged, to be handled later).",
				},
				"note": {
					Type:        "string",
					Description: "optional human note explaining the decision.",
				},
			}, "ref", "disposition"),
		},
	}, "session_id", "round_number")
}

func sessionIDInputSchema() *jsonschema.Schema {
	return objectSchema(map[string]*jsonschema.Schema{
		"session_id": {Type: "string"},
	}, "session_id")
}
