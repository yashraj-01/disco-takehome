package measure

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
	"github.com/yashraj/disco/internal/pipeline"
)

// Campaign pairs a brief number with the campaign it produced, so findings
// can cite which brief they came from. Defined locally (rather than reusing
// internal/eval.Result) so this package has no dependency on eval — it
// measures the same artifact eval gates, but the two are independent
// concerns and neither should require the other to compile or change.
type Campaign struct {
	BriefN   int
	Campaign model.Campaign
}

// Classification bands for a single (entry, sub-score) citation.
const (
	bandConsistent   = "consistent"
	bandContradicted = "contradicted"
	bandWeak         = "weak"
	bandUnscored     = "unscored"
)

// Thresholds. Design decision: polarity comes from LedgerEntry.Verdict, not
// from any sentiment analysis of the reason text.
//
//   - excluded:    cited sub-score <= excludedConsistentMax   -> consistent
//     cited sub-score >= excludedContradictedMin -> contradicted
//     otherwise                                   -> weak
//   - recommended: cited sub-score >= recommendedConsistentMin   -> consistent
//     cited sub-score <= recommendedContradictedMax -> contradicted
//     otherwise                                      -> weak
//   - considered:  ambiguous polarity; citation is counted but classified
//     unscored, and excluded from the rate.
const (
	excludedConsistentMax      = 0.50
	excludedContradictedMin    = 0.80
	recommendedConsistentMin   = 0.50
	recommendedContradictedMax = 0.20
)

// detailTruncateLen bounds how much of a reason a Finding quotes.
const detailTruncateLen = 140

// gateIdentifiers are the raw hard-gate string identifiers (as opposed to
// gateReason's full prose). A model that writes "Excluded due to hard gate:
// category_mismatch." is parroting our own machinery, not reasoning in its
// own words, even though that exact sentence never comes from gateReason
// itself — so it is skipped like any other generated reason. Reusing
// pipeline's exported gate constants (rather than retyping the strings)
// keeps this list in sync with pipeline automatically.
var gateIdentifiers = []string{
	pipeline.GateNotConsumerDTC,
	pipeline.GateCategoryMismatch,
	pipeline.GateDemographicMismatch,
}

// isGeneratedReason reports whether reason is code-written rather than
// model-written: either an exact match on one of gateReason's fixed
// strings (delegated to pipeline.GeneratedReason so the two can never drift
// apart), or a reason that merely echoes one of our internal gate
// identifiers.
func isGeneratedReason(reason string) bool {
	if pipeline.GeneratedReason(reason) {
		return true
	}
	lower := strings.ToLower(reason)
	for _, g := range gateIdentifiers {
		if g != "" && strings.Contains(lower, g) {
			return true
		}
	}
	return false
}

// subScorePhrases maps each sub-score to the phrases that count as citing
// it, checked case-insensitively.
//
// Convention for anyone adding a phrase: matching is regexp, anchored to a
// leading word boundary only (`\b` + the literal phrase) — not a bare
// substring, and not anchored on the trailing end. A leading boundary alone
// is enough to stop "age" matching inside "beverages" or "package", and
// "her"/"male" matching inside "leather" or "female", while still letting a
// stem like "spend" match "spending" and "sustainab" match "sustainability".
// Patterns are compiled once, at package init, not per call.
//
// Order within a category no longer affects correctness (the boundary
// anchor disambiguates "women" from "men" on its own), but the first match
// in table order is still what a Finding quotes, so list the more specific
// or more informative phrase first where it's a toss-up.
type subScoreCategory struct {
	Name     string
	Patterns []phrasePattern
}

// phrasePattern is one phrase and its compiled, word-boundary-anchored
// regexp.
type phrasePattern struct {
	Phrase string
	re     *regexp.Regexp
}

// compilePatterns compiles each phrase as `\b` + the literal phrase, so the
// match requires a word boundary immediately before it but not after.
func compilePatterns(phrases []string) []phrasePattern {
	out := make([]phrasePattern, len(phrases))
	for i, p := range phrases {
		out[i] = phrasePattern{Phrase: p, re: regexp.MustCompile(`\b` + regexp.QuoteMeta(p))}
	}
	return out
}

// dollarAmountPattern flags an explicit dollar figure ("$198"), one of two
// publisher-independent regex additions to the hand-written keyword lists
// (see the FIX-4 doc comment on augmentedCategories). It deliberately has no
// leading \b: "$" is not a word character, so a boundary assertion right
// before it would require the *preceding* character to be a word character
// too, which is never true in practice (a dollar figure is always preceded
// by whitespace or punctuation) — the anchor would silently never fire.
var dollarAmountPattern = phrasePattern{Phrase: "<dollar-amount>", re: regexp.MustCompile(`\$\d+`)}

// ageRangePattern flags an explicit age range ("25-45", "50–70": two 2-digit
// numbers separated by a hyphen or en dash). Leading-boundary anchored like
// everything else, so it doesn't fire in the middle of a longer digit run.
var ageRangePattern = phrasePattern{Phrase: "<age-range>", re: regexp.MustCompile(`\b\d{2}[-–]\d{2}`)}

var subScorePhrases = []subScoreCategory{
	{"Category", compilePatterns([]string{"category", "unrelated", "adjacent", "vertical", "product type", "assortment", "different space", "not a fit for"})},
	{"AOVAlignment", append(compilePatterns([]string{"order value", "aov", "price point", "basket", "spend", "cheaper", "expensive", "affordab", "premium pricing", "far less than", "far more than"}), dollarAmountPattern)},
	{"AgeOverlap", append(compilePatterns([]string{"age", "older", "younger", "skew", "demographic", "generation", "millennial", "gen z", "mid-life", "retire"}), ageRangePattern)},
	{"ValuesMatch", compilePatterns([]string{"sustainab", "values", "eco", "ethical", "craftsman", "heritage", "clean-ingredient", "transparen", "science-backed", "vet-recommend", "greenwash"})},
	{"GenderFit", compilePatterns([]string{"women", "female", "men", "male", "gender", "she", "her"})},
	{"IncomeTier", compilePatterns([]string{"income", "affluent", "wealthy", "disposable", "high-end", "mid-market", "budget-conscious", "luxury"})},
}

// nonCategoryKeywords is the set of every hand-written phrase configured for
// a sub-score OTHER than Category. It backs ambiguity rule 3 in
// augmentedCategories: a publisher subcategory of "women" must not also
// register as a Category citation, because GenderFit's keyword list already
// claims that exact word.
var nonCategoryKeywords = func() map[string]bool {
	set := map[string]bool{}
	for _, cat := range subScorePhrases {
		if cat.Name == "Category" {
			continue
		}
		for _, p := range cat.Patterns {
			set[p.Phrase] = true
		}
	}
	return set
}()

// firstMatch returns the first phrasePattern (in table order) for category
// whose word-boundary-anchored pattern matches reasonLower, the byte offsets
// of that match, and whether one was found.
func firstMatch(category subScoreCategory, reasonLower string) (p phrasePattern, start, end int, ok bool) {
	for _, p := range category.Patterns {
		if loc := p.re.FindStringIndex(reasonLower); loc != nil {
			return p, loc[0], loc[1], true
		}
	}
	return phrasePattern{}, -1, -1, false
}

// Abstention reasons: the citation was found, but the metric declines to
// score its polarity because doing so would require guessing rather than
// reading arithmetic. See ReasonConsistency's doc comment: precision is
// prioritized over recall, so an ambiguous citation is classified and
// excluded from the rate rather than scored either way.
const (
	abstainConcessive     = "concessive"
	abstainAdvertiserTerm = "advertiser_term"
)

// concessivePatterns flag a concessive construction: the model conceding a
// signal ("despite good age overlap") while excluding — or recommending —
// for a different reason entirely. Same leading-word-boundary convention as
// subScorePhrases.
var concessivePatterns = compilePatterns([]string{
	"despite", "although", "even though", "though", "while", "whilst",
	"notwithstanding", "in spite of", "granted", "admittedly",
	"aside from", "apart from", "setting aside",
})

func containsConcessiveMarker(text string) bool {
	for _, p := range concessivePatterns {
		if p.re.MatchString(text) {
			return true
		}
	}
	return false
}

// clausePrefix returns the portion of reasonLower from the start of the
// clause containing byte offset pos up to pos itself. A clause is delimited
// by ',', ';', or '.', so a concessive marker elsewhere in a long reason —
// in a different clause — does not suppress an unrelated citation: only a
// marker in the same clause, at or before the citation, does.
func clausePrefix(reasonLower string, pos int) string {
	start := 0
	for i := 0; i < pos && i < len(reasonLower); i++ {
		switch reasonLower[i] {
		case ',', ';', '.':
			start = i + 1
		}
	}
	if start > pos {
		start = pos
	}
	return reasonLower[start:pos]
}

// advertiserDescriptorText concatenates the advertiser's own descriptor
// fields into one lowercased string with underscores normalized to spaces
// (so "pet_food" can match the phrase "pet food"). A citation phrase that
// also appears here is ambiguous between describing the publisher's
// audience and describing the advertiser's own product — e.g. "luxury" in
// "...do not match luxury leather accessories" names the advertiser's
// product, not the publisher's IncomeTier — so it is abstained on rather
// than scored as if it plainly described the publisher.
func advertiserDescriptorText(p model.AdvertiserProfile) string {
	parts := make([]string, 0, 3+len(p.Subcategories)+len(p.Values))
	parts = append(parts, p.PrimaryCategory, p.PriceTier, p.BusinessModel)
	parts = append(parts, p.Subcategories...)
	parts = append(parts, p.Values...)
	text := strings.ToLower(strings.Join(parts, " "))
	return strings.ReplaceAll(text, "_", " ")
}

// citation is one (entry, sub-score) pair extracted from a reason. Abstain
// is empty for a normally-scored citation, or one of the abstain* constants
// when the metric declined to score it. MatchedOn is the actual matched
// excerpt from the reason (not a static label), so a regex-derived citation
// (an age range, a dollar figure) reports what it really found.
type citation struct {
	SubScore  string
	MatchedOn string
	Value     float64
	Abstain   string
}

// normalizeUnderscores lowercases s and turns underscores into spaces, so a
// catalog value like "pet_food" can match the phrase "pet food".
func normalizeUnderscores(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "_", " ")
}

// categoryDynamicPatterns derives Category vocabulary from pub's own
// catalog record: its Category value always counts (ambiguity rule 4 — the
// ten category values don't collide with any other sub-score's keywords),
// and each Subcategory counts if it's a multi-word term (rule 2, a strong
// signal on its own) or a single-word term that no OTHER sub-score's
// keyword list already claims (rule 3 — e.g. a subcategory of "women" is
// GenderFit's word, not Category's, so it is left for GenderFit to match).
func categoryDynamicPatterns(pub *catalog.Publisher) []phrasePattern {
	terms := []string{normalizeUnderscores(pub.Category)}
	for _, sub := range pub.Subcategories {
		term := normalizeUnderscores(sub)
		if strings.Contains(term, " ") || !nonCategoryKeywords[term] {
			terms = append(terms, term)
		}
	}
	return compilePatterns(terms)
}

// keywordDynamicPatterns derives ValuesMatch vocabulary from the
// publisher's own precomputed Keywords (the value vocabulary catalog.Load
// already derives from that publisher's notes and subcategories).
func keywordDynamicPatterns(pub *catalog.Publisher) []phrasePattern {
	terms := make([]string, len(pub.Keywords))
	for i, k := range pub.Keywords {
		terms[i] = normalizeUnderscores(k)
	}
	return compilePatterns(terms)
}

// incomeDynamicPatterns derives IncomeTier vocabulary from the publisher's
// own audience income tier ("mid-high", "high", "mid", ...).
func incomeDynamicPatterns(pub *catalog.Publisher) []phrasePattern {
	if pub.Audience.IncomeTier == "" {
		return nil
	}
	return compilePatterns([]string{strings.ToLower(pub.Audience.IncomeTier)})
}

// ageSkewDynamicPatterns derives an additional AgeOverlap term from the
// publisher's own literal audience age-skew string, alongside the general
// age-range regex already in subScorePhrases.
func ageSkewDynamicPatterns(pub *catalog.Publisher) []phrasePattern {
	if pub.Audience.AgeSkew == "" {
		return nil
	}
	return compilePatterns([]string{strings.ToLower(pub.Audience.AgeSkew)})
}

// augmentedCategories returns subScorePhrases augmented with vocabulary
// derived from pub's own catalog record — the same principle behind the
// advertiser-term abstention: prefer data the pipeline already holds over a
// hand-maintained word list. pub may be nil (no catalog available, as in a
// unit test with a synthetic publisher ID), in which case the static table
// alone is returned unchanged.
//
// GenderFit gets no augmentation: the catalog has no vocabulary for it
// beyond what the hand-written keywords already cover.
func augmentedCategories(pub *catalog.Publisher) []subScoreCategory {
	if pub == nil {
		return subScorePhrases
	}
	out := make([]subScoreCategory, len(subScorePhrases))
	for i, cat := range subScorePhrases {
		var extra []phrasePattern
		switch cat.Name {
		case "Category":
			extra = categoryDynamicPatterns(pub)
		case "ValuesMatch":
			extra = keywordDynamicPatterns(pub)
		case "IncomeTier":
			extra = incomeDynamicPatterns(pub)
		case "AgeOverlap":
			extra = ageSkewDynamicPatterns(pub)
		}
		if len(extra) == 0 {
			out[i] = cat
			continue
		}
		patterns := make([]phrasePattern, 0, len(cat.Patterns)+len(extra))
		patterns = append(patterns, cat.Patterns...)
		patterns = append(patterns, extra...)
		out[i] = subScoreCategory{Name: cat.Name, Patterns: patterns}
	}
	return out
}

// citedSubScores returns every sub-score category cited by reasonLower
// (already lowercased), in table order, each at most once. advertiserText
// is the advertiser's own descriptor text (see advertiserDescriptorText),
// used to detect and abstain on FIX-2-style ambiguity. categories is the
// (possibly per-publisher-augmented) pattern table to scan — see
// augmentedCategories.
func citedSubScores(reasonLower string, sub model.SubScores, advertiserText string, categories []subScoreCategory) []citation {
	var out []citation
	for _, cat := range categories {
		p, start, end, ok := firstMatch(cat, reasonLower)
		if !ok {
			continue
		}
		cit := citation{SubScore: cat.Name, MatchedOn: reasonLower[start:end], Value: subScoreValue(sub, cat.Name)}
		switch {
		case containsConcessiveMarker(clausePrefix(reasonLower, start)):
			cit.Abstain = abstainConcessive
		case advertiserText != "" && p.re.MatchString(advertiserText):
			cit.Abstain = abstainAdvertiserTerm
		}
		out = append(out, cit)
	}
	return out
}

func subScoreValue(s model.SubScores, name string) float64 {
	switch name {
	case "Category":
		return s.Category
	case "AOVAlignment":
		return s.AOVAlignment
	case "AgeOverlap":
		return s.AgeOverlap
	case "ValuesMatch":
		return s.ValuesMatch
	case "GenderFit":
		return s.GenderFit
	case "IncomeTier":
		return s.IncomeTier
	default:
		return 0
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ReasonConsistency asks not "is this reason good?" but "is this reason
// true?" — for every publisher-ledger entry with a checkable citation, it
// compares the polarity implied by LedgerEntry.Verdict against the
// pipeline's own sub-score for the signal the reason names, using no
// labels, no judge, and no invented ground truth: the truth is arithmetic
// the pipeline already computed.
//
// It is a heuristic detector with high precision and unknown recall, and it
// is built to keep that precision even at recall's expense: wherever it
// cannot read a citation's polarity or referent confidently, it abstains —
// classifies the citation and excludes it from the rate — rather than
// guessing. Two abstention modes exist for exactly this reason: a
// concessive construction ("despite good age overlap, the category doesn't
// fit") states a polarity opposite the verdict's default assumption, and
// guessing which way to score it would be worse than not scoring it at all;
// a phrase that also appears in the advertiser's own descriptor fields
// (e.g. "luxury" naming the advertiser's product, not the publisher's
// income tier) is ambiguous about which side of the match it describes.
// Both are counted and reported (see the concessive and advertiser_term
// counts) so the abstentions stay visible rather than quietly shrinking the
// denominator. coverage_rate says how much of the found evidence the
// metric could actually adjudicate.
//
// A contradiction this metric does report is real. A reason it counts as
// vague may simply use words the phrase map does not contain — see the
// package doc comment.
//
// Phrase matching is word-boundary anchored (see subScorePhrases), not bare
// substring matching, so it does not fire on "age" inside "beverages" or
// "her" inside "leather".
//
// The vocabulary a citation is checked against is not a fixed list alone: it
// is derived per (entry, publisher) from that publisher's own catalog
// record (see augmentedCategories) and unioned with the hand-written
// phrases. A fixed list goes stale the moment the model's phrasing improves
// — when a prompt change stopped the model naming gate identifiers like
// "category_mismatch" and it started describing publishers in plain
// language instead ("Sells workout apparel and activewear subscriptions"),
// a fixed meta-vocabulary list went blind to that plain language and
// vagueness_rate spiked, penalizing the model for getting better. cat may
// be nil (as in a unit test with a synthetic publisher ID not in any real
// catalog), in which case matching falls back to the hand-written lists
// alone.
func ReasonConsistency(campaigns []Campaign, cat *catalog.Catalog) Metric {
	var (
		entries, skippedGenerated                           int
		citations, consistent, contradicted, weak, unscored int
		concessive, advertiserTerm, vague                   int
		findings                                            []Finding
	)

	for _, c := range campaigns {
		advertiserText := advertiserDescriptorText(c.Campaign.Advertiser.DerivedProfile)

		for _, e := range c.Campaign.PublisherLedger {
			entries++

			reason := strings.TrimSpace(e.Reason)
			if isGeneratedReason(reason) {
				skippedGenerated++
				continue
			}

			var pub *catalog.Publisher
			if cat != nil {
				pub, _ = cat.Publisher(e.PublisherID)
			}
			categories := augmentedCategories(pub)

			cited := citedSubScores(strings.ToLower(reason), e.SubScores, advertiserText, categories)
			if len(cited) == 0 {
				vague++
				continue
			}

			for _, cit := range cited {
				citations++
				switch cit.Abstain {
				case abstainConcessive:
					concessive++
					continue
				case abstainAdvertiserTerm:
					advertiserTerm++
					continue
				}
				switch e.Verdict {
				case "excluded":
					switch {
					case cit.Value <= excludedConsistentMax:
						consistent++
					case cit.Value >= excludedContradictedMin:
						contradicted++
						findings = append(findings, contradictionFinding(c.BriefN, e, cit, reason))
					default:
						weak++
					}
				case "recommended":
					switch {
					case cit.Value >= recommendedConsistentMin:
						consistent++
					case cit.Value <= recommendedContradictedMax:
						contradicted++
						findings = append(findings, contradictionFinding(c.BriefN, e, cit, reason))
					default:
						weak++
					}
				default: // "considered", or anything unrecognized: polarity is ambiguous.
					unscored++
				}
			}
		}
	}

	scored := consistent + contradicted + weak
	checkable := entries - skippedGenerated

	m := Metric{
		Name: "reason_consistency",
		Values: map[string]float64{
			"consistency_rate":   safeDiv(consistent, scored),
			"contradiction_rate": safeDiv(contradicted, scored),
			"vagueness_rate":     safeDiv(vague, checkable),
			"coverage_rate":      safeDiv(scored, citations),
		},
		Counts: map[string]int{
			"entries":           entries,
			"skipped_generated": skippedGenerated,
			"citations":         citations,
			"consistent":        consistent,
			"contradicted":      contradicted,
			"weak":              weak,
			"unscored":          unscored,
			"concessive":        concessive,
			"advertiser_term":   advertiserTerm,
			"vague_reasons":     vague,
		},
		Findings: findings,
	}
	m.Summary = fmt.Sprintf(
		"%d citations from %d ledger entries (%d skipped as code-generated gate reasons): "+
			"%.1f%% consistent, %.1f%% contradicted, %.1f%% weak, %d unscored (considered verdict), "+
			"%d abstained as concessive constructions, %d abstained as advertiser-described terms "+
			"(coverage_rate=%.3f: scored citations / all citations found — abstention is deliberate, "+
			"precision is prioritized over recall). "+
			"%.1f%% of checkable reasons cited nothing in the phrase map (vague) — checked against "+
			"the hand-written phrases UNIONED with vocabulary derived per publisher from the catalog "+
			"(its Category, Subcategories, Keywords, and Audience fields), not a fixed list alone, so "+
			"the metric doesn't go blind the moment the model's phrasing improves.",
		citations, entries, skippedGenerated,
		pct(consistent, scored), pct(contradicted, scored), pct(weak, scored), unscored,
		concessive, advertiserTerm, safeDiv(scored, citations),
		pct(vague, checkable))
	return m
}

func contradictionFinding(briefN int, e model.LedgerEntry, cit citation, reason string) Finding {
	return Finding{
		BriefN:      briefN,
		PublisherID: e.PublisherID,
		Detail: fmt.Sprintf(
			"verdict=%s cited %s=%.2f (matched %q) — %q",
			e.Verdict, cit.SubScore, cit.Value, cit.MatchedOn, truncate(reason, detailTruncateLen)),
	}
}

func safeDiv(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func pct(n, d int) float64 {
	return safeDiv(n, d) * 100
}
