package measure

import (
	"fmt"
	"regexp"
	"strings"

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

// isGeneratedReason reports whether reason is one of the fixed strings the
// pipeline's own code writes for a hard-gated publisher, as opposed to text
// a model wrote. It delegates to pipeline.GeneratedReason rather than
// hand-duplicating gateReason's strings here, so the two can never drift
// apart.
func isGeneratedReason(reason string) bool {
	return pipeline.GeneratedReason(reason)
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

var subScorePhrases = []subScoreCategory{
	{"Category", compilePatterns([]string{"category", "unrelated", "adjacent", "vertical", "product type", "assortment", "different space", "not a fit for"})},
	{"AOVAlignment", compilePatterns([]string{"order value", "aov", "price point", "basket", "spend", "cheaper", "expensive", "affordab", "premium pricing", "far less than", "far more than"})},
	{"AgeOverlap", compilePatterns([]string{"age", "older", "younger", "skew", "demographic", "generation", "millennial", "gen z", "mid-life", "retire"})},
	{"ValuesMatch", compilePatterns([]string{"sustainab", "values", "eco", "ethical", "craftsman", "heritage", "clean-ingredient", "transparen", "science-backed", "vet-recommend", "greenwash"})},
	{"GenderFit", compilePatterns([]string{"women", "female", "men", "male", "gender", "she", "her"})},
	{"IncomeTier", compilePatterns([]string{"income", "affluent", "wealthy", "disposable", "high-end", "mid-market", "budget-conscious", "luxury"})},
}

// firstMatch returns the first phrase (in table order) for category whose
// word-boundary-anchored pattern matches reasonLower, and whether one was
// found.
func firstMatch(category subScoreCategory, reasonLower string) (string, bool) {
	for _, p := range category.Patterns {
		if p.re.MatchString(reasonLower) {
			return p.Phrase, true
		}
	}
	return "", false
}

// citation is one (entry, sub-score) pair extracted from a reason.
type citation struct {
	SubScore  string
	MatchedOn string
	Value     float64
}

// citedSubScores returns every sub-score category cited by reasonLower
// (already lowercased), in table order, each at most once.
func citedSubScores(reasonLower string, sub model.SubScores) []citation {
	var out []citation
	for _, cat := range subScorePhrases {
		phrase, ok := firstMatch(cat, reasonLower)
		if !ok {
			continue
		}
		out = append(out, citation{SubScore: cat.Name, MatchedOn: phrase, Value: subScoreValue(sub, cat.Name)})
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
// It is a heuristic detector with high precision and unknown recall. A
// contradiction it reports is real. A reason it counts as vague may simply
// use words the phrase map does not contain — see the package doc comment.
//
// Phrase matching is word-boundary anchored (see subScorePhrases), not bare
// substring matching, so it does not fire on "age" inside "beverages" or
// "her" inside "leather".
func ReasonConsistency(campaigns []Campaign) Metric {
	var (
		entries, skippedGenerated                           int
		citations, consistent, contradicted, weak, unscored int
		vague                                               int
		findings                                            []Finding
	)

	for _, c := range campaigns {
		for _, e := range c.Campaign.PublisherLedger {
			entries++

			reason := strings.TrimSpace(e.Reason)
			if isGeneratedReason(reason) {
				skippedGenerated++
				continue
			}

			cited := citedSubScores(strings.ToLower(reason), e.SubScores)
			if len(cited) == 0 {
				vague++
				continue
			}

			for _, cit := range cited {
				citations++
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
		},
		Counts: map[string]int{
			"entries":           entries,
			"skipped_generated": skippedGenerated,
			"citations":         citations,
			"consistent":        consistent,
			"contradicted":      contradicted,
			"weak":              weak,
			"unscored":          unscored,
			"vague_reasons":     vague,
		},
		Findings: findings,
	}
	m.Summary = fmt.Sprintf(
		"%d citations from %d ledger entries (%d skipped as code-generated gate reasons): "+
			"%.1f%% consistent, %.1f%% contradicted, %.1f%% weak, %d unscored (considered verdict). "+
			"%.1f%% of checkable reasons cited nothing in the phrase map (vague).",
		citations, entries, skippedGenerated,
		pct(consistent, scored), pct(contradicted, scored), pct(weak, scored), unscored,
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
