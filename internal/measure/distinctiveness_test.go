package measure

import (
	"testing"

	"github.com/yashraj/disco/internal/model"
)

// cr builds a minimal Creative for a test.
func cr(personaID, headline, body string, levers ...string) model.Creative {
	return model.Creative{PersonaID: personaID, Headline: headline, Body: body, MessagingLevers: levers}
}

func campaignOfCreatives(briefN int, creatives ...model.Creative) Campaign {
	return Campaign{BriefN: briefN, Campaign: model.Campaign{Creatives: creatives}}
}

// TestCreativeDistinctiveness_IdenticalCreativesScoreOne catches the
// corruption: a similarity computation that never reaches 1.0 even for
// literally identical text (e.g. an off-by-one in the intersection/union
// count, or comparing token slices instead of sets so duplicate words
// inside one creative distort the denominator).
func TestCreativeDistinctiveness_IdenticalCreativesScoreOne(t *testing.T) {
	campaigns := []Campaign{campaignOfCreatives(1,
		cr("p1", "Save Big Today", "Get premium quality delivered fast."),
		cr("p2", "Save Big Today", "Get premium quality delivered fast."),
	)}

	m := CreativeDistinctiveness(campaigns, nil)
	if got := m.Values["mean_copy_overlap"]; got != 1.0 {
		t.Errorf("mean_copy_overlap = %v, want 1.0 for identical creatives", got)
	}
	if got := m.Values["max_copy_overlap"]; got != 1.0 {
		t.Errorf("max_copy_overlap = %v, want 1.0", got)
	}
}

// TestCreativeDistinctiveness_NoSharedContentWordsScoresZero catches the
// corruption: a similarity function that reports a nonzero floor even when
// two token sets are fully disjoint (e.g. forgetting to subtract the
// intersection when computing the union, which would never reach 0).
func TestCreativeDistinctiveness_NoSharedContentWordsScoresZero(t *testing.T) {
	campaigns := []Campaign{campaignOfCreatives(1,
		cr("p1", "Sleek Modern Sofas", "Handcrafted walnut frames built to last."),
		cr("p2", "Bright Playful Sneakers", "Vegan canvas kicks for weekend adventures."),
	)}

	m := CreativeDistinctiveness(campaigns, nil)
	if got := m.Values["mean_copy_overlap"]; got != 0 {
		t.Errorf("mean_copy_overlap = %v, want 0 for disjoint content words", got)
	}
}

// TestCreativeDistinctiveness_StopwordsDoNotInflateSimilarity catches the
// corruption: a tokenizer that forgets to drop stopwords, so two creatives
// with nothing in common except function words ("the", "a", "of") would
// score a nonzero, misleadingly reassuring similarity instead of 0.
func TestCreativeDistinctiveness_StopwordsDoNotInflateSimilarity(t *testing.T) {
	campaigns := []Campaign{campaignOfCreatives(1,
		cr("p1", "The Sofa", "This is a sofa of the highest quality for the home."),
		cr("p2", "The Sneaker", "This is a sneaker of the finest kind for the street."),
	)}

	m := CreativeDistinctiveness(campaigns, nil)
	if got := m.Values["mean_copy_overlap"]; got != 0 {
		t.Errorf("mean_copy_overlap = %v, want 0 (shared words are all stopwords)", got)
	}
}

// TestCreativeDistinctiveness_LeverOverlapIndependentOfCopy catches the
// corruption: lever_overlap computed from copy text (or blended with it)
// instead of from MessagingLevers directly. Constructs a case with low copy
// overlap but identical lever sets, and confirms the two signals diverge as
// designed rather than tracking each other.
func TestCreativeDistinctiveness_LeverOverlapIndependentOfCopy(t *testing.T) {
	campaigns := []Campaign{campaignOfCreatives(1,
		cr("p1", "Sleek Modern Sofas", "Handcrafted walnut frames built to last.",
			"free_shipping", "durability"),
		cr("p2", "Bright Playful Sneakers", "Vegan canvas kicks for weekend adventures.",
			"free_shipping", "durability"),
	)}

	m := CreativeDistinctiveness(campaigns, nil)
	if got := m.Values["mean_copy_overlap"]; got != 0 {
		t.Errorf("mean_copy_overlap = %v, want 0", got)
	}
	if got := m.Values["mean_lever_overlap"]; got != 1.0 {
		t.Errorf("mean_lever_overlap = %v, want 1.0 (identical lever sets)", got)
	}
}

// TestCreativeDistinctiveness_SingleCreativeSkippedNotDividedByZero catches
// the corruption: a campaign with exactly one creative (0 possible pairs)
// either panicking on a division by zero or being silently folded into the
// measured denominator instead of being counted as skipped.
func TestCreativeDistinctiveness_SingleCreativeSkippedNotDividedByZero(t *testing.T) {
	campaigns := []Campaign{
		campaignOfCreatives(1, cr("p1", "Only One", "Just one creative here.")),
		campaignOfCreatives(2,
			cr("p1", "Two Creatives A", "Alpha copy entirely of its own."),
			cr("p2", "Two Creatives B", "Beta copy entirely of its own too, extra.")),
	}

	m := CreativeDistinctiveness(campaigns, nil)
	if got := m.Counts["skipped_too_few"]; got != 1 {
		t.Errorf("skipped_too_few = %d, want 1", got)
	}
	if got := m.Values["campaigns_measured"]; got != 1 {
		t.Errorf("campaigns_measured = %v, want 1", got)
	}
	if got := m.Counts["campaigns"]; got != 2 {
		t.Errorf("counts[campaigns] = %d, want 2", got)
	}
	if got := m.Counts["creatives"]; got != 3 {
		t.Errorf("counts[creatives] = %d, want 3 (1 skipped + 2 measured)", got)
	}
}

// TestCreativeDistinctiveness_PairCountIsNChooseTwo catches the corruption:
// an incorrect pair-generation loop (e.g. counting ordered pairs including
// self-pairs, or double-counting (i,j) and (j,i)) for a campaign of N > 2
// creatives, where N(N-1)/2 != N and != N^2.
func TestCreativeDistinctiveness_PairCountIsNChooseTwo(t *testing.T) {
	campaigns := []Campaign{campaignOfCreatives(1,
		cr("p1", "Alpha One", "Alpha body text unique."),
		cr("p2", "Beta Two", "Beta body text unique."),
		cr("p3", "Gamma Three", "Gamma body text unique."),
		cr("p4", "Delta Four", "Delta body text unique."),
	)}

	m := CreativeDistinctiveness(campaigns, nil)
	want := 4 * 3 / 2 // N(N-1)/2 = 6
	if got := m.Counts["pairs_compared"]; got != want {
		t.Errorf("pairs_compared = %d, want %d", got, want)
	}
}

// TestCreativeDistinctiveness_MaxIsWorstAcrossAllCampaigns catches the
// corruption: max_copy_overlap computed as the worst PER-CAMPAIGN MEAN
// (which would understate it) instead of the single worst pair found
// anywhere across every campaign. Campaign 1's pairs are all moderately
// similar (raising its mean but not to 1.0); campaign 2 has one pair of
// literally identical creatives buried among otherwise-average pairs, so
// its mean is unremarkable even though it contains the global worst pair.
func TestCreativeDistinctiveness_MaxIsWorstAcrossAllCampaigns(t *testing.T) {
	campaigns := []Campaign{
		campaignOfCreatives(1,
			cr("p1", "Moderate Match One", "Some shared words appear across creatives here."),
			cr("p2", "Moderate Match Two", "Some shared words appear across creatives there."),
		),
		campaignOfCreatives(2,
			cr("p1", "Totally Unrelated First", "Nothing in common with the others in this set."),
			cr("p2", "Identical Twin Copy", "Exact duplicate text appears in both creatives now."),
			cr("p3", "Identical Twin Copy", "Exact duplicate text appears in both creatives now."),
		),
	}

	m := CreativeDistinctiveness(campaigns, nil)
	if got := m.Values["max_copy_overlap"]; got != 1.0 {
		t.Errorf("max_copy_overlap = %v, want 1.0 (the identical pair buried in campaign 2)", got)
	}

	// The per-campaign means must NOT be 1.0 — campaign 2's mean is dragged
	// down by its two dissimilar pairs, proving max isn't just echoing a
	// campaign mean.
	if got := m.Values["mean_copy_overlap"]; got >= 1.0 {
		t.Errorf("mean_copy_overlap = %v, want < 1.0 (must differ from max_copy_overlap)", got)
	}

	if len(m.Findings) == 0 {
		t.Fatal("expected at least one finding")
	}
	top := m.Findings[0]
	if top.BriefN != 2 {
		t.Errorf("top finding BriefN = %d, want 2 (the campaign containing the worst pair)", top.BriefN)
	}
}
