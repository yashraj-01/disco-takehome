package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/model"
	"github.com/yashraj/disco/prompts"
)

// multiFixtureDeps records one response per persona.
func multiFixtureDeps(t *testing.T, brief string, byPersona map[string]any) Deps {
	t.Helper()
	f := llm.NewFixture(t.TempDir())
	for personaID, resp := range byPersona {
		b, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		key := fixtureKey("creative", brief+"|"+personaID)
		if err := f.Record(llm.Request{Stage: "creative", FixtureKey: key}, b); err != nil {
			t.Fatal(err)
		}
	}
	return Deps{Provider: f, Catalog: loadCatalog(t)}
}

func creativeBody(levers []string) map[string]any {
	return map[string]any{
		"headline":         "Joint support your senior dog can feel",
		"body":             "Vet-formulated, grain-free meals built for older dogs. Delivered monthly.",
		"rationale":        "Leads with the vet formulation this persona screens for.",
		"messaging_levers": levers,
		"avoided":          []string{"generic pet brands"},
	}
}

func creativeBodyWithAvoided(levers, avoided []string) map[string]any {
	b := creativeBody(levers)
	b["avoided"] = avoided
	return b
}

func selection(ids ...string) model.PersonaSelection {
	var s model.PersonaSelection
	for _, id := range ids {
		s.Selected = append(s.Selected, model.PersonaPick{PersonaID: id, Rationale: "r"})
	}
	return s
}

func TestCreativesOnePerPersonaInOrder(t *testing.T) {
	brief := "premium dog food"
	sel := selection("persona_004", "persona_002", "persona_001")
	d := multiFixtureDeps(t, brief, map[string]any{
		"persona_004": creativeBody([]string{"vet-recommended"}),
		"persona_002": creativeBody([]string{"trusted by parents"}),
		"persona_001": creativeBody([]string{"science-backed claims"}),
	})

	got, err := Creatives(context.Background(), d, withBrief(dogFood(), brief), sel)
	if err != nil {
		t.Fatalf("Creatives: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d creatives, want 3", len(got))
	}
	for i, want := range []string{"persona_004", "persona_002", "persona_001"} {
		if got[i].PersonaID != want {
			t.Errorf("creative[%d] persona = %s, want %s", i, got[i].PersonaID, want)
		}
	}
}

func TestCreativesEnforceLengthLimits(t *testing.T) {
	brief := "premium dog food"
	long := creativeBody([]string{"vet-recommended"})
	long["headline"] = strings.Repeat("x", 90)
	long["body"] = strings.Repeat("y", 300)

	d := multiFixtureDeps(t, brief, map[string]any{
		"persona_004": long,
		"persona_002": creativeBody([]string{"trusted by parents"}),
		"persona_001": creativeBody([]string{"science-backed claims"}),
	})

	got, err := Creatives(context.Background(), d,
		withBrief(dogFood(), brief), selection("persona_004", "persona_002", "persona_001"))
	if err != nil {
		t.Fatalf("Creatives: %v", err)
	}
	for _, c := range got {
		if len([]rune(c.Headline)) > 60 {
			t.Errorf("%s headline is %d runes, want <= 60", c.PersonaID, len([]rune(c.Headline)))
		}
		if len([]rune(c.Body)) > 200 {
			t.Errorf("%s body is %d runes, want <= 200", c.PersonaID, len([]rune(c.Body)))
		}
	}
}

// A lever the persona record does not actually contain is dropped: the field
// exists to prove the copy is grounded, so an unverifiable entry is worthless.
// The fixture deliberately mixes one grounded and one invented lever so that a
// filter which forgot to filter (kept everything) and a filter which dropped
// everything (over-filtered) would both be caught.
func TestCreativesDropUngroundedMessagingLevers(t *testing.T) {
	brief := "premium dog food"
	d := multiFixtureDeps(t, brief, map[string]any{
		"persona_004": creativeBody([]string{"vet-recommended", "invented lever"}),
		"persona_002": creativeBody([]string{"trusted by parents"}),
		"persona_001": creativeBody([]string{"science-backed claims"}),
	})

	got, err := Creatives(context.Background(), d,
		withBrief(dogFood(), brief), selection("persona_004", "persona_002", "persona_001"))
	if err != nil {
		t.Fatalf("Creatives: %v", err)
	}
	for _, c := range got {
		if c.PersonaID != "persona_004" {
			continue
		}
		for _, l := range c.MessagingLevers {
			if l == "invented lever" {
				t.Error("an ungrounded messaging lever must be dropped")
			}
		}
		if len(c.MessagingLevers) == 0 {
			t.Error("the grounded lever should have survived")
		}
	}
}

// Same rule, applied to "avoided" against disinterested_in. The fixture again
// mixes a grounded and an invented entry so neither "kept everything" nor
// "dropped everything" would pass.
func TestCreativesDropUngroundedAvoided(t *testing.T) {
	brief := "premium dog food"
	d := multiFixtureDeps(t, brief, map[string]any{
		// persona_004 disinterested_in: "generic pet brands", "ultra-cheap positioning"
		"persona_004": creativeBodyWithAvoided([]string{"vet-recommended"},
			[]string{"generic pet brands", "made up avoidance"}),
		"persona_002": creativeBody([]string{"trusted by parents"}),
		"persona_001": creativeBody([]string{"science-backed claims"}),
	})

	got, err := Creatives(context.Background(), d,
		withBrief(dogFood(), brief), selection("persona_004", "persona_002", "persona_001"))
	if err != nil {
		t.Fatalf("Creatives: %v", err)
	}
	for _, c := range got {
		if c.PersonaID != "persona_004" {
			continue
		}
		for _, a := range c.Avoided {
			if a == "made up avoidance" {
				t.Error("an ungrounded avoided entry must be dropped")
			}
		}
		if len(c.Avoided) == 0 {
			t.Error("the grounded avoided entry should have survived")
		}
	}
}

// A lever that differs from the persona record only by surrounding
// whitespace must still be treated as grounded: groundedIn normalizes both
// sides through model.NormalizeLever, the same helper eval.Check uses, so a
// lever the model returns with incidental whitespace does not survive the
// production filter only to fail the eval as a false failure.
func TestGroundedInIgnoresSurroundingWhitespace(t *testing.T) {
	got := groundedIn([]string{"  vet-recommended  "}, []string{"vet-recommended"})
	if len(got) != 1 || got[0] != "  vet-recommended  " {
		t.Errorf("groundedIn = %v, want the whitespace-padded lever kept", got)
	}
}

// A persona ID that is not in the catalog must be skipped rather than
// producing an empty creative, and the surviving creatives must still be in
// selection order.
func TestCreativesSkipsUnknownPersonaPreservesOrder(t *testing.T) {
	brief := "premium dog food"
	d := multiFixtureDeps(t, brief, map[string]any{
		"persona_004": creativeBody([]string{"vet-recommended"}),
		// no fixture recorded for persona_999 on purpose: it is not in the
		// catalog, so the code must never even try to call the provider for it.
		"persona_002": creativeBody([]string{"trusted by parents"}),
	})

	got, err := Creatives(context.Background(), d,
		withBrief(dogFood(), brief), selection("persona_004", "persona_999", "persona_002"))
	if err != nil {
		t.Fatalf("Creatives: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d creatives, want 2 (persona_999 skipped)", len(got))
	}
	if got[0].PersonaID != "persona_004" || got[1].PersonaID != "persona_002" {
		t.Errorf("got order %v, want [persona_004 persona_002]", []string{got[0].PersonaID, got[1].PersonaID})
	}
	for _, c := range got {
		if c.Headline == "" {
			t.Errorf("%s: creative must not be empty", c.PersonaID)
		}
	}
}

// A provider error for any one persona must fail the whole stage rather than
// being silently swallowed into a partial result.
func TestCreativesPropagatesProviderError(t *testing.T) {
	brief := "premium dog food"
	// persona_002 has no recorded fixture, so the fixture provider returns an
	// error for it.
	d := multiFixtureDeps(t, brief, map[string]any{
		"persona_004": creativeBody([]string{"vet-recommended"}),
	})

	_, err := Creatives(context.Background(), d,
		withBrief(dogFood(), brief), selection("persona_004", "persona_002"))
	if err == nil {
		t.Fatal("want an error when one persona's call fails, got nil")
	}
}

// ctxAwareProvider blocks the call for one specific fixture key until its
// context is cancelled, then reports whether cancellation actually arrived.
type ctxAwareProvider struct {
	inner      llm.Provider
	blockKey   string
	started    chan struct{}
	cancelled  chan struct{}
	startOnce  sync.Once
	cancelOnce sync.Once
}

func (p *ctxAwareProvider) Name() string { return "ctx-aware-test-provider" }

func (p *ctxAwareProvider) Complete(ctx context.Context, r llm.Request) (json.RawMessage, error) {
	if r.FixtureKey != p.blockKey {
		return p.inner.Complete(ctx, r)
	}
	p.startOnce.Do(func() { close(p.started) })
	<-ctx.Done()
	p.cancelOnce.Do(func() { close(p.cancelled) })
	return nil, ctx.Err()
}

// The stage must run every persona call under the errgroup's derived context,
// so that one persona's failure cancels the in-flight calls for the others
// instead of leaving them to run to completion (or hang) uselessly.
func TestCreativesCancelsInFlightCallsOnError(t *testing.T) {
	brief := "premium dog food"
	inner := multiFixtureDeps(t, brief, map[string]any{
		"persona_002": creativeBody([]string{"trusted by parents"}),
		// persona_004 deliberately has no fixture: the fixture provider errors
		// for it immediately, which should cancel the group's context.
	})

	blockKey := fixtureKey("creative", brief+"|persona_001")
	wrapped := &ctxAwareProvider{
		inner:     inner.Provider,
		blockKey:  blockKey,
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
	d := Deps{Provider: wrapped, Catalog: inner.Catalog}

	done := make(chan error, 1)
	go func() {
		_, err := Creatives(context.Background(), d,
			withBrief(dogFood(), brief), selection("persona_004", "persona_001", "persona_002"))
		done <- err
	}()

	select {
	case <-wrapped.started:
	case <-time.After(2 * time.Second):
		t.Fatal("persona_001's call never started")
	}

	select {
	case <-wrapped.cancelled:
		// good: cancellation reached the in-flight call
	case <-time.After(2 * time.Second):
		t.Fatal("persona_004's failure never cancelled persona_001's in-flight call")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Error("want Creatives to return an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Creatives never returned")
	}
}

// TestCreativeRoundTripSchema marshals a fully-populated model.Creative,
// validates it against the shipped response schema, unmarshals it back, and
// checks every schema-covered field survived. PersonaID and
// SuggestedPublishers are stamped by Go after the call and are not part of
// what the model produces, so the schema (and this test) do not cover them.
func TestCreativeRoundTripSchema(t *testing.T) {
	want := model.Creative{
		PersonaID:           "persona_004",
		Headline:            "Distinctive headline value",
		Body:                "Distinctive body value that describes the offer in more detail than the headline.",
		Rationale:           "Distinctive rationale value.",
		SuggestedPublishers: []string{"pub_007"},
		MessagingLevers:     []string{"vet-recommended", "ingredient transparency"},
		Avoided:             []string{"generic pet brands"},
	}

	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, schema, err := prompts.Load("creative")
	if err != nil {
		t.Fatalf("prompts.Load: %v", err)
	}
	if err := llm.Validate(schema, b); err != nil {
		t.Fatalf("creative.schema.json rejected a fully-populated Creative: %v", err)
	}

	var got model.Creative
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Headline != want.Headline {
		t.Errorf("headline = %q, want %q", got.Headline, want.Headline)
	}
	if got.Body != want.Body {
		t.Errorf("body = %q, want %q", got.Body, want.Body)
	}
	if got.Rationale != want.Rationale {
		t.Errorf("rationale = %q, want %q", got.Rationale, want.Rationale)
	}
	if strings.Join(got.MessagingLevers, ",") != strings.Join(want.MessagingLevers, ",") {
		t.Errorf("messaging_levers = %v, want %v", got.MessagingLevers, want.MessagingLevers)
	}
	if strings.Join(got.Avoided, ",") != strings.Join(want.Avoided, ",") {
		t.Errorf("avoided = %v, want %v", got.Avoided, want.Avoided)
	}
}
