package llm

import (
	"encoding/json"
	"testing"

	"github.com/yashraj/disco/prompts"
	"google.golang.org/genai"
)

func TestToGenaiSchemaConvertsTypes(t *testing.T) {
	raw := json.RawMessage(`{
	  "type": "object",
	  "properties": {
	    "name": {"type": "string", "description": "who"},
	    "count": {"type": "integer"},
	    "score": {"type": "number"},
	    "ok": {"type": "boolean"},
	    "verdict": {"type": "string", "enum": ["a","b"]},
	    "tags": {"type": "array", "items": {"type": "string"}}
	  },
	  "required": ["name","count"],
	  "additionalProperties": false
	}`)

	got, err := toGenaiSchema(raw)
	if err != nil {
		t.Fatalf("toGenaiSchema: %v", err)
	}
	if got.Type != genai.TypeObject {
		t.Errorf("type = %v, want OBJECT", got.Type)
	}
	if got.Properties["name"].Type != genai.TypeString {
		t.Errorf("name type = %v, want STRING", got.Properties["name"].Type)
	}
	if got.Properties["count"].Type != genai.TypeInteger {
		t.Errorf("count type = %v, want INTEGER", got.Properties["count"].Type)
	}
	if got.Properties["score"].Type != genai.TypeNumber {
		t.Errorf("score type = %v, want NUMBER", got.Properties["score"].Type)
	}
	if got.Properties["ok"].Type != genai.TypeBoolean {
		t.Errorf("ok type = %v, want BOOLEAN", got.Properties["ok"].Type)
	}
	if got.Properties["tags"].Items == nil ||
		got.Properties["tags"].Items.Type != genai.TypeString {
		t.Error("tags items should be STRING")
	}
	if len(got.Properties["verdict"].Enum) != 2 {
		t.Errorf("verdict enum = %v, want 2 values", got.Properties["verdict"].Enum)
	}
	if len(got.Required) != 2 {
		t.Errorf("required = %v, want 2", got.Required)
	}
}

func TestToGenaiSchemaRejectsUnknownType(t *testing.T) {
	if _, err := toGenaiSchema(json.RawMessage(`{"type":"tuple"}`)); err == nil {
		t.Fatal("want error for an unsupported type")
	}
}

// Every shipped schema file must survive conversion, or a stage fails at
// runtime rather than at build time.
func TestShippedSchemasConvert(t *testing.T) {
	for _, stage := range []string{"profile", "fit", "personas", "creative", "judge"} {
		_, schema, err := prompts.Load(stage)
		if err != nil {
			t.Fatalf("prompts.Load(%q): %v", stage, err)
		}
		if _, err := toGenaiSchema(schema); err != nil {
			t.Errorf("%s.schema.json does not convert: %v", stage, err)
		}
	}
}
