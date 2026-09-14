package measure

import (
	"testing"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
)

// ps is a small constructor for a hand-built model.PublisherScore fixture:
// Score is the full (all-six-weights) total, and sub is whatever Sub values
// the test wants to drive the ablated totals with. Real pipeline output
// always has Score == the weighted sum of Sub, but ablateBrief only reads
// Score for the full ranking and Sub for the ablated ones, so tests are
// free to set them independently to isolate exactly one thing at a time.
func ps(id string, score float64, sub model.SubScores) model.PublisherScore {
	return model.PublisherScore{PublisherID: id, Score: score, Sub: sub}
}

func gated(id, gate string) model.PublisherScore {
	return model.PublisherScore{PublisherID: id, HardGate: gate}
}

func full(sub model.SubScores) float64 {
	return ablatedTotal(sub, "") // "" omits nothing: the full six-weight sum
}

func newAcc() map[string]*scoringAblationAcc {
	acc := make(map[string]*scoringAblationAcc, len(subScoreNames))
	for _, n := range subScoreNames {
		acc[n] = &scoringAblationAcc{}
	}
	return acc
}

// TestAblateBrief_DecisiveSubScoreChangesTop5 catches the corruption: a
// sub-score wired to always contribute 0 influence (e.g. a copy-paste bug
// that ablates the wrong dimension, or a weight accidentally hardcoded to
// 0 already). With Category ranging from 1.0 down to 0.0 across six
// publishers and every other sub-score tied at 0.5, Category (weight 0.30,
// the largest) is decisive: removing it promotes the bottom publishers
// enough to displace the top 5.
func TestAblateBrief_DecisiveSubScoreChangesTop5(t *testing.T) {
	mk := func(id string, cat, aov float64) model.PublisherScore {
		sub := model.SubScores{Category: cat, AOVAlignment: aov, AgeOverlap: 0.5, ValuesMatch: 0.5, GenderFit: 0.5, IncomeTier: 0.5}
		return ps(id, full(sub), sub)
	}
	// Full ranking (by Category, since AOV/others tie): p1..p6 in that order.
	// Ablating Category: order flips to be governed by AOV, which is inverse.
	scores := []model.PublisherScore{
		mk("p1", 1.0, 0.0),
		mk("p2", 0.9, 0.1),
		mk("p3", 0.8, 0.2),
		mk("p4", 0.2, 0.8),
		mk("p5", 0.1, 0.9),
		mk("p6", 0.0, 1.0),
	}

	acc := newAcc()
	ungatedCount := ablateBrief(1, scores, acc)
	if ungatedCount != 6 {
		t.Fatalf("ungatedCount = %d, want 6", ungatedCount)
	}

	if got := acc["Category"].top5Changed; got != 1 {
		t.Errorf("Category top5Changed = %d, want 1 (a decisive sub-score must show non-zero top5_change_rate)", got)
	}
	if !acc["Category"].haveExample {
		t.Errorf("Category should have recorded a changed-top5 example")
	}
}

// TestAblateBrief_ConstantSubScoreIsInert catches the corruption: a
// sub-score whose weight was accidentally left non-zero in the ablation
// even though the metric is supposed to isolate it. If a sub-score is
// identical across every publisher, subtracting it (zeroing its weight)
// changes every publisher's total by the same constant amount, so the
// order — and therefore top5 and rank — must be completely unaffected.
func TestAblateBrief_ConstantSubScoreIsInert(t *testing.T) {
	mk := func(id string, cat float64) model.PublisherScore {
		// GenderFit is pinned at 0.7 for every publisher: identical across
		// the board. Category varies so there is a real ranking to preserve.
		sub := model.SubScores{Category: cat, AOVAlignment: 0.5, AgeOverlap: 0.5, ValuesMatch: 0.5, GenderFit: 0.7, IncomeTier: 0.5}
		return ps(id, full(sub), sub)
	}
	scores := []model.PublisherScore{
		mk("p1", 1.0), mk("p2", 0.8), mk("p3", 0.6),
		mk("p4", 0.4), mk("p5", 0.2), mk("p6", 0.0),
	}

	acc := newAcc()
	ablateBrief(1, scores, acc)

	g := acc["GenderFit"]
	if g.top5Changed != 0 {
		t.Errorf("GenderFit top5Changed = %d, want 0 (a constant sub-score cannot change any ordering)", g.top5Changed)
	}
	if g.rankShiftSum != 0 {
		t.Errorf("GenderFit rankShiftSum = %v, want 0", g.rankShiftSum)
	}
	if g.haveExample {
		t.Errorf("GenderFit should not have recorded a changed-top5 example")
	}
}

// TestAblateBrief_GatedPublishersExcluded catches the corruption: gated
// publishers leaking into the ranking (e.g. forgetting the HardGate filter,
// or filtering only one of the two rankings compared). Adding gated
// publishers — which always score 0 — must not change the ungated
// publishers' ranks at all, in either the full or the ablated ranking.
func TestAblateBrief_GatedPublishersExcluded(t *testing.T) {
	mkUngated := func(id string, cat float64) model.PublisherScore {
		sub := model.SubScores{Category: cat, AOVAlignment: 0.5, AgeOverlap: 0.5, ValuesMatch: 0.5, GenderFit: 0.5, IncomeTier: 0.5}
		return ps(id, full(sub), sub)
	}
	ungated := []model.PublisherScore{
		mkUngated("p1", 1.0), mkUngated("p2", 0.6), mkUngated("p3", 0.2),
	}

	accWithoutGated := newAcc()
	countWithout := ablateBrief(1, ungated, accWithoutGated)

	withGated := append(append([]model.PublisherScore{}, ungated...),
		gated("g1", "category_mismatch"), gated("g2", "demographic_mismatch"))
	accWithGated := newAcc()
	countWith := ablateBrief(1, withGated, accWithGated)

	if countWithout != 3 || countWith != 3 {
		t.Fatalf("ungated counts = %d, %d, want 3, 3 (gated publishers must not be counted)", countWithout, countWith)
	}
	for _, name := range subScoreNames {
		a, b := accWithoutGated[name], accWithGated[name]
		if a.top5Changed != b.top5Changed || a.rankShiftSum != b.rankShiftSum {
			t.Errorf("%s: gated publishers changed the result (without=%+v with=%+v)", name, a, b)
		}
	}
}

// TestAblateBrief_TiesBreakDeterministically catches the corruption: rank
// comparisons keyed on map iteration order or an unstable sort, so the same
// input produces different ranks (and therefore different mean_rank_shift)
// from one run to the next. Every publisher here ties on every sub-score,
// so the only thing that can determine order is the publisher-ID tiebreak.
func TestAblateBrief_TiesBreakDeterministically(t *testing.T) {
	sub := model.SubScores{Category: 0.5, AOVAlignment: 0.5, AgeOverlap: 0.5, ValuesMatch: 0.5, GenderFit: 0.5, IncomeTier: 0.5}
	scores := []model.PublisherScore{
		ps("charlie", full(sub), sub),
		ps("alpha", full(sub), sub),
		ps("bravo", full(sub), sub),
	}

	want := rankByScore(ungatedIDs(scores), func(s model.PublisherScore) float64 { return s.Score })
	for i := 0; i < 20; i++ {
		got := rankByScore(ungatedIDs(scores), func(s model.PublisherScore) float64 { return s.Score })
		if len(got) != len(want) {
			t.Fatalf("run %d: length changed", i)
		}
		for j := range got {
			if got[j] != want[j] {
				t.Fatalf("run %d: order changed: got %v, want %v", i, got, want)
			}
		}
	}
	if want[0] != "alpha" || want[1] != "bravo" || want[2] != "charlie" {
		t.Errorf("tie-broken order = %v, want alphabetical [alpha bravo charlie]", want)
	}

	acc1, acc2 := newAcc(), newAcc()
	ablateBrief(1, scores, acc1)
	ablateBrief(1, scores, acc2)
	for _, name := range subScoreNames {
		a1, a2 := acc1[name], acc2[name]
		if a1.top5Changed != a2.top5Changed || a1.rankShiftSum != a2.rankShiftSum {
			t.Errorf("%s: repeated run diverged: %+v vs %+v", name, a1, a2)
		}
	}
}

// TestAblateBrief_MeanRankShiftCatchesMidPackReshuffle catches the
// corruption: a metric that only ever looks at the top 5 (e.g. computing
// mean_rank_shift from the top5 sets instead of the full ungated ranking).
// Here AgeOverlap is decisive for the middle of the pack (p4..p7) but the
// top 5 (by Category, the dominant weight) is untouched by ablating
// AgeOverlap, so top5_change_rate must be 0 while mean_rank_shift is not.
func TestAblateBrief_MeanRankShiftCatchesMidPackReshuffle(t *testing.T) {
	mk := func(id string, cat, age float64) model.PublisherScore {
		sub := model.SubScores{Category: cat, AOVAlignment: 0.5, AgeOverlap: age, ValuesMatch: 0.5, GenderFit: 0.5, IncomeTier: 0.5}
		return ps(id, full(sub), sub)
	}
	scores := []model.PublisherScore{
		mk("p1", 1.00, 0.5),
		mk("p2", 0.95, 0.5),
		mk("p3", 0.90, 0.5),
		mk("p4", 0.85, 0.5),
		mk("p5", 0.80, 0.5),
		// Middle of the pack: close Category scores, but AgeOverlap swings
		// the order between them once AgeOverlap's weight is removed.
		mk("p6", 0.50, 0.0),
		mk("p7", 0.49, 1.0),
		mk("p8", 0.10, 0.5),
	}

	acc := newAcc()
	ablateBrief(1, scores, acc)

	a := acc["AgeOverlap"]
	if a.top5Changed != 0 {
		t.Errorf("AgeOverlap top5Changed = %d, want 0 (top 5 is governed by Category here)", a.top5Changed)
	}
	if a.rankShiftSum <= 0 {
		t.Errorf("AgeOverlap rankShiftSum = %v, want > 0 (p6/p7 must swap once AgeOverlap is zeroed)", a.rankShiftSum)
	}
}

// TestScoringAblation_MeanUngatedPerBrief catches the corruption: an
// off-by-one or wrong-denominator bug in mean_ungated_per_brief (counting
// gated publishers, or dividing by the wrong number of briefs).
func TestScoringAblation_MeanUngatedPerBrief(t *testing.T) {
	sub := model.SubScores{Category: 0.6, AOVAlignment: 0.5, AgeOverlap: 0.5, ValuesMatch: 0.5, GenderFit: 0.5, IncomeTier: 0.5}

	acc := newAcc()
	// Brief A: 4 ungated, 1 gated.
	n1 := ablateBrief(1, []model.PublisherScore{
		ps("p1", full(sub), sub), ps("p2", full(sub), sub),
		ps("p3", full(sub), sub), ps("p4", full(sub), sub),
		gated("g1", "not_consumer_dtc"),
	}, acc)
	// Brief B: 2 ungated, 2 gated.
	n2 := ablateBrief(2, []model.PublisherScore{
		ps("p1", full(sub), sub), ps("p2", full(sub), sub),
		gated("g1", "category_mismatch"), gated("g2", "demographic_mismatch"),
	}, acc)

	total := n1 + n2
	briefs := 2
	got := safeDiv(total, briefs)
	want := 3.0 // (4+2)/2
	if got != want {
		t.Errorf("mean_ungated_per_brief = %v, want %v", got, want)
	}
}

// TestScoringAblation_EndToEnd exercises the public entry point — the one
// wired into the measure subcommand — against a small synthetic catalog and
// two model.Campaign values built entirely in memory (no data/, no network,
// no model calls: pipeline.ScoreAll is pure computation). It catches the
// corruption: mean_ungated_per_brief (or the metric shape generally) wired
// up wrong at the ScoringAblation level even if the lower-level ablateBrief
// math is correct — e.g. dividing by the wrong denominator, or forgetting a
// brief with zero ungated publishers entirely instead of counting it as 0.
func TestScoringAblation_EndToEnd(t *testing.T) {
	cat := &catalog.Catalog{Publishers: []catalog.Publisher{
		{ID: "a", Category: "dog_food", AvgOrderValueUSD: 50, Audience: catalog.Audience{AgeSkew: "25-45", GenderSplit: map[string]float64{"female": 0.5, "male": 0.5}, IncomeTier: "mid"}},
		{ID: "b", Category: "dog_food", AvgOrderValueUSD: 80, Audience: catalog.Audience{AgeSkew: "25-45", GenderSplit: map[string]float64{"female": 0.5, "male": 0.5}, IncomeTier: "mid"}},
		{ID: "c", Category: "fashion", AvgOrderValueUSD: 60, Audience: catalog.Audience{AgeSkew: "25-45", GenderSplit: map[string]float64{"female": 0.5, "male": 0.5}, IncomeTier: "mid"}},
		{ID: "d", Category: "dog_food", AvgOrderValueUSD: 60, Audience: catalog.Audience{AgeSkew: "60-75", GenderSplit: map[string]float64{"female": 0.5, "male": 0.5}, IncomeTier: "mid"}},
	}}

	// Brief 1: a, b stay ungated (category matches, age overlaps); c is
	// category-gated (fashion isn't dog_food or adjacent to it); d is
	// demographic-gated (60-75 doesn't overlap the 25-45 target). 2 ungated.
	profile1 := model.AdvertiserProfile{
		IsConsumerDTC: true, PrimaryCategory: "dog_food",
		TargetAgeMin: 25, TargetAgeMax: 45, EstimatedAOVUSD: 60, PriceTier: "mid",
	}
	// Brief 2: not consumer DTC, so every publisher is gated. 0 ungated.
	profile2 := model.AdvertiserProfile{IsConsumerDTC: false, PrimaryCategory: "dog_food"}

	campaigns := []Campaign{
		{BriefN: 1, Campaign: model.Campaign{Advertiser: model.AdvertiserBlock{DerivedProfile: profile1}}},
		{BriefN: 2, Campaign: model.Campaign{Advertiser: model.AdvertiserBlock{DerivedProfile: profile2}}},
	}

	m := ScoringAblation(campaigns, cat)

	if m.Name != "scoring_ablation" {
		t.Errorf("Name = %q, want scoring_ablation", m.Name)
	}
	if got, want := m.Counts["briefs"], 2; got != want {
		t.Errorf("counts[briefs] = %d, want %d", got, want)
	}
	if got, want := m.Values["mean_ungated_per_brief"], 1.0; got != want {
		t.Errorf("mean_ungated_per_brief = %v, want %v (2 ungated on brief 1, 0 on brief 2)", got, want)
	}
	if len(m.Findings) != len(subScoreNames) {
		t.Errorf("len(Findings) = %d, want %d (one per sub-score)", len(m.Findings), len(subScoreNames))
	}
	for _, name := range subScoreNames {
		if _, ok := m.Values["top5_change_rate."+name]; !ok {
			t.Errorf("missing values[top5_change_rate.%s]", name)
		}
		if _, ok := m.Values["mean_rank_shift."+name]; !ok {
			t.Errorf("missing values[mean_rank_shift.%s]", name)
		}
		if _, ok := m.Counts["top5_changed."+name]; !ok {
			t.Errorf("missing counts[top5_changed.%s]", name)
		}
	}
}
