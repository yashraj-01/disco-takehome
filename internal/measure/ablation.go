package measure

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
	"github.com/yashraj/disco/internal/pipeline"
)

// subScoreNames lists the six sub-scores in the order their weights are
// stated in pipeline/scoring.go.
var subScoreNames = []string{
	"Category", "AOVAlignment", "AgeOverlap", "ValuesMatch", "GenderFit", "IncomeTier",
}

// Ablation weights. These must track wCategory..wIncome in
// pipeline/scoring.go (they sum to 1.0 there too). They are re-declared here
// rather than imported because pipeline does not export them, and this
// package is not allowed to modify pipeline to export them — see the
// ScoringAblation doc comment for why recomputing locally, from the Sub
// scores pipeline.ScoreAll already returns, is the intended design rather
// than a workaround.
const (
	ablationWCategory = 0.30
	ablationWAOV      = 0.20
	ablationWAge      = 0.15
	ablationWValues   = 0.15
	ablationWGender   = 0.10
	ablationWIncome   = 0.10
)

// ablatedTotal recomputes the weighted sum of sub with one sub-score's
// weight zeroed (omit names which). It deliberately does not renormalize
// the remaining five weights back up to 1.0 — see ScoringAblation's doc
// comment.
func ablatedTotal(sub model.SubScores, omit string) float64 {
	var total float64
	if omit != "Category" {
		total += ablationWCategory * sub.Category
	}
	if omit != "AOVAlignment" {
		total += ablationWAOV * sub.AOVAlignment
	}
	if omit != "AgeOverlap" {
		total += ablationWAge * sub.AgeOverlap
	}
	if omit != "ValuesMatch" {
		total += ablationWValues * sub.ValuesMatch
	}
	if omit != "GenderFit" {
		total += ablationWGender * sub.GenderFit
	}
	if omit != "IncomeTier" {
		total += ablationWIncome * sub.IncomeTier
	}
	return total
}

// ungatedIDs returns the publisher scores with an empty HardGate: a gated
// publisher scores exactly 0 regardless of any weight (see scoreOne in
// pipeline/scoring.go, which computes Sub before checking the gate but
// returns before computing Score), so it can never move and is excluded
// from every ranking compared by this file.
func ungatedIDs(scores []model.PublisherScore) []model.PublisherScore {
	out := make([]model.PublisherScore, 0, len(scores))
	for _, s := range scores {
		if s.HardGate == "" {
			out = append(out, s)
		}
	}
	return out
}

// rankByScore orders ungated (a copy is sorted; the input is left
// untouched) by scoreOf descending, breaking ties by publisher ID ascending
// so equal scores always produce the same order — see design decision 5 in
// the ScoringAblation doc comment.
func rankByScore(ungated []model.PublisherScore, scoreOf func(model.PublisherScore) float64) []string {
	sorted := make([]model.PublisherScore, len(ungated))
	copy(sorted, ungated)
	sort.Slice(sorted, func(i, j int) bool {
		si, sj := scoreOf(sorted[i]), scoreOf(sorted[j])
		if si != sj {
			return si > sj
		}
		return sorted[i].PublisherID < sorted[j].PublisherID
	})
	ids := make([]string, len(sorted))
	for i, s := range sorted {
		ids[i] = s.PublisherID
	}
	return ids
}

// rankIndex maps each id to its 1-indexed position in ids.
func rankIndex(ids []string) map[string]int {
	m := make(map[string]int, len(ids))
	for i, id := range ids {
		m[id] = i + 1
	}
	return m
}

// top5Set returns the first (up to) 5 ids as a set.
func top5Set(ids []string) map[string]bool {
	n := len(ids)
	if n > 5 {
		n = 5
	}
	set := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		set[ids[i]] = true
	}
	return set
}

func setsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// setDiffSorted returns the ids in a but not in b, sorted, so a Finding's
// wording is deterministic across runs.
func setDiffSorted(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// scoringAblationAcc accumulates the ablation comparison for one sub-score
// across every brief measured.
type scoringAblationAcc struct {
	top5Changed  int
	rankShiftSum float64 // sum, across briefs, of that brief's mean |rank shift|

	haveExample    bool
	exampleBriefN  int
	exampleEntered []string
	exampleLeft    []string
}

// ablateBrief compares one brief's full ranking of ungated publishers
// (scores, exactly as pipeline.ScoreAll produced them) against six ablated
// rankings, one per sub-score, each obtained by zeroing that sub-score's
// weight and recomputing the total locally from Sub (ablatedTotal). It
// folds the comparison into acc, keyed by sub-score name, and returns how
// many publishers were ungated on this brief.
//
// This function is pure over []model.PublisherScore — it calls neither
// pipeline nor catalog — so the comparison math is directly testable with
// hand-built fixtures, independent of whatever profile or catalog produced
// the scores.
func ablateBrief(briefN int, scores []model.PublisherScore, acc map[string]*scoringAblationAcc) int {
	ungated := ungatedIDs(scores)

	fullOrder := rankByScore(ungated, func(s model.PublisherScore) float64 { return s.Score })
	fullRank := rankIndex(fullOrder)
	fullTop5 := top5Set(fullOrder)

	for _, name := range subScoreNames {
		ablatedOrder := rankByScore(ungated, func(s model.PublisherScore) float64 { return ablatedTotal(s.Sub, name) })
		ablatedRank := rankIndex(ablatedOrder)
		ablatedTop5 := top5Set(ablatedOrder)

		a := acc[name]
		if !setsEqual(fullTop5, ablatedTop5) {
			a.top5Changed++
			if !a.haveExample {
				a.haveExample = true
				a.exampleBriefN = briefN
				a.exampleEntered = setDiffSorted(ablatedTop5, fullTop5)
				a.exampleLeft = setDiffSorted(fullTop5, ablatedTop5)
			}
		}

		if len(fullOrder) > 0 {
			var shiftSum float64
			for _, id := range fullOrder {
				shiftSum += math.Abs(float64(fullRank[id] - ablatedRank[id]))
			}
			a.rankShiftSum += shiftSum / float64(len(fullOrder))
		}
	}
	return len(ungated)
}

// ScoringAblation asks, for each of the six weighted sub-scores in
// pipeline/scoring.go, whether it is load-bearing for the publisher
// *ranking* a brief produces — whether zeroing its weight would ever change
// which publishers rank where — rather than whether it changes the
// absolute fit score. For each brief it takes the full ranking
// pipeline.ScoreAll already produced and, for each sub-score, recomputes
// every ungated publisher's total from the Sub values ScoreAll already
// computed (ablatedTotal), with that one sub-score's weight zeroed, then
// compares the resulting order against the unablated one.
//
// Design decision: rankings are compared directly, not renormalized scores.
// Renormalizing the remaining five weights back up to sum to 1.0 would
// divide every ungated publisher's ablated total by the same constant
// (1 minus the zeroed weight), which cannot change their relative order —
// so this deliberately does not renormalize; it would be extra arithmetic
// that changes nothing about the answer. Do not "fix" this by adding
// renormalization back in.
//
// Gated publishers (non-empty HardGate) score exactly 0 regardless of any
// weight, so they can never move and are excluded from every ranking
// compared here. mean_ungated_per_brief reports the mean number of ungated
// publishers per brief, since if most of the catalog is gated on a typical
// brief, ablation has little room to matter and the influence numbers need
// that context to be read honestly.
//
// Two complementary measures are reported per sub-score, because each
// catches a failure the other misses:
//   - top5_change_rate is the fraction of briefs where the SET of the top 5
//     ungated publishers differs between the full and ablated ranking —
//     the decision-relevant number, since stage 3 is seeded with this
//     ordering.
//   - mean_rank_shift is the mean absolute change in rank position across
//     all ungated publishers on a brief, averaged over briefs. It catches a
//     sub-score that reshuffles the middle of the pack while leaving the
//     top 5 untouched, which top5_change_rate alone would miss entirely.
//
// Ties (equal ablated or full totals) are broken by publisher ID in both
// rankings (see rankByScore), so the same input always produces the same
// ranks and mean_rank_shift is signal, not sort-order noise.
//
// If a sub-score's top 5 never changes on any brief, that is reported as an
// explicit finding rather than silently producing zero findings for it —
// per the design brief, an inert dimension is the most important possible
// result of this metric, and it must not be silent.
func ScoringAblation(campaigns []Campaign, cat *catalog.Catalog) Metric {
	acc := make(map[string]*scoringAblationAcc, len(subScoreNames))
	for _, name := range subScoreNames {
		acc[name] = &scoringAblationAcc{}
	}

	briefs := len(campaigns)
	var totalUngated int

	for _, c := range campaigns {
		scores := pipeline.ScoreAll(c.Campaign.Advertiser.DerivedProfile, cat)
		totalUngated += ablateBrief(c.BriefN, scores, acc)
	}

	values := map[string]float64{"mean_ungated_per_brief": safeDiv(totalUngated, briefs)}
	counts := map[string]int{"briefs": briefs}
	var findings []Finding

	type influence struct {
		name  string
		rate  float64
		shift float64
	}
	ranked := make([]influence, 0, len(subScoreNames))

	for _, name := range subScoreNames {
		a := acc[name]
		rate := safeDiv(a.top5Changed, briefs)
		var shift float64
		if briefs > 0 {
			shift = a.rankShiftSum / float64(briefs)
		}
		values["top5_change_rate."+name] = rate
		values["mean_rank_shift."+name] = shift
		counts["top5_changed."+name] = a.top5Changed
		ranked = append(ranked, influence{name, rate, shift})

		if a.haveExample {
			findings = append(findings, Finding{
				BriefN:      a.exampleBriefN,
				PublisherID: representativePublisher(a.exampleEntered, a.exampleLeft),
				Detail: fmt.Sprintf(
					"zeroing %s's weight changed the top 5: entered [%s], left [%s]",
					name, formatIDs(a.exampleEntered), formatIDs(a.exampleLeft)),
			})
		} else {
			findings = append(findings, Finding{
				BriefN:      0,
				PublisherID: "n/a",
				Detail: fmt.Sprintf(
					"%s: zeroing its weight changed the top 5 on none of the %d briefs measured — inert for ranking purposes",
					name, briefs),
			})
		}
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].rate != ranked[j].rate {
			return ranked[i].rate > ranked[j].rate
		}
		return ranked[i].shift > ranked[j].shift
	})
	parts := make([]string, len(ranked))
	for i, r := range ranked {
		parts[i] = fmt.Sprintf("%s (%.3f)", r.name, r.rate)
	}

	return Metric{
		Name: "scoring_ablation",
		Summary: fmt.Sprintf(
			"sub-scores ranked by influence on ranking (top5_change_rate, ties broken by mean_rank_shift), most to least: %s",
			strings.Join(parts, " > ")),
		Values:   values,
		Counts:   counts,
		Findings: findings,
	}
}

func representativePublisher(entered, left []string) string {
	if len(entered) > 0 {
		return entered[0]
	}
	if len(left) > 0 {
		return left[0]
	}
	return ""
}

func formatIDs(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}
