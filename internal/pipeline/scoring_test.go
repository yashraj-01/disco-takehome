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

func TestPetPublishersOutrankUnrelatedOnes(t *testing.T) {
	c := loadCatalog(t)
	got := ScoreAll(dogFood(), c)
	pawline := scoreFor(t, got, "pub_007")    // pet, mid-high, AOV 64
	velvetline := scoreFor(t, got, "pub_013") // beauty, mid, AOV 61
	if pawline.Score <= velvetline.Score {
		t.Errorf("Pawline %v should outscore Velvetline %v", pawline.Score, velvetline.Score)
	}
	if pawline.HardGate != GateNone {
		t.Errorf("Pawline gate = %q, want none", pawline.HardGate)
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
