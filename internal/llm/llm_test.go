package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// compactJSON strips insignificant whitespace so a recorded fixture (which
// Record pretty-prints for human review) can be compared against a compact
// literal without the test being sensitive to formatting.
func compactJSON(t *testing.T, b json.RawMessage) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		t.Fatalf("compactJSON: %v", err)
	}
	return buf.String()
}

var personSchema = json.RawMessage(`{
  "type": "object",
  "properties": {"name": {"type": "string"}, "age": {"type": "integer"}},
  "required": ["name", "age"],
  "additionalProperties": false
}`)

func TestValidateAcceptsConformingDocument(t *testing.T) {
	if err := Validate(personSchema, json.RawMessage(`{"name":"a","age":3}`)); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestValidateRejectsMissingField(t *testing.T) {
	err := Validate(personSchema, json.RawMessage(`{"name":"a"}`))
	if err == nil {
		t.Fatal("want error for missing required field")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("error %q should mention the schema", err)
	}
}

func TestValidateRejectsWrongType(t *testing.T) {
	if err := Validate(personSchema, json.RawMessage(`{"name":"a","age":"three"}`)); err == nil {
		t.Fatal("want error for wrong type")
	}
}

func TestCacheKeyChangesWithEveryInput(t *testing.T) {
	base := Request{Stage: "fit", Prompt: "p", Schema: personSchema}
	k := CacheKey("gemini", "m", base)

	other := base
	other.Prompt = "p2"
	if CacheKey("gemini", "m", other) == k {
		t.Error("prompt change should change the key")
	}
	if CacheKey("gemini", "m2", base) == k {
		t.Error("model change should change the key")
	}
	if CacheKey("fixture", "m", base) == k {
		t.Error("provider change should change the key")
	}
	if CacheKey("gemini", "m", base) != k {
		t.Error("identical inputs should produce identical keys")
	}
}

func TestFixtureRoundTrip(t *testing.T) {
	dir := t.TempDir()
	f := NewFixture(dir)

	r := Request{Stage: "profile", Prompt: "anything", Schema: personSchema,
		FixtureKey: "profile-abc12345"}
	want := json.RawMessage(`{"name":"a","age":3}`)

	if err := f.Record(r, want); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := f.Complete(context.Background(), r)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if compactJSON(t, got) != compactJSON(t, want) {
		t.Errorf("got %s, want %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "profile-abc12345.json")); err != nil {
		t.Errorf("fixture file not written: %v", err)
	}
}

// A fixture replay is stable across prompt edits: the same brief keeps working
// after the prompt wording changes.
func TestFixtureIgnoresPromptChanges(t *testing.T) {
	dir := t.TempDir()
	f := NewFixture(dir)
	r := Request{Stage: "fit", Prompt: "v1", Schema: personSchema, FixtureKey: "fit-deadbeef"}
	if err := f.Record(r, json.RawMessage(`{"name":"a","age":3}`)); err != nil {
		t.Fatal(err)
	}
	r.Prompt = "v2 — reworded"
	if _, err := f.Complete(context.Background(), r); err != nil {
		t.Errorf("prompt edit should not invalidate a fixture: %v", err)
	}
}

func TestFixtureMissingIsAClearError(t *testing.T) {
	f := NewFixture(t.TempDir())
	_, err := f.Complete(context.Background(),
		Request{Stage: "fit", FixtureKey: "fit-nothere"})
	if err == nil {
		t.Fatal("want error for a missing fixture")
	}
	var miss *ErrNoFixture
	if !errors.As(err, &miss) {
		t.Fatalf("error %v should be a typed *ErrNoFixture so a chain can tell an "+
			"absence from a real read failure", err)
	}
	if !strings.Contains(err.Error(), "fit-nothere.json") {
		t.Errorf("error %q should name the path it looked for", err)
	}
}

func TestFixtureName(t *testing.T) {
	if got := NewFixture(t.TempDir()).Name(); got != "fixture" {
		t.Errorf("Name = %q, want fixture", got)
	}
}
