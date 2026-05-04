package schema

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

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

// ValidateReviewOutput validates raw reviewer output against the canonical schema.
func ValidateReviewOutput(raw json.RawMessage) error {
	compiled, err := reviewOutputSchema()
	if err != nil {
		return err
	}

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("invalid review output json: %w", err)
	}
	if err := compiled.Validate(inst); err != nil {
		return fmt.Errorf("review output schema violation: %w", err)
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
