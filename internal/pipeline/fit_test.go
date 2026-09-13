package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/model"
	"github.com/yashraj/disco/prompts"
)

func TestFitReturnsOneVerdictPerPublisher(t *testing.T) {
	brief := "premium dog food"
	c := loadCatalog(t)
	scores := ScoreAll(dogFood(), c)

	var vs []map[string]any
	for i, s := range scores {
		verdict, rank := "excluded", 0
		if i < 3 && s.HardGate == GateNone {
			verdict, rank = "recommended", i+1
		}
		vs = append(vs, map[string]any{
			"publisher_id": s.PublisherID, "verdict": verdict,
			"rank": rank, "reason": "because"})
	}
	d := fixtureDeps(t, "fit", brief, map[string]any{"verdicts": vs})
	d.Catalog = c

	got, err := Fit(context.Background(), d, withBrief(dogFood(), brief), scores)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if len(got) != len(c.Publishers) {
		t.Fatalf("got %d verdicts, want %d", len(got), len(c.Publishers))
	}
}

// The model does not get to promote a hard-gated publisher, whatever it returns.
func TestFitCannotPromoteAHardGatedPublisher(t *testing.T) {
	brief := "sustainable activewear for women"
	c := loadCatalog(t)
	scores := ScoreAll(activewear(), c)

	var vs []map[string]any
	for _, s := range scores {
		// Deliberately try to recommend everything, including gated publishers.
		vs = append(vs, map[string]any{
			"publisher_id": s.PublisherID, "verdict": "recommended",
			"rank": 1, "reason": "model says yes"})
	}
	d := fixtureDeps(t, "fit", brief, map[string]any{"verdicts": vs})
	d.Catalog = c

	got, err := Fit(context.Background(), d, withBrief(activewear(), brief), scores)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	byID := map[string]model.FitVerdict{}
	for _, v := range got {
		byID[v.PublisherID] = v
	}
	if v := byID["pub_005"]; v.Verdict != "excluded" { // Linden Park, age-gated
		t.Errorf("gated publisher verdict = %q, want excluded", v.Verdict)
	}
	if v := byID["pub_005"]; v.Rank != 0 {
		t.Errorf("gated publisher rank = %d, want 0", v.Rank)
	}
}

// An invented publisher ID must not reach the campaign config.
func TestFitDropsUnknownPublisherIDs(t *testing.T) {
	brief := "premium dog food"
	c := loadCatalog(t)
	scores := ScoreAll(dogFood(), c)

	vs := []map[string]any{
		{"publisher_id": "pub_999", "verdict": "recommended", "rank": 1, "reason": "invented"},
	}
	for _, s := range scores {
		vs = append(vs, map[string]any{
			"publisher_id": s.PublisherID, "verdict": "considered", "rank": 0, "reason": "ok"})
	}
	d := fixtureDeps(t, "fit", brief, map[string]any{"verdicts": vs})
	d.Catalog = c

	got, err := Fit(context.Background(), d, withBrief(dogFood(), brief), scores)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	for _, v := range got {
		if v.PublisherID == "pub_999" {
			t.Error("pub_999 is not in the catalog and must be dropped")
		}
	}
	if len(got) != len(c.Publishers) {
		t.Fatalf("got %d verdicts, want %d (pub_999 should not add an extra entry)", len(got), len(c.Publishers))
	}
}

// A publisher the model forgot still gets an entry, so the ledger stays complete.
func TestFitBackfillsOmittedPublishers(t *testing.T) {
	brief := "premium dog food"
	c := loadCatalog(t)
	scores := ScoreAll(dogFood(), c)

	vs := []map[string]any{
		{"publisher_id": scores[0].PublisherID, "verdict": "recommended", "rank": 1, "reason": "ok"},
	}
	d := fixtureDeps(t, "fit", brief, map[string]any{"verdicts": vs})
	d.Catalog = c

	got, err := Fit(context.Background(), d, withBrief(dogFood(), brief), scores)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if len(got) != len(c.Publishers) {
		t.Fatalf("got %d verdicts, want %d", len(got), len(c.Publishers))
	}
	for _, v := range got {
		if v.Reason == "" {
			t.Errorf("%s has no reason", v.PublisherID)
		}
		if v.PublisherID == scores[0].PublisherID {
			continue
		}
		if v.Verdict != "excluded" {
			t.Errorf("omitted publisher %s verdict = %q, want excluded", v.PublisherID, v.Verdict)
		}
	}
}

// A schema property name that drifts from its struct's json tag validates
// cleanly and decodes to a zero value with no error anywhere else in the
// suite. This test marshals a fully-populated fitResponse, validates it
// against the shipped schema, unmarshals it back, and checks every field
// survived with its exact value.
func TestFitResponseSchemaRoundTrip(t *testing.T) {
	want := fitResponse{
		Verdicts: []model.FitVerdict{
			{
				PublisherID: "pub_007",
				Verdict:     "recommended",
				Rank:        3,
				Reason:      "Subscription-heavy pet buyers who already pay a premium for health positioning.",
			},
		},
	}

	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, schema, err := prompts.Load("fit")
	if err != nil {
		t.Fatalf("prompts.Load: %v", err)
	}
	if err := llm.Validate(schema, json.RawMessage(b)); err != nil {
		t.Fatalf("fitResponse does not validate against fit.schema.json: %v", err)
	}

	var got fitResponse
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got.Verdicts) != 1 {
		t.Fatalf("got %d verdicts, want 1", len(got.Verdicts))
	}
	gv, wv := got.Verdicts[0], want.Verdicts[0]
	if gv.PublisherID != wv.PublisherID {
		t.Errorf("PublisherID = %q, want %q", gv.PublisherID, wv.PublisherID)
	}
	if gv.Verdict != wv.Verdict {
		t.Errorf("Verdict = %q, want %q", gv.Verdict, wv.Verdict)
	}
	if gv.Rank != wv.Rank {
		t.Errorf("Rank = %d, want %d", gv.Rank, wv.Rank)
	}
	if gv.Reason != wv.Reason {
		t.Errorf("Reason = %q, want %q", gv.Reason, wv.Reason)
	}
}

func withBrief(p model.AdvertiserProfile, brief string) model.AdvertiserProfile {
	p.RawBrief = brief
	return p
}
