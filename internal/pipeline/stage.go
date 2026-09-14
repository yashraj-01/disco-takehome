package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/logging"
	"github.com/yashraj/disco/prompts"
)

// Deps is what every LLM stage needs.
type Deps struct {
	Provider llm.Provider
	Catalog  *catalog.Catalog
}

// callStage renders a stage's prompt with its input appended as JSON, calls the
// provider, validates the response against the stage's schema, and decodes it.
//
// Validation runs here as well as inside the live provider's repair loop, so a
// stale or hand-edited fixture fails as loudly as a bad model response.
func callStage[T any](ctx context.Context, d Deps, stage, fixtureKey string, input any) (T, error) {
	var zero T

	text, schema, err := prompts.Load(stage)
	if err != nil {
		return zero, err
	}
	payload, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return zero, fmt.Errorf("pipeline: %s: encoding input: %w", stage, err)
	}
	prompt := text + "\n\n## Input\n\n```json\n" + string(payload) + "\n```\n"

	started := time.Now()
	out, err := d.Provider.Complete(ctx, llm.Request{
		Stage: stage, Prompt: prompt, Schema: schema, FixtureKey: fixtureKey,
	})
	if err != nil {
		return zero, err
	}
	// DEBUG, not INFO: four of these per run is useful when you are asking
	// where the time went, and noise when you are not. The provider already
	// logs the one thing that always matters — whether the call was live.
	logging.L().Debug("stage served", "stage", stage,
		"took", logging.Elapsed(time.Since(started)), "bytes", len(out))
	if err := llm.Validate(schema, out); err != nil {
		return zero, fmt.Errorf("pipeline: %s: %w", stage, err)
	}

	var v T
	if err := json.Unmarshal(out, &v); err != nil {
		return zero, fmt.Errorf("pipeline: %s: decoding response: %w", stage, err)
	}
	return v, nil
}

// fixtureKey names the recorded response for a stage and brief. It excludes the
// prompt so committed fixtures survive prompt edits.
func fixtureKey(stage, brief string) string {
	return stage + "-" + llm.ShortHash(brief)
}
