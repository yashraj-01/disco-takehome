package catalog

import "testing"

func TestLoadReadsBothFiles(t *testing.T) {
	c, err := Load("../../data")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(c.Publishers); got != 20 {
		t.Errorf("publishers = %d, want 20", got)
	}
	if got := len(c.Personas); got != 10 {
		t.Errorf("personas = %d, want 10", got)
	}
	if c.MinAOV != 28 || c.MaxAOV != 198 {
		t.Errorf("AOV bounds = %d/%d, want 28/198", c.MinAOV, c.MaxAOV)
	}
}

func TestLookupByID(t *testing.T) {
	c, _ := Load("../../data")
	p, ok := c.Publisher("pub_007")
	if !ok {
		t.Fatal("pub_007 not found")
	}
	if p.Name != "Pawline" {
		t.Errorf("name = %q, want Pawline", p.Name)
	}
	if _, ok := c.Publisher("pub_999"); ok {
		t.Error("pub_999 should not exist")
	}
	if _, ok := c.Persona("persona_004"); !ok {
		t.Error("persona_004 should exist")
	}
}

func TestKeywordsPrecomputed(t *testing.T) {
	c, _ := Load("../../data")
	p, _ := c.Publisher("pub_008") // Pantrygood: "Values-driven shoppers. Responsive to
	// clean-ingredient, sustainability, and wellness claims." + organic/natural subcategories
	if !contains(p.Keywords, "sustainability") {
		t.Errorf("Pantrygood keywords = %v, want sustainability", p.Keywords)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestParseAgeRange(t *testing.T) {
	for _, tc := range []struct {
		in       string
		lo, hi   int
		wantErr  bool
	}{
		{"18-34", 18, 34, false},
		{"50-70", 50, 70, false},
		{"28-40", 28, 40, false},
		{"garbage", 0, 0, true},
	} {
		lo, hi, err := ParseAgeRange(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseAgeRange(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if err == nil && (lo != tc.lo || hi != tc.hi) {
			t.Errorf("ParseAgeRange(%q) = %d,%d want %d,%d", tc.in, lo, hi, tc.lo, tc.hi)
		}
	}
}
