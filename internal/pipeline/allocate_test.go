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

// Every declared cap must hold in the emitted result, not merely in an
// intermediate pass.
func TestAllocateRespectsAllCapsAfterRedistribution(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 50_000
	for _, got := range [][]Allocation{
		Allocate(dogFoodCandidates(), p),
		Allocate(dogFoodCandidates()[:2], p),
		Allocate(dogFoodCandidates()[:1], p),
	} {
		for _, a := range got {
			if a.Share < p.MinShare-1e-9 && len(got) > 1 {
				t.Errorf("%s share %.4f below floor %.2f", a.PublisherID, a.Share, p.MinShare)
			}
			if a.Share > p.MaxShare+1e-9 && len(got) > 1 {
				t.Errorf("%s share %.4f above cap %.2f", a.PublisherID, a.Share, p.MaxShare)
			}
		}
		if s := sumShares(got); math.Abs(s-1) > 1e-9 {
			t.Errorf("shares sum to %v, want 1", s)
		}
	}
}

// A lone survivor takes the whole budget rather than being dropped by the floor.
func TestAllocateSingleCandidateTakesEverything(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 25_000
	got := Allocate(dogFoodCandidates()[:1], p)
	if len(got) != 1 || math.Abs(got[0].Share-1) > 1e-9 {
		t.Fatalf("got %+v, want a single 100%% allocation", got)
	}
}

func TestAllocateEmptyInput(t *testing.T) {
	if got := Allocate(nil, DefaultAllocParams()); len(got) != 0 {
		t.Errorf("got %d allocations, want 0", len(got))
	}
}

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

// cascadeCandidates is engineered (not the dog-food set) so that a single pass
// of capping is provably insufficient: A clips hard on its deliverability
// ceiling (its slice is worth far less than its fit^gamma share implies), and
// releasing A's excess back to the pool pushes B — which was comfortably under
// the concentration cap on the first look — over 40%. Gamma is pinned to 1 so
// the pre-cap shares are exact fractions (5:3:1.4:0.6 of 10 = .50/.30/.14/.06),
// isolating the cap interaction from gamma's math.
func cascadeCandidates() []Candidate {
	return []Candidate{
		// EstCPM/MonthlyImpressions chosen so deliverableUSD/TotalUSD == 0.10,
		// well under both its 0.50 raw share and the 0.40 concentration cap.
		{PublisherID: "A", Fit: 5, EstCPM: 10, MonthlyImpressions: 666_667},
		// B, C, D have generous inventory so only the concentration cap (0.40)
		// can ever bind for them.
		{PublisherID: "B", Fit: 3, EstCPM: 10, MonthlyImpressions: 10_000_000},
		{PublisherID: "C", Fit: 1.4, EstCPM: 10, MonthlyImpressions: 10_000_000},
		{PublisherID: "D", Fit: 0.6, EstCPM: 10, MonthlyImpressions: 10_000_000},
	}
}

// TestFixedPointLoopIsRequired constructs the textbook cascade the brief
// warns about: clipping A and releasing its excess back to the pool pushes a
// previously-fine survivor (B) over the concentration cap. A single pass sees
// only A over cap; it takes a second pass, after redistribution, to notice B
// is now over cap too. This test proves the loop is load-bearing, not
// decorative: pinning maxAllocPasses to 1 must reproduce the violation, and
// restoring it must fix it.
func TestFixedPointLoopIsRequired(t *testing.T) {
	c := cascadeCandidates()
	p := AllocParams{Gamma: 1, MaxShare: 0.40, SOVCap: 0.15, MinShare: 0.05,
		TotalUSD: 10_000, Days: 30}

	orig := maxAllocPasses
	defer func() { maxAllocPasses = orig }()

	// A single pass: prove it actually violates the cap for this input, per
	// the brief's own warning, rather than merely asserting it in the
	// abstract.
	maxAllocPasses = 1
	single := Allocate(c, p)
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
	maxAllocPasses = orig
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
