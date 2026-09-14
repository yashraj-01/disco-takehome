package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/yashraj/disco/internal/logging"
)

// GeminiOptions configures the live provider.
type GeminiOptions struct {
	APIKey   string
	Model    string // e.g. gemini-3.5-flash-lite
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
		logging.L().Debug("cache hit", "stage", r.Stage)
		// Record on a cache hit too. The cache and the fixture store answer
		// different questions — "have I asked this before on this machine" versus
		// "is this brief part of the committed offline demo" — so a response
		// served from cache must still be able to become a fixture, or a brief
		// drafted twice would never get recorded at all.
		if err := g.record(r, cached); err != nil {
			return nil, err
		}
		return cached, nil
	}

	schema, err := toGenaiSchema(r.Schema)
	if err != nil {
		return nil, fmt.Errorf("llm: stage %s: %w", r.Stage, err)
	}

	prompt := r.Prompt
	var lastErr error

	for repair := 0; repair < 2; repair++ {
		// INFO, not DEBUG: this is the line that says real quota is about to
		// be spent. On the free tier that is the scarcest thing in the system,
		// so it stays visible at the default level.
		logging.L().Info("calling model", "stage", r.Stage, "model", g.model)
		start := time.Now()

		out, err := g.generate(ctx, r.Stage, prompt, schema)
		if err != nil {
			return nil, err
		}
		if err := Validate(r.Schema, out); err == nil {
			logging.L().Debug("model responded", "stage", r.Stage,
				"took", logging.Elapsed(time.Since(start)), "bytes", len(out))
			g.writeCache(r, out)
			if err := g.record(r, out); err != nil {
				return nil, err
			}
			return out, nil
		} else {
			logging.L().Warn("schema rejected, repairing",
				"stage", r.Stage, "attempt", repair+1, "err", err)
			lastErr = err
			prompt = r.Prompt + "\n\nYour previous response was rejected: " + err.Error() +
				"\nReturn JSON that satisfies the schema exactly."
		}
	}
	return nil, fmt.Errorf("llm: stage %s failed schema validation twice: %w", r.Stage, lastErr)
}

// record writes a response to the fixture store when a recorder is configured.
// A recorder failure is fatal: --record is an explicit request, and a silently
// dropped fixture yields an incomplete offline demo that fails much later.
func (g *Gemini) record(r Request, out json.RawMessage) error {
	if g.recorder == nil {
		return nil
	}
	if err := g.recorder.Record(r, out); err != nil {
		return fmt.Errorf("llm: recording fixture: %w", err)
	}
	return nil
}

// generate issues one request, waiting for a rate-limit slot and backing off on
// 429 responses.
func (g *Gemini) generate(ctx context.Context, stage, prompt string, schema *genai.Schema) (json.RawMessage, error) {
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
		logging.L().Warn("rate limited, backing off", "stage", stage,
			"attempt", attempt+1, "of", maxAttempts, "wait", wait)
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
		timer.Stop()
	}
	return nil, errors.New("llm: exhausted attempts")
}

// isRateLimited reports whether err is a 429 the caller should back off and
// retry for. google.golang.org/genai@v1.71.0 wraps a non-2xx HTTP response in
// a typed genai.APIError carrying the numeric status code (see
// newAPIError/APIError.Error in the SDK's api_client.go), so that is checked
// first via errors.As. The string match stays only as a fallback for an error
// the SDK does not wrap that way — e.g. a transport-level failure whose
// message still names the status.
func isRateLimited(err error) bool {
	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == http.StatusTooManyRequests
	}
	s := err.Error()
	return strings.Contains(s, "429") ||
		strings.Contains(s, "RESOURCE_EXHAUSTED") ||
		strings.Contains(strings.ToLower(s), "rate limit")
}

func (g *Gemini) cachePath(r Request) string {
	return filepath.Join(g.cacheDir, CacheKey(g.Name(), g.model, r)+".json")
}

// readCache returns a cache hit only if the cached bytes still satisfy the
// request's schema. A corrupt or hand-edited cache file — or one left
// truncated by a crash mid-write — is treated as a miss rather than served
// straight through or returned as an error: falling through to regenerate is
// what preserves the validate-and-repair guarantee this layer exists for, and
// a bad cache entry must never be fatal.
func (g *Gemini) readCache(r Request) (json.RawMessage, bool) {
	if g.cacheDir == "" {
		return nil, false
	}
	b, err := os.ReadFile(g.cachePath(r))
	if err != nil {
		return nil, false
	}
	if err := Validate(r.Schema, b); err != nil {
		return nil, false
	}
	return json.RawMessage(b), true
}

// writeCache writes out atomically: a temp file in the cache directory is
// written and fsynced, then renamed over the target. Renames are atomic on
// the same filesystem, so a crash mid-write can never leave a truncated cache
// entry for readCache to find later — the temp file either never gets renamed
// (and is simply absent) or the rename completes with the full content.
func (g *Gemini) writeCache(r Request, out json.RawMessage) {
	if g.cacheDir == "" {
		return
	}
	if err := os.MkdirAll(g.cacheDir, 0o755); err != nil {
		return // the cache is an optimisation; failing to write it is not fatal
	}
	tmp, err := os.CreateTemp(g.cacheDir, ".cache-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(out)
	if werr == nil {
		werr = tmp.Sync()
	}
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, g.cachePath(r)); err != nil {
		os.Remove(tmpName)
	}
}

// jsonSchema is the subset of JSON Schema the prompt files use. Enum is left
// as raw JSON rather than []string so a non-string enum (e.g. on an integer
// type) can be rejected with a clear, property-scoped error in convert
// instead of failing the top-level json.Unmarshal with a generic type
// mismatch that names no property.
type jsonSchema struct {
	Type        string                `json:"type"`
	Description string                `json:"description"`
	Properties  map[string]jsonSchema `json:"properties"`
	Items       *jsonSchema           `json:"items"`
	Required    []string              `json:"required"`
	Enum        json.RawMessage       `json:"enum"`
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
		var enum []string
		if err := json.Unmarshal(js.Enum, &enum); err != nil {
			return nil, fmt.Errorf("enum: only string enum values are supported: %w", err)
		}
		out.Enum = enum
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
	// An array schema with no "items" would otherwise convert silently and
	// only fail later, mid-run, when the live API rejects it — reject it here
	// instead so the failure names the offending property at build time.
	if t == genai.TypeArray && js.Items == nil {
		return nil, errors.New(`array schema requires "items"`)
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
