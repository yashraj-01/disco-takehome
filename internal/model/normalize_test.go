package model

import "testing"

// TestNormalizeLeverTrimsAndLowers pins the single rule both the production
// filter (pipeline.groundedIn) and the eval assertion (eval.Check) must apply
// identically: a lever differing only in case or in surrounding whitespace is
// the same lever.
func TestNormalizeLeverTrimsAndLowers(t *testing.T) {
	cases := []struct{ a, b string }{
		{"vet-recommended", "vet-recommended"},
		{"  vet-recommended  ", "vet-recommended"},
		{"Vet-Recommended", "vet-recommended"},
		{"\tVet-Recommended\n", "vet-recommended"},
	}
	for _, c := range cases {
		if got := NormalizeLever(c.a); got != c.b {
			t.Errorf("NormalizeLever(%q) = %q, want %q", c.a, got, c.b)
		}
	}
}
