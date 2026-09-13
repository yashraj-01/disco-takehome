package pipeline

import (
	"strings"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
)

// Hard gate reasons. A gated publisher is excluded regardless of its
// sub-scores, and stage 3 may not override the exclusion.
const (
	GateNone                = ""
	GateNotConsumerDTC      = "not_consumer_dtc"
	GateCategoryMismatch    = "category_mismatch"
	GateDemographicMismatch = "demographic_mismatch"
)

// Scoring weights. These sum to 1.0.
const (
	wCategory = 0.30
	wAOV      = 0.20
	wAge      = 0.15
	wValues   = 0.15
	wGender   = 0.10
	wIncome   = 0.10
)

// ScoreAll scores every publisher in the catalog against the profile and
// returns the results in catalog order. It performs no I/O and calls no model,
// so its output is reproducible for a given profile.
func ScoreAll(p model.AdvertiserProfile, c *catalog.Catalog) []model.PublisherScore {
	out := make([]model.PublisherScore, 0, len(c.Publishers))
	for i := range c.Publishers {
		out = append(out, scoreOne(p, &c.Publishers[i]))
	}
	return out
}

func scoreOne(p model.AdvertiserProfile, pub *catalog.Publisher) model.PublisherScore {
	s := model.PublisherScore{PublisherID: pub.ID}

	s.Sub = model.SubScores{
		Category:     categoryScore(p, pub),
		AOVAlignment: aovScore(p.EstimatedAOVUSD, pub.AvgOrderValueUSD),
		AgeOverlap:   ageScore(p, pub),
		ValuesMatch:  valuesScore(p.Values, pub.Keywords),
		GenderFit:    genderScore(p.TargetGenderSkew, pub.Audience.GenderSplit),
		IncomeTier:   incomeScore(p.PriceTier, pub.Audience.IncomeTier),
	}

	switch {
	case !p.IsConsumerDTC:
		s.HardGate = GateNotConsumerDTC
	case s.Sub.Category == 0:
		s.HardGate = GateCategoryMismatch
	case s.Sub.AgeOverlap == 0:
		s.HardGate = GateDemographicMismatch
	}
	if s.HardGate != GateNone {
		return s // Score stays 0; a gated publisher never competes for budget.
	}

	s.Score = wCategory*s.Sub.Category + wAOV*s.Sub.AOVAlignment + wAge*s.Sub.AgeOverlap +
		wValues*s.Sub.ValuesMatch + wGender*s.Sub.GenderFit + wIncome*s.Sub.IncomeTier
	return s
}

// categoryScore prefers an exact category match, falls back to subcategory
// overlap, then to the adjacency map.
func categoryScore(p model.AdvertiserProfile, pub *catalog.Publisher) float64 {
	if p.PrimaryCategory != "" && p.PrimaryCategory == pub.Category {
		return 1.0
	}
	if j := jaccard(p.Subcategories, pub.Subcategories); j > 0 {
		return 0.4 + 0.5*j
	}
	if isAdjacent(p.PrimaryCategory, pub.Category) {
		return 0.5
	}
	return 0.0
}

// jaccard is the intersection-over-union of two lowercase token sets.
func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]bool, len(a))
	for _, x := range a {
		set[strings.ToLower(x)] = true
	}
	inter := 0
	seen := make(map[string]bool, len(b))
	for _, y := range b {
		y = strings.ToLower(y)
		if seen[y] {
			continue
		}
		seen[y] = true
		if set[y] {
			inter++
		}
	}
	union := len(set) + len(seen) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// aovScore rewards advertisers whose order value is in the same ballpark as the
// publisher's shoppers. A $1,200 handbag on a $28-AOV publisher scores 0.
func aovScore(advertiser, publisher int) float64 {
	if advertiser <= 0 || publisher <= 0 {
		return 0.5 // no signal either way
	}
	a, b := float64(advertiser), float64(publisher)
	r := min(a, b) / max(a, b)
	return clamp((r-0.25)/0.75, 0, 1)
}

// ageScore is the overlap of the two age ranges as a fraction of the narrower
// one. Zero overlap triggers the demographic hard gate.
func ageScore(p model.AdvertiserProfile, pub *catalog.Publisher) float64 {
	plo, phi, err := catalog.ParseAgeRange(pub.Audience.AgeSkew)
	if err != nil {
		return 0.5
	}
	alo, ahi := p.TargetAgeMin, p.TargetAgeMax
	if alo == 0 && ahi == 0 {
		return 0.5 // profile gave no age signal
	}
	lo, hi := max(alo, plo), min(ahi, phi)
	overlap := hi - lo + 1
	if overlap <= 0 {
		return 0
	}
	narrower := min(ahi-alo, phi-plo) + 1
	if narrower <= 0 {
		return 0
	}
	return clamp(float64(overlap)/float64(narrower), 0, 1)
}

// valuesScore is the fraction of the advertiser's stated values that the
// publisher's notes and subcategories signal.
func valuesScore(profileValues, pubKeywords []string) float64 {
	if len(profileValues) == 0 {
		return 0.5
	}
	have := make(map[string]bool, len(pubKeywords))
	for _, k := range pubKeywords {
		have[strings.ToLower(k)] = true
	}
	hit := 0
	for _, v := range profileValues {
		if have[strings.ToLower(v)] {
			hit++
		}
	}
	return clamp(float64(hit)/float64(len(profileValues)), 0, 1)
}

// genderScore rewards publishers whose audience leans the way the advertiser
// needs. A balanced or unknown target is neutral.
func genderScore(skew string, split map[string]float64) float64 {
	if skew != "female" && skew != "male" {
		return 0.5
	}
	share, ok := split[skew]
	if !ok {
		return 0.5
	}
	return clamp((share-0.3)/0.5, 0, 1)
}

// priceTierIncome and incomeIndex place price tiers and audience income tiers
// on one ordinal axis so the distance between them is meaningful.
var priceTierIncome = map[string]float64{"budget": 0, "mid": 0.5, "premium": 1.5, "luxury": 2}
var incomeIndex = map[string]float64{"mid": 0, "mid-high": 1, "high": 2}

func incomeScore(priceTier, incomeTier string) float64 {
	a, okA := priceTierIncome[priceTier]
	b, okB := incomeIndex[incomeTier]
	if !okA || !okB {
		return 0.5
	}
	d := a - b
	if d < 0 {
		d = -d
	}
	return clamp(1-0.5*d, 0, 1)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
