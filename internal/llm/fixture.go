package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Fixture replays recorded responses from disk. It makes the test suite
// deterministic and lets someone clone the repo and see the whole pipeline run
// without an API key or a billing account.
type Fixture struct{ dir string }

// NewFixture returns a replayer reading from dir.
func NewFixture(dir string) *Fixture { return &Fixture{dir: dir} }

// Name identifies the provider in campaign metadata.
func (f *Fixture) Name() string { return "fixture" }

func (f *Fixture) path(r Request) string {
	key := r.FixtureKey
	if key == "" {
		key = r.Stage
	}
	return filepath.Join(f.dir, key+".json")
}

// Complete returns the recorded response for this request.
func (f *Fixture) Complete(_ context.Context, r Request) (json.RawMessage, error) {
	b, err := os.ReadFile(f.path(r))
	if err != nil {
		return nil, fmt.Errorf(
			"llm: no fixture for stage %q at %s — run once with --provider gemini --record to create it",
			r.Stage, f.path(r))
	}
	return json.RawMessage(b), nil
}

// Record writes a response so later runs can replay it.
func (f *Fixture) Record(r Request, out json.RawMessage) error {
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out, "", "  "); err != nil {
		pretty.Reset()
		pretty.Write(out)
	}
	if err := os.WriteFile(f.path(r), pretty.Bytes(), 0o644); err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	return nil
}
