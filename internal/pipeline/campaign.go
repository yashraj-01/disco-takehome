package pipeline

import (
	"fmt"
	"sort"
	"time"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
)

// PipelineVersion is stamped into every campaign so a stored config can be
// traced back to the logic that produced it.
const PipelineVersion = "1"

// unallocatedFloorUSD is the smallest shortfall worth reporting; anything
// under one cent is float noise from the allocator's arithmetic, not a real
// unspent dollar, and is clamped to exactly zero.
const unallocatedFloorUSD = 0.01

// BuildInput is everything stage 6 needs. Scores and Verdicts must each cover
// every catalog publisher.
type BuildInput struct {
	Brief     string
	Profile   model.AdvertiserProfile
	Scores    []model.PublisherScore
	Verdicts  []model.FitVerdict
	Personas  model.PersonaSelection
	Creatives []model.Creative
	Catalog   *catalog.Catalog
	Params    AllocParams
	Model     string
	Provider  string
}

// Build assembles the campaign config. It owns every number in the output:
// allocation, CPM, impressions, bid range, and daily cap are all computed here
// from the deterministic stage-2 scores, never taken from a model response.
func Build(in BuildInput) model.Campaign {
	scoreByID := make(map[string]model.PublisherScore, len(in.Scores))
	for _, s := range in.Scores {
		scoreByID[s.PublisherID] = s
	}
	verdictByID := make(map[string]model.FitVerdict, len(in.Verdicts))
	for _, v := range in.Verdicts {
		verdictByID[v.PublisherID] = v
	}

	c := model.Campaign{
		Status: statusFor(in.Profile),
		Advertiser: model.AdvertiserBlock{
			Brief:          in.Brief,
			DerivedProfile: in.Profile,
			Confidence:     in.Profile.Confidence,
			Assumptions:    in.Profile.Assumptions,
			MissingSignals: in.Profile.MissingSignals,
		},
		Objective: objectiveFor(in.Profile),
		Flight: model.Flight{
			Start:        time.Now().UTC().Format("2006-01-02"),
			DurationDays: in.Params.Days,
		},
		Meta: model.Meta{
			GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
			Model:           in.Model,
			Provider:        in.Provider,
			PipelineVersion: PipelineVersion,
		},
	}

	// Ledger: one entry per catalog publisher, always.
	for i := range in.Catalog.Publishers {
		pub := &in.Catalog.Publishers[i]
		s := scoreByID[pub.ID]
		v, ok := verdictByID[pub.ID]
		if !ok {
			v = model.FitVerdict{Verdict: "excluded", Reason: "not evaluated"}
		}
		// Fit already enforces this; this is a deliberate second line of
		// defence against a future caller that builds a campaign from
		// verdicts that did not go through Fit. Keep both — do not delete
		// this one, and do not delete Fit's as "redundant" either.
		if s.HardGate != GateNone {
			v.Verdict = "excluded" // a hard gate is not overridable
			v.Rank = 0
			if v.Reason == "" {
				v.Reason = gateReason(s.HardGate)
			}
		}
		c.PublisherLedger = append(c.PublisherLedger, model.LedgerEntry{
			PublisherID: pub.ID, PublisherName: pub.Name,
			Verdict: v.Verdict, Rank: v.Rank,
			Score: s.Score, SubScores: s.Sub, HardGate: s.HardGate,
			Reason: v.Reason,
		})
	}

	if c.Status == model.StatusNoRecommendation {
		c.Budget = model.Budget{TotalUSD: in.Params.TotalUSD, UnallocatedUSD: clampUnallocated(in.Params.TotalUSD)}
		c.Bid = model.Bid{Model: "CPM", Strategy: "none",
			Reasoning: "No publisher in this catalog reaches this advertiser's buyers."}
		c.Clarifications = []string{
			"This catalog is consumer DTC only. Which consumer-facing product should we place?",
		}
		return c
	}

	// Budget: only recommended, ungated publishers compete.
	var cands []Candidate
	for _, e := range c.PublisherLedger {
		if e.Verdict != "recommended" || e.HardGate != GateNone {
			continue
		}
		pub, _ := in.Catalog.Publisher(e.PublisherID)
		cands = append(cands, Candidate{
			PublisherID:        e.PublisherID,
			Fit:                e.Score,
			EstCPM:             EstimateCPM(pub, in.Catalog.MinAOV, in.Catalog.MaxAOV),
			MonthlyImpressions: pub.MonthlyImpressions,
		})
	}

	reasonByID := make(map[string]string, len(c.PublisherLedger))
	for _, e := range c.PublisherLedger {
		reasonByID[e.PublisherID] = e.Reason
	}
	var weightedCPM, deployed float64
	for _, a := range Allocate(cands, in.Params) {
		pub, _ := in.Catalog.Publisher(a.PublisherID)
		rationale := reasonByID[a.PublisherID]
		if a.ExceedsMaxShare {
			// State the effect, not a cause: ExceedsMaxShare can also fire
			// when inventory reconciliation redistributes leftover budget
			// among three or more survivors, not only when fewer than three
			// qualified. Asserting "fewer than 3 publishers" here would
			// sometimes be a confidently wrong explanation.
			rationale += " This publisher's share exceeds the 40% concentration guideline, " +
				"which was relaxed because it could not be satisfied for this set of recommended publishers."
		}
		c.Budget.Allocation = append(c.Budget.Allocation, model.AllocationEntry{
			PublisherID: a.PublisherID, PublisherName: pub.Name,
			Share: a.Share, AmountUSD: a.AmountUSD,
			EstCPMUSD: a.EstCPMUSD, EstImpressions: a.EstImpressions,
			Rationale:       rationale,
			ExceedsMaxShare: a.ExceedsMaxShare,
		})
		weightedCPM += a.Share * a.EstCPMUSD
		deployed += a.AmountUSD
	}
	// The most this publisher set can absorb in one flight. Always reported:
	// as the budget itself when the advertiser named none, and as a comparison
	// when they did.
	c.Budget.RecommendedUSD = RecommendedBudgetUSD(cands, in.Params)

	total := in.Params.TotalUSD
	if total <= 0 {
		// No budget stated — size the campaign to deliverable inventory rather
		// than to an invented constant.
		total = c.Budget.RecommendedUSD
	}
	c.Budget.TotalUSD = total
	if in.Params.Days > 0 {
		c.Budget.DailyCapUSD = total / float64(in.Params.Days)
	}
	// AmountUSD is read straight off each Allocation above, never derived as
	// TotalUSD*Share: when the recommended set's deliverable inventory falls
	// short of the budget, AmountUSD sums to less than TotalUSD even though
	// Share still sums to 1.0 over what was actually deployed. UnallocatedUSD
	// is exactly that shortfall.
	c.Budget.UnallocatedUSD = clampUnallocated(total - deployed)

	c.Bid = buildBid(in.Profile, weightedCPM)
	c.Targeting = buildTargeting(in, c.Budget.Allocation)
	c.Creatives = in.Creatives
	c.PersonaRejections = in.Personas.Rejected

	if c.Status == model.StatusNeedsClarification {
		c.Clarifications = clarificationsFor(in.Profile)
	}
	return c
}

// clampUnallocated treats anything under one cent as zero, so float noise
// from the allocator's arithmetic never shows up as a phantom shortfall.
func clampUnallocated(v float64) float64 {
	if v < unallocatedFloorUSD {
		return 0
	}
	return v
}

func statusFor(p model.AdvertiserProfile) string {
	switch {
	case !p.IsConsumerDTC:
		return model.StatusNoRecommendation
	case p.Confidence == "low":
		return model.StatusNeedsClarification
	default:
		return model.StatusReady
	}
}

func objectiveFor(p model.AdvertiserProfile) string {
	switch {
	case p.BusinessModel == "b2b" || p.BusinessModel == "service":
		return "consideration"
	case p.BusinessModel == "subscription":
		return "conversion"
	case p.PriceTier == "luxury":
		return "awareness"
	default:
		return "conversion"
	}
}

func gateReason(gate string) string {
	switch gate {
	case GateNotConsumerDTC:
		return "This catalog reaches consumer DTC shoppers; this advertiser does not sell to them."
	case GateCategoryMismatch:
		return "No category or subcategory overlap with this advertiser."
	case GateDemographicMismatch:
		return "Audience age range does not overlap the advertiser's target at all."
	default:
		return "Excluded."
	}
}

// generatedGates lists every hard-gate constant gateReason produces fixed
// text for. GateNone is deliberately excluded: it is never passed to
// gateReason (a publisher with no hard gate has no gate-written reason), so
// including it would make GeneratedReason("Excluded.") true for text that
// never actually occurs from GateNone.
var generatedGates = []string{GateNotConsumerDTC, GateCategoryMismatch, GateDemographicMismatch}

// GeneratedReason reports whether s is one of the fixed reasons this package
// writes for a hard-gated publisher (see gateReason above), rather than
// prose a model produced. It exists so other packages — notably
// internal/measure, which scores the truthfulness of model-written reasons
// — can tell the two apart without measuring our own code as if it were the
// model's.
func GeneratedReason(s string) bool {
	for _, gate := range generatedGates {
		if s == gateReason(gate) {
			return true
		}
	}
	return false
}

func buildBid(p model.AdvertiserProfile, weightedCPM float64) model.Bid {
	b := model.Bid{
		Model:      "CPM",
		Strategy:   "even_pacing",
		FloorUSD:   0.8 * weightedCPM,
		TargetUSD:  weightedCPM,
		CeilingUSD: 1.4 * weightedCPM,
	}
	if p.EstimatedAOVUSD > 0 {
		b.Strategy = "target_cpa_capped"
		b.TargetCPAUSD = 0.35 * float64(p.EstimatedAOVUSD)
		b.Reasoning = fmt.Sprintf(
			"Target bid tracks the share-weighted CPM of the selected publishers ($%.2f). "+
				"CPA ceiling assumes 35%% of a $%d order value is an acceptable acquisition cost.",
			weightedCPM, p.EstimatedAOVUSD)
	} else {
		b.Reasoning = fmt.Sprintf(
			"Target bid tracks the share-weighted CPM of the selected publishers ($%.2f). "+
				"No order value was inferable, so pacing is even rather than CPA-capped.",
			weightedCPM)
	}
	return b
}

func buildTargeting(in BuildInput, alloc []model.AllocationEntry) model.Targeting {
	t := model.Targeting{
		GenderSkew: in.Profile.TargetGenderSkew,
	}
	if in.Profile.TargetAgeMin > 0 || in.Profile.TargetAgeMax > 0 {
		t.AgeRange = fmt.Sprintf("%d-%d", in.Profile.TargetAgeMin, in.Profile.TargetAgeMax)
	}
	for _, p := range in.Personas.Selected {
		t.PersonaIDs = append(t.PersonaIDs, p.PersonaID)
	}

	geos, tiers, cats := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, a := range alloc {
		pub, ok := in.Catalog.Publisher(a.PublisherID)
		if !ok {
			continue
		}
		for _, g := range pub.Audience.TopGeos {
			geos[g] = true
		}
		tiers[pub.Audience.IncomeTier] = true
		cats[pub.Category] = true
		for _, s := range pub.Subcategories {
			cats[s] = true
		}
	}
	t.Geos, t.IncomeTiers, t.ContextualCategories = sortedKeys(geos), sortedKeys(tiers), sortedKeys(cats)
	return t
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// clarificationsFor turns the profile's missing signals into questions a person
// can actually answer. The campaign below them is still emitted, clearly marked
// provisional: refusing a vague brief outright is as unhelpful as pretending it
// was clear.
func clarificationsFor(p model.AdvertiserProfile) []string {
	if len(p.MissingSignals) == 0 {
		return []string{"What exactly do you sell, and roughly what does it cost?"}
	}
	out := make([]string, 0, len(p.MissingSignals))
	for _, m := range p.MissingSignals {
		out = append(out, fmt.Sprintf("We had to guess at %s. What is it actually?", m))
	}
	return out
}
