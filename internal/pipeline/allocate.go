package pipeline

import (
	"math"
	"sort"
)

// AllocParams are the budget-splitting knobs. Every one is exposed as a CLI
// flag so the shape of an allocation can be interrogated and changed live.
type AllocParams struct {
	Gamma    float64 // share exponent; 1.0 is proportional, higher concentrates
	MaxShare float64 // concentration cap per publisher
	SOVCap   float64 // share of a publisher's monthly impressions we will buy
	MinShare float64 // below this a slice is dust and is redistributed
	TotalUSD float64
	Days     int
}

// DefaultAllocParams returns the documented defaults.
func DefaultAllocParams() AllocParams {
	return AllocParams{Gamma: 1.5, MaxShare: 0.40, SOVCap: 0.15, MinShare: 0.05,
		TotalUSD: 25_000, Days: 30}
}

// Candidate is one publisher competing for budget. Fit is the deterministic
// stage-2 score; a model never supplies it.
type Candidate struct {
	PublisherID        string
	Fit                float64
	EstCPM             float64
	MonthlyImpressions int64
}

// Allocation is one publisher's resulting slice of the budget.
type Allocation struct {
	PublisherID string
	Share       float64
	AmountUSD   float64
	EstCPMUSD   float64
	// ExceedsMaxShare is true when the final Share exceeds AllocParams.MaxShare.
	// This only happens when the candidate set makes MaxShare infeasible to
	// honor for everyone at once (e.g. fewer than ceil(1/MaxShare) survivors) —
	// the SOV cap always holds, but the concentration cap is policy, not
	// physics, and yields rather than leave budget stranded or a fitting
	// publisher dropped. The rationale layer surfaces this rather than hiding
	// a silently-abandoned cap.
	ExceedsMaxShare bool
	EstImpressions  int64
}

const maxAllocPasses = 5

// Allocate splits TotalUSD across candidates in proportion to Fit^Gamma, then
// applies three caps: a per-publisher concentration cap, a deliverability cap
// derived from the publisher's inventory at its modelled CPM, and a floor below
// which a slice is too small to be worth buying.
//
// The caps interact — releasing money from a clipped publisher can push a
// survivor over the concentration cap, and dropping a sub-floor publisher
// redistributes money that can do the same — so they are iterated to a fixed
// point rather than applied in a single pass.
func Allocate(cands []Candidate, p AllocParams) []Allocation {
	return allocateWithPasses(cands, p, maxAllocPasses)
}

// allocateWithPasses is Allocate's implementation, parameterised on the pass
// count so a test can pin it to 1 and demonstrate that the fixed-point loop
// is load-bearing rather than decorative.
func allocateWithPasses(cands []Candidate, p AllocParams, passes int) []Allocation {
	if len(cands) == 0 {
		return nil
	}

	share := make(map[string]float64, len(cands))
	byID := make(map[string]Candidate, len(cands))
	var total float64
	for _, c := range cands {
		w := math.Pow(math.Max(c.Fit, 0), p.Gamma)
		share[c.PublisherID] = w
		byID[c.PublisherID] = c
		total += w
	}
	if total == 0 { // no candidate has any fit; split evenly
		for id := range share {
			share[id] = 1 / float64(len(share))
		}
	} else {
		for id := range share {
			share[id] /= total
		}
	}
	// origShare is the fit-proportional split before any capping. It is the
	// basis for pro-rata redistribution when a policy cap has to yield (see
	// below) — a locked candidate's *current* share is just its cap value,
	// not a fit-proportional quantity, so it cannot be used for that split.
	origShare := make(map[string]float64, len(share))
	for id, v := range share {
		origShare[id] = v
	}

	locked := map[string]float64{} // publishers pinned at a cap
	// sovBound records, for each locked publisher, whether the pin is the SOV
	// (physical inventory) cap — which must never yield — as opposed to the
	// MaxShare (policy) cap, which may yield when the candidate set makes it
	// infeasible for everyone to stay under it.
	sovBound := map[string]bool{}
	// released marks a publisher that has already been granted an exception
	// from MaxShare because the set was infeasible under it. From then on
	// only its SOV ceiling is ever enforced for that publisher.
	released := map[string]bool{}

	sovCapOf := func(id string) float64 {
		c := byID[id]
		if c.EstCPM <= 0 || p.TotalUSD <= 0 {
			return math.Inf(1) // no physical constraint we can compute
		}
		deliverableUSD := float64(c.MonthlyImpressions) * p.SOVCap / 1000 * c.EstCPM
		return deliverableUSD / p.TotalUSD
	}

	for pass := 0; pass < passes; pass++ {
		changed := false

		// Redistribute whatever is not locked across the unlocked survivors.
		var freeTotal, lockedTotal float64
		for id, v := range share {
			if _, isLocked := locked[id]; isLocked {
				lockedTotal += v
			} else {
				freeTotal += v
			}
		}

		// Every survivor is pinned at a cap, but the caps together still
		// don't cover the whole budget — e.g. two candidates each held under
		// the 40% concentration cap can never sum past 80%, regardless of
		// their SOV ceilings. There is no unlocked survivor left to carry the
		// shortfall. Rather than drop a publisher that genuinely fits and has
		// inventory (which was tried and rejected: it discards real capacity
		// and swaps one cap violation for a worse one), let MaxShare yield
		// for whichever locked publishers are policy-bound, not SOV-bound:
		// redistribute the whole non-SOV-locked pool pro-rata by original fit
		// among them, exceeding MaxShare where needed. The SOV cap never
		// yields — a publisher locked at its physical inventory ceiling stays
		// there. If nobody has SOV headroom either, there is nothing left to
		// do: fall through to the closing renormalisation as the last resort.
		if freeTotal == 0 && lockedTotal < 1-1e-9 && len(share) > 1 {
			var releasable []string
			var sovLockedTotal float64
			for id := range locked {
				if sovBound[id] {
					sovLockedTotal += locked[id]
				} else {
					releasable = append(releasable, id)
				}
			}
			if len(releasable) > 0 {
				pool := 1 - sovLockedTotal
				var w float64
				for _, id := range releasable {
					w += origShare[id]
				}
				for _, id := range releasable {
					var v float64
					if w > 0 {
						v = origShare[id] / w * pool
					} else {
						v = pool / float64(len(releasable))
					}
					share[id] = v
					released[id] = true
					// Unlock so the cap check below re-validates against the
					// SOV ceiling only (released publishers never have
					// MaxShare re-applied) — it may still bind if this
					// pro-rata split overshoots this publisher's inventory.
					delete(locked, id)
					delete(sovBound, id)
				}
				changed = true
				continue
			}
			// No releasable candidate: caps are physically infeasible for
			// this set. Nothing more to try; let the final step renormalise.
		}

		if freeTotal > 0 {
			avail := 1 - lockedTotal
			for id := range share {
				if _, isLocked := locked[id]; !isLocked {
					share[id] = share[id] / freeTotal * avail
				}
			}
		}

		// Concentration and deliverability caps. A released publisher is only
		// ever checked against its SOV ceiling from here on.
		for id, v := range share {
			if _, isLocked := locked[id]; isLocked {
				continue
			}
			sovCap := sovCapOf(id)
			boundBySOV := released[id] || sovCap <= p.MaxShare
			cap := sovCap
			if !boundBySOV {
				cap = p.MaxShare
			}
			if v > cap+1e-12 {
				share[id] = cap
				locked[id] = cap
				sovBound[id] = boundBySOV
				changed = true
			}
		}

		// Dust floor. Never drop the last survivor.
		if len(share) > 1 {
			for id, v := range share {
				if _, isLocked := locked[id]; isLocked {
					continue
				}
				if v < p.MinShare-1e-12 && len(share) > 1 {
					delete(share, id)
					changed = true
				}
			}
		}

		if !changed {
			break
		}
	}

	// Renormalise so the emitted shares sum to exactly 1.
	var sum float64
	for _, v := range share {
		sum += v
	}
	out := make([]Allocation, 0, len(share))
	for id, v := range share {
		c := byID[id]
		s := v / sum
		amount := p.TotalUSD * s
		var impressions int64
		if c.EstCPM > 0 {
			impressions = int64(amount / c.EstCPM * 1000)
		}
		out = append(out, Allocation{
			PublisherID: id, Share: s, AmountUSD: amount,
			EstCPMUSD: c.EstCPM, EstImpressions: impressions,
			ExceedsMaxShare: s > p.MaxShare+1e-9,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Share != out[j].Share {
			return out[i].Share > out[j].Share
		}
		return out[i].PublisherID < out[j].PublisherID // stable for equal shares
	})
	return out
}
