package pipeline

import (
	"math"
	"strings"
	"testing"

	"github.com/yashraj/disco/internal/model"
)

func buildInput(t *testing.T, p model.AdvertiserProfile) BuildInput {
	t.Helper()
	c := loadCatalog(t)
	scores := ScoreAll(p, c)

	// Stand in for stage 3: recommend the four best-scoring ungated publishers.
	var verdicts []model.FitVerdict
	rank := 0
	for _, s := range scores {
		v := model.FitVerdict{PublisherID: s.PublisherID, Verdict: "excluded", Reason: "low fit"}
		if s.HardGate == GateNone && s.Score > 0.45 && rank < 4 {
			rank++
			v = model.FitVerdict{PublisherID: s.PublisherID, Verdict: "recommended",
				Rank: rank, Reason: "strong category and audience match"}
		}
		verdicts = append(verdicts, v)
	}

	return BuildInput{
		Brief:    p.RawBrief,
		Profile:  p,
		Scores:   scores,
		Verdicts: verdicts,
		Personas: model.PersonaSelection{
			Selected: []model.PersonaPick{
				{PersonaID: "persona_004", Rationale: "pet parent"},
				{PersonaID: "persona_002", Rationale: "busy parent"},
				{PersonaID: "persona_001", Rationale: "wellness optimizer"},
			},
			Rejected: []model.PersonaRejection{{PersonaID: "persona_003", Reason: "no pet affinity"}},
		},
		Creatives: []model.Creative{
			{PersonaID: "persona_004", Headline: "a", Body: "b"},
			{PersonaID: "persona_002", Headline: "c", Body: "d"},
			{PersonaID: "persona_001", Headline: "e", Body: "f"},
		},
		Catalog:  c,
		Params:   DefaultAllocParams(),
		Model:    "gemini-2.5-flash",
		Provider: "fixture",
	}
}

func TestBuildLedgerCoversEveryPublisher(t *testing.T) {
	c := loadCatalog(t)
	got := Build(buildInput(t, dogFood()))
	if len(got.PublisherLedger) != len(c.Publishers) {
		t.Fatalf("ledger has %d entries, want %d", len(got.PublisherLedger), len(c.Publishers))
	}
	seen := map[string]bool{}
	for _, e := range got.PublisherLedger {
		if e.PublisherName == "" {
			t.Errorf("%s has no publisher name", e.PublisherID)
		}
		if e.Reason == "" {
			t.Errorf("%s has no reason", e.PublisherID)
		}
		seen[e.PublisherID] = true
	}
	for _, p := range c.Publishers {
		if !seen[p.ID] {
			t.Errorf("%s missing from ledger", p.ID)
		}
	}
}

func TestBuildAllocatesOnlyToRecommended(t *testing.T) {
	got := Build(buildInput(t, dogFood()))
	rec := map[string]bool{}
	for _, e := range got.PublisherLedger {
		if e.Verdict == "recommended" {
			rec[e.PublisherID] = true
		}
	}
	if len(got.Budget.Allocation) == 0 {
		t.Fatal("no allocation produced")
	}
	var sum float64
	for _, a := range got.Budget.Allocation {
		if !rec[a.PublisherID] {
			t.Errorf("%s got budget but is not recommended", a.PublisherID)
		}
		sum += a.Share
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Errorf("allocation shares sum to %v, want 1", sum)
	}
	if got.Budget.DailyCapUSD <= 0 {
		t.Error("daily cap not set")
	}
}

func TestBuildDerivesObjective(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*model.AdvertiserProfile)
		want   string
	}{
		{"b2b", func(p *model.AdvertiserProfile) { p.BusinessModel = "b2b" }, "consideration"},
		{"service", func(p *model.AdvertiserProfile) { p.BusinessModel = "service" }, "consideration"},
		{"subscription", func(p *model.AdvertiserProfile) { p.BusinessModel = "subscription" }, "conversion"},
		{"luxury", func(p *model.AdvertiserProfile) {
			p.BusinessModel = "one_off"
			p.PriceTier = "luxury"
		}, "awareness"},
		{"default", func(p *model.AdvertiserProfile) {
			p.BusinessModel = "one_off"
			p.PriceTier = "mid"
		}, "conversion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := dogFood()
			tc.mutate(&p)
			if got := Build(buildInput(t, p)).Objective; got != tc.want {
				t.Errorf("objective = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildDerivesBidFromWeightedCPM(t *testing.T) {
	got := Build(buildInput(t, dogFood()))
	var w float64
	for _, a := range got.Budget.Allocation {
		w += a.Share * a.EstCPMUSD
	}
	if math.Abs(got.Bid.TargetUSD-w) > 0.01 {
		t.Errorf("bid target = %.2f, want weighted CPM %.2f", got.Bid.TargetUSD, w)
	}
	if math.Abs(got.Bid.FloorUSD-0.8*w) > 0.01 {
		t.Errorf("bid floor = %.2f, want %.2f", got.Bid.FloorUSD, 0.8*w)
	}
	if math.Abs(got.Bid.CeilingUSD-1.4*w) > 0.01 {
		t.Errorf("bid ceiling = %.2f, want %.2f", got.Bid.CeilingUSD, 1.4*w)
	}
	if got.Bid.Strategy != "target_cpa_capped" {
		t.Errorf("strategy = %q, want target_cpa_capped", got.Bid.Strategy)
	}
	if want := 0.35 * float64(dogFood().EstimatedAOVUSD); math.Abs(got.Bid.TargetCPAUSD-want) > 0.01 {
		t.Errorf("target CPA = %.2f, want %.2f", got.Bid.TargetCPAUSD, want)
	}
}

// A non-consumer brief produces no budget, no creatives, and a ledger in which
// every publisher is excluded for the same honest reason.
func TestBuildNoRecommendationForNonConsumerBrief(t *testing.T) {
	p := dogFood()
	p.IsConsumerDTC = false
	in := buildInput(t, p)
	in.Creatives = nil
	in.Personas = model.PersonaSelection{}
	got := Build(in)

	if got.Status != model.StatusNoRecommendation {
		t.Errorf("status = %q, want %q", got.Status, model.StatusNoRecommendation)
	}
	if len(got.Budget.Allocation) != 0 {
		t.Errorf("got %d allocations, want 0", len(got.Budget.Allocation))
	}
	if len(got.Creatives) != 0 {
		t.Errorf("got %d creatives, want 0", len(got.Creatives))
	}
	for _, e := range got.PublisherLedger {
		if e.Verdict != "excluded" {
			t.Errorf("%s verdict = %q, want excluded", e.PublisherID, e.Verdict)
		}
	}
}

func TestBuildLowConfidenceAsksForClarification(t *testing.T) {
	p := dogFood()
	p.Confidence = "low"
	p.MissingSignals = []string{"price point", "who buys it"}
	got := Build(buildInput(t, p))
	if got.Status != model.StatusNeedsClarification {
		t.Errorf("status = %q, want %q", got.Status, model.StatusNeedsClarification)
	}
	if len(got.Clarifications) == 0 {
		t.Error("no clarifying questions produced")
	}
	if len(got.Budget.Allocation) == 0 {
		t.Error("a provisional campaign should still be emitted at low confidence")
	}
}

func TestBuildTargetingUnionsRecommendedPublishers(t *testing.T) {
	got := Build(buildInput(t, dogFood()))
	if len(got.Targeting.Geos) == 0 {
		t.Error("no geos derived")
	}
	if len(got.Targeting.PersonaIDs) != 3 {
		t.Errorf("got %d persona IDs, want 3", len(got.Targeting.PersonaIDs))
	}
	if got.Targeting.AgeRange == "" {
		t.Error("no age range derived")
	}
}

// --- Ruling-driven tests (design rulings 3 and 1 in the task-5 instructions) ---

// TestBuildSurfacesUnallocatedBudget forces an inventory shortfall (budget far
// exceeds what the recommended publishers can physically deliver in the
// flight) and checks that Budget.UnallocatedUSD reports it honestly as
// TotalUSD minus the sum of what was actually deployed. This would fail
// against a stub that always returns 0, and against a naive implementation
// that computes AmountUSD as TotalUSD*Share (ruling 2) instead of reading
// Allocate's AmountUSD directly, since that identity does not hold here.
func TestBuildSurfacesUnallocatedBudget(t *testing.T) {
	in := buildInput(t, dogFood())
	in.Params.TotalUSD = 5_000_000 // far beyond any publisher's deliverable inventory
	got := Build(in)

	if len(got.Budget.Allocation) == 0 {
		t.Fatal("no allocation produced")
	}
	var sum float64
	for _, a := range got.Budget.Allocation {
		sum += a.AmountUSD
	}
	if sum >= in.Params.TotalUSD {
		t.Fatalf("test setup did not create a shortfall: allocated %.2f of %.2f", sum, in.Params.TotalUSD)
	}
	want := in.Params.TotalUSD - sum
	if math.Abs(got.Budget.UnallocatedUSD-want) > 0.01 {
		t.Errorf("unallocated_usd = %.2f, want %.2f", got.Budget.UnallocatedUSD, want)
	}
	if got.Budget.UnallocatedUSD <= 0 {
		t.Error("expected a positive unallocated amount given the inventory shortfall")
	}

	// Sanity check on ruling 2: AmountUSD must not equal TotalUSD*Share here.
	for _, a := range got.Budget.Allocation {
		naive := in.Params.TotalUSD * a.Share
		if math.Abs(a.AmountUSD-naive) < 0.01 {
			t.Errorf("%s: AmountUSD (%.2f) suspiciously equals TotalUSD*Share (%.2f) under a shortfall",
				a.PublisherID, a.AmountUSD, naive)
		}
	}
}

// TestBuildUnallocatedIsZeroWhenFullyDeployed checks the non-shortfall path:
// with the default budget, the recommended publishers can absorb the whole
// spend, so UnallocatedUSD must be (approximately) zero. This fails against
// an implementation that always reports TotalUSD - naive-something or that
// forgets to clamp float noise near zero to exactly zero.
func TestBuildUnallocatedIsZeroWhenFullyDeployed(t *testing.T) {
	got := Build(buildInput(t, dogFood()))
	var sum float64
	for _, a := range got.Budget.Allocation {
		sum += a.AmountUSD
	}
	if sum < got.Budget.TotalUSD-0.01 && got.Budget.UnallocatedUSD != 0 {
		t.Errorf("unallocated_usd = %.4f, want 0 when spend is fully deployed", got.Budget.UnallocatedUSD)
	}
}

// TestBuildSurfacesExceedsMaxShare recommends a single publisher, which makes
// the 40% concentration cap mechanically infeasible (one survivor always
// takes 100% of what was deployed). The resulting allocation entry must carry
// ExceedsMaxShare and a rationale sentence a reader can see. This would fail
// against an implementation that drops the ExceedsMaxShare field on the way
// from pipeline.Allocation to model.AllocationEntry, or that never appends
// the explanatory sentence to Rationale.
func TestBuildSurfacesExceedsMaxShare(t *testing.T) {
	c := loadCatalog(t)
	p := dogFood()
	scores := ScoreAll(p, c)

	best := scores[0]
	for _, s := range scores {
		if s.HardGate == GateNone && s.Score > best.Score {
			best = s
		}
	}

	var verdicts []model.FitVerdict
	for _, s := range scores {
		if s.PublisherID == best.PublisherID {
			verdicts = append(verdicts, model.FitVerdict{
				PublisherID: s.PublisherID, Verdict: "recommended", Rank: 1,
				Reason: "sole qualifying publisher",
			})
			continue
		}
		verdicts = append(verdicts, model.FitVerdict{
			PublisherID: s.PublisherID, Verdict: "excluded", Reason: "low fit",
		})
	}

	in := BuildInput{
		Brief:    p.RawBrief,
		Profile:  p,
		Scores:   scores,
		Verdicts: verdicts,
		Catalog:  c,
		Params:   DefaultAllocParams(),
		Model:    "gemini-2.5-flash",
		Provider: "fixture",
	}
	got := Build(in)

	if len(got.Budget.Allocation) != 1 {
		t.Fatalf("got %d allocations, want 1", len(got.Budget.Allocation))
	}
	a := got.Budget.Allocation[0]
	if !a.ExceedsMaxShare {
		t.Fatal("expected ExceedsMaxShare to be true for a lone recommended publisher")
	}
	if !strings.Contains(strings.ToLower(a.Rationale), "40%") && !strings.Contains(strings.ToLower(a.Rationale), "concentration") {
		t.Errorf("rationale does not mention the forced concentration: %q", a.Rationale)
	}
}
