package pipeline

import (
	"math"
	"testing"
)

// The dog-food shortlist, with CPMs as produced by EstimateCPM.
func dogFoodCandidates() []Candidate {
	return []Candidate{
		{PublisherID: "pub_007", Fit: 0.84, EstCPM: 15.4765, MonthlyImpressions: 4_800_000},  // Pawline
		{PublisherID: "pub_009", Fit: 0.71, EstCPM: 11.4368, MonthlyImpressions: 62_000_000}, // Ruffco
		{PublisherID: "pub_008", Fit: 0.52, EstCPM: 17.8700, MonthlyImpressions: 10_400_000}, // Pantrygood
		{PublisherID: "pub_018", Fit: 0.44, EstCPM: 11.0679, MonthlyImpressions: 8_400_000},  // Tailcrate
	}
}

func shareOf(t *testing.T, as []Allocation, id string) float64 {
	t.Helper()
	for _, a := range as {
		if a.PublisherID == id {
			return a.Share
		}
	}
	return 0
}

func sumShares(as []Allocation) float64 {
	var s float64
	for _, a := range as {
		s += a.Share
	}
	return s
}

// At $25k no cap binds, so the result is pure fit^gamma. This pins the gamma
// maths; every other case builds on it.
func TestAllocateUncappedFollowsGamma(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 25_000
	got := Allocate(dogFoodCandidates(), p)

	for _, tc := range []struct {
		id   string
		want float64
	}{
		{"pub_007", 0.37832},
		{"pub_009", 0.29399},
		{"pub_008", 0.18427},
		{"pub_018", 0.14342},
	} {
		if g := shareOf(t, got, tc.id); math.Abs(g-tc.want) > 0.0005 {
			t.Errorf("%s share = %.5f, want %.5f", tc.id, g, tc.want)
		}
	}
	if s := sumShares(got); math.Abs(s-1) > 1e-9 {
		t.Errorf("shares sum to %v, want 1", s)
	}
}

// Gamma concentrates spend on better fits relative to a plain proportional
// split, without abandoning the tail.
func TestGammaConcentratesRelativeToProportional(t *testing.T) {
	c := dogFoodCandidates()
	p := DefaultAllocParams()
	p.TotalUSD = 25_000

	sharpened := Allocate(c, p)
	p.Gamma = 1.0
	flat := Allocate(c, p)

	ratio := func(as []Allocation) float64 {
		return shareOf(t, as, "pub_007") / shareOf(t, as, "pub_018")
	}
	if ratio(sharpened) <= ratio(flat) {
		t.Errorf("gamma=1.5 ratio %.2f should exceed gamma=1.0 ratio %.2f",
			ratio(sharpened), ratio(flat))
	}
}

// At $50k Pawline is still the best fit but cannot absorb its share: 37.8% of
// the budget buys 1.22M impressions against a 720k deliverable ceiling. It
// clips to exactly the ceiling and drops from first place to third.
func TestAllocateClipsOnDeliverableInventory(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 50_000
	got := Allocate(dogFoodCandidates(), p)

	for _, tc := range []struct {
		id   string
		want float64
	}{
		{"pub_009", 0.36750},
		{"pub_008", 0.23035},
		{"pub_007", 0.22286},
		{"pub_018", 0.17929},
	} {
		if g := shareOf(t, got, tc.id); math.Abs(g-tc.want) > 0.0005 {
			t.Errorf("%s share = %.5f, want %.5f", tc.id, g, tc.want)
		}
	}
	if got[0].PublisherID != "pub_009" {
		t.Errorf("top allocation = %s, want pub_009 (Ruffco) after Pawline clips",
			got[0].PublisherID)
	}
	for _, a := range got {
		if a.PublisherID == "pub_007" && a.EstImpressions > 720_000 {
			t.Errorf("Pawline impressions = %d, exceeds 15%% of 4.8M", a.EstImpressions)
		}
	}
}

// policyOnlyCandidates has generous inventory on every publisher (SOV never
// binds below MaxShare), but fit-driven proportional shares (6:3:1 of 10)
// would put the top publisher at 60% and the second at 45% before capping —
// both above the 40% concentration cap. With 3 candidates, 3*0.40=1.20>1, so
// unlike the 2-candidate case this is feasible: MaxShare should hold exactly,
// with no exception granted.
func policyOnlyCandidates() []Candidate {
	return []Candidate{
		{PublisherID: "X", Fit: 6, EstCPM: 10, MonthlyImpressions: 100_000_000},
		{PublisherID: "Y", Fit: 3, EstCPM: 10, MonthlyImpressions: 100_000_000},
		{PublisherID: "Z", Fit: 1, EstCPM: 10, MonthlyImpressions: 100_000_000},
	}
}

// Every declared cap must hold in the emitted result, not merely in an
// intermediate pass — except MaxShare, which is policy and yields when the
// candidate set makes it infeasible for everyone to stay under it (fewer
// than ceil(1/MaxShare) survivors). The SOV cap is physical and never yields.
func TestAllocateRespectsAllCapsAfterRedistribution(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 50_000

	// Feasible cases (>=3 survivors, or MaxShare not binding at all): MaxShare
	// must hold exactly, and nothing is ever marked ExceedsMaxShare. The lone
	// survivor holds 100% of the deployed spend, so its MaxShare checks stay
	// gated by len(got) > 1 exactly as the others are -- but it is NOT exempt
	// from the inventory ceiling; see TestAllocateSingleCandidateTakesEverything
	// and TestLoneSurvivorStillRespectsInventoryCeiling.
	feasible := [][]Allocation{
		Allocate(dogFoodCandidates(), p),     // 4 survivors
		Allocate(dogFoodCandidates()[:1], p), // lone survivor: share 1 by definition
		Allocate(policyOnlyCandidates(), AllocParams{Gamma: 1, MaxShare: 0.40, SOVCap: 0.15, MinShare: 0.05, TotalUSD: 50_000, Days: 30}),
	}
	for _, got := range feasible {
		for _, a := range got {
			// MinShare is deliberately NOT asserted on the final share. It is
			// a floor on the loop's fit-driven TARGET shares, applied before
			// inventory reconciliation; reconciliation then clamps survivors
			// to their inventory ceilings and redistributes the remainder, so
			// a final share below MinShare is a legitimate outcome (the $50k
			// 4-candidate case here does clip pub_007 to its ceiling). What
			// actually holds afterwards is that every emitted row carries real
			// money. Asserting MinShare here previously passed only because
			// these particular fixtures never happened to trip it; the loop's
			// own MinShare behaviour is covered by TestFixedPointLoopIsRequired,
			// whose fixture has non-binding inventory so reconciliation is a
			// no-op and the loop's output IS the final answer.
			if a.AmountUSD < 0.01 {
				t.Errorf("%s emitted with amount %.4f, below one cent", a.PublisherID, a.AmountUSD)
			}
			if a.Share > p.MaxShare+1e-9 && len(got) > 1 {
				t.Errorf("%s share %.4f above cap %.2f", a.PublisherID, a.Share, p.MaxShare)
			}
			if a.ExceedsMaxShare && len(got) > 1 {
				t.Errorf("%s marked ExceedsMaxShare in a feasible case (share %.4f)", a.PublisherID, a.Share)
			}
		}
		if s := sumShares(got); math.Abs(s-1) > 1e-9 {
			t.Errorf("shares sum to %v, want 1", s)
		}
	}

	// Infeasible case: exactly 2 candidates under a 0.40 cap can never both
	// stay under it and sum to 1 (0.40+0.40 < 1). pub_007's SOV ceiling
	// (720k impressions) never yields; pub_009's concentration cap does,
	// absorbing the remainder instead of pub_007 being dropped.
	got := Allocate(dogFoodCandidates()[:2], p)
	if s := sumShares(got); math.Abs(s-1) > 1e-9 {
		t.Errorf("shares sum to %v, want 1", s)
	}
	for _, tc := range []struct {
		id          string
		wantShare   float64
		wantExceeds bool
	}{
		{"pub_007", 0.22286, false},
		{"pub_009", 0.77714, true},
	} {
		var found bool
		for _, a := range got {
			if a.PublisherID != tc.id {
				continue
			}
			found = true
			if math.Abs(a.Share-tc.wantShare) > 0.0005 {
				t.Errorf("%s share = %.5f, want %.5f", tc.id, a.Share, tc.wantShare)
			}
			if a.ExceedsMaxShare != tc.wantExceeds {
				t.Errorf("%s ExceedsMaxShare = %v, want %v", tc.id, a.ExceedsMaxShare, tc.wantExceeds)
			}
		}
		if !found {
			t.Fatalf("no allocation for %s in %+v", tc.id, got)
		}
	}
	if len(got) != 2 {
		t.Fatalf("expected both publishers to survive (fitting inventory not dropped), got %+v", got)
	}
}

// No allocation may ever exceed its publisher's SOV (inventory) ceiling in
// impressions, in any of these multi-candidate scenarios — including the
// infeasible 2-candidate case where MaxShare itself is allowed to yield, and
// including the lone-survivor case, which is not exempt: a single candidate
// takes 100% of DEPLOYED spend, which is not the same claim as taking more
// impressions than its publisher has. This is the cap that must never yield.
func TestAllocateNeverExceedsSOVCeiling(t *testing.T) {
	p25 := DefaultAllocParams()
	p25.TotalUSD = 25_000
	p50 := DefaultAllocParams()
	p50.TotalUSD = 50_000
	cascadeParams := AllocParams{Gamma: 1, MaxShare: 0.40, SOVCap: 0.15, MinShare: 0.05, TotalUSD: 10_000, Days: 30}

	cases := []struct {
		name  string
		cands []Candidate
		p     AllocParams
	}{
		{"4-candidate $25k", dogFoodCandidates(), p25},
		{"4-candidate $50k", dogFoodCandidates(), p50},
		{"2-candidate $50k (infeasible MaxShare)", dogFoodCandidates()[:2], p50},
		{"1-candidate $50k (lone survivor, not exempt)", dogFoodCandidates()[:1], p50},
		{"cascade", cascadeCandidates(), cascadeParams},
	}
	for _, tc := range cases {
		got := Allocate(tc.cands, tc.p)
		byID := map[string]Candidate{}
		for _, c := range tc.cands {
			byID[c.PublisherID] = c
		}
		for _, a := range got {
			c := byID[a.PublisherID]
			ceiling := float64(c.MonthlyImpressions) * tc.p.SOVCap
			if float64(a.EstImpressions) > ceiling+1 {
				t.Errorf("%s: %s impressions = %d, exceeds SOV ceiling %.0f",
					tc.name, a.PublisherID, a.EstImpressions, ceiling)
			}
		}
	}
}

// A lone survivor takes 100% of the deployed spend rather than being dropped
// by the floor -- but "100%" is a statement about share, not a licence to buy
// inventory that does not exist. pub_007 can sell $11,143.08 at a 15% SOV cap
// (720,000 impressions), so a $25,000 budget deploys that much and no more.
// See TestLoneSurvivorStillRespectsInventoryCeiling for the two counterexamples
// that made this distinction load-bearing.
func TestAllocateSingleCandidateTakesEverything(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 25_000
	got := Allocate(dogFoodCandidates()[:1], p)
	if len(got) != 1 || math.Abs(got[0].Share-1) > 1e-9 {
		t.Fatalf("got %+v, want a single 100%% allocation", got)
	}
	if math.Abs(got[0].AmountUSD-11143.08) > 0.01 {
		t.Errorf("amount = %.2f, want 11143.08 (its whole deliverable inventory)", got[0].AmountUSD)
	}
	if got[0].EstImpressions > 720_000 {
		t.Errorf("impressions = %d, exceeds the 720,000 SOV ceiling", got[0].EstImpressions)
	}
}

func TestAllocateEmptyInput(t *testing.T) {
	if got := Allocate(nil, DefaultAllocParams()); len(got) != 0 {
		t.Errorf("got %d allocations, want 0", len(got))
	}
}

// AmountUSD == TotalUSD*Share only holds when the candidate set is not
// inventory-starved (see Allocation's doc comment) -- $25k is well under the
// dog-food shortlist's combined SOV ceiling of ~$159,328, so that identity is
// exactly what this case exercises. It does NOT hold in the starved case
// (see TestAllocateFullyStarvedDeployment): there, AmountUSD is capped at
// each candidate's SOV ceiling and Share is redefined as a fraction of the
// (smaller) deployed total, not of TotalUSD.
func TestAllocateComputesDollarsAndImpressions(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 25_000
	got := Allocate(dogFoodCandidates(), p)
	for _, a := range got {
		wantUSD := p.TotalUSD * a.Share
		if math.Abs(a.AmountUSD-wantUSD) > 0.01 {
			t.Errorf("%s amount = %.2f, want %.2f", a.PublisherID, a.AmountUSD, wantUSD)
		}
		wantImp := int64(a.AmountUSD / a.EstCPMUSD * 1000)
		if a.EstImpressions != wantImp {
			t.Errorf("%s impressions = %d, want %d", a.PublisherID, a.EstImpressions, wantImp)
		}
	}
}

// cascadeCandidates is engineered so a single pass of the fit/MaxShare/dust
// loop is provably insufficient, purely from MaxShare + dust-floor
// interaction (SOV plays no role: every candidate has generous inventory, so
// reconciliation never touches these numbers -- the loop's own output is the
// final answer). Gamma is pinned to 1 so the pre-cap shares are exact
// fractions of 16: A=0.625, B=0.28125, C=0.0625, D=0.03125.
//
// Pass 0 locks A at the 0.40 cap and dust-drops D (0.03125 < the 0.05 floor)
// in the same pass. Only pass 1's redistribution of D's freed share among
// B and C reveals that B (0.28125 -> 0.490909) now also exceeds 0.40; pass 2
// stabilizes C. A single pass never reaches that redistribution at all, so
// A itself -- the very share the single pass just "capped" -- ends up back
// over 0.40 once the closing renormalisation divides every survivor
// (including locked A) by a sum that is short of 1.
func cascadeCandidates() []Candidate {
	return []Candidate{
		{PublisherID: "A", Fit: 10, EstCPM: 10, MonthlyImpressions: 100_000_000},
		{PublisherID: "B", Fit: 4.5, EstCPM: 10, MonthlyImpressions: 100_000_000},
		{PublisherID: "C", Fit: 1, EstCPM: 10, MonthlyImpressions: 100_000_000},
		{PublisherID: "D", Fit: 0.5, EstCPM: 10, MonthlyImpressions: 100_000_000},
	}
}

// TestFixedPointLoopIsRequired proves the loop is load-bearing, not
// decorative: calling the unexported allocateWithPasses with passes=1 must
// reproduce a cap violation on cascadeCandidates, and the real pass count
// must fix it -- with every survivor's SOV ceiling untouched either way,
// since generous inventory means this is a pure MaxShare/dust-floor test.
func TestFixedPointLoopIsRequired(t *testing.T) {
	c := cascadeCandidates()
	p := AllocParams{Gamma: 1, MaxShare: 0.40, SOVCap: 0.15, MinShare: 0.05,
		TotalUSD: 10_000, Days: 30}

	// A single pass: prove it actually violates the cap for this input, per
	// the brief's own warning, rather than merely asserting it in the
	// abstract.
	single := allocateWithPasses(c, p, 1)
	violated := false
	for _, a := range single {
		if a.Share > p.MaxShare+1e-9 {
			violated = true
			t.Logf("single pass: %s share = %.4f (cap %.2f) -- violates cap, as expected",
				a.PublisherID, a.Share, p.MaxShare)
		}
	}
	if !violated {
		t.Fatalf("expected a single pass to violate the concentration cap on this input; "+
			"got %+v -- the constructed cascade no longer stresses the loop", single)
	}

	// The real, fully-iterated algorithm must not violate it.
	fixed := Allocate(c, p)
	for _, a := range fixed {
		if a.Share > p.MaxShare+1e-9 {
			t.Errorf("fixed-point result: %s share = %.4f exceeds cap %.2f",
				a.PublisherID, a.Share, p.MaxShare)
		}
		if a.Share < p.MinShare-1e-9 {
			t.Errorf("fixed-point result: %s share = %.4f below floor %.2f",
				a.PublisherID, a.Share, p.MinShare)
		}
	}
	if s := sumShares(fixed); math.Abs(s-1) > 1e-9 {
		t.Errorf("shares sum to %v, want 1", s)
	}
}

// The dog-food shortlist's combined SOV (inventory) ceiling at the default
// 15% cap is $11,143.08 + $106,362.24 + $27,877.20 + $13,945.55 = $159,328.07.
// At $200,000 -- an ordinary campaign budget, not a synthetic edge -- the
// budget cannot be fully deployed on this shortlist at all: every candidate
// is starved simultaneously. The correct answer is to buy each candidate's
// full inventory and leave the shortfall undeployed, not to inflate anyone
// past their physical ceiling. Expected values are the exact numbers the
// implementation computes (deliverableUSD per candidate, and each one's
// fraction of the $159,328.07 actually deployed), asserted to a tolerance of
// 0.0005 on shares per the spec, not trusted from hand-rounding.
func TestAllocateFullyStarvedDeployment(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 200_000
	got := Allocate(dogFoodCandidates(), p)

	for _, tc := range []struct {
		id          string
		wantShare   float64
		wantAmount  float64
		wantExceeds bool
	}{
		{"pub_007", 0.06994, 11143.08, false},
		{"pub_009", 0.66757, 106362.24, true},
		{"pub_008", 0.17497, 27877.20, false},
		{"pub_018", 0.08753, 13945.55, false},
	} {
		var found bool
		for _, a := range got {
			if a.PublisherID != tc.id {
				continue
			}
			found = true
			if math.Abs(a.Share-tc.wantShare) > 0.0005 {
				t.Errorf("%s share = %.5f, want %.5f", tc.id, a.Share, tc.wantShare)
			}
			if math.Abs(a.AmountUSD-tc.wantAmount) > 0.01 {
				t.Errorf("%s amount = %.2f, want %.2f", tc.id, a.AmountUSD, tc.wantAmount)
			}
			if a.ExceedsMaxShare != tc.wantExceeds {
				t.Errorf("%s ExceedsMaxShare = %v, want %v", tc.id, a.ExceedsMaxShare, tc.wantExceeds)
			}
		}
		if !found {
			t.Fatalf("no allocation for %s in %+v", tc.id, got)
		}
	}

	// Amounts sum to the deliverable total, strictly below the requested
	// budget -- the shortfall is real and must not be silently absorbed.
	var sumAmount float64
	for _, a := range got {
		sumAmount += a.AmountUSD
	}
	const wantDeployed = 159_328.07
	if math.Abs(sumAmount-wantDeployed) > 1 {
		t.Errorf("amounts sum to %.2f, want %.2f (deliverable total)", sumAmount, wantDeployed)
	}
	if sumAmount >= p.TotalUSD {
		t.Errorf("amounts sum to %.2f, want strictly less than the %.0f budget", sumAmount, p.TotalUSD)
	}

	// Shares still sum to 1.0 -- of what was actually deployed, not of TotalUSD.
	if s := sumShares(got); math.Abs(s-1) > 1e-9 {
		t.Errorf("shares sum to %v, want 1", s)
	}
}

// fiveCandidates extends the dog-food shortlist with a fifth publisher, for
// sweep coverage at 5 survivors.
func fiveCandidates() []Candidate {
	c := dogFoodCandidates()
	return append(c, Candidate{PublisherID: "pub_099", Fit: 0.30, EstCPM: 12.0, MonthlyImpressions: 6_000_000})
}

// zeroCPMCandidates has one candidate with no usable CPM data alongside two
// normal ones -- its deliverableUSD must come out to exactly zero, not NaN
// or +Inf, and it must receive nothing rather than crash the reconciliation.
func zeroCPMCandidates() []Candidate {
	return []Candidate{
		{PublisherID: "nocpm", Fit: 0.6, EstCPM: 0, MonthlyImpressions: 5_000_000},
		{PublisherID: "normal1", Fit: 0.5, EstCPM: 10, MonthlyImpressions: 50_000_000},
		{PublisherID: "normal2", Fit: 0.3, EstCPM: 10, MonthlyImpressions: 50_000_000},
	}
}

// zeroImpressionsCandidates has one candidate with zero monthly inventory
// alongside two normal ones -- same zero-deliverable requirement as above,
// via the other input that can produce it.
func zeroImpressionsCandidates() []Candidate {
	return []Candidate{
		{PublisherID: "noimp", Fit: 0.6, EstCPM: 10, MonthlyImpressions: 0},
		{PublisherID: "normal1", Fit: 0.5, EstCPM: 10, MonthlyImpressions: 50_000_000},
		{PublisherID: "normal2", Fit: 0.3, EstCPM: 10, MonthlyImpressions: 50_000_000},
	}
}

// No allocation may ever exceed its publisher's SOV (inventory) ceiling in
// impressions, at any budget -- non-starved, at the edge of starvation, or
// fully starved -- and across varied candidate SETS, not just varied
// budgets against one fixed set. A fixed-set sweep is exactly what missed
// the mid-loop starvation regression (TestFullyStarvedMidLoopRegression):
// the dog-food shortlist's dust floor never fires, so it never exercised the
// path where a dust-drop shrinks the survivor set's combined capacity. This
// is the one cap the spec says has no exceptions.
func TestAllocateNeverExceedsSOVCeilingAcrossBudgetsAndSets(t *testing.T) {
	base := DefaultAllocParams()
	cascadeParams := AllocParams{Gamma: 1, MaxShare: 0.40, SOVCap: 0.15, MinShare: 0.05, TotalUSD: 10_000, Days: 30}
	zeroParams := AllocParams{Gamma: 1.5, MaxShare: 0.40, SOVCap: 0.15, MinShare: 0.05, TotalUSD: 5_000, Days: 30}

	// No row is exempt. An earlier version of this sweep skipped the ceiling
	// assertion for the single-candidate case, which is precisely why a lone
	// survivor taking 100% of the budget against a fraction of the inventory
	// shipped: the sweep that should have caught it was told not to look.
	type sweepCase struct {
		name  string
		cands []Candidate
		p     AllocParams
	}
	var cases []sweepCase
	// The dog-food shortlist's combined SOV ceiling is ~$159,328: $159k sits
	// just under it (normal path), $200k and $500k sit over it (fully-starved
	// path) -- both sides of the boundary are covered, at 1/2/3/4/5 survivors.
	for _, budget := range []float64{25_000, 50_000, 159_000, 200_000, 500_000} {
		p := base
		p.TotalUSD = budget
		full := dogFoodCandidates()
		cases = append(cases,
			sweepCase{"1-candidate", full[:1], p},
			sweepCase{"2-candidate", full[:2], p},
			sweepCase{"3-candidate", full[:3], p},
			sweepCase{"4-candidate", full, p},
			sweepCase{"5-candidate", fiveCandidates(), p},
		)
	}
	cases = append(cases,
		sweepCase{"sub-floor cascade", cascadeCandidates(), cascadeParams},
		sweepCase{"zero-CPM candidate", zeroCPMCandidates(), zeroParams},
		sweepCase{"zero-impressions candidate", zeroImpressionsCandidates(), zeroParams},
		sweepCase{"dust floor collapses to one survivor", dustFloorSurvivorCandidates(), cascadeParams},
		sweepCase{"negative fits collapse to one survivor", negativeFitSurvivorCandidates(), cascadeParams},
	)

	for _, tc := range cases {
		byID := map[string]Candidate{}
		for _, c := range tc.cands {
			byID[c.PublisherID] = c
		}
		got := Allocate(tc.cands, tc.p)
		for _, a := range got {
			c := byID[a.PublisherID]
			ceiling := float64(c.MonthlyImpressions) * tc.p.SOVCap
			if float64(a.EstImpressions) > ceiling+1 {
				t.Errorf("%s ($%.0f): %s impressions = %d, exceeds SOV ceiling %.0f",
					tc.name, tc.p.TotalUSD, a.PublisherID, a.EstImpressions, ceiling)
			}
			if math.IsNaN(a.Share) || math.IsInf(a.Share, 0) {
				t.Errorf("%s: %s share = %v, not finite", tc.name, a.PublisherID, a.Share)
			}
		}
	}
}

// All candidates delivering zero inventory (or having no usable CPM) must
// return nil -- not a slice of NaN or +Inf shares, and not a slice of
// zero-dollar rows. This holds however many candidates there are: a lone
// survivor with nothing to sell is covered by
// TestZeroInventoryLoneSurvivorReturnsNil.
func TestAllocateAllZeroDeliverableReturnsNil(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cands []Candidate
	}{
		{"all zero impressions", []Candidate{
			{PublisherID: "A", Fit: 0.5, EstCPM: 10, MonthlyImpressions: 0},
			{PublisherID: "B", Fit: 0.5, EstCPM: 10, MonthlyImpressions: 0},
		}},
		{"all zero CPM", []Candidate{
			{PublisherID: "A", Fit: 0.5, EstCPM: 0, MonthlyImpressions: 5_000_000},
			{PublisherID: "B", Fit: 0.5, EstCPM: 0, MonthlyImpressions: 5_000_000},
		}},
	} {
		got := Allocate(tc.cands, DefaultAllocParams())
		if len(got) != 0 {
			t.Errorf("%s: got %d allocations, want 0 (nil): %+v", tc.name, len(got), got)
		}
	}
}

// TestFullyStarvedMidLoopRegression is the exact counterexample that broke an
// earlier design (an up-front, one-time starvation check): the candidate set
// is NOT starved up front ($11,000 combined SOV capacity against a $10,000
// budget), so the normal path runs. Pass 0 locks A and B at the 0.40 MaxShare
// cap and dust-drops C (its 0.04 fit gives a raw share under the 0.05 floor)
// -- and the SURVIVING pair's combined SOV capacity is only $9,000, against
// the still-unchanged $10,000 budget: the set became starved mid-loop, after
// a one-time up-front check would already have cleared it. Reconciliation
// (run unconditionally after the loop finishes, never as a one-time check
// against the original candidate list) clips both A and B to their $4,500
// SOV ceilings regardless -- this holds by construction, with no need to
// re-detect starvation against a shrinking survivor set at all.
func TestFullyStarvedMidLoopRegression(t *testing.T) {
	cands := []Candidate{
		{PublisherID: "A", Fit: .48, EstCPM: 10, MonthlyImpressions: 3_000_000},
		{PublisherID: "B", Fit: .48, EstCPM: 10, MonthlyImpressions: 3_000_000},
		{PublisherID: "C", Fit: .04, EstCPM: 10, MonthlyImpressions: 1_333_334},
	}
	p := AllocParams{Gamma: 1, MaxShare: 0.40, SOVCap: 0.15, MinShare: 0.05, TotalUSD: 10_000, Days: 30}
	got := Allocate(cands, p)

	if len(got) != 2 {
		t.Fatalf("got %d allocations, want 2 (C dust-dropped): %+v", len(got), got)
	}
	var deployed float64
	for _, a := range got {
		if a.PublisherID != "A" && a.PublisherID != "B" {
			t.Errorf("unexpected survivor %s", a.PublisherID)
		}
		if a.EstImpressions != 450_000 {
			t.Errorf("%s impressions = %d, want exactly 450,000 (its SOV ceiling)",
				a.PublisherID, a.EstImpressions)
		}
		if math.Abs(a.AmountUSD-4500) > 0.01 {
			t.Errorf("%s amount = %.2f, want 4500.00", a.PublisherID, a.AmountUSD)
		}
		deployed += a.AmountUSD
	}
	if math.Abs(deployed-9000) > 0.01 {
		t.Errorf("deployed = %.2f, want 9000.00 ($1,000 underspend)", deployed)
	}
}

// dustFloorSurvivorCandidates is the reviewer's counterexample: a single
// best-fit publisher with a tiny inventory, plus two candidates whose fits are
// small enough that the dust floor removes them. The removal leaves exactly
// one survivor -- and a lone survivor must still be bound by its own SOV
// ceiling. 1,000,000 monthly impressions at a 15% cap is a 150,000-impression
// ceiling; buying the whole $10,000 budget at a $10 CPM would be 1,000,000
// impressions, 6.7x more inventory than the publisher has to sell.
func dustFloorSurvivorCandidates() []Candidate {
	return []Candidate{
		{PublisherID: "A", Fit: 1.0, EstCPM: 10, MonthlyImpressions: 1_000_000},
		{PublisherID: "B", Fit: 0.05, EstCPM: 10, MonthlyImpressions: 50_000_000},
		{PublisherID: "C", Fit: 0.05, EstCPM: 10, MonthlyImpressions: 50_000_000},
	}
}

// negativeFitSurvivorCandidates reaches the same lone-survivor state by the
// other route: two candidates score zero or below (a negative fit is clamped
// to a zero weight), so the dust floor removes them and B is left alone.
func negativeFitSurvivorCandidates() []Candidate {
	return []Candidate{
		{PublisherID: "A", Fit: -1, EstCPM: 10, MonthlyImpressions: 50_000_000},
		{PublisherID: "B", Fit: 0.5, EstCPM: 10, MonthlyImpressions: 1_000_000},
		{PublisherID: "C", Fit: -0.2, EstCPM: 10, MonthlyImpressions: 50_000_000},
	}
}

// A lone survivor takes 100% of what is DEPLOYED -- that rule was always about
// share of spend, never about escaping inventory. Impressions that do not
// exist cannot be bought, so the survivor's EstImpressions must still sit at
// or under its own SOV ceiling, and the budget it cannot absorb is left
// honestly undeployed rather than spent on inventory nobody has.
//
// Deliberately NOT fixed here: the dust-dropped candidates in both fixtures do
// have inventory, so the undeployed remainder could in principle be placed
// with them. Reinstating dropped candidates would re-couple the dust floor to
// inventory reconciliation; the underspend is visible and honest, and is
// logged as separate follow-up work.
func TestLoneSurvivorStillRespectsInventoryCeiling(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 10_000

	for _, tc := range []struct {
		name       string
		cands      []Candidate
		wantID     string
		wantAmount float64
		wantImps   int64
	}{
		{"dust floor leaves one survivor", dustFloorSurvivorCandidates(), "A", 1500, 150_000},
		{"negative fits leave one survivor", negativeFitSurvivorCandidates(), "B", 1500, 150_000},
	} {
		got := Allocate(tc.cands, p)
		if len(got) != 1 {
			t.Fatalf("%s: got %d allocations, want 1: %+v", tc.name, len(got), got)
		}
		a := got[0]
		if a.PublisherID != tc.wantID {
			t.Fatalf("%s: survivor = %s, want %s", tc.name, a.PublisherID, tc.wantID)
		}
		// 100% of deployed spend -- the part of the rule that survives.
		if math.Abs(a.Share-1) > 1e-9 {
			t.Errorf("%s: share = %v, want 1", tc.name, a.Share)
		}
		// ...but bounded by inventory, which is the part that never yielded.
		if a.EstImpressions > tc.wantImps {
			t.Errorf("%s: %s impressions = %d, exceeds SOV ceiling %d",
				tc.name, a.PublisherID, a.EstImpressions, tc.wantImps)
		}
		if math.Abs(a.AmountUSD-tc.wantAmount) > 0.01 {
			t.Errorf("%s: amount = %.2f, want %.2f (min(budget, deliverable))",
				tc.name, a.AmountUSD, tc.wantAmount)
		}
		if a.AmountUSD >= p.TotalUSD {
			t.Errorf("%s: amount = %.2f, want strictly less than the %.0f budget",
				tc.name, a.AmountUSD, p.TotalUSD)
		}
	}
}

// A lone survivor with no inventory at all can be sold nothing, so there is no
// allocation to make: the answer is nil, not a publisher booked for impressions
// that do not exist.
func TestZeroInventoryLoneSurvivorReturnsNil(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 25_000
	cands := []Candidate{
		{PublisherID: "A", Fit: 0.9, EstCPM: 10, MonthlyImpressions: 0},
		{PublisherID: "B", Fit: 0.01, EstCPM: 10, MonthlyImpressions: 0},
	}
	if got := Allocate(cands, p); len(got) != 0 {
		t.Errorf("got %d allocations, want 0 (nil): %+v", len(got), got)
	}
}

// Non-finite inputs must be rejected at the boundary, not propagated. A NaN
// Fit slips past math.Max(NaN, 0) and a NaN EstCPM slips past a `<= 0` guard;
// either one, left alone, turns every Share and AmountUSD in the result into
// NaN. A non-finite value means "no usable data", which is a zero weight or a
// zero deliverable -- never a reason to emit NaN.
func TestAllocateRejectsNonFiniteInputs(t *testing.T) {
	nan := math.NaN()
	inf := math.Inf(1)
	p := DefaultAllocParams()
	p.TotalUSD = 10_000

	for _, tc := range []struct {
		name  string
		cands []Candidate
	}{
		{"NaN fit", []Candidate{
			{PublisherID: "bad", Fit: nan, EstCPM: 10, MonthlyImpressions: 50_000_000},
			{PublisherID: "good", Fit: 0.5, EstCPM: 10, MonthlyImpressions: 50_000_000},
		}},
		{"NaN CPM", []Candidate{
			{PublisherID: "bad", Fit: 0.5, EstCPM: nan, MonthlyImpressions: 50_000_000},
			{PublisherID: "good", Fit: 0.5, EstCPM: 10, MonthlyImpressions: 50_000_000},
		}},
		{"+Inf fit", []Candidate{
			{PublisherID: "bad", Fit: inf, EstCPM: 10, MonthlyImpressions: 50_000_000},
			{PublisherID: "good", Fit: 0.5, EstCPM: 10, MonthlyImpressions: 50_000_000},
		}},
		{"+Inf CPM", []Candidate{
			{PublisherID: "bad", Fit: 0.5, EstCPM: inf, MonthlyImpressions: 50_000_000},
			{PublisherID: "good", Fit: 0.5, EstCPM: 10, MonthlyImpressions: 50_000_000},
		}},
		{"every candidate non-finite", []Candidate{
			{PublisherID: "bad1", Fit: nan, EstCPM: nan, MonthlyImpressions: 50_000_000},
			{PublisherID: "bad2", Fit: nan, EstCPM: nan, MonthlyImpressions: 50_000_000},
		}},
	} {
		byID := map[string]Candidate{}
		for _, c := range tc.cands {
			byID[c.PublisherID] = c
		}
		got := Allocate(tc.cands, p) // nil is an acceptable answer; NaN is not
		for _, a := range got {
			if math.IsNaN(a.Share) || math.IsInf(a.Share, 0) {
				t.Errorf("%s: %s share = %v, not finite", tc.name, a.PublisherID, a.Share)
			}
			if math.IsNaN(a.AmountUSD) || math.IsInf(a.AmountUSD, 0) {
				t.Errorf("%s: %s amount = %v, not finite", tc.name, a.PublisherID, a.AmountUSD)
			}
			if a.EstImpressions < 0 {
				t.Errorf("%s: %s impressions = %d, want >= 0", tc.name, a.PublisherID, a.EstImpressions)
			}
			c := byID[a.PublisherID]
			ceiling := float64(c.MonthlyImpressions) * p.SOVCap
			if float64(a.EstImpressions) > ceiling+1 {
				t.Errorf("%s: %s impressions = %d, exceeds SOV ceiling %.0f",
					tc.name, a.PublisherID, a.EstImpressions, ceiling)
			}
		}
		if len(got) > 0 {
			if s := sumShares(got); math.Abs(s-1) > 1e-9 {
				t.Errorf("%s: shares sum to %v, want 1", tc.name, s)
			}
		}
	}
}

// A survivor clamped to nothing is not an allocation. Emitting a
// "Share 0.000000, AmountUSD 0.00" row invites a downstream consumer to book a
// publisher for zero dollars; sub-cent rows are dropped instead, and the
// remaining shares still sum to 1 over what was actually deployed.
func TestAllocateSuppressesZeroDollarRows(t *testing.T) {
	p := AllocParams{Gamma: 1.5, MaxShare: 0.40, SOVCap: 0.15, MinShare: 0.05,
		TotalUSD: 5_000, Days: 30}

	for _, tc := range []struct {
		name    string
		cands   []Candidate
		wantOut string // the publisher that must not appear
	}{
		{"zero CPM", zeroCPMCandidates(), "nocpm"},
		{"zero impressions", zeroImpressionsCandidates(), "noimp"},
	} {
		got := Allocate(tc.cands, p)
		for _, a := range got {
			if a.PublisherID == tc.wantOut {
				t.Errorf("%s: %s emitted with share %.6f, amount %.2f; a zero-dollar row is not an allocation",
					tc.name, a.PublisherID, a.Share, a.AmountUSD)
			}
			if a.AmountUSD < 0.01 {
				t.Errorf("%s: %s amount = %.4f, below one cent", tc.name, a.PublisherID, a.AmountUSD)
			}
		}
		if len(got) != 2 {
			t.Errorf("%s: got %d allocations, want 2: %+v", tc.name, len(got), got)
		}
		if s := sumShares(got); math.Abs(s-1) > 1e-9 {
			t.Errorf("%s: shares sum to %v, want 1", tc.name, s)
		}
	}
}
