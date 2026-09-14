package measure

import (
	"strings"
	"testing"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
)

// testCatalog loads the repository's real catalog (never modified by these
// tests), reached from this package's location at internal/measure. Tests
// of the catalog-derived vocabulary (FIX: augmentedCategories) use real
// publisher records rather than inventing fixture data, since the whole
// point of that fix is behavior driven by the actual data shape.
func testCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load("../../data")
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return c
}

// testPublisher returns the real catalog publisher with the given id,
// failing the test if it doesn't exist (a typo'd id would otherwise
// silently fall back to nil-catalog behavior and the test would pass for
// the wrong reason).
func testPublisher(t *testing.T, cat *catalog.Catalog, id string) *catalog.Publisher {
	t.Helper()
	p, ok := cat.Publisher(id)
	if !ok {
		t.Fatalf("no publisher %q in the real catalog", id)
	}
	return p
}

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

// campaignWithProfile builds a campaign carrying an advertiser profile, for
// tests of the advertiser_term abstention (FIX 2), which reads
// Campaign.Advertiser.DerivedProfile.
func campaignWithProfile(profile model.AdvertiserProfile, entries ...model.LedgerEntry) []Campaign {
	return []Campaign{{BriefN: 1, Campaign: model.Campaign{
		Advertiser:      model.AdvertiserBlock{DerivedProfile: profile},
		PublisherLedger: entries,
	}}}
}

// A contradiction: excluded publisher, cited AgeOverlap sub-score is 1.0
// (a perfect overlap), while the reason claims age is the problem.
func TestContradictionDetected(t *testing.T) {
	e := entry("pub_1", "excluded", "the audience skews too old for our brand", "AgeOverlap", 1.0)
	m := ReasonConsistency(campaignOf(e), nil)

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
	m := ReasonConsistency(campaignOf(e), nil)

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
	excluded := entry("pub_2", "excluded", "values alignment is the core problem for this campaign", "ValuesMatch", 0.9)

	mRec := ReasonConsistency(campaignOf(recommended), nil)
	if got := mRec.Counts["consistent"]; got != 1 {
		t.Fatalf("recommended citation: consistent = %d, want 1", got)
	}
	if got := mRec.Counts["contradicted"]; got != 0 {
		t.Fatalf("recommended citation: contradicted = %d, want 0", got)
	}

	mExc := ReasonConsistency(campaignOf(excluded), nil)
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
	m := ReasonConsistency(campaignOf(e), nil)

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
	m := ReasonConsistency(campaignOf(e), nil)

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

// "women" must not be mis-attributed to the "men" phrase: bare-substring
// matching finds "men" inside "women" (the "wo-MEN-" span), so without the
// leading word-boundary anchor this would be a false GenderFit citation
// whenever "women" appears at all.
// Corruption this catches: matching phrases as a bare substring instead of
// `\b` + the phrase (e.g. dropping the anchor, or using
// strings.Contains directly).
func TestWomenDoesNotMatchMenPattern(t *testing.T) {
	reason := "our audience is mostly women shoppers, a mismatch for this advertiser"
	if !strings.Contains(strings.ToLower(reason), "men") {
		t.Fatalf("test fixture invalid: expected %q to literally contain the substring trap", reason)
	}
	if patternMatches(t, "GenderFit", "men", reason) {
		t.Fatalf(`\bmen matched inside "women" — the word-boundary anchor did not disambiguate them`)
	}

	cat := genderFitCategory(t)
	p, _, _, ok := firstMatch(cat, strings.ToLower(reason))
	if !ok {
		t.Fatalf("expected a GenderFit match")
	}
	if p.Phrase != "women" {
		t.Fatalf("matched phrase = %q, want \"women\"", p.Phrase)
	}

	// And end to end: a single reason mentioning "women" must produce
	// exactly one GenderFit citation, not two from the trap.
	e := entry("pub_1", "excluded", reason, "GenderFit", 0.1)
	m := ReasonConsistency(campaignOf(e), nil)
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

// patternMatches reports whether category's compiled pattern for phrase
// matches text (lowercased first, as ReasonConsistency does). It fails the
// test outright if no such phrase is registered for that category, so a
// typo in a test's phrase argument cannot silently read as "no match".
func patternMatches(t *testing.T, categoryName, phrase, text string) bool {
	t.Helper()
	for _, c := range subScorePhrases {
		if c.Name != categoryName {
			continue
		}
		for _, p := range c.Patterns {
			if p.Phrase == phrase {
				return p.re.MatchString(strings.ToLower(text))
			}
		}
		t.Fatalf("phrase %q not registered under category %q", phrase, categoryName)
	}
	t.Fatalf("category %q not found in subScorePhrases", categoryName)
	return false
}

// The word-boundary anchor must reject "age" inside "beverages" and
// "package" — this is the specific false positive that inflated the
// contradiction rate before the fix (nearly every excluded beverage
// publisher's reason mentions "beverages", which bare-substring matching
// misread as an AgeOverlap citation).
// Corruption this catches: matching "age" (or any phrase) as a bare
// substring instead of anchoring it to a leading word boundary.
func TestAgeDoesNotMatchInsideBeveragesOrPackage(t *testing.T) {
	for _, text := range []string{
		"Excluded due to category_mismatch (apparel vs beverages).",
		"A budget-friendly package aimed at value shoppers.",
	} {
		if patternMatches(t, "AgeOverlap", "age", text) {
			t.Fatalf("\\bage matched inside %q; the word-boundary anchor did not stop the substring trap", text)
		}
	}
}

// The word-boundary anchor must reject "her" inside "leather".
// Corruption this catches: matching "her" as a bare substring, or relying
// only on the old trailing-space hack ("her ") instead of a leading
// boundary.
func TestHerDoesNotMatchInsideLeather(t *testing.T) {
	text := "Category mismatch: organic grocery does not match luxury leather accessories."
	if patternMatches(t, "GenderFit", "her", text) {
		t.Fatalf("\\bher matched inside %q", text)
	}
}

// "male" must not match inside "female", the mirror image of men/women.
// Corruption this catches: fixing only the men/women pair (e.g. keeping a
// trailing-space hack for one but not the other) instead of applying one
// general leading-boundary rule to every phrase.
func TestMaleDoesNotMatchInsideFemale(t *testing.T) {
	text := "This publisher over-indexes on a female audience."
	if patternMatches(t, "GenderFit", "male", text) {
		t.Fatalf("\\bmale matched inside %q", text)
	}
	if patternMatches(t, "GenderFit", "men", "a strongly women-led brand") {
		t.Fatalf(`\bmen matched inside "women-led"`)
	}
}

// "age" must still match as a real word or stem: "ages" and "aged" are
// exactly the citations the phrase exists to catch.
// Corruption this catches: over-correcting into a whole-word-only match
// (anchoring both ends) so a stem like "ages"/"aged" stops matching.
func TestAgeMatchesRealAgeMentions(t *testing.T) {
	for _, text := range []string{
		"The audience ages 50-70, well outside our target.",
		"An aged demographic that skews older than ideal.",
	} {
		if !patternMatches(t, "AgeOverlap", "age", text) {
			t.Fatalf("\\bage did not match %q, want a match", text)
		}
	}
}

// "spend" must still match "spending" — the anchor is leading-only, so a
// stem still works.
// Corruption this catches: anchoring both the leading and trailing edge of
// the phrase (making it whole-word-only), which would break every stem
// phrase in the table ("spend", "sustainab", "affordab", "transparen", …).
func TestSpendMatchesSpending(t *testing.T) {
	if !patternMatches(t, "AOVAlignment", "spend", "Way outside their typical spending habits.") {
		t.Fatalf(`\bspend did not match "spending"`)
	}
}

// A phrase at the end of a sentence, with no trailing space (only
// punctuation), must still match — this is exactly the case the old
// trailing-space hack ("men ") got wrong.
// Corruption this catches: reverting to the old trailing-space convention
// instead of a real word-boundary anchor.
func TestPatternMatchesEndOfSentenceNoTrailingSpace(t *testing.T) {
	if !patternMatches(t, "GenderFit", "men", "This publisher's catalog is aimed at men.") {
		t.Fatalf(`\bmen did not match "...aimed at men." (no trailing space before the period)`)
	}
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
	m := ReasonConsistency(campaignOf(e), nil)

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

	m := ReasonConsistency(campaignOf(twoCitation, vagueEntry1, vagueEntry2), nil)

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
	m := ReasonConsistency(campaignOf(e), nil)

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
	m := ReasonConsistency(campaignOf(e), nil)

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

// A concessive clause abstains: the model concedes a signal is fine
// ("despite good age overlap") while excluding for a different, unrelated
// reason (category). Verbatim shape of the real brief 5 / pub_017 finding
// that motivated FIX 1.
// Corruption this catches: removing concessive-marker detection entirely,
// which would score AgeOverlap=1.00 against the excluded verdict as a
// contradiction — exactly the false positive this fix exists to remove.
func TestConcessiveClauseAbstains(t *testing.T) {
	e := entry("pub_017", "excluded",
		"Tech-adjacent activewear and shoe publisher; despite good age overlap, "+
			"the apparel category does not fit wellness services.",
		"AgeOverlap", 1.0)
	m := ReasonConsistency(campaignOf(e), nil)

	if got := m.Counts["concessive"]; got != 1 {
		t.Fatalf("concessive = %d, want 1", got)
	}
	if got := m.Counts["contradicted"]; got != 0 {
		t.Fatalf("contradicted = %d, want 0 (the AgeOverlap citation must abstain, not score as a contradiction)", got)
	}
}

// A concessive marker in a different clause must not suppress an unrelated
// citation in the same reason: the same sentence above also cites Category
// (in the clause after the comma, with no concessive marker of its own),
// and that citation must still be scored normally.
// Corruption this catches: scoping concessive detection to the whole reason
// instead of the clause containing the match, which would also abstain the
// Category citation and drop "consistent" to 0.
func TestConcessiveInDifferentClauseDoesNotSuppressOtherCitation(t *testing.T) {
	e := entry("pub_017", "excluded",
		"Tech-adjacent activewear and shoe publisher; despite good age overlap, "+
			"the apparel category does not fit wellness services.",
		"AgeOverlap", 1.0)
	m := ReasonConsistency(campaignOf(e), nil)

	if got := m.Counts["citations"]; got != 2 {
		t.Fatalf("citations = %d, want 2 (AgeOverlap + Category)", got)
	}
	if got := m.Counts["consistent"]; got != 1 {
		t.Fatalf("consistent = %d, want 1 (the Category citation, unaffected by the earlier clause's \"despite\")", got)
	}
}

// A phrase appearing in the advertiser's own PriceTier abstains: "luxury"
// in "...do not match luxury leather accessories" describes the
// advertiser's product (PriceTier=luxury), not the publisher's IncomeTier.
// Verbatim shape of the real brief 10 / pub_008 finding.
// Corruption this catches: removing the advertiser-descriptor check, which
// would score IncomeTier=1.00 against the excluded verdict as a
// contradiction.
func TestAdvertiserPriceTierAbstains(t *testing.T) {
	e := entry("pub_008", "excluded",
		"Category mismatch: organic grocery and pantry items do not match luxury leather accessories.",
		"IncomeTier", 1.0)
	profile := model.AdvertiserProfile{PriceTier: "luxury"}
	m := ReasonConsistency(campaignWithProfile(profile, e), nil)

	if got := m.Counts["advertiser_term"]; got != 1 {
		t.Fatalf("advertiser_term = %d, want 1", got)
	}
	if got := m.Counts["contradicted"]; got != 0 {
		t.Fatalf("contradicted = %d, want 0 (the IncomeTier citation must abstain as an advertiser term)", got)
	}
}

// A phrase appearing in Subcategories abstains too, including the
// underscore-to-space normalization: the two-word phrase "product type"
// only appears in "specialty_product_type_goods" once underscores become
// spaces ("specialty product type goods"). Raw, un-normalized, the
// Subcategory has no literal "product type" substring at all (an
// underscore sits where the phrase needs a space), so this specifically
// requires the normalization step, not just an advertiser-profile check.
// Corruption this catches: comparing the raw (underscored) Subcategories
// text instead of normalizing it first, which would fail to match
// "product type" against "specialty_product_type_goods" and let the
// citation score as a contradiction.
func TestAdvertiserSubcategoryAbstainsWithUnderscoreNormalization(t *testing.T) {
	e := entry("pub_014", "excluded",
		"This publisher's readership is a different product type than what this advertiser sells.",
		"Category", 1.0)
	profile := model.AdvertiserProfile{Subcategories: []string{"specialty_product_type_goods"}}
	m := ReasonConsistency(campaignWithProfile(profile, e), nil)

	if got := m.Counts["advertiser_term"]; got != 1 {
		t.Fatalf("advertiser_term = %d, want 1", got)
	}
	if got := m.Counts["contradicted"]; got != 0 {
		t.Fatalf("contradicted = %d, want 0", got)
	}
}

// The same phrase, when the advertiser's profile does NOT contain it, must
// still be scored normally — the abstention is conditional on an actual
// match against the advertiser's descriptors, not a blanket rule for
// certain phrases.
// Corruption this catches: abstaining on any GenderFit/IncomeTier citation
// unconditionally (or on any phrase in some hardcoded list) instead of
// actually checking the advertiser profile.
func TestAdvertiserTermNotAbstainedWhenAbsentFromProfile(t *testing.T) {
	e := entry("pub_014", "excluded",
		"Excluded due to category mismatch. Kitchenware and home goods do not overlap with the women and family category.",
		"GenderFit", 0.96)
	m := ReasonConsistency(campaignOf(e), nil) // no advertiser profile at all

	if got := m.Counts["advertiser_term"]; got != 0 {
		t.Fatalf("advertiser_term = %d, want 0 (nothing in an empty profile should match)", got)
	}
	if got := m.Counts["contradicted"]; got != 1 {
		t.Fatalf("contradicted = %d, want 1 (GenderFit=0.96 against excluded, with no advertiser-term ambiguity, is a real contradiction)", got)
	}
}

// A reason that echoes a gate identifier ("category_mismatch") even without
// matching gateReason's exact prose is skipped like any other
// code-generated reason. Verbatim shape of the real brief 8 / pub_014
// reason.
// Corruption this catches: only checking pipeline.GeneratedReason's exact
// strings and not the raw gate-identifier substrings, which would measure
// this as if it were the model's own reasoning.
func TestGateIdentifierSubstringSkipped(t *testing.T) {
	e := entry("pub_014", "excluded", "Excluded due to hard gate: category_mismatch.", "Category", 0.05)
	m := ReasonConsistency(campaignOf(e), nil)

	if got := m.Counts["skipped_generated"]; got != 1 {
		t.Fatalf("skipped_generated = %d, want 1", got)
	}
	if got := m.Counts["citations"]; got != 0 {
		t.Fatalf("citations = %d, want 0", got)
	}
}

// coverage_rate must be scored citations / all citations found — including
// unscored, concessive, and advertiser_term citations in the denominator,
// but only consistent+contradicted+weak in the numerator.
// Corruption this catches: computing coverage_rate over entries, or over
// only scored+unscored (omitting the new abstention counts) instead of the
// full citation count.
func TestCoverageRateDenominator(t *testing.T) {
	profile := model.AdvertiserProfile{PriceTier: "luxury"}

	concessiveCase := entry("pub_A", "excluded",
		"Tech-adjacent activewear and shoe publisher; despite good age overlap, "+
			"the apparel category does not fit wellness services.",
		"AgeOverlap", 1.0) // -> Category consistent, AgeOverlap concessive

	advertiserTermCase := entry("pub_B", "excluded",
		"Category mismatch: organic grocery and pantry items do not match luxury leather accessories.",
		"IncomeTier", 1.0) // -> Category consistent, IncomeTier advertiser_term

	contradictionCase := entry("pub_C", "excluded", "the audience skews too old for our brand", "AgeOverlap", 1.0) // -> AgeOverlap contradicted

	consideredCase := entry("pub_D", "considered", "audience skews a bit older than ideal", "AgeOverlap", 0.9) // -> AgeOverlap unscored

	m := ReasonConsistency(campaignWithProfile(profile,
		concessiveCase, advertiserTermCase, contradictionCase, consideredCase), nil)

	if got := m.Counts["citations"]; got != 6 {
		t.Fatalf("citations = %d, want 6", got)
	}
	if got := m.Counts["consistent"] + m.Counts["contradicted"] + m.Counts["weak"]; got != 3 {
		t.Fatalf("scored (consistent+contradicted+weak) = %d, want 3", got)
	}
	if got := m.Values["coverage_rate"]; got != 0.5 {
		t.Fatalf("coverage_rate = %v, want 0.5 (3 scored of 6 citations found)", got)
	}
}

// The exact reason from the coordinator's regression report: after the
// prompt stopped the model naming gate identifiers, this is what a
// wellness-services publisher's exclusion reads like in plain language. It
// names no meta-vocabulary word ("category", "demographic", ...) at all, so
// the hand-written phrase map alone finds nothing — the regression this fix
// repairs. The publisher's own Subcategories include "fitness_classes",
// which normalizes to the multi-word term "fitness classes" and appears in
// the reason verbatim.
// Corruption this catches: not deriving Category vocabulary from the
// publisher's catalog record at all (i.e. never augmenting past the static
// hand-written list), which would leave this reason vague.
func TestCategoryVocabularyDerivedFromCatalogCatchesPlainLanguageReason(t *testing.T) {
	cat := testCatalog(t)
	pub := testPublisher(t, cat, "pub_003") // wellness_services; subcategories include fitness_classes, spa, yoga, personal_training
	if pub.Category != "wellness_services" {
		t.Fatalf("test fixture assumption broken: pub_003 category = %q, want wellness_services", pub.Category)
	}

	e := entry("pub_003", "excluded",
		"Operates appointment-based fitness classes and spa services, which does not overlap with packaged beverage retail.",
		"Category", 0.05)
	m := ReasonConsistency(campaignOf(e), cat)

	if got := m.Counts["vague_reasons"]; got != 0 {
		t.Fatalf("vague_reasons = %d, want 0 (the catalog-derived \"fitness classes\" term should have been found)", got)
	}
	if got := m.Counts["citations"]; got != 1 {
		t.Fatalf("citations = %d, want 1", got)
	}
	if got := m.Counts["consistent"]; got != 1 {
		t.Fatalf("consistent = %d, want 1 (Category=0.05 against excluded is consistent)", got)
	}
}

// A publisher subcategory "pet_food" matches the phrase "pet food" in a
// reason, via the underscore-to-space normalization.
// Corruption this catches: comparing Subcategories against reason text
// without normalizing underscores to spaces first.
func TestSubcategoryPetFoodMatchesPetFoodPhrase(t *testing.T) {
	cat := testCatalog(t)
	pub := testPublisher(t, cat, "pub_007") // pet; subcategories include pet_food
	found := false
	for _, s := range pub.Subcategories {
		if s == "pet_food" {
			found = true
		}
	}
	if !found {
		t.Fatalf("test fixture assumption broken: pub_007 subcategories = %v, want pet_food among them", pub.Subcategories)
	}

	e := entry("pub_007", "recommended", "This pet food specialist is a strong match for the advertiser's brief.", "Category", 0.9)
	m := ReasonConsistency(campaignOf(e), cat)

	if got := m.Counts["citations"]; got != 1 {
		t.Fatalf("citations = %d, want 1", got)
	}
	if got := m.Counts["consistent"]; got != 1 {
		t.Fatalf("consistent = %d, want 1 (Category=0.9 against recommended is consistent)", got)
	}
}

// The normalization specifically, isolated from any confound: "pet food"
// above also contains the bare word "pet" (pub_007's own Category value),
// which would produce a citation on its own even if subcategory
// normalization were broken — so that test alone cannot prove
// normalization works. pub_014's subcategory "non_toxic" shares no prefix
// with its Category ("home"), so a reason naming only "non toxic" isolates
// the normalization step cleanly.
// Corruption this catches: deriving Category vocabulary from raw
// (underscored) Subcategories instead of normalizing them first — this
// specific reason has no other word that would produce a Category
// citation, so a bug here shows up as vague, not as a false pass.
func TestSubcategoryNormalizationIsolatedFromCategoryPrefixConfound(t *testing.T) {
	cat := testCatalog(t)
	pub := testPublisher(t, cat, "pub_014") // home; subcategories include non_toxic
	found := false
	for _, s := range pub.Subcategories {
		if s == "non_toxic" {
			found = true
		}
	}
	if !found {
		t.Fatalf("test fixture assumption broken: pub_014 subcategories = %v, want non_toxic among them", pub.Subcategories)
	}

	e := entry("pub_014", "recommended", "This non toxic housewares specialist is a strong match for the advertiser's brief.", "Category", 0.9)
	m := ReasonConsistency(campaignOf(e), cat)

	if got := m.Counts["citations"]; got != 1 {
		t.Fatalf("citations = %d, want 1 (\"non toxic\" should match the normalized \"non_toxic\" subcategory)", got)
	}
	if got := m.Counts["vague_reasons"]; got != 0 {
		t.Fatalf("vague_reasons = %d, want 0", got)
	}
}

// An explicit age range ("25-45") in a reason produces an AgeOverlap
// citation, via the new age-range regex.
// Corruption this catches: not adding the age-range regex to AgeOverlap's
// pattern list, which would leave a reason using only numbers (no keyword
// like "age" or "skew") vague.
func TestExplicitAgeRangeProducesAgeOverlapCitation(t *testing.T) {
	e := entry("pub_1", "excluded", "This publisher's readership is 25-45, a poor match for our brief.", "AgeOverlap", 1.0)
	m := ReasonConsistency(campaignOf(e), nil) // publisher-independent regex; no catalog needed

	if got := m.Counts["citations"]; got != 1 {
		t.Fatalf("citations = %d, want 1", got)
	}
	if got := m.Counts["contradicted"]; got != 1 {
		t.Fatalf("contradicted = %d, want 1 (AgeOverlap=1.00 against excluded is a contradiction)", got)
	}
}

// A single-word subcategory that collides with a GenderFit keyword
// ("women") must NOT produce a Category citation: GenderFit's own
// hand-written list already claims that word (ambiguity rule 3).
// Corruption this catches: adding every subcategory to Category regardless
// of collisions, which would double-count "women" as both a Category and a
// GenderFit citation from the same word.
func TestSingleWordSubcategoryCollisionDoesNotProduceCategoryCitation(t *testing.T) {
	cat := testCatalog(t)
	pub := testPublisher(t, cat, "pub_002") // apparel; subcategories include the single word "women"
	found := false
	for _, s := range pub.Subcategories {
		if s == "women" {
			found = true
		}
	}
	if !found {
		t.Fatalf("test fixture assumption broken: pub_002 subcategories = %v, want \"women\" among them", pub.Subcategories)
	}

	e := entry("pub_002", "excluded", "This audience is predominantly women, which doesn't match our brief.", "GenderFit", 0.9)
	m := ReasonConsistency(campaignOf(e), cat)

	if got := m.Counts["citations"]; got != 1 {
		t.Fatalf("citations = %d, want 1 (GenderFit only — \"women\" must not also register as Category)", got)
	}
	if got := m.Counts["contradicted"]; got != 1 {
		t.Fatalf("contradicted = %d, want 1 (the GenderFit citation)", got)
	}
}

// A publisher's Category value always produces a Category citation
// (ambiguity rule 4), independent of any subcategory collision logic.
// Corruption this catches: only deriving Category vocabulary from
// Subcategories and never including the Category field itself.
func TestPublisherCategoryValueAlwaysProducesCategoryCitation(t *testing.T) {
	cat := testCatalog(t)
	pub := testPublisher(t, cat, "pub_020") // category = "beverages"
	if pub.Category != "beverages" {
		t.Fatalf("test fixture assumption broken: pub_020 category = %q, want beverages", pub.Category)
	}

	e := entry("pub_020", "excluded", "This beverages retailer doesn't fit the advertiser's brief at all.", "Category", 0.05)
	m := ReasonConsistency(campaignOf(e), cat)

	if got := m.Counts["citations"]; got != 1 {
		t.Fatalf("citations = %d, want 1", got)
	}
	if got := m.Counts["consistent"]; got != 1 {
		t.Fatalf("consistent = %d, want 1", got)
	}
}

// The advertiser_term abstention still takes precedence over a
// catalog-derived match: if the advertiser's own profile happens to name
// the same term the catalog derived for the publisher, the metric abstains
// rather than scoring it as an ordinary data-derived citation.
// Corruption this catches: checking the advertiser-term abstention only
// against hand-written-keyword citations, forgetting to apply it to
// catalog-derived ones too.
func TestAdvertiserTermAbstentionTakesPrecedenceOverCatalogDerivedMatch(t *testing.T) {
	cat := testCatalog(t)
	pub := testPublisher(t, cat, "pub_003") // category = "wellness_services"

	e := entry("pub_003", "excluded", "This wellness services publisher's model doesn't fit our brief.", "Category", 0.9)
	profile := model.AdvertiserProfile{PrimaryCategory: pub.Category} // advertiser is itself "wellness_services"
	m := ReasonConsistency(campaignWithProfile(profile, e), cat)

	if got := m.Counts["advertiser_term"]; got != 1 {
		t.Fatalf("advertiser_term = %d, want 1", got)
	}
	if got := m.Counts["contradicted"]; got != 0 {
		t.Fatalf("contradicted = %d, want 0 (must abstain, not score, when the catalog-derived term also names the advertiser)", got)
	}
}

// A genuinely empty reason ("not a fit") is still counted vague even with a
// real catalog publisher attached: the fix adds vocabulary, it doesn't turn
// every reason into a citation.
// Corruption this catches: over-eager augmentation that manufactures a
// citation from an unrelated publisher's data regardless of the actual
// reason text.
func TestGenuinelyEmptyReasonStillCountsVagueWithCatalog(t *testing.T) {
	cat := testCatalog(t)
	e := entry("pub_001", "excluded", "not a fit", "AgeOverlap", 1.0)
	m := ReasonConsistency(campaignOf(e), cat)

	if got := m.Counts["vague_reasons"]; got != 1 {
		t.Fatalf("vague_reasons = %d, want 1", got)
	}
	if got := m.Counts["citations"]; got != 0 {
		t.Fatalf("citations = %d, want 0", got)
	}
}
