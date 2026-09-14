package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Chain tries a primary provider and falls back to a second one when the
// primary has nothing recorded for the request.
//
// It exists for one demo problem: the fixture provider only answers the briefs
// somebody already recorded, so a reviewer who types their own sentence hits a
// missing-fixture error on their first try. With a key configured, the chain
// serves recorded briefs instantly and offline, and quietly calls the model for
// anything new — recording it on the way through, so the second run of that
// brief is free too.
//
// Only a fixture MISS falls through. A permissions error or unreadable
// directory is a real failure and is returned as-is, rather than being masked
// by a network call that hides the actual problem.
type Chain struct {
	primary  Provider
	fallback Provider
}

// NewChain returns a provider that reads from primary and falls back to
// fallback on a miss. A nil fallback makes the chain behave exactly like
// primary alone.
func NewChain(primary, fallback Provider) *Chain {
	return &Chain{primary: primary, fallback: fallback}
}

// Name reports both halves, so the campaign metadata records how a run was
// actually served.
func (c *Chain) Name() string {
	if c.fallback == nil {
		return c.primary.Name()
	}
	return c.primary.Name() + "+" + c.fallback.Name()
}

// Complete returns the primary's response, falling back on a miss.
func (c *Chain) Complete(ctx context.Context, r Request) (json.RawMessage, error) {
	out, err := c.primary.Complete(ctx, r)
	if err == nil {
		return out, nil
	}
	var miss *ErrNoFixture
	if !errors.As(err, &miss) {
		return nil, err // a real failure, not an absence
	}
	if c.fallback == nil {
		return nil, fmt.Errorf(
			"%w\n\nThis brief has no recorded response. The offline demo covers the "+
				"15 briefs in evals/briefs.txt; set GEMINI_API_KEY to draft campaigns "+
				"for any brief", err)
	}
	return c.fallback.Complete(ctx, r)
}
