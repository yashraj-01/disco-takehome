package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type stubProvider struct {
	name   string
	out    json.RawMessage
	err    error
	called int
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Complete(context.Context, Request) (json.RawMessage, error) {
	s.called++
	return s.out, s.err
}

var okJSON = json.RawMessage(`{"ok":true}`)

// A fixture miss must fall through to the live provider — this is the whole
// reason the chain exists. Corruption it catches: dropping the fallback call.
func TestChainFallsThroughOnFixtureMiss(t *testing.T) {
	primary := &stubProvider{name: "fixture", err: &ErrNoFixture{Stage: "profile", Path: "p.json"}}
	fallback := &stubProvider{name: "gemini", out: okJSON}

	got, err := NewChain(primary, fallback).Complete(context.Background(), Request{Stage: "profile"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if string(got) != string(okJSON) {
		t.Errorf("got %s, want the fallback's response", got)
	}
	if fallback.called != 1 {
		t.Errorf("fallback called %d times, want 1", fallback.called)
	}
}

// A hit must NOT reach the fallback — otherwise every recorded brief would
// spend quota. Corruption it catches: calling the fallback unconditionally.
func TestChainDoesNotCallFallbackOnHit(t *testing.T) {
	primary := &stubProvider{name: "fixture", out: okJSON}
	fallback := &stubProvider{name: "gemini", out: json.RawMessage(`{"wrong":true}`)}

	got, err := NewChain(primary, fallback).Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if string(got) != string(okJSON) {
		t.Errorf("got %s, want the primary's response", got)
	}
	if fallback.called != 0 {
		t.Errorf("fallback called %d times on a hit, want 0", fallback.called)
	}
}

// A real read failure is NOT an absence and must surface, not be masked by a
// network call that hides the actual problem. Corruption: falling through on
// any error rather than only on *ErrNoFixture.
func TestChainSurfacesRealErrorsWithoutFallback(t *testing.T) {
	boom := errors.New("llm: permission denied reading fixtures dir")
	primary := &stubProvider{name: "fixture", err: boom}
	fallback := &stubProvider{name: "gemini", out: okJSON}

	_, err := NewChain(primary, fallback).Complete(context.Background(), Request{})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the primary's real error surfaced", err)
	}
	if fallback.called != 0 {
		t.Errorf("fallback called %d times on a real error, want 0", fallback.called)
	}
}

// With no fallback configured the miss message must name both recovery paths.
func TestChainMissWithoutFallbackExplainsBothPaths(t *testing.T) {
	primary := &stubProvider{name: "fixture", err: &ErrNoFixture{Stage: "profile", Path: "p.json"}}

	_, err := NewChain(primary, nil).Complete(context.Background(), Request{})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"evals/briefs.txt", "GEMINI_API_KEY", "p.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %q", err.Error(), want)
		}
	}
	var miss *ErrNoFixture
	if !errors.As(err, &miss) {
		t.Error("the typed miss must remain unwrappable through the chain")
	}
}

func TestChainName(t *testing.T) {
	p := &stubProvider{name: "fixture"}
	if got := NewChain(p, nil).Name(); got != "fixture" {
		t.Errorf("Name() = %q, want fixture", got)
	}
	if got := NewChain(p, &stubProvider{name: "gemini"}).Name(); got != "fixture+gemini" {
		t.Errorf("Name() = %q, want fixture+gemini", got)
	}
}
