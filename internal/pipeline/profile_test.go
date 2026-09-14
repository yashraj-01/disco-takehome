package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yashraj/disco/internal/llm"
)

// fixtureDeps builds a pipeline that replays canned responses from a temp dir.
func fixtureDeps(t *testing.T, stage, brief string, response any) Deps {
	t.Helper()
	dir := t.TempDir()
	f := llm.NewFixture(dir)

	b, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := f.Record(llm.Request{Stage: stage, FixtureKey: fixtureKey(stage, brief)}, b); err != nil {
		t.Fatalf("record fixture: %v", err)
	}
	return Deps{Provider: f, Catalog: loadCatalog(t)}
}

func TestProfileDecodesAndStampsBrief(t *testing.T) {
	brief := "We sell premium dog food for senior dogs, vet-formulated, subscription."
	d := fixtureDeps(t, "profile", brief, map[string]any{
		"primary_category":   "pet",
		"subcategories":      []string{"pet_food", "subscription"},
		"price_tier":         "premium",
		"estimated_aov_usd":  70,
		"target_age_min":     30,
		"target_age_max":     55,
		"target_gender_skew": "balanced",
		"values":             []string{"science_backed"},
		"business_model":     "subscription",
		"is_consumer_dtc":    true,
		"confidence":         "high",
		"assumptions":        []string{"subscription implies repeat purchase"},
		"missing_signals":    []string{},
	})

	got, err := Profile(context.Background(), d, brief)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if got.PrimaryCategory != "pet" {
		t.Errorf("category = %q, want pet", got.PrimaryCategory)
	}
	if got.RawBrief != brief {
		t.Errorf("RawBrief = %q, want the original brief", got.RawBrief)
	}
	if !got.IsConsumerDTC {
		t.Error("IsConsumerDTC = false, want true")
	}
}

// A response missing a required field must fail loudly rather than decode into
// a zero-valued profile that silently drives the rest of the pipeline.
func TestProfileRejectsSchemaViolation(t *testing.T) {
	brief := "anything"
	d := fixtureDeps(t, "profile", brief, map[string]any{"primary_category": "pet"})
	if _, err := Profile(context.Background(), d, brief); err == nil {
		t.Fatal("want a schema validation error")
	}
}

func TestProfileLowConfidenceCarriesMissingSignals(t *testing.T) {
	brief := "idk just try it"
	d := fixtureDeps(t, "profile", brief, map[string]any{
		"primary_category": "unknown", "subcategories": []string{},
		"price_tier": "mid", "estimated_aov_usd": 0,
		"target_age_min": 25, "target_age_max": 55,
		"target_gender_skew": "unknown", "values": []string{},
		"business_model": "one_off", "is_consumer_dtc": true,
		"confidence":      "low",
		"assumptions":     []string{"assumed a consumer product"},
		"missing_signals": []string{"what is being sold", "price point", "who buys it"},
	})

	got, err := Profile(context.Background(), d, brief)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if got.Confidence != "low" {
		t.Errorf("confidence = %q, want low", got.Confidence)
	}
	if len(got.MissingSignals) == 0 {
		t.Error("a low-confidence profile must name what it is missing")
	}
}
