package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/genai"
)

// GeminiOptions configures the live provider.
type GeminiOptions struct {
	APIKey   string
	Model    string // e.g. gemini-2.5-flash
	CacheDir string // "" disables the disk cache
	RPM      int    // provider requests-per-minute quota; 0 disables limiting
	Recorder *Fixture
}

// Gemini calls the Gemini API for structured output, spacing requests to stay
// inside the free-tier quota and caching responses on disk so repeated runs of
// the same brief cost nothing.
type Gemini struct {
	client   *genai.Client
	model    string
	cacheDir string
	limiter  *Limiter
	recorder *Fixture
}

// NewGemini constructs the live provider.
func NewGemini(o GeminiOptions) (*Gemini, error) {
	if o.APIKey == "" {
		return nil, errors.New("llm: GEMINI_API_KEY is not set — " +
			"get a free key at aistudio.google.com, or use --provider fixture")
	}
	c, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  o.APIKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("llm: %w", err)
	}
	return &Gemini{
		client: c, model: o.Model, cacheDir: o.CacheDir,
		limiter: NewLimiter(o.RPM), recorder: o.Recorder,
	}, nil
}

// Name identifies the provider in campaign metadata.
func (g *Gemini) Name() string { return "gemini" }

const (
	maxAttempts = 3
	backoffBase = 2 * time.Second
)

// Complete returns schema-valid JSON for the request. On a schema violation it
// retries once with the validator's complaint appended to the prompt; a second
// failure is a hard error rather than a silently defaulted value.
func (g *Gemini) Complete(ctx context.Context, r Request) (json.RawMessage, error) {
	if cached, ok := g.readCache(r); ok {
		return cached, nil
	}

	schema, err := toGenaiSchema(r.Schema)
	if err != nil {
		return nil, fmt.Errorf("llm: stage %s: %w", r.Stage, err)
	}

	prompt := r.Prompt
	var lastErr error

	for repair := 0; repair < 2; repair++ {
		out, err := g.generate(ctx, prompt, schema)
		if err != nil {
			return nil, err
		}
		if err := Validate(r.Schema, out); err == nil {
			g.writeCache(r, out)
			if g.recorder != nil {
				if err := g.recorder.Record(r, out); err != nil {
					return nil, fmt.Errorf("llm: recording fixture: %w", err)
				}
			}
			return out, nil
		} else {
			lastErr = err
			prompt = r.Prompt + "\n\nYour previous response was rejected: " + err.Error() +
				"\nReturn JSON that satisfies the schema exactly."
		}
	}
	return nil, fmt.Errorf("llm: stage %s failed schema validation twice: %w", r.Stage, lastErr)
}

// generate issues one request, waiting for a rate-limit slot and backing off on
// 429 responses.
func (g *Gemini) generate(ctx context.Context, prompt string, schema *genai.Schema) (json.RawMessage, error) {
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := g.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		resp, err := g.client.Models.GenerateContent(ctx, g.model,
			genai.Text(prompt),
			&genai.GenerateContentConfig{
				ResponseMIMEType: "application/json",
				ResponseSchema:   schema,
			})
		if err == nil {
			text := resp.Text()
			if strings.TrimSpace(text) == "" {
				return nil, errors.New("llm: empty response")
			}
			return json.RawMessage(text), nil
		}
		if !isRateLimited(err) || attempt == maxAttempts-1 {
			return nil, fmt.Errorf("llm: %w", err)
		}
		wait := backoffBase * time.Duration(1<<attempt)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("llm: exhausted attempts")
}

func isRateLimited(err error) bool {
	s := err.Error()
	return strings.Contains(s, "429") ||
		strings.Contains(s, "RESOURCE_EXHAUSTED") ||
		strings.Contains(strings.ToLower(s), "rate limit")
}

func (g *Gemini) cachePath(r Request) string {
	return filepath.Join(g.cacheDir, CacheKey(g.Name(), g.model, r)+".json")
}

func (g *Gemini) readCache(r Request) (json.RawMessage, bool) {
	if g.cacheDir == "" {
		return nil, false
	}
	b, err := os.ReadFile(g.cachePath(r))
	if err != nil {
		return nil, false
	}
	return json.RawMessage(b), true
}

func (g *Gemini) writeCache(r Request, out json.RawMessage) {
	if g.cacheDir == "" {
		return
	}
	if err := os.MkdirAll(g.cacheDir, 0o755); err != nil {
		return // the cache is an optimisation; failing to write it is not fatal
	}
	_ = os.WriteFile(g.cachePath(r), out, 0o644)
}

// jsonSchema is the subset of JSON Schema the prompt files use.
type jsonSchema struct {
	Type        string                `json:"type"`
	Description string                `json:"description"`
	Properties  map[string]jsonSchema `json:"properties"`
	Items       *jsonSchema           `json:"items"`
	Required    []string              `json:"required"`
	Enum        []string              `json:"enum"`
}

var genaiTypes = map[string]genai.Type{
	"object":  genai.TypeObject,
	"array":   genai.TypeArray,
	"string":  genai.TypeString,
	"integer": genai.TypeInteger,
	"number":  genai.TypeNumber,
	"boolean": genai.TypeBoolean,
}

// toGenaiSchema converts a JSON Schema document into the SDK's schema type.
// genai.Schema is not JSON Schema — its Type is an uppercase enum — so the
// conversion is explicit rather than a hopeful unmarshal. Schema files are
// restricted to the subset handled here: no $ref, no oneOf, no allOf.
func toGenaiSchema(raw json.RawMessage) (*genai.Schema, error) {
	var js jsonSchema
	if err := json.Unmarshal(raw, &js); err != nil {
		return nil, fmt.Errorf("parsing schema: %w", err)
	}
	return convert(&js)
}

func convert(js *jsonSchema) (*genai.Schema, error) {
	t, ok := genaiTypes[js.Type]
	if !ok {
		return nil, fmt.Errorf("unsupported schema type %q", js.Type)
	}
	out := &genai.Schema{Type: t, Description: js.Description, Required: js.Required}
	if len(js.Enum) > 0 {
		out.Enum = js.Enum
	}
	if len(js.Properties) > 0 {
		out.Properties = make(map[string]*genai.Schema, len(js.Properties))
		for name, sub := range js.Properties {
			s := sub
			c, err := convert(&s)
			if err != nil {
				return nil, fmt.Errorf("property %q: %w", name, err)
			}
			out.Properties[name] = c
		}
	}
	if js.Items != nil {
		c, err := convert(js.Items)
		if err != nil {
			return nil, fmt.Errorf("items: %w", err)
		}
		out.Items = c
	}
	return out, nil
}
