package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLDiscardsUntilSet(t *testing.T) {
	// The package default must be silent: pipeline and llm call L() on every
	// run, and "go test ./..." would otherwise print pipeline chatter.
	h := L().Handler()
	for _, l := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		if h.Enabled(t.Context(), l) {
			t.Errorf("default logger should discard %v", l)
		}
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelInfo)
	log.Debug("dropped")
	log.Info("kept")
	log.Warn("also kept")

	out := buf.String()
	if strings.Contains(out, "dropped") {
		t.Errorf("debug record should not appear at INFO level: %q", out)
	}
	if !strings.Contains(out, "kept") || !strings.Contains(out, "also kept") {
		t.Errorf("info and warn records should appear: %q", out)
	}
	if got := strings.Count(out, "\n"); got != 2 {
		t.Errorf("want 2 lines, got %d: %q", got, out)
	}
}

func TestAttrsAreRendered(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, slog.LevelInfo).Info("calling model", "stage", "fit", "attempt", 2)
	out := buf.String()
	for _, want := range []string{"calling model", "stage=fit", "attempt=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

// A logger derived twice from one parent must not let the two children
// overwrite each other's attributes — stage 5 derives one per persona.
func TestWithAttrsDoesNotShareBacking(t *testing.T) {
	var buf bytes.Buffer
	parent := New(&buf, slog.LevelInfo).With("brief", "abc12345")
	parent.With("persona", "p1").Info("one")
	parent.With("persona", "p2").Info("two")

	out := buf.String()
	if !strings.Contains(out, "persona=p1") || !strings.Contains(out, "persona=p2") {
		t.Errorf("both derived attributes should survive: %q", out)
	}
	if got := strings.Count(out, "brief=abc12345"); got != 2 {
		t.Errorf("parent attribute should appear on both lines, got %d: %q", got, out)
	}
}

// Stage 5 fires one model call per persona concurrently and each goroutine
// logs. Run with -race.
func TestConcurrentHandleIsSafe(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelInfo)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			log.With("persona", n).Info("calling model", "stage", "creative")
		}(i)
	}
	wg.Wait()

	if got := strings.Count(buf.String(), "\n"); got != 50 {
		t.Errorf("want 50 interleaved lines, got %d", got)
	}
}

func TestElapsedKeepsSubMillisecondVisible(t *testing.T) {
	// The bug this guards: rounding everything under a second to whole
	// milliseconds rendered a 400µs fixture replay as "0s".
	cases := []struct {
		in   time.Duration
		want string
	}{
		{412 * time.Microsecond, "412µs"},
		{1834 * time.Microsecond, "1.8ms"},
		{2500 * time.Millisecond, "2.5s"},
	}
	for _, c := range cases {
		if got := Elapsed(c.in).String(); got != c.want {
			t.Errorf("Elapsed(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestBriefTruncates(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := Brief(long)
	if len([]rune(got)) != 48 {
		t.Errorf("want 48 runes, got %d", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated brief should be marked: %q", got)
	}
	if got := Brief("  two   spaced\nlines  "); got != "two spaced lines" {
		t.Errorf("Brief should collapse whitespace, got %q", got)
	}
}

// A record must occupy exactly one line even when an attribute value does not.
// The provider's missing-fixture error is three paragraphs.
func TestMultilineAttrCollapsesToOneLine(t *testing.T) {
	var buf bytes.Buffer
	err := errors.New("llm: no recorded response\n\nSet GEMINI_API_KEY to draft any brief")
	New(&buf, slog.LevelInfo).Error("run failed", "err", err)

	out := buf.String()
	if got := strings.Count(out, "\n"); got != 1 {
		t.Errorf("want exactly 1 newline (the record terminator), got %d: %q", got, out)
	}
	if !strings.Contains(out, "Set GEMINI_API_KEY") {
		t.Errorf("collapsing must not truncate the value: %q", out)
	}
}
