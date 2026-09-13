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
//
// AmountUSD == AllocParams.TotalUSD * Share holds only when the candidate set
// is not inventory-starved. When the survivors' combined SOV ceilings fall
// short of TotalUSD, the budget cannot be fully deployed on this set at all:
// each is allocated up to its own SOV ceiling, AmountUSD sums to that
// (smaller) deployed total rather than to TotalUSD, and Share is each
// candidate's fraction of the deployed total, not of TotalUSD. MinShare (the
// dust floor) does not apply on this path either — it is enforced earlier,
// against the fit-driven target shares, not against the inventory-clamped
// amounts; a survivor can end up with a final Share below MinShare once
// inventory reconciliation redistributes what a capped publisher couldn't
// absorb.
//
// EstImpressions never exceeds MonthlyImpressions*SOVCap for its publisher.
// That invariant has no exceptions — not for a lone survivor, not for a set
// the dust floor collapsed down to one. Impressions that do not exist cannot
// be bought, so a publisher that can deliver nothing is not allocated
// anything, and allocations below one cent are not emitted at all.
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

// minAllocUSD is the smallest amount worth emitting as an allocation. A
// survivor clamped below a cent has effectively been allocated nothing, and a
// zero-dollar row invites a downstream consumer to book a publisher for no
// money; such rows are dropped from the result instead.
const minAllocUSD = 0.01

// isFinite reports whether v is a real number we can compute with. NaN and
// ±Inf are not: they pass ordinary `<= 0` guards silently (NaN compares false
// against everything) and then poison every arithmetic result downstream, so
// they are rejected at the boundary and treated as "no usable data" — a zero
// weight or a zero deliverable — rather than propagated.
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// deliverableUSD is the dollar value of the most impressions we are willing
// to buy from c: SOVCap's share of its monthly inventory, priced at its
// modelled CPM. A publisher with no usable CPM or no impressions can't
// deliver anything, so it correctly yields zero. Both the inputs and the
// computed product are checked for finiteness: a NaN CPM would otherwise slip
// past `<= 0`, and a non-finite result would turn every share into NaN.
func deliverableUSD(c Candidate, p AllocParams) float64 {
	if !isFinite(c.EstCPM) || c.EstCPM <= 0 || c.MonthlyImpressions <= 0 {
		return 0
	}
	if !isFinite(p.SOVCap) || p.SOVCap <= 0 {
		return 0
	}
	d := float64(c.MonthlyImpressions) * p.SOVCap / 1000 * c.EstCPM
	if !isFinite(d) || d <= 0 {
		return 0
	}
	return d
}

// Allocate splits TotalUSD across candidates in proportion to Fit^Gamma, then
// applies the concentration cap (MaxShare) and a dust floor (MinShare) to
// produce a target share for each survivor, and finally reconciles those
// targets against each survivor's SOV (inventory) ceiling — the one cap that
// must never yield, however the target shares came out. See
// reconcileToInventory for why that reconciliation, not the fixed-point loop
// above it, is what guarantees the SOV invariant.
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

	byID := make(map[string]Candidate, len(cands))
	origWeight := make(map[string]float64, len(cands)) // fit^gamma, unnormalized
	share := make(map[string]float64, len(cands))
	var total float64
	for _, c := range cands {
		// A non-finite Fit is not a very large fit, it is missing data:
		// math.Max(NaN, 0) is NaN, which would make every share NaN, and an
		// infinite weight makes the normalisation Inf/Inf. Either way the
		// candidate contributes no weight.
		fit := c.Fit
		if !isFinite(fit) || fit < 0 {
			fit = 0
		}
		w := math.Pow(fit, p.Gamma)
		if !isFinite(w) || w < 0 {
			w = 0
		}
		byID[c.PublisherID] = c
		origWeight[c.PublisherID] = w
		share[c.PublisherID] = w
		total += w
	}
	if !isFinite(total) || total <= 0 { // no candidate has any usable fit; split evenly
		for id := range share {
			share[id] = 1 / float64(len(share))
		}
	} else {
		for id := range share {
			share[id] /= total
		}
	}

	locked := map[string]float64{} // publishers pinned at the concentration cap

	// This loop resolves fit^gamma proportionality against exactly two
	// things: the concentration cap (MaxShare) and the dust floor (MinShare).
	// It deliberately knows nothing about SOV (inventory) any more — an
	// earlier version tried to make it SOV-aware too, so it could let
	// MaxShare yield precisely when the survivor set made it infeasible
	// alongside SOV. That was wrong: a dust-drop triggered by *this same
	// pass* can shrink the survivor set's combined SOV capacity out from
	// under an interaction the loop had already "resolved" on a now-stale
	// survivor set, and nothing forced a re-check. No amount of in-loop
	// bookkeeping converges to a guarantee against that — only a final,
	// unconditional reconciliation after the loop does (see
	// reconcileToInventory). So here MaxShare is the only cap, and its own
	// interactions — releasing a clipped publisher's excess can push a
	// survivor over MaxShare, and dropping a sub-floor publisher can do the
	// same — are still genuinely iterative, which is what
	// TestFixedPointLoopIsRequired demonstrates.
	for pass := 0; pass < passes; pass++ {
		changed := false

		var freeTotal, lockedTotal float64
		for id, v := range share {
			if _, isLocked := locked[id]; isLocked {
				lockedTotal += v
			} else {
				freeTotal += v
			}
		}
		if freeTotal > 0 {
			avail := 1 - lockedTotal
			for id := range share {
				if _, isLocked := locked[id]; !isLocked {
					share[id] = share[id] / freeTotal * avail
				}
			}
		}

		for id, v := range share {
			if _, isLocked := locked[id]; isLocked {
				continue
			}
			if v > p.MaxShare+1e-12 {
				share[id] = p.MaxShare
				locked[id] = p.MaxShare
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

	var sum float64
	for _, v := range share {
		sum += v
	}
	if sum <= 0 {
		return nil
	}

	survivors := make([]Candidate, 0, len(share))
	targetShare := make(map[string]float64, len(share))
	for id, v := range share {
		targetShare[id] = v / sum
		survivors = append(survivors, byID[id])
	}

	// Every survivor set — including a set of one — goes through inventory
	// reconciliation. The "single candidate takes everything" rule is about
	// share of DEPLOYED spend, not about escaping inventory: those are
	// different claims, and only the first one is a rule. A lone survivor
	// still comes out of reconciliation with Share 1.0 (it is the whole of
	// what was deployed) but with AmountUSD = min(TotalUSD, its own
	// deliverable), so it can never be booked for impressions its publisher
	// does not have. If it can deliver nothing at all, there is no allocation
	// to make and the result is nil. Exempting this path was a back door into
	// the one invariant that has no exceptions, reachable whenever the dust
	// floor happened to collapse a multi-candidate set down to one.
	return reconcileToInventory(survivors, targetShare, origWeight, p)
}

// reconcileToInventory turns the loop's fit/MaxShare-driven target shares
// into final amounts that never exceed any survivor's SOV (inventory)
// ceiling — by construction, not by convergence, and for every survivor set
// including a set of one. Each survivor starts at
// min(target, its own ceiling); whatever a clipped survivor couldn't absorb
// is redistributed pro-rata by original fit^gamma weight among survivors that
// still have headroom under their own ceiling, re-clamping as it goes, until
// either the leftover is exhausted or nobody has headroom left — in which
// case the budget is honestly under-deployed rather than pushing anyone past
// a physical ceiling that must never yield.
//
// This terminates in at most len(survivors) iterations: every iteration
// either exhausts the leftover (nothing left to place) or newly saturates at
// least one more survivor (permanently removing it from the headroom set), so
// there can be at most len(survivors) such saturations before the headroom
// set is empty and the loop stops regardless of the leftover.
func reconcileToInventory(survivors []Candidate, targetShare, origWeight map[string]float64, p AllocParams) []Allocation {
	amount := make(map[string]float64, len(survivors))
	deliverable := make(map[string]float64, len(survivors))
	for _, c := range survivors {
		d := deliverableUSD(c, p)
		deliverable[c.PublisherID] = d
		target := targetShare[c.PublisherID] * p.TotalUSD
		if !isFinite(target) || target < 0 {
			target = 0 // a non-finite budget or share buys nothing, not NaN
		}
		amount[c.PublisherID] = math.Min(target, d)
	}

	const epsilon = 1e-6
	for i := 0; i < len(survivors); i++ {
		var spent float64
		for _, v := range amount {
			spent += v
		}
		leftover := p.TotalUSD - spent
		if leftover <= epsilon {
			break
		}

		var headroomIDs []string
		var headroomWeight float64
		for _, c := range survivors {
			id := c.PublisherID
			if h := deliverable[id] - amount[id]; h > epsilon {
				headroomIDs = append(headroomIDs, id)
				headroomWeight += origWeight[id]
			}
		}
		if len(headroomIDs) == 0 {
			break // nobody can absorb more; the shortfall is real
		}
		for _, id := range headroomIDs {
			var add float64
			if headroomWeight > 0 {
				add = leftover * origWeight[id] / headroomWeight
			} else {
				add = leftover / float64(len(headroomIDs))
			}
			amount[id] = math.Min(amount[id]+add, deliverable[id])
		}
	}

	// Only survivors actually carrying money are allocations. Sub-cent rows
	// are dropped before the deployed total is computed, so the emitted
	// shares still sum to exactly 1 over what is emitted.
	kept := make([]Candidate, 0, len(survivors))
	var deployed float64
	for _, c := range survivors {
		a := amount[c.PublisherID]
		if !isFinite(a) || a < minAllocUSD {
			continue
		}
		kept = append(kept, c)
		deployed += a
	}
	if deployed <= 0 || !isFinite(deployed) {
		return nil // nothing can be delivered to anyone in this set
	}

	out := make([]Allocation, 0, len(kept))
	for _, c := range kept {
		id := c.PublisherID
		a := amount[id]
		s := a / deployed
		var impressions int64
		if isFinite(c.EstCPM) && c.EstCPM > 0 {
			if imp := a / c.EstCPM * 1000; isFinite(imp) && imp > 0 {
				impressions = int64(imp)
			}
		}
		out = append(out, Allocation{
			PublisherID: id, Share: s, AmountUSD: a,
			EstCPMUSD: c.EstCPM, EstImpressions: impressions,
			ExceedsMaxShare: s > p.MaxShare+1e-9,
		})
	}
	sortByShareDesc(out)
	return out
}

func sortByShareDesc(out []Allocation) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Share != out[j].Share {
			return out[i].Share > out[j].Share
		}
		return out[i].PublisherID < out[j].PublisherID // stable for equal shares
	})
}
