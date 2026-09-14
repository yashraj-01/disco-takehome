package main

import (
	"flag"
	"strings"
	"testing"

	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/pipeline"
)

// dataDir is the repo's real data directory, reached from this package's
// location at cmd/disco. Tests that load the catalog use it directly rather
// than duplicating fixtures — data/ is never modified by this test.
const dataDir = "../../data"

// TestRegisterCommonDefaults locks the CLI's allocation defaults to
// pipeline.DefaultAllocParams() so the two cannot silently drift apart: if
// someone changes one without the other, this test catches it.
func TestRegisterCommonDefaults(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c := registerCommon(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}

	want := pipeline.DefaultAllocParams()
	if c.params != want {
		t.Errorf("registerCommon defaults = %+v, want %+v (pipeline.DefaultAllocParams())", c.params, want)
	}
	// "auto" replays recorded briefs and only reaches for the live model when
	// GEMINI_API_KEY is set, so the offline default behaviour is unchanged.
	if c.provider != "auto" {
		t.Errorf("default provider = %q, want %q", c.provider, "auto")
	}
}

// TestRegisterCommonParsesFlags checks that every allocation threshold flag
// actually reaches AllocParams, so a budget split can be changed from the
// command line without editing code.
func TestRegisterCommonParsesFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c := registerCommon(fs)
	args := []string{
		"--gamma=2.5",
		"--max-share=0.5",
		"--sov-cap=0.2",
		"--min-share=0.1",
		"--budget=10000",
		"--days=14",
		"--provider=gemini",
		"--model=gemini-2.5-pro",
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse: %v", err)
	}

	want := pipeline.AllocParams{
		Gamma: 2.5, MaxShare: 0.5, SOVCap: 0.2, MinShare: 0.1,
		TotalUSD: 10000, Days: 14,
	}
	if c.params != want {
		t.Errorf("parsed params = %+v, want %+v", c.params, want)
	}
	if c.provider != "gemini" {
		t.Errorf("provider = %q, want gemini", c.provider)
	}
	if c.model != "gemini-2.5-pro" {
		t.Errorf("model = %q, want gemini-2.5-pro", c.model)
	}
}

// TestBuildProviderFixtureDefault checks that the default configuration
// returns a fixture provider and successfully loads the real catalog, with no
// API key and no network.
func TestBuildProviderFixtureDefault(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c := registerCommon(fs)
	c.dataDir = dataDir

	provider, cat, err := buildProvider(c)
	if err != nil {
		t.Fatalf("buildProvider: %v", err)
	}
	// With no GEMINI_API_KEY the default "auto" provider is a chain with no
	// live fallback, so it behaves exactly like the fixture provider alone.
	if provider.Name() != "fixture" {
		t.Errorf("provider.Name() = %q, want fixture", provider.Name())
	}
	if _, ok := provider.(*llm.Chain); !ok {
		t.Errorf("provider is %T, want *llm.Chain", provider)
	}
	if cat == nil || len(cat.Publishers) == 0 {
		t.Errorf("expected a loaded catalog with publishers, got %+v", cat)
	}
}

// TestBuildProviderUnknown checks that an unrecognized --provider value fails
// clearly instead of silently falling back to something.
func TestBuildProviderUnknown(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c := registerCommon(fs)
	c.dataDir = dataDir
	c.provider = "bogus"

	_, _, err := buildProvider(c)
	if err == nil {
		t.Fatal("expected an error for an unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error %q does not name the offending provider value", err.Error())
	}
}

// TestBuildProviderGeminiNoAPIKey checks that requesting the live provider
// without GEMINI_API_KEY set fails with a clear, actionable error rather than
// attempting a network call. No API key and no network are available in this
// test environment, so this also guards against an accidental network
// dependency creeping into buildProvider.
func TestBuildProviderGeminiNoAPIKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c := registerCommon(fs)
	c.dataDir = dataDir
	c.provider = "gemini"

	_, _, err := buildProvider(c)
	if err == nil {
		t.Fatal("expected an error when GEMINI_API_KEY is unset, got nil")
	}
	if !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Errorf("error %q does not mention GEMINI_API_KEY", err.Error())
	}
}

// TestCmdRunEmptyBrief checks that an empty brief produces a helpful error
// instead of a panic or a silent no-op run.
func TestCmdRunEmptyBrief(t *testing.T) {
	err := cmdRun([]string{"--data=" + dataDir, "   "})
	if err == nil {
		t.Fatal("expected an error for an empty brief, got nil")
	}
	if !strings.Contains(err.Error(), "no brief given") {
		t.Errorf("error %q is not the expected empty-brief message", err.Error())
	}
}

// TestCmdRunMissingFixture checks that running the default (fixture)
// provider against a brief with no recorded fixture fails with an actionable
// error naming the fixture path and telling the user how to create it — the
// expected behaviour until fixtures are recorded in a later task.
func TestCmdRunMissingFixture(t *testing.T) {
	err := cmdRun([]string{"--data=" + dataDir, "--fixtures=does-not-exist", "a test brief"})
	if err == nil {
		t.Fatal("expected an error when no fixture is recorded, got nil")
	}
	// The message a user actually meets must name both recovery paths: which
	// briefs are recorded, and how to draft one that is not.
	for _, want := range []string{"evals/briefs.txt", "GEMINI_API_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// TestCmdServeAndEvalStubs checks the temporary stubs return an error rather
// than panicking or silently succeeding, and do not import internal/server or
// internal/eval (neither package exists yet).
func TestCmdServeAndEvalStubs(t *testing.T) {
	if err := cmdServe(nil); err == nil {
		t.Error("cmdServe: expected a not-implemented error, got nil")
	}
	if err := cmdEval(nil); err == nil {
		t.Error("cmdEval: expected a not-implemented error, got nil")
	}
}
