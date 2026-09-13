package pipeline

import (
	"math"
	"testing"
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

// Every publisher should land in a range a real display buyer would recognise.
func TestEstimateCPMStaysInPlausibleRange(t *testing.T) {
	c := loadCatalog(t)
	for i := range c.Publishers {
		got := EstimateCPM(&c.Publishers[i], c.MinAOV, c.MaxAOV)
		if got < 8 || got > 25 {
			t.Errorf("%s CPM = %.2f, want [8,25]", c.Publishers[i].ID, got)
		}
	}
}

// AOV is capped at a 15% effect so it modifies the number without driving it.
func TestAOVIsAModifierNotADriver(t *testing.T) {
	c := loadCatalog(t)
	lowAOVHighTier, _ := c.Publisher("pub_003") // wellness_services, high, AOV 42
	highAOVLowTier, _ := c.Publisher("pub_006") // apparel, mid, AOV 89
	a := EstimateCPM(lowAOVHighTier, c.MinAOV, c.MaxAOV)
	b := EstimateCPM(highAOVLowTier, c.MinAOV, c.MaxAOV)
	if a <= b {
		t.Errorf("high-tier low-AOV (%.2f) should exceed mid-tier high-AOV (%.2f); "+
			"AOV is overpowering income tier", a, b)
	}
}
