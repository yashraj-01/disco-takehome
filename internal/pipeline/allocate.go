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
	PublisherID    string
	Share          float64
	AmountUSD      float64
	EstCPMUSD      float64
	EstImpressions int64
}

// maxAllocPasses is a var, not a const, solely so a test can prove the loop
// is load-bearing by pinning it to 1 and observing a cap violation.
var maxAllocPasses = 5

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

	locked := map[string]float64{} // publishers pinned at their cap

	for pass := 0; pass < maxAllocPasses; pass++ {
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

		// Every survivor is pinned at its cap, but the caps together still
		// don't cover the whole budget (e.g. two candidates each held under
		// the 40% concentration cap can never sum past 80%). There is no
		// unlocked survivor left to carry the shortfall, so the weakest
		// locked candidate is dropped and its budget freed for the rest —
		// the same resolution the dust floor uses, just triggered by cap
		// infeasibility instead of raw smallness. Without this, the final
		// renormalisation below would inflate every locked share, including
		// capped ones, back past the very caps that pinned them.
		if freeTotal == 0 && lockedTotal < 1-1e-9 && len(share) > 1 {
			weakest := ""
			for id, v := range locked {
				if weakest == "" || v < locked[weakest] ||
					(v == locked[weakest] && id < weakest) {
					weakest = id
				}
			}
			delete(share, weakest)
			delete(locked, weakest)
			continue
		}

		if freeTotal > 0 {
			avail := 1 - lockedTotal
			for id := range share {
				if _, isLocked := locked[id]; !isLocked {
					share[id] = share[id] / freeTotal * avail
				}
			}
		}

		// Concentration and deliverability caps.
		for id, v := range share {
			if _, isLocked := locked[id]; isLocked {
				continue
			}
			cap := p.MaxShare
			if c := byID[id]; c.EstCPM > 0 && p.TotalUSD > 0 {
				deliverableUSD := float64(c.MonthlyImpressions) * p.SOVCap / 1000 * c.EstCPM
				if s := deliverableUSD / p.TotalUSD; s < cap {
					cap = s
				}
			}
			if v > cap+1e-12 {
				share[id] = cap
				locked[id] = cap
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
