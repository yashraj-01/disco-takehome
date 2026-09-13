package llm

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// A hand-edited or corrupt cache file must never be served straight through:
// it bypasses the validate-and-repair guarantee this layer exists to
// provide. It must be treated as a miss (so the caller regenerates), not
// returned and not treated as fatal.
func TestReadCacheTreatsSchemaViolationAsMiss(t *testing.T) {
	dir := t.TempDir()
	g := &Gemini{model: "m", cacheDir: dir}
	r := Request{Stage: "profile", Prompt: "p", Schema: personSchema}

	path := filepath.Join(dir, CacheKey(g.Name(), g.model, r)+".json")
	// Missing the required "age" field — violates personSchema.
	if err := os.WriteFile(path, []byte(`{"name":"a"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, ok := g.readCache(r); ok {
		t.Error("readCache should treat a schema-violating cache entry as a miss, not a hit")
	}
}

// Cache misses on entirely malformed JSON must also fall through quietly,
// not be reported as an error to the caller.
func TestReadCacheTreatsMalformedJSONAsMiss(t *testing.T) {
	dir := t.TempDir()
	g := &Gemini{model: "m", cacheDir: dir}
	r := Request{Stage: "profile", Prompt: "p", Schema: personSchema}

	path := filepath.Join(dir, CacheKey(g.Name(), g.model, r)+".json")
	if err := os.WriteFile(path, []byte(`{"name":`), 0o644); err != nil { // truncated
		t.Fatal(err)
	}

	if _, ok := g.readCache(r); ok {
		t.Error("readCache should treat truncated/malformed JSON as a miss")
	}
}

// writeCache must write via a temp file plus rename rather than truncating
// the target in place, so a crash mid-write can never leave a partial cache
// entry behind. This checks the round trip and that no stray temp file is
// left in the cache directory once the write completes — a leftover would
// mean the rename step was skipped or forgotten.
func TestWriteCacheAtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	g := &Gemini{model: "m", cacheDir: dir}
	r := Request{Stage: "profile", Prompt: "p", Schema: personSchema}
	out := json.RawMessage(`{"name":"a","age":3}`)

	g.writeCache(r, out)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("cache dir has %d entries, want exactly 1 (no leftover temp file): %v", len(entries), entries)
	}
	if entries[0].Name() != CacheKey(g.Name(), g.model, r)+".json" {
		t.Errorf("unexpected cache dir entry %q, want the final cache file only", entries[0].Name())
	}

	got, ok := g.readCache(r)
	if !ok {
		t.Fatal("expected a cache hit after writeCache")
	}
	if compactJSON(t, got) != compactJSON(t, out) {
		t.Errorf("got %s, want %s", got, out)
	}
}

// An array schema with no "items" would otherwise convert to a genai.Schema
// with a nil Items field, deferring the failure to a live API call instead of
// catching it at conversion time.
func TestConvertRejectsArrayWithoutItems(t *testing.T) {
	if _, err := toGenaiSchema(json.RawMessage(`{"type":"array"}`)); err == nil {
		t.Fatal("want error for an array schema missing items")
	} else if !strings.Contains(err.Error(), "items") {
		t.Errorf("error %q should mention items", err)
	}
}

// The same check, nested under a property, must still name that property so
// the error points at the offending part of the schema file.
func TestConvertRejectsNestedArrayWithoutItems(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"tags":{"type":"array"}}}`)
	_, err := toGenaiSchema(raw)
	if err == nil {
		t.Fatal("want error for a nested array schema missing items")
	}
	if !strings.Contains(err.Error(), `"tags"`) {
		t.Errorf("error %q should name the offending property", err)
	}
	if !strings.Contains(err.Error(), "items") {
		t.Errorf("error %q should mention items", err)
	}
}

// jsonSchema.Enum used to be typed []string, so a non-string enum value (e.g.
// on an integer type) failed the top-level json.Unmarshal with a generic type
// mismatch instead of a clear, schema-scoped error.
func TestConvertRejectsNonStringEnum(t *testing.T) {
	_, err := toGenaiSchema(json.RawMessage(`{"type":"integer","enum":[1,2]}`))
	if err == nil {
		t.Fatal("want error for non-string enum values")
	}
	// A generic json.Unmarshal type-mismatch error also happens to contain
	// the word "string" (as in "...of type string"), so this checks for the
	// specific, deliberate wording rather than that incidental substring —
	// otherwise this test would pass even against the unfixed code.
	if !strings.Contains(err.Error(), "only string enum values are supported") {
		t.Errorf("error %q should clearly say only string enums are supported", err)
	}
}

func TestConvertRejectsNonStringEnumNamesProperty(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"score":{"type":"integer","enum":[1,2]}}}`)
	_, err := toGenaiSchema(raw)
	if err == nil {
		t.Fatal("want error for a nested non-string enum")
	}
	if !strings.Contains(err.Error(), `"score"`) {
		t.Errorf("error %q should name the offending property", err)
	}
}

// isRateLimited must recognize the SDK's typed genai.APIError by status code,
// not just by sniffing the error string — a message that happens not to
// contain "429" or "RESOURCE_EXHAUSTED" would otherwise cost a retry the
// caller was entitled to.
func TestIsRateLimitedDetectsTypedAPIError(t *testing.T) {
	err := genai.APIError{Code: 429, Message: "Too Many Requests"}
	if !isRateLimited(err) {
		t.Error("want a typed 429 APIError to be recognized as rate-limited")
	}
	// Also recognized when wrapped, as the client's transport layer might.
	if !isRateLimited(errors.Join(errors.New("request failed"), err)) {
		t.Error("want a wrapped typed 429 APIError to be recognized as rate-limited")
	}
}

func TestIsRateLimitedRejectsOtherTypedStatusCodes(t *testing.T) {
	err := genai.APIError{Code: 500, Message: "Internal Server Error"}
	if isRateLimited(err) {
		t.Error("a typed 500 APIError should not be treated as rate-limited")
	}
}

func TestIsRateLimitedFallsBackToStringMatch(t *testing.T) {
	if !isRateLimited(errors.New("request failed: 429 rate limited")) {
		t.Error("want the string fallback to catch an untyped error mentioning 429")
	}
}
