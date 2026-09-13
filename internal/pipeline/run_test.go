package pipeline

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/model"
)

// countingProvider wraps a fixture provider and records which stages ran.
//
// Stage 5 (creatives) fans out one goroutine per persona, so Complete is
// called concurrently once a run reaches it; mu guards stages against that
// (go test -race catches the unsynchronized append otherwise).
type countingProvider struct {
	inner  llm.Provider
	mu     sync.Mutex
	stages []string
}

func (c *countingProvider) Name() string { return "counting" }
func (c *countingProvider) Complete(ctx context.Context, r llm.Request) (json.RawMessage, error) {
	c.mu.Lock()
	c.stages = append(c.stages, r.Stage)
	c.mu.Unlock()
	return c.inner.Complete(ctx, r)
}
func (c *countingProvider) ran(stage string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.stages {
		if s == stage {
			return true
		}
	}
	return false
}

// fullRunProvider records a complete, consistent set of fixtures for one brief.
func fullRunProvider(t *testing.T, brief string, profile map[string]any) *countingProvider {
	t.Helper()
	c := loadCatalog(t)
	f := llm.NewFixture(t.TempDir())

	rec := func(stage, key string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Record(llm.Request{Stage: stage, FixtureKey: key}, b); err != nil {
			t.Fatal(err)
		}
	}

	rec("profile", fixtureKey("profile", brief), profile)

	var verdicts []map[string]any
	rank := 0
	for i := range c.Publishers {
		v, r := "excluded", 0
		if c.Publishers[i].Category == "pet" && rank < 3 {
			rank++
			v, r = "recommended", rank
		}
		verdicts = append(verdicts, map[string]any{
			"publisher_id": c.Publishers[i].ID, "verdict": v, "rank": r,
			"reason": "pet-category audience overlap"})
	}
	rec("fit", fixtureKey("fit", brief), map[string]any{"verdicts": verdicts})

	rec("personas", fixtureKey("personas", brief), map[string]any{
		"selected": []map[string]any{
			{"persona_id": "persona_004", "rationale": "pet parent", "primary_publishers": []string{"pub_007"}},
			{"persona_id": "persona_002", "rationale": "busy parent", "primary_publishers": []string{"pub_009"}},
			{"persona_id": "persona_001", "rationale": "optimizer", "primary_publishers": []string{"pub_007"}},
		},
		"rejected": []map[string]any{{"persona_id": "persona_003", "reason": "no pet affinity"}},
	})

	for _, id := range []string{"persona_004", "persona_002", "persona_001"} {
		rec("creative", fixtureKey("creative", brief+"|"+id), map[string]any{
			"headline": "Built for senior dogs", "body": "Vet-formulated meals, delivered monthly.",
			"rationale": "grounded", "messaging_levers": []string{}, "avoided": []string{}})
	}

	return &countingProvider{inner: f}
}

func consumerProfile() map[string]any {
	return map[string]any{
		"primary_category": "pet", "subcategories": []string{"pet_food", "subscription"},
		"price_tier": "premium", "estimated_aov_usd": 70,
		"target_age_min": 30, "target_age_max": 55, "target_gender_skew": "balanced",
		"values": []string{"science_backed"}, "business_model": "subscription",
		"is_consumer_dtc": true, "confidence": "high",
		"assumptions": []string{}, "missing_signals": []string{},
	}
}

func TestRunProducesACompleteCampaign(t *testing.T) {
	brief := "We sell premium dog food for senior dogs."
	p := fullRunProvider(t, brief, consumerProfile())
	c := loadCatalog(t)

	got, err := Run(context.Background(), brief, Options{
		Provider: p, Catalog: c, Params: DefaultAllocParams(), Model: "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != model.StatusReady {
		t.Errorf("status = %q, want ready", got.Status)
	}
	if len(got.PublisherLedger) != len(c.Publishers) {
		t.Errorf("ledger has %d entries, want %d", len(got.PublisherLedger), len(c.Publishers))
	}
	if n := len(got.Creatives); n < 3 || n > 5 {
		t.Errorf("got %d creatives, want 3 to 5", n)
	}
	if len(got.Budget.Allocation) == 0 {
		t.Error("no budget allocated")
	}
	for _, stage := range []string{"profile", "fit", "personas", "creative"} {
		if !p.ran(stage) {
			t.Errorf("stage %q never ran", stage)
		}
	}
}

// Brief #7: B2B SaaS for dental practices. The run must stop after scoring.
func TestRunShortCircuitsNonConsumerBrief(t *testing.T) {
	brief := "B2B SaaS for dental practices. We automate patient recall."
	prof := consumerProfile()
	prof["is_consumer_dtc"] = false
	prof["primary_category"] = "b2b_saas"
	prof["business_model"] = "b2b"

	p := fullRunProvider(t, brief, prof)
	got, err := Run(context.Background(), brief, Options{
		Provider: p, Catalog: loadCatalog(t), Params: DefaultAllocParams(), Model: "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got.Status != model.StatusNoRecommendation {
		t.Errorf("status = %q, want %q", got.Status, model.StatusNoRecommendation)
	}
	if len(got.Creatives) != 0 {
		t.Errorf("got %d creatives, want 0", len(got.Creatives))
	}
	if len(got.Budget.Allocation) != 0 {
		t.Errorf("got %d allocations, want 0", len(got.Budget.Allocation))
	}
	for _, stage := range []string{"fit", "personas", "creative"} {
		if p.ran(stage) {
			t.Errorf("stage %q ran for a brief this catalog cannot serve", stage)
		}
	}
	if len(got.PublisherLedger) == 0 {
		t.Error("the ledger should still explain every publisher")
	}
}

func TestRunLowConfidenceStillProducesAProvisionalCampaign(t *testing.T) {
	brief := "idk just try it"
	prof := consumerProfile()
	prof["confidence"] = "low"
	prof["missing_signals"] = []string{"what is being sold", "price point"}

	p := fullRunProvider(t, brief, prof)
	got, err := Run(context.Background(), brief, Options{
		Provider: p, Catalog: loadCatalog(t), Params: DefaultAllocParams(), Model: "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != model.StatusNeedsClarification {
		t.Errorf("status = %q, want %q", got.Status, model.StatusNeedsClarification)
	}
	if len(got.Clarifications) == 0 {
		t.Error("want clarifying questions")
	}
	if len(got.Creatives) == 0 {
		t.Error("a provisional campaign should still carry creatives")
	}
}
