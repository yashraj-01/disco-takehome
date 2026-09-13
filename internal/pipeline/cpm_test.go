package pipeline

import (
	"math"
	"testing"

	"github.com/yashraj/disco/internal/catalog"
)

// Spot values fixed by the design doc. If these move, the budget figures in
// every demo and every eval snapshot move with them.
func TestEstimateCPMSpotValues(t *testing.T) {
	c := loadCatalog(t)
	for _, tc := range []struct {
		id   string
		name string
		want float64
	}{
		{"pub_001", "Swiftcart", 8.00},
		{"pub_009", "Ruffco", 11.44},
		{"pub_007", "Pawline", 15.48},
		{"pub_008", "Pantrygood", 17.87},
		{"pub_014", "Hearthstone Goods", 24.15},
	} {
		pub, ok := c.Publisher(tc.id)
		if !ok {
			t.Fatalf("%s missing", tc.id)
		}
		got := EstimateCPM(pub, c.MinAOV, c.MaxAOV)
		if math.Abs(got-tc.want) > 0.005 {
			t.Errorf("%s CPM = %.4f, want %.2f", tc.name, got, tc.want)
		}
	}
}

// Every publisher should land in a range a real display buyer would recognise,
// with real spread across the 20 publishers (not a stub constant).
func TestEstimateCPMStaysInPlausibleRange(t *testing.T) {
	c := loadCatalog(t)
	seen := make(map[float64]bool)
	var minCPM, maxCPM float64
	minCPM = 1e10
	for i := range c.Publishers {
		got := EstimateCPM(&c.Publishers[i], c.MinAOV, c.MaxAOV)
		if got < 8 || got > 25 {
			t.Errorf("%s CPM = %.2f, want [8,25]", c.Publishers[i].ID, got)
		}
		seen[got] = true
		if got < minCPM {
			minCPM = got
		}
		if got > maxCPM {
			maxCPM = got
		}
	}
	// Minimum should be below 9, maximum should be above 23
	if minCPM >= 9 {
		t.Errorf("minimum CPM = %.2f, want < 9", minCPM)
	}
	if maxCPM <= 23 {
		t.Errorf("maximum CPM = %.2f, want > 23", maxCPM)
	}
	// Should have at least 10 distinct values across 20 publishers
	if len(seen) < 10 {
		t.Errorf("only %d distinct CPM values found, want >= 10", len(seen))
	}
}

// AOV is capped at 15% effect: with identical category and income tier,
// moving from minAOV to maxAOV should increase CPM by at most 15%.
func TestAOVCappedAt15Percent(t *testing.T) {
	c := loadCatalog(t)

	// Construct two publishers with identical category and income tier
	// but AOV at the catalog extremes
	lowAOV := &catalog.Publisher{
		Category:         "apparel",
		AvgOrderValueUSD: c.MinAOV,
		Audience: catalog.Audience{
			IncomeTier: "high",
		},
	}
	highAOV := &catalog.Publisher{
		Category:         "apparel",
		AvgOrderValueUSD: c.MaxAOV,
		Audience: catalog.Audience{
			IncomeTier: "high",
		},
	}

	cpmLow := EstimateCPM(lowAOV, c.MinAOV, c.MaxAOV)
	cpmHigh := EstimateCPM(highAOV, c.MinAOV, c.MaxAOV)

	// cpmHigh should be strictly greater than cpmLow
	if cpmHigh <= cpmLow {
		t.Errorf("AOV should have some effect: cpmHigh (%.4f) should exceed cpmLow (%.4f)", cpmHigh, cpmLow)
	}

	// But the ratio should not exceed 1.15 (15% lift)
	ratio := cpmHigh / cpmLow
	if ratio > 1.15 {
		t.Errorf("AOV lift ratio = %.4f, want <= 1.15", ratio)
	}
}

// Income and category dominate the ordering: high-income categories
// command higher CPM than low-income ones regardless of AOV variation.
func TestIncomeAndCategoryDominateOrdering(t *testing.T) {
	c := loadCatalog(t)
	lowAOVHighTier, _ := c.Publisher("pub_003") // wellness_services, high, AOV 42
	highAOVLowTier, _ := c.Publisher("pub_006") // apparel, mid, AOV 89
	a := EstimateCPM(lowAOVHighTier, c.MinAOV, c.MaxAOV)
	b := EstimateCPM(highAOVLowTier, c.MinAOV, c.MaxAOV)
	if a <= b {
		t.Errorf("high-tier low-AOV (%.2f) should exceed mid-tier high-AOV (%.2f); "+
			"income tier and category should dominate", a, b)
	}
}

// All publishers' income tiers and categories are in the pricing tables.
// This guards against silent zero-value lookups if new data is added.
func TestPremiumTablesConsistentWithCatalog(t *testing.T) {
	c := loadCatalog(t)
	for i := range c.Publishers {
		pub := &c.Publishers[i]

		// Check income tier is in the table
		if _, ok := IncomePremium[pub.Audience.IncomeTier]; !ok {
			t.Errorf("%s: income tier %q not in IncomePremium table", pub.ID, pub.Audience.IncomeTier)
		}

		// Check category is in the table
		if _, ok := CategoryPremium[pub.Category]; !ok {
			t.Errorf("%s: category %q not in CategoryPremium table", pub.ID, pub.Category)
		}
	}
}
