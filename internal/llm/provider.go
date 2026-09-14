// Package llm wraps model access behind a two-implementation interface: a real
// provider for live runs and a fixture replayer so the test suite and a
// reviewer's first run need no API key.
package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Request is one structured-output call.
type Request struct {
	Stage  string          // profile | fit | personas | creative
	Prompt string          // fully rendered prompt text
	Schema json.RawMessage // JSON Schema the response must satisfy

	// FixtureKey identifies this call for fixture replay. It is deliberately
	// coarser than the prompt hash — stage plus a hash of the advertiser brief —
	// so committed demo fixtures survive prompt edits.
	FixtureKey string
}

// Provider returns JSON conforming to the request's schema.
type Provider interface {
	Name() string
	Complete(ctx context.Context, r Request) (json.RawMessage, error)
}

// CacheKey identifies a call exactly. Any change to the provider, model,
// prompt, or schema produces a different key, so the disk cache never serves a
// stale response after a prompt edit.
func CacheKey(provider, model string, r Request) string {
	h := sha256.New()
	for _, part := range []string{provider, model, r.Stage, r.Prompt, string(r.Schema)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// ShortHash is a stable 8-character hash, used to build fixture keys from a
// brief.
func ShortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}
