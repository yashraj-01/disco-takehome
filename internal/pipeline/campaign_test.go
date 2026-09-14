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
// every publisher is excluded for the same honest reason. The upstream
// artifacts here are deliberately fully populated, and stage 3's verdicts
// deliberately recommend several publishers: the no-recommendation path must
// discard that work, not merely pass through inputs that were already empty.
// A stub that unconditionally copies c.Creatives = in.Creatives (or unions
// selected personas into Targeting.PersonaIDs) regardless of Status would
// fail this test, whereas it would have passed the old version that fed it
// nil/empty inputs to begin with.
func TestBuildNoRecommendationForNonConsumerBrief(t *testing.T) {
	c := loadCatalog(t)
	p := dogFood()
	p.IsConsumerDTC = false
	scores := ScoreAll(p, c)

	var verdicts []model.FitVerdict
	for i, s := range scores {
		if i < 5 {
			verdicts = append(verdicts, model.FitVerdict{
				PublisherID: s.PublisherID, Verdict: "recommended", Rank: i + 1, Reason: "strong fit",
			})
			continue
		}
		verdicts = append(verdicts, model.FitVerdict{PublisherID: s.PublisherID, Verdict: "excluded", Reason: "low fit"})
	}

	in := BuildInput{
		Brief:    p.RawBrief,
		Profile:  p,
		Scores:   scores,
		Verdicts: verdicts,
		Personas: model.PersonaSelection{
			Selected: []model.PersonaPick{
				{PersonaID: "persona_004", Rationale: "pet parent"},
				{PersonaID: "persona_002", Rationale: "busy parent"},
			},
			Rejected: []model.PersonaRejection{{PersonaID: "persona_003", Reason: "no pet affinity"}},
		},
		Creatives: []model.Creative{
			{PersonaID: "persona_004", Headline: "a", Body: "b"},
			{PersonaID: "persona_002", Headline: "c", Body: "d"},
		},
		Catalog:  c,
		Params:   DefaultAllocParams(),
		Model:    "gemini-2.5-flash",
		Provider: "fixture",
	}
	got := Build(in)

	if got.Status != model.StatusNoRecommendation {
		t.Errorf("status = %q, want %q", got.Status, model.StatusNoRecommendation)
	}
	if len(got.Budget.Allocation) != 0 {
		t.Errorf("got %d allocations, want 0", len(got.Budget.Allocation))
	}
	if len(got.Creatives) != 0 {
		t.Errorf("got %d creatives, want 0 (upstream creatives must be discarded, not just passed through)", len(got.Creatives))
	}
	if len(got.Targeting.PersonaIDs) != 0 {
		t.Errorf("got %d persona IDs in targeting, want 0", len(got.Targeting.PersonaIDs))
	}
	if len(got.PublisherLedger) != len(c.Publishers) {
		t.Fatalf("ledger has %d entries, want %d", len(got.PublisherLedger), len(c.Publishers))
	}
	for _, e := range got.PublisherLedger {
		if e.Verdict != "excluded" {
			t.Errorf("%s verdict = %q, want excluded", e.PublisherID, e.Verdict)
		}
	}
}

// TestBuildHardGateOverridesContradictingVerdict feeds Build verdicts that
// deliberately contradict the hard gates - every publisher marked
// "recommended" with a nonzero rank, including ones a real stage 3 would
// never approve. The gate override in Build must still win: a hard-gated
// publisher must come out excluded with Rank 0 regardless of what verdict it
// arrived with. Deleting that override block would not fail the other tests
// (buildInput's mock stage 3 never recommends a gated publisher in the first
// place) but must fail this one.
func TestBuildHardGateOverridesContradictingVerdict(t *testing.T) {
	c := loadCatalog(t)

	contradictingVerdicts := func(scores []model.PublisherScore) []model.FitVerdict {
		var verdicts []model.FitVerdict
		for i, s := range scores {
			verdicts = append(verdicts, model.FitVerdict{
				PublisherID: s.PublisherID, Verdict: "recommended", Rank: i + 1, Reason: "stage 3 says yes",
			})
		}
		return verdicts
	}

	t.Run("partial_gate", func(t *testing.T) {
		p := activewear()
		scores := ScoreAll(p, c)
		got := Build(BuildInput{
			Brief: p.RawBrief, Profile: p, Scores: scores, Verdicts: contradictingVerdicts(scores),
			Catalog: c, Params: DefaultAllocParams(), Model: "m", Provider: "fixture",
		})

		var gatedChecked, ungatedFound bool
		for _, e := range got.PublisherLedger {
			if e.PublisherID == "pub_005" { // Linden Park: 50-70 audience, activewear targets 25-45
				gatedChecked = true
				if e.HardGate == GateNone {
					t.Fatal("test setup broken: pub_005 is not hard-gated under the activewear profile")
				}
				if e.Verdict != "excluded" {
					t.Errorf("pub_005 (hard-gated) verdict = %q, want excluded despite the contradicting verdict", e.Verdict)
				}
				if e.Rank != 0 {
					t.Errorf("pub_005 (hard-gated) rank = %d, want 0", e.Rank)
				}
			}
			if e.HardGate == GateNone && e.Verdict == "recommended" {
				ungatedFound = true
				if e.Rank == 0 {
					t.Errorf("%s: ungated recommended publisher lost its rank", e.PublisherID)
				}
			}
		}
		if !gatedChecked {
			t.Fatal("pub_005 missing from ledger")
		}
		if !ungatedFound {
			t.Fatal("no ungated publisher kept its recommendation - gate override may be too aggressive")
		}
	})

	t.Run("all_gated_non_consumer", func(t *testing.T) {
		p := activewear()
		p.IsConsumerDTC = false
		scores := ScoreAll(p, c)
		got := Build(BuildInput{
			Brief: p.RawBrief, Profile: p, Scores: scores, Verdicts: contradictingVerdicts(scores),
			Catalog: c, Params: DefaultAllocParams(), Model: "m", Provider: "fixture",
		})
		for _, e := range got.PublisherLedger {
			if e.HardGate == GateNone {
				t.Fatalf("test setup broken: %s is not hard-gated under a non-consumer profile", e.PublisherID)
			}
			if e.Verdict != "excluded" {
				t.Errorf("%s verdict = %q, want excluded (every publisher is hard-gated)", e.PublisherID, e.Verdict)
			}
			if e.Rank != 0 {
				t.Errorf("%s rank = %d, want 0", e.PublisherID, e.Rank)
			}
		}
	})
}

// TestBuildExceedsMaxShareRationaleStatesEffectNotCause pins the wording rule:
// the rationale sentence added when ExceedsMaxShare is set must describe the
// effect (share over the 40% guideline) without asserting a specific cause,
// since the flag can be set for reasons other than "fewer than 3 publishers
// qualified" (see allocate.go's doc comment on inventory reconciliation). It
// also checks the sentence is absent when the flag is not set.
func TestBuildExceedsMaxShareRationaleStatesEffectNotCause(t *testing.T) {
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
				PublisherID: s.PublisherID, Verdict: "recommended", Rank: 1, Reason: "sole qualifying publisher",
			})
			continue
		}
		verdicts = append(verdicts, model.FitVerdict{PublisherID: s.PublisherID, Verdict: "excluded", Reason: "low fit"})
	}
	got := Build(BuildInput{
		Brief: p.RawBrief, Profile: p, Scores: scores, Verdicts: verdicts,
		Catalog: c, Params: DefaultAllocParams(), Model: "m", Provider: "fixture",
	})

	if len(got.Budget.Allocation) != 1 {
		t.Fatalf("got %d allocations, want 1", len(got.Budget.Allocation))
	}
	a := got.Budget.Allocation[0]
	if !a.ExceedsMaxShare {
		t.Fatal("expected ExceedsMaxShare true for a lone recommended publisher")
	}
	lower := strings.ToLower(a.Rationale)
	if !strings.Contains(lower, "40%") {
		t.Errorf("rationale does not mention the 40%% guideline: %q", a.Rationale)
	}
	if strings.Contains(lower, "fewer than 3") || strings.Contains(lower, "fewer than three") {
		t.Errorf("rationale asserts a specific cause it cannot know: %q", a.Rationale)
	}

	// With several recommended publishers the cap is satisfiable, so no
	// entry should carry the concentration sentence at all.
	got2 := Build(buildInput(t, p))
	for _, e := range got2.Budget.Allocation {
		if e.ExceedsMaxShare {
			t.Fatalf("test setup broken: %s unexpectedly exceeds max share in the multi-publisher case", e.PublisherID)
		}
		if strings.Contains(strings.ToLower(e.Rationale), "concentration guideline") {
			t.Errorf("%s: rationale mentions the concentration guideline despite ExceedsMaxShare=false: %q",
				e.PublisherID, e.Rationale)
		}
	}
}

// TestBuildRecommendedButNothingAllocated pins ruling 4: Allocate can return
// an empty list even when publishers were recommended (here, zero deliverable
// inventory via SOVCap=0). Build must not assume a 1:1 correspondence between
// recommended publishers and allocation rows - e.g. it must not index into
// Allocate's result positionally by candidate index, which would panic the
// moment the result is shorter than the candidate list.
func TestBuildRecommendedButNothingAllocated(t *testing.T) {
	in := buildInput(t, dogFood())
	in.Params.SOVCap = 0 // no publisher can deliver any inventory at all
	got := Build(in)

	if got.Status != model.StatusReady {
		t.Errorf("status = %q, want %q", got.Status, model.StatusReady)
	}
	if len(got.Budget.Allocation) != 0 {
		t.Errorf("got %d allocations, want 0 (zero deliverable inventory)", len(got.Budget.Allocation))
	}
	for name, v := range map[string]float64{
		"floor": got.Bid.FloorUSD, "target": got.Bid.TargetUSD,
		"ceiling": got.Bid.CeilingUSD, "target_cpa": got.Bid.TargetCPAUSD,
	} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("bid.%s is not finite: %v", name, v)
		}
	}
	if got.Bid.TargetUSD != 0 {
		t.Errorf("bid target = %v, want 0 when nothing was allocated", got.Bid.TargetUSD)
	}
	if len(got.Targeting.Geos) != 0 || len(got.Targeting.IncomeTiers) != 0 || len(got.Targeting.ContextualCategories) != 0 {
		t.Error("targeting derived from allocation should be empty when nothing was allocated")
	}
	if math.Abs(got.Budget.UnallocatedUSD-in.Params.TotalUSD) > 0.01 {
		t.Errorf("unallocated_usd = %.2f, want the full budget %.2f", got.Budget.UnallocatedUSD, in.Params.TotalUSD)
	}
	c := loadCatalog(t)
	if len(got.PublisherLedger) != len(c.Publishers) {
		t.Errorf("ledger has %d entries, want %d", len(got.PublisherLedger), len(c.Publishers))
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

// TestGeneratedReasonMatchesEveryGate locks GeneratedReason to gateReason's
// actual output for every real gate constant, so the two cannot silently
// drift apart (GeneratedReason exists specifically so other packages, like
// internal/measure, don't have to hand-copy these strings).
func TestGeneratedReasonMatchesEveryGate(t *testing.T) {
	for _, gate := range []string{GateNotConsumerDTC, GateCategoryMismatch, GateDemographicMismatch} {
		if !GeneratedReason(gateReason(gate)) {
			t.Errorf("GeneratedReason(gateReason(%q)) = false, want true", gate)
		}
	}
}

// TestGeneratedReasonRejectsModelText checks that GeneratedReason does not
// mistake ordinary model-written prose for a code-generated gate reason,
// even when it shares words with one.
func TestGeneratedReasonRejectsModelText(t *testing.T) {
	if GeneratedReason("This audience's age range does not overlap ours much.") {
		t.Error("GeneratedReason should not match model-written prose that merely resembles a gate reason")
	}
	if GeneratedReason("") {
		t.Error("GeneratedReason(\"\") should be false: GateNone never produces gate-written text")
	}
}
