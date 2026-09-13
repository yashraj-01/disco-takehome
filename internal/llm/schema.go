package llm

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Validate checks doc against schema. The same schema file is the model's
// response contract and this validator, so a drifting prompt cannot quietly
// produce a shape the pipeline does not expect.
func Validate(schema, doc json.RawMessage) error {
	compiler := jsonschema.NewCompiler()

	sch, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return fmt.Errorf("llm: parsing schema: %w", err)
	}
	if err := compiler.AddResource("schema.json", sch); err != nil {
		return fmt.Errorf("llm: adding schema: %w", err)
	}
	compiled, err := compiler.Compile("schema.json")
	if err != nil {
		return fmt.Errorf("llm: compiling schema: %w", err)
	}

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return fmt.Errorf("llm: parsing response: %w", err)
	}
	if err := compiled.Validate(inst); err != nil {
		return fmt.Errorf("llm: response does not satisfy schema: %w", err)
	}
	return nil
}
