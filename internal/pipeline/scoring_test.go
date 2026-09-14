package pipeline

import (
	"math"
	"testing"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
)

func loadCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load("../../data")
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return c
}

func scoreFor(t *testing.T, scores []model.PublisherScore, id string) model.PublisherScore {
	t.Helper()
	for _, s := range scores {
		if s.PublisherID == id {
			return s
		}
	}
	t.Fatalf("no score for %s", id)
	return model.PublisherScore{}
}

// dogFood is example brief #1: premium senior dog food, vet-formulated,
// subscription, owners who care about joint health.
func dogFood() model.AdvertiserProfile {
	return model.AdvertiserProfile{
		RawBrief:         "premium dog food for senior dogs",
		PrimaryCategory:  "pet",
		Subcategories:    []string{"pet_food", "subscription"},
		PriceTier:        "premium",
		EstimatedAOVUSD:  70,
		TargetAgeMin:     30,
		TargetAgeMax:     55,
		TargetGenderSkew: "balanced",
		Values:           []string{"science_backed"},
		BusinessModel:    "subscription",
		IsConsumerDTC:    true,
		Confidence:       "high",
	}
}

// activewear is example brief #2: sustainable women's activewear, recycled
// ocean plastic, priced between Lululemon and Girlfriend Collective.
func activewear() model.AdvertiserProfile {
	return model.AdvertiserProfile{
		RawBrief:         "sustainable activewear for women",
		PrimaryCategory:  "apparel",
		Subcategories:    []string{"activewear", "women"},
		PriceTier:        "premium",
		EstimatedAOVUSD:  95,
		TargetAgeMin:     25,
		TargetAgeMax:     45,
		TargetGenderSkew: "female",
		Values:           []string{"sustainability"},
		BusinessModel:    "one_off",
		IsConsumerDTC:    true,
		Confidence:       "high",
	}
}

func TestScoreAllCoversEveryPublisher(t *testing.T) {
	c := loadCatalog(t)
	got := ScoreAll(dogFood(), c)
	if len(got) != len(c.Publishers) {
		t.Fatalf("got %d scores, want %d", len(got), len(c.Publishers))
	}
	for i, s := range got {
		if s.PublisherID != c.Publishers[i].ID {
			t.Errorf("score[%d] id = %s, want %s", i, s.PublisherID, c.Publishers[i].ID)
		}
		if s.Score < 0 || s.Score > 1 {
			t.Errorf("%s score = %v, want [0,1]", s.PublisherID, s.Score)
		}
	}
}

// Pawline (pet, exact category match) and Movewell (apparel, subcategory-jaccard
// match only) are both GateNone under the dog-food profile, so this exercises
// the weighted sum on both sides rather than comparing against a gated (and
// therefore zero-forced) score.
func TestPetPublisherOutscoresApparelOnUngatedComparison(t *testing.T) {
	c := loadCatalog(t)
	got := ScoreAll(dogFood(), c)
	pawline := scoreFor(t, got, "pub_007")  // pet, exact category match
	movewell := scoreFor(t, got, "pub_002") // apparel, subcategory overlap only
	if pawline.HardGate != GateNone {
		t.Fatalf("Pawline gate = %q, want none", pawline.HardGate)
	}
	if movewell.HardGate != GateNone {
		t.Fatalf("Movewell gate = %q, want none", movewell.HardGate)
	}
	if pawline.Score <= movewell.Score {
		t.Errorf("Pawline %v should outscore Movewell %v", pawline.Score, movewell.Score)
	}
}

// Velvetline (beauty) is neither an exact category match nor adjacent to nor
// subcategory-overlapping with "pet", so it is gated to a forced zero score.
// This pins that gate behaviour on its own, separate from the ungated
// comparison above.
func TestVelvetlineIsCategoryGatedForPetBrief(t *testing.T) {
	c := loadCatalog(t)
	got := ScoreAll(dogFood(), c)
	velvetline := scoreFor(t, got, "pub_013") // beauty, mid, AOV 61
	if velvetline.HardGate != GateCategoryMismatch {
		t.Errorf("Velvetline gate = %q, want %q", velvetline.HardGate, GateCategoryMismatch)
	}
	if velvetline.Score != 0 {
		t.Errorf("Velvetline score = %v, want 0", velvetline.Score)
	}
}

// Marlowe & Co. (pub_004) skews 45-65; a 25-45 activewear brief touches its
// range at exactly one year (age 45). Age ranges are inclusive, so this must
// NOT be treated as zero overlap and gated out — Marlowe should score a small
// positive AgeOverlap and stay ungated, matching apparel/women/workwear,
// 96% female, AOV 112.
func TestBoundaryYearCountsAsInclusiveOverlap(t *testing.T) {
	c := loadCatalog(t)
	got := ScoreAll(activewear(), c)
	marlowe := scoreFor(t, got, "pub_004")
	if marlowe.HardGate != GateNone {
		t.Fatalf("Marlowe & Co. gate = %q, want none (45 is a shared boundary year, not zero overlap)", marlowe.HardGate)
	}
	want := 1.0 / 21.0 // one shared year (45) over the narrower range's 21 inclusive years (45-65)
	if math.Abs(marlowe.Sub.AgeOverlap-want) > 1e-9 {
		t.Errorf("Marlowe & Co. AgeOverlap = %v, want %v", marlowe.Sub.AgeOverlap, want)
	}
}

// Linden Park skews 50-70; a 25-45 activewear brief has zero age overlap, so it
// must be gated out on demographics rather than merely scored low.
func TestZeroAgeOverlapGatesDemographically(t *testing.T) {
	c := loadCatalog(t)
	got := ScoreAll(activewear(), c)
	linden := scoreFor(t, got, "pub_005")
	if linden.HardGate != GateDemographicMismatch {
		t.Errorf("Linden Park gate = %q, want %q", linden.HardGate, GateDemographicMismatch)
	}
	if linden.Sub.AgeOverlap != 0 {
		t.Errorf("Linden Park age overlap = %v, want 0", linden.Sub.AgeOverlap)
	}
	movewell := scoreFor(t, got, "pub_002") // apparel, activewear, 25-44, 82% female
	if movewell.HardGate != GateNone {
		t.Errorf("Movewell gate = %q, want none", movewell.HardGate)
	}
}

// Brief #7 is B2B SaaS for dental practices. No publisher in a DTC consumer
// catalog can serve it, and the system must say so rather than pick a winner.
func TestNonConsumerBriefGatesEveryPublisher(t *testing.T) {
	c := loadCatalog(t)
	p := dogFood()
	p.IsConsumerDTC = false
	p.PrimaryCategory = "b2b_saas"
	p.BusinessModel = "b2b"
	for _, s := range ScoreAll(p, c) {
		if s.HardGate != GateNotConsumerDTC {
			t.Errorf("%s gate = %q, want %q", s.PublisherID, s.HardGate, GateNotConsumerDTC)
		}
	}
}

// Brief #10 is $1,200 Italian handbags. Swiftcart's shoppers spend $28 a time.
func TestAOVAlignmentPunishesMismatch(t *testing.T) {
	c := loadCatalog(t)
	p := dogFood()
	p.EstimatedAOVUSD = 1200
	got := ScoreAll(p, c)
	swiftcart := scoreFor(t, got, "pub_001") // AOV 28
	if swiftcart.Sub.AOVAlignment != 0 {
		t.Errorf("Swiftcart AOV alignment = %v, want 0", swiftcart.Sub.AOVAlignment)
	}
}

func TestSubScoresAreWeightedIntoScore(t *testing.T) {
	c := loadCatalog(t)
	got := ScoreAll(dogFood(), c)
	s := scoreFor(t, got, "pub_007")
	want := 0.30*s.Sub.Category + 0.20*s.Sub.AOVAlignment + 0.15*s.Sub.AgeOverlap +
		0.15*s.Sub.ValuesMatch + 0.10*s.Sub.GenderFit + 0.10*s.Sub.IncomeTier
	if math.Abs(s.Score-want) > 1e-9 {
		t.Errorf("score = %v, weighted sub-scores = %v", s.Score, want)
	}
}

func TestGatedPublisherScoresZero(t *testing.T) {
	c := loadCatalog(t)
	got := ScoreAll(activewear(), c)
	linden := scoreFor(t, got, "pub_005")
	if linden.Score != 0 {
		t.Errorf("gated publisher score = %v, want 0", linden.Score)
	}
}

// TestGenderFitDiscriminatesBySkew guards against genderScore being replaced
// with a constant: a female-skew profile should read a materially different
// GenderFit off a 98%-female publisher than off a 48%-female one, and each
// value must match the clamp((share-0.3)/0.5, 0, 1) formula exactly. The
// profile's Subcategories are chosen to jaccard-overlap both publishers'
// subcategories ("convenience" with Swiftcart, "women" with Linden Park) and
// its age fields are left zero (neutral), so neither publisher is gated and
// both GenderFit values are read off a live, weighted Score.
func TestGenderFitDiscriminatesBySkew(t *testing.T) {
	c := loadCatalog(t)
	p := model.AdvertiserProfile{
		Subcategories:    []string{"convenience", "women"},
		TargetGenderSkew: "female",
		IsConsumerDTC:    true,
	}
	got := ScoreAll(p, c)

	cases := []struct {
		pubID string
		share float64 // publisher's female audience share, from data/publishers.json
	}{
		{"pub_005", 0.98}, // Linden Park
		{"pub_001", 0.48}, // Swiftcart
	}
	for _, tc := range cases {
		s := scoreFor(t, got, tc.pubID)
		if s.HardGate != GateNone {
			t.Fatalf("%s unexpectedly gated: %q", tc.pubID, s.HardGate)
		}
		want := clamp((tc.share-0.3)/0.5, 0, 1)
		if math.Abs(s.Sub.GenderFit-want) > 1e-9 {
			t.Errorf("%s GenderFit = %v, want %v", tc.pubID, s.Sub.GenderFit, want)
		}
	}

	linden := scoreFor(t, got, "pub_005")
	swiftcart := scoreFor(t, got, "pub_001")
	if linden.Sub.GenderFit <= swiftcart.Sub.GenderFit {
		t.Errorf("Linden Park GenderFit %v should exceed Swiftcart's %v", linden.Sub.GenderFit, swiftcart.Sub.GenderFit)
	}
}

// TestIncomeTierMatchesOrdinalDistance guards against incomeScore being
// replaced with a constant, pinning three points on the
// priceTierIncome/incomeIndex distance formula against real publishers.
func TestIncomeTierMatchesOrdinalDistance(t *testing.T) {
	c := loadCatalog(t)
	cases := []struct {
		name      string
		priceTier string
		pubID     string
		category  string // the publisher's real category, so it is never gated
		want      float64
	}{
		{"luxury_vs_high", "luxury", "pub_005", "apparel", 1.0},
		{"luxury_vs_mid", "luxury", "pub_001", "instant_delivery", 0.0},
		{"mid_vs_mid_high", "mid", "pub_002", "apparel", 0.75},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := model.AdvertiserProfile{
				PrimaryCategory: tc.category,
				PriceTier:       tc.priceTier,
				IsConsumerDTC:   true,
			}
			got := ScoreAll(p, c)
			s := scoreFor(t, got, tc.pubID)
			if math.Abs(s.Sub.IncomeTier-tc.want) > 1e-9 {
				t.Errorf("IncomeTier = %v, want %v", s.Sub.IncomeTier, tc.want)
			}
		})
	}
}

// TestCategoryGateTakesPrecedenceOverAgeGate pins the switch order in
// scoreOne: Velvetline (beauty, age 18-34) fails both the category check
// (against a "pet" brief with no subcategory overlap or adjacency to beauty)
// and the age check (against a 60-70 target, which does not overlap 18-34) at
// once. If the category and age cases in the switch were ever swapped, this
// would start reporting GateDemographicMismatch instead.
func TestCategoryGateTakesPrecedenceOverAgeGate(t *testing.T) {
	c := loadCatalog(t)
	p := dogFood()
	p.TargetAgeMin = 60
	p.TargetAgeMax = 70
	got := ScoreAll(p, c)
	velvetline := scoreFor(t, got, "pub_013")
	if velvetline.Sub.Category != 0 {
		t.Fatalf("Velvetline category score = %v, want 0 (precondition)", velvetline.Sub.Category)
	}
	if velvetline.Sub.AgeOverlap != 0 {
		t.Fatalf("Velvetline age overlap = %v, want 0 (precondition)", velvetline.Sub.AgeOverlap)
	}
	if velvetline.HardGate != GateCategoryMismatch {
		t.Errorf("Velvetline gate = %q, want %q (category must be checked before age)", velvetline.HardGate, GateCategoryMismatch)
	}
}

// TestAOVAlignmentPivot pins the formula's pivot point: a ratio of exactly
// 0.25 must score 0, and a ratio just above it must score a small positive
// number, not merely "not 0".
func TestAOVAlignmentPivot(t *testing.T) {
	c := loadCatalog(t)

	atPivot := dogFood()
	atPivot.EstimatedAOVUSD = 256 // Pawline's AOV (64) * 4 -> ratio exactly 0.25
	got := ScoreAll(atPivot, c)
	pawline := scoreFor(t, got, "pub_007")
	if pawline.Sub.AOVAlignment != 0 {
		t.Errorf("at ratio 0.25, AOVAlignment = %v, want 0", pawline.Sub.AOVAlignment)
	}

	justAbove := dogFood()
	justAbove.EstimatedAOVUSD = 250 // ratio 64/250 = 0.256, just above the pivot
	got2 := ScoreAll(justAbove, c)
	pawline2 := scoreFor(t, got2, "pub_007")
	if pawline2.Sub.AOVAlignment <= 0 || pawline2.Sub.AOVAlignment > 0.05 {
		t.Errorf("just above ratio 0.25, AOVAlignment = %v, want a small positive value in (0, 0.05]", pawline2.Sub.AOVAlignment)
	}
}

// TestValuesMatchIsCaseInsensitive guards against a live LLM stage emitting
// "Sustainability" (or any other casing) and silently scoring 0 against a
// publisher whose derived keywords are lowercase.
func TestValuesMatchIsCaseInsensitive(t *testing.T) {
	c := loadCatalog(t)
	p := activewear()
	p.Values = []string{"Sustainability"}
	got := ScoreAll(p, c)
	for _, s := range got {
		if s.Sub.ValuesMatch != 1 && s.Sub.ValuesMatch != 0 {
			t.Fatalf("%s ValuesMatch = %v, want 0 or 1 for a single-value profile", s.PublisherID, s.Sub.ValuesMatch)
		}
	}
	lower := activewear()
	lower.Values = []string{"sustainability"}
	gotLower := ScoreAll(lower, c)
	for i := range got {
		if got[i].Sub.ValuesMatch != gotLower[i].Sub.ValuesMatch {
			t.Errorf("%s ValuesMatch differs by casing: %v (mixed-case) vs %v (lowercase)",
				got[i].PublisherID, got[i].Sub.ValuesMatch, gotLower[i].Sub.ValuesMatch)
		}
	}
}
