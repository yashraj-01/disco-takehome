package measure

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/yashraj/disco/internal/catalog"
)

// stopwords is a small, modest English stopword list: articles, pronouns,
// prepositions, auxiliary verbs, and conjunctions. It deliberately excludes
// domain words like "delivered" or "premium" — those carry real meaning in
// ad copy and stripping them would throw away the signal this metric exists
// to measure. No dependency: this is the whole list.
var stopwords = map[string]bool{
	"a": true, "an": true, "the": true,
	"i": true, "me": true, "my": true, "we": true, "our": true, "ours": true,
	"you": true, "your": true, "yours": true,
	"he": true, "him": true, "his": true, "she": true, "her": true, "hers": true,
	"it": true, "its": true, "they": true, "them": true, "their": true, "theirs": true,
	"this": true, "that": true, "these": true, "those": true,
	"in": true, "on": true, "at": true, "by": true, "for": true, "with": true,
	"about": true, "against": true, "between": true, "into": true, "through": true,
	"during": true, "before": true, "after": true, "above": true, "below": true,
	"to": true, "from": true, "up": true, "down": true, "of": true, "off": true,
	"over": true, "under": true, "out": true,
	"is": true, "am": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "having": true,
	"do": true, "does": true, "did": true, "doing": true,
	"will": true, "would": true, "shall": true, "should": true,
	"can": true, "could": true, "may": true, "might": true, "must": true,
	"and": true, "but": true, "or": true, "nor": true, "so": true, "yet": true,
	"because": true, "as": true, "if": true, "than": true, "then": true,
	"not": true, "no": true, "there": true, "here": true,
	"what": true, "which": true, "who": true, "whom": true,
	"all": true, "each": true, "few": true, "more": true, "most": true,
	"other": true, "some": true, "such": true, "only": true, "own": true,
	"same": true, "too": true, "very": true, "just": true,
}

// tokenPattern extracts runs of letters and digits from already-lowercased
// text — the punctuation-stripping step. Apostrophes and every other mark
// are dropped along with whitespace, so "brand's" tokenizes to "brand" "s"
// (and "s" is a stopword, so it's discarded next).
var tokenPattern = regexp.MustCompile(`[a-z0-9]+`)

// contentTokens lowercases text, strips punctuation, and returns the
// resulting words as a set with stopwords removed.
func contentTokens(text string) map[string]bool {
	words := tokenPattern.FindAllString(strings.ToLower(text), -1)
	set := make(map[string]bool, len(words))
	for _, w := range words {
		if stopwords[w] {
			continue
		}
		set[w] = true
	}
	return set
}

// leverSet turns a creative's MessagingLevers into a comparable set:
// trimmed and lowercased so incidental whitespace or casing differences
// don't understate the overlap between two creatives that cited the same
// lever.
func leverSet(levers []string) map[string]bool {
	set := make(map[string]bool, len(levers))
	for _, l := range levers {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" {
			continue
		}
		set[l] = true
	}
	return set
}

// jaccardSimilarity is |A∩B| / |A∪B|. Two empty sets are defined as 0
// similarity rather than the undefined 0/0 — an empty creative shares
// nothing checkable with anything, which is the conservative reading here.
func jaccardSimilarity(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	return safeDivF(float64(inter), float64(union))
}

func safeDivF(n, d float64) float64 {
	if d == 0 {
		return 0
	}
	return n / d
}

// creativePair is one pairwise comparison within a campaign, kept around so
// the worst pairs across every campaign can be found and quoted verbatim.
type creativePair struct {
	briefN               int
	personaA, personaB   string
	headlineA, headlineB string
	copyOverlap          float64
	leverOverlap         float64
}

// findingDetail renders one pair as a quotable, self-checkable line: both
// persona IDs, the similarity, and both headlines verbatim, so a reader can
// judge for themselves whether the metric is right without re-deriving
// anything.
func (p creativePair) findingDetail() string {
	return fmt.Sprintf(
		"copy_overlap=%.3f lever_overlap=%.3f — %q vs %q",
		p.copyOverlap, p.leverOverlap, p.headlineA, p.headlineB)
}

// CreativeDistinctiveness measures whether the creatives the pipeline wrote
// for one campaign — one model call per shopper persona, each call seeing
// only that persona's record — actually come out distinct from each other.
// That per-persona split is the whole architecture of stage 5, justified by
// the claim that asking one call for several variants reliably produces
// several paraphrases of one idea instead of genuinely different angles.
// This metric is the first thing that tests that claim directly.
//
// Two independent signals are reported, deliberately not blended into one
// number:
//
//   - copy_overlap: pairwise Jaccard similarity of the content-word sets
//     drawn from Headline+Body — lowercased, punctuation stripped, a small
//     stopword list removed (see stopwords; domain words like "delivered"
//     or "premium" are kept, since they carry the meaning this metric
//     exists to catch or clear).
//   - lever_overlap: pairwise Jaccard similarity of MessagingLevers, the
//     set each creative cites from its own persona's record. Two creatives
//     citing the same levers are targeting the same motivation regardless
//     of how differently they phrase it.
//
// The two can disagree, and the disagreement is itself informative: similar
// levers with different words means the model is dressing up one idea in
// synonyms; different levers with similar copy means it found distinct
// angles but writes every one of them in the same monotone. Reporting only
// one signal — or worse, averaging them together — would erase exactly the
// case this metric is built to surface.
//
// Interpretation, read carefully before treating either number as an
// answer:
//
//   - High overlap (either signal) is strong evidence the per-persona split
//     is not earning its cost: the model produced near-duplicate output
//     from independent calls that were supposed to prevent exactly that.
//   - Low overlap is weaker evidence than it looks. It shows the copy
//     differs, not that each piece is right for its persona — two texts can
//     be entirely distinct from each other and both wrong for the audience
//     they were written for. Metric 5 (persona attribution) is what tests
//     targeting; this one only tests differentiation between creatives.
//   - Jaccard on content words is a crude instrument: synonyms ("save" vs
//     "discount") read as totally different tokens, while any shared proper
//     noun — the brand name, the product name — inflates similarity across
//     every single pair in a campaign, including pairs that are otherwise
//     unrelated. Expect a non-zero floor from that alone; a small positive
//     mean_copy_overlap is not on its own evidence of a problem.
//
// No pass/fail threshold is computed or reported. There is no baseline yet
// for what "too similar" looks like, and a threshold invented today would
// be a guess wearing the clothes of a standard. This metric reports the
// numbers; Summary says what was measured and what the worst pair was, not
// whether the campaign "passed".
//
// A campaign is skipped (and counted under skipped_too_few) when it has
// fewer than 2 creatives: a refusal produces none, and a single creative
// has no partner to compare against — dividing by zero pairs rather than
// skipping would either crash or silently report a meaningless 0.
func CreativeDistinctiveness(campaigns []Campaign, cat *catalog.Catalog) Metric {
	_ = cat // no catalog lookups needed: everything compared here already
	// lives on the creative (Headline, Body, MessagingLevers) or the
	// campaign it belongs to (BriefN). Kept as a parameter so this metric's
	// signature matches ReasonConsistency and ScoringAblation and plugs
	// into the same measure subcommand without a special case.

	var (
		campaignsMeasured, skippedTooFew, pairsCompared, totalCreatives int
		sumMeanCopy, sumMeanLever                                       float64
		allPairs                                                        []creativePair
	)

	for _, c := range campaigns {
		n := len(c.Campaign.Creatives)
		totalCreatives += n
		if n < 2 {
			skippedTooFew++
			continue
		}
		campaignsMeasured++

		tokenSets := make([]map[string]bool, n)
		leverSets := make([]map[string]bool, n)
		for i, cr := range c.Campaign.Creatives {
			tokenSets[i] = contentTokens(cr.Headline + " " + cr.Body)
			leverSets[i] = leverSet(cr.MessagingLevers)
		}

		var campaignCopySum, campaignLeverSum float64
		pairsInCampaign := 0
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				copyOverlap := jaccardSimilarity(tokenSets[i], tokenSets[j])
				leverOverlap := jaccardSimilarity(leverSets[i], leverSets[j])
				campaignCopySum += copyOverlap
				campaignLeverSum += leverOverlap
				pairsInCampaign++

				allPairs = append(allPairs, creativePair{
					briefN:       c.BriefN,
					personaA:     c.Campaign.Creatives[i].PersonaID,
					personaB:     c.Campaign.Creatives[j].PersonaID,
					headlineA:    c.Campaign.Creatives[i].Headline,
					headlineB:    c.Campaign.Creatives[j].Headline,
					copyOverlap:  copyOverlap,
					leverOverlap: leverOverlap,
				})
			}
		}
		pairsCompared += pairsInCampaign
		sumMeanCopy += campaignCopySum / float64(pairsInCampaign)
		sumMeanLever += campaignLeverSum / float64(pairsInCampaign)
	}

	meanCopy := safeDivF(sumMeanCopy, float64(campaignsMeasured))
	meanLever := safeDivF(sumMeanLever, float64(campaignsMeasured))

	// Sort a copy of allPairs by copy overlap descending (ties broken
	// deterministically by brief number then persona IDs) so the worst
	// pair — across ALL campaigns, not per-campaign — is allPairs[0], and
	// the top 5 findings are stable across runs on the same input.
	sorted := make([]creativePair, len(allPairs))
	copy(sorted, allPairs)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].copyOverlap != sorted[j].copyOverlap {
			return sorted[i].copyOverlap > sorted[j].copyOverlap
		}
		if sorted[i].briefN != sorted[j].briefN {
			return sorted[i].briefN < sorted[j].briefN
		}
		if sorted[i].personaA != sorted[j].personaA {
			return sorted[i].personaA < sorted[j].personaA
		}
		return sorted[i].personaB < sorted[j].personaB
	})

	var maxCopyOverlap float64
	var worst *creativePair
	if len(sorted) > 0 {
		maxCopyOverlap = sorted[0].copyOverlap
		worst = &sorted[0]
	}

	const maxFindings = 5
	findings := make([]Finding, 0, maxFindings)
	for i := 0; i < len(sorted) && i < maxFindings; i++ {
		p := sorted[i]
		findings = append(findings, Finding{
			BriefN:      p.briefN,
			PublisherID: fmt.Sprintf("%s vs %s", p.personaA, p.personaB),
			Detail:      p.findingDetail(),
		})
	}

	summary := fmt.Sprintf(
		"%d campaigns measured (%d skipped for fewer than 2 creatives, %d creatives total, %d pairs compared): "+
			"mean copy overlap %.3f, mean lever overlap %.3f.",
		campaignsMeasured, skippedTooFew, totalCreatives, pairsCompared, meanCopy, meanLever)
	if worst != nil {
		summary += fmt.Sprintf(
			" Worst pair: brief %d, %s vs %s, copy overlap %.3f (%q vs %q).",
			worst.briefN, worst.personaA, worst.personaB, worst.copyOverlap, worst.headlineA, worst.headlineB)
	}

	return Metric{
		Name:    "creative_distinctiveness",
		Summary: summary,
		Values: map[string]float64{
			"mean_copy_overlap":  meanCopy,
			"mean_lever_overlap": meanLever,
			"max_copy_overlap":   maxCopyOverlap,
			"campaigns_measured": float64(campaignsMeasured),
		},
		Counts: map[string]int{
			"campaigns":       len(campaigns),
			"skipped_too_few": skippedTooFew,
			"pairs_compared":  pairsCompared,
			"creatives":       totalCreatives,
		},
		Findings: findings,
	}
}
