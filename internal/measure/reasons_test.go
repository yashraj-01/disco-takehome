package measure

import (
	"strings"
	"testing"

	"github.com/yashraj/disco/internal/model"
)

// entry builds a minimal LedgerEntry for a test, filling only the sub-score
// named (all others at a neutral 0.5) so a citation on one sub-score can
// never accidentally match another.
func entry(publisherID, verdict, reason, subScore string, value float64) model.LedgerEntry {
	e := model.LedgerEntry{
		PublisherID: publisherID,
		Verdict:     verdict,
		Reason:      reason,
		SubScores: model.SubScores{
			Category: 0.5, AOVAlignment: 0.5, AgeOverlap: 0.5,
			ValuesMatch: 0.5, GenderFit: 0.5, IncomeTier: 0.5,
		},
	}
	switch subScore {
	case "Category":
		e.SubScores.Category = value
	case "AOVAlignment":
		e.SubScores.AOVAlignment = value
	case "AgeOverlap":
		e.SubScores.AgeOverlap = value
	case "ValuesMatch":
		e.SubScores.ValuesMatch = value
	case "GenderFit":
		e.SubScores.GenderFit = value
	case "IncomeTier":
		e.SubScores.IncomeTier = value
	}
	return e
}

func campaignOf(entries ...model.LedgerEntry) []Campaign {
	return []Campaign{{BriefN: 1, Campaign: model.Campaign{PublisherLedger: entries}}}
}

// A contradiction: excluded publisher, cited AgeOverlap sub-score is 1.0
// (a perfect overlap), while the reason claims age is the problem.
func TestContradictionDetected(t *testing.T) {
	e := entry("pub_1", "excluded", "the audience skews too old for our brand", "AgeOverlap", 1.0)
	m := ReasonConsistency(campaignOf(e))

	if got := m.Counts["contradicted"]; got != 1 {
		t.Fatalf("contradicted = %d, want 1", got)
	}
	if got := m.Counts["consistent"]; got != 0 {
		t.Fatalf("consistent = %d, want 0 (corruption check: a real contradiction must not also count as consistent)", got)
	}
	if len(m.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(m.Findings))
	}
	if m.Findings[0].PublisherID != "pub_1" {
		t.Fatalf("finding publisher = %q, want pub_1", m.Findings[0].PublisherID)
	}
}

// A consistent citation must not be flagged as a contradiction.
// Corruption this catches: swapping the excluded threshold direction
// (e.g. using >= instead of <=) would misclassify this as contradicted.
func TestConsistentCitationNotFlagged(t *testing.T) {
	e := entry("pub_1", "excluded", "the audience skews too old for our brand", "AgeOverlap", 0.1)
	m := ReasonConsistency(campaignOf(e))

	if got := m.Counts["consistent"]; got != 1 {
		t.Fatalf("consistent = %d, want 1", got)
	}
	if got := m.Counts["contradicted"]; got != 0 {
		t.Fatalf("contradicted = %d, want 0", got)
	}
	if len(m.Findings) != 0 {
		t.Fatalf("findings = %d, want 0", len(m.Findings))
	}
}

// Polarity flips correctly for recommended vs excluded: the same high
// sub-score value is consistent when citing a strength (recommended) but
// would be contradictory when citing a problem (excluded).
// Corruption this catches: using one fixed threshold direction regardless
// of verdict.
func TestPolarityFlipsWithVerdict(t *testing.T) {
	recommended := entry("pub_1", "recommended", "strong values alignment with this advertiser", "ValuesMatch", 0.9)
	excluded := entry("pub_2", "excluded", "values alignment is the issue here", "ValuesMatch", 0.9)

	mRec := ReasonConsistency(campaignOf(recommended))
	if got := mRec.Counts["consistent"]; got != 1 {
		t.Fatalf("recommended citation: consistent = %d, want 1", got)
	}
	if got := mRec.Counts["contradicted"]; got != 0 {
		t.Fatalf("recommended citation: contradicted = %d, want 0", got)
	}

	mExc := ReasonConsistency(campaignOf(excluded))
	if got := mExc.Counts["contradicted"]; got != 1 {
		t.Fatalf("excluded citation: contradicted = %d, want 1", got)
	}
	if got := mExc.Counts["consistent"]; got != 0 {
		t.Fatalf("excluded citation: consistent = %d, want 0", got)
	}
}

// A gateReason string (code-generated, not model-written) must be skipped
// rather than measured. Corruption this catches: forgetting the
// isGeneratedReason check would score this as a real, and highly
// consistent-looking, citation instead of excluding it entirely.
func TestGateReasonSkipped(t *testing.T) {
	e := entry("pub_1", "excluded", "No category or subcategory overlap with this advertiser.", "Category", 0.05)
	m := ReasonConsistency(campaignOf(e))

	if got := m.Counts["skipped_generated"]; got != 1 {
		t.Fatalf("skipped_generated = %d, want 1", got)
	}
	if got := m.Counts["citations"]; got != 0 {
		t.Fatalf("citations = %d, want 0 (a skipped entry must not also be counted as a citation)", got)
	}
	if got := m.Counts["consistent"]; got != 0 {
		t.Fatalf("consistent = %d, want 0", got)
	}
}

// A reason citing nothing checkable counts as vague.
// Corruption this catches: treating an unmatched reason as "consistent by
// default" instead of flagging it as unmeasurable.
func TestVagueReasonCounted(t *testing.T) {
	e := entry("pub_1", "excluded", "low fit", "AgeOverlap", 1.0)
	m := ReasonConsistency(campaignOf(e))

	if got := m.Counts["vague_reasons"]; got != 1 {
		t.Fatalf("vague_reasons = %d, want 1", got)
	}
	if got := m.Counts["citations"]; got != 0 {
		t.Fatalf("citations = %d, want 0", got)
	}
	if got := m.Values["vagueness_rate"]; got != 1.0 {
		t.Fatalf("vagueness_rate = %v, want 1.0", got)
	}
}

// "women" must not be mis-attributed to the "men " phrase. "women shoppers"
// literally contains "men " as a substring (the "wo-MEN- " span), so this
// guards the table-order fix rather than just the final classification,
// which would look identical either way since both phrases map to the same
// sub-score.
// Corruption this catches: reordering the GenderFit phrase list (or
// building the match from an unordered map) so "men "/"male" is tested
// before "women"/"female".
func TestWomenDoesNotMatchMenPattern(t *testing.T) {
	reason := "our audience is mostly women shoppers, a mismatch for this advertiser"
	if !strings.Contains(strings.ToLower(reason), "men ") {
		t.Fatalf("test fixture invalid: expected %q to literally contain the substring trap", reason)
	}

	cat := genderFitCategory(t)
	phrase, ok := firstMatch(cat, strings.ToLower(reason))
	if !ok {
		t.Fatalf("expected a GenderFit match")
	}
	if phrase == "men " {
		t.Fatalf("matched phrase = %q, want \"women\" (order must check women/female before men/male)", phrase)
	}
	if phrase != "women" {
		t.Fatalf("matched phrase = %q, want \"women\"", phrase)
	}

	// And end to end: a single reason mentioning "women" must produce
	// exactly one GenderFit citation, not two from the trap.
	e := entry("pub_1", "excluded", reason, "GenderFit", 0.1)
	m := ReasonConsistency(campaignOf(e))
	if got := m.Counts["citations"]; got != 1 {
		t.Fatalf("citations = %d, want 1 (women/men substring trap must not double-count)", got)
	}
}

func genderFitCategory(t *testing.T) subScoreCategory {
	t.Helper()
	for _, c := range subScorePhrases {
		if c.Name == "GenderFit" {
			return c
		}
	}
	t.Fatal("GenderFit category not found in subScorePhrases")
	return subScoreCategory{}
}

// A reason citing two sub-scores produces two citations.
// Corruption this catches: short-circuiting after the first matched
// category across the whole table instead of checking every category.
func TestTwoSubScoresProduceTwoCitations(t *testing.T) {
	e := model.LedgerEntry{
		PublisherID: "pub_1",
		Verdict:     "excluded",
		Reason:      "wrong category entirely, and the audience skews much older too",
		SubScores: model.SubScores{
			Category: 0.05, AOVAlignment: 0.5, AgeOverlap: 0.9,
			ValuesMatch: 0.5, GenderFit: 0.5, IncomeTier: 0.5,
		},
	}
	m := ReasonConsistency(campaignOf(e))

	if got := m.Counts["citations"]; got != 2 {
		t.Fatalf("citations = %d, want 2", got)
	}
	// Category=0.05 is consistent with excluded; AgeOverlap=0.9 contradicts it.
	if got := m.Counts["consistent"]; got != 1 {
		t.Fatalf("consistent = %d, want 1", got)
	}
	if got := m.Counts["contradicted"]; got != 1 {
		t.Fatalf("contradicted = %d, want 1", got)
	}
}

// Rates must be computed over citations, not entries.
// Corruption this catches: dividing by len(campaigns[*].PublisherLedger)
// (entry count) instead of the citation count, which silently changes the
// rate whenever any entry contributes zero or more than one citation.
func TestRatesUseCitationDenominator(t *testing.T) {
	// One entry contributing two citations (one consistent, one
	// contradicted) and two vague entries (zero citations each, two
	// entries). entries=3, citations=2: any bug that denominates the rate
	// by entries instead of citations must produce a different number from
	// the one computed here (1/3... vs 1/2), so this is not a coincidental
	// pass.
	twoCitation := model.LedgerEntry{
		PublisherID: "pub_1",
		Verdict:     "excluded",
		Reason:      "wrong category entirely, and the audience skews much older too",
		SubScores: model.SubScores{
			Category: 0.05, AOVAlignment: 0.5, AgeOverlap: 0.9,
			ValuesMatch: 0.5, GenderFit: 0.5, IncomeTier: 0.5,
		},
	}
	vagueEntry1 := entry("pub_2", "excluded", "low fit", "AgeOverlap", 1.0)
	vagueEntry2 := entry("pub_3", "excluded", "not a good match", "AgeOverlap", 1.0)

	m := ReasonConsistency(campaignOf(twoCitation, vagueEntry1, vagueEntry2))

	if got := m.Counts["entries"]; got != 3 {
		t.Fatalf("entries = %d, want 3", got)
	}
	if got := m.Counts["citations"]; got != 2 {
		t.Fatalf("citations = %d, want 2", got)
	}
	// consistency_rate must be 1/2 (one consistent of two citations), not
	// 1/3 (diluted by the two vague entries that contributed no citation).
	if got := m.Values["consistency_rate"]; got != 0.5 {
		t.Fatalf("consistency_rate = %v, want 0.5 (scored citations = 2, consistent = 1)", got)
	}
	if got := m.Values["contradiction_rate"]; got != 0.5 {
		t.Fatalf("contradiction_rate = %v, want 0.5", got)
	}
	if got := m.Values["vagueness_rate"]; got != 2.0/3.0 {
		t.Fatalf("vagueness_rate = %v, want %v (2 vague of 3 checkable entries)", got, 2.0/3.0)
	}
}

// A "considered" verdict is ambiguous: the citation counts but is
// classified unscored and excluded from the rate.
func TestConsideredIsUnscored(t *testing.T) {
	e := entry("pub_1", "considered", "audience skews a bit older than ideal", "AgeOverlap", 0.9)
	m := ReasonConsistency(campaignOf(e))

	if got := m.Counts["unscored"]; got != 1 {
		t.Fatalf("unscored = %d, want 1", got)
	}
	if got := m.Counts["citations"]; got != 1 {
		t.Fatalf("citations = %d, want 1", got)
	}
	if got := m.Counts["consistent"] + m.Counts["contradicted"] + m.Counts["weak"]; got != 0 {
		t.Fatalf("scored (consistent+contradicted+weak) = %d, want 0", got)
	}
}

// A weak citation (between thresholds) is counted separately, not as a
// violation.
func TestWeakCitationCounted(t *testing.T) {
	e := entry("pub_1", "excluded", "the audience skews a bit older than we'd like", "AgeOverlap", 0.65)
	m := ReasonConsistency(campaignOf(e))

	if got := m.Counts["weak"]; got != 1 {
		t.Fatalf("weak = %d, want 1", got)
	}
	if got := m.Counts["consistent"]; got != 0 {
		t.Fatalf("consistent = %d, want 0", got)
	}
	if got := m.Counts["contradicted"]; got != 0 {
		t.Fatalf("contradicted = %d, want 0", got)
	}
}
