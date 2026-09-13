package pipeline

import "github.com/yashraj/disco/internal/catalog"

// The supplied catalog carries no CPM, CPC, or rate card. EstimateCPM derives a
// stand-in from the three fields that proxy how valuable an audience is to
// advertisers: income tier, category demand, and order value.
//
// This is a stand-in, not a measurement. Swapping in a real rate card means
// replacing this one function and changing nothing else.
const (
	cpmFloor   = 8.0  // dollars per thousand impressions
	aovMaxLift = 0.15 // AOV can move the number by at most 15%
)

var incomePremium = map[string]float64{
	"mid":      0,
	"mid-high": 4,
	"high":     9,
}

var categoryPremium = map[string]float64{
	"groceries":         0,
	"instant_delivery":  0,
	"meal_kits":         0,
	"beverages":         1,
	"apparel":           2,
	"pet":               3,
	"wellness_dtc":      3,
	"wellness_services": 3,
	"beauty":            3,
	"home":              4,
}

// EstimateCPM returns the modelled cost per thousand impressions for a
// publisher. minAOV and maxAOV are the catalog-wide bounds, used to place this
// publisher's order value on a 0..1 index.
func EstimateCPM(pub *catalog.Publisher, minAOV, maxAOV int) float64 {
	base := cpmFloor + incomePremium[pub.Audience.IncomeTier] + categoryPremium[pub.Category]

	var idx float64
	if maxAOV > minAOV {
		idx = clamp(float64(pub.AvgOrderValueUSD-minAOV)/float64(maxAOV-minAOV), 0, 1)
	}
	return base * (1 + aovMaxLift*idx)
}
