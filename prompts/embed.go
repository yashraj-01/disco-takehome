// Package prompts embeds the prompt text and response schemas. It lives inside
// prompts/ because go:embed cannot read outside its own directory, and the
// exercise asks for every prompt to be reviewable at prompts/ in the repo root.
package prompts

import (
	"embed"
	"encoding/json"
	"fmt"
)

//go:embed *.md *.schema.json
var files embed.FS

// Load returns the prompt text and response schema for a pipeline stage.
func Load(stage string) (string, json.RawMessage, error) {
	text, err := files.ReadFile(stage + ".md")
	if err != nil {
		return "", nil, fmt.Errorf("prompts: %w", err)
	}
	schema, err := files.ReadFile(stage + ".schema.json")
	if err != nil {
		return "", nil, fmt.Errorf("prompts: %w", err)
	}
	return string(text), json.RawMessage(schema), nil
}
