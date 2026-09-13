package pipeline

import (
	"context"

	"github.com/yashraj/disco/internal/model"
)

type fitResponse struct {
	Verdicts []model.FitVerdict `json:"verdicts"`
}

// Fit is stage 3. It hands the model every publisher with its deterministic
// sub-scores and gate status, and takes back a verdict and a written reason for
// each.
//
// Three things are enforced here rather than trusted to the prompt: a hard gate
// is never overridable, an unknown publisher ID is dropped, and a publisher the
// model omitted is backfilled — so the ledger always covers the whole catalog.
//
// The returned slice's order is not guaranteed: it is model order for
// publishers the model returned, followed by backfilled publishers in catalog
// order. A live model's JSON array order is not stable across calls, so
// callers must not depend on this slice's order — key by PublisherID instead.
func Fit(ctx context.Context, d Deps, p model.AdvertiserProfile, scores []model.PublisherScore) ([]model.FitVerdict, error) {
	type pubView struct {
		ID                 string          `json:"id"`
		Name               string          `json:"name"`
		Category           string          `json:"category"`
		Subcategories      []string        `json:"subcategories"`
		MonthlyImpressions int64           `json:"monthly_impressions"`
		AvgOrderValueUSD   int             `json:"avg_order_value_usd"`
		Audience           any             `json:"audience"`
		Notes              string          `json:"notes"`
		Score              float64         `json:"score"`
		SubScores          model.SubScores `json:"sub_scores"`
		HardGate           string          `json:"hard_gate"`
	}

	scoreByID := make(map[string]model.PublisherScore, len(scores))
	for _, s := range scores {
		scoreByID[s.PublisherID] = s
	}

	views := make([]pubView, 0, len(d.Catalog.Publishers))
	for i := range d.Catalog.Publishers {
		pub := &d.Catalog.Publishers[i]
		s := scoreByID[pub.ID]
		views = append(views, pubView{
			ID: pub.ID, Name: pub.Name, Category: pub.Category,
			Subcategories: pub.Subcategories, MonthlyImpressions: pub.MonthlyImpressions,
			AvgOrderValueUSD: pub.AvgOrderValueUSD, Audience: pub.Audience, Notes: pub.Notes,
			Score: s.Score, SubScores: s.Sub, HardGate: s.HardGate,
		})
	}

	resp, err := callStage[fitResponse](ctx, d, "fit", fixtureKey("fit", p.RawBrief),
		map[string]any{"advertiser": p, "publishers": views})
	if err != nil {
		return nil, err
	}

	// out is built by walking the model's own verdicts first (so a
	// publisher's rank reflects the model's relative ordering), then
	// backfilling whatever the model left out. Every entry — model-supplied
	// or backfilled — passes through applyGate, so nothing above this
	// function can rely on the model's verdict for a gated publisher.
	seen := make(map[string]bool, len(d.Catalog.Publishers))
	out := make([]model.FitVerdict, 0, len(d.Catalog.Publishers))

	for _, v := range resp.Verdicts {
		// Enforcement rule 2: an ID the model invented is not in the catalog
		// and must never reach the campaign config.
		if !d.Catalog.HasPublisher(v.PublisherID) {
			continue
		}
		if seen[v.PublisherID] {
			// The model repeated a publisher_id. Keep-first is a deliberate
			// choice (not an accident of loop order): the first verdict is
			// what the model committed to before second-guessing itself, and
			// picking a stable, simple rule here beats trying to reconcile
			// two conflicting reasons.
			continue
		}
		seen[v.PublisherID] = true
		out = append(out, applyGate(v, scoreByID[v.PublisherID].HardGate))
	}

	// Enforcement rule 3: a publisher the model omitted is backfilled, in
	// catalog order, so the ledger always covers every catalog publisher.
	for i := range d.Catalog.Publishers {
		id := d.Catalog.Publishers[i].ID
		if seen[id] {
			continue
		}
		v := model.FitVerdict{PublisherID: id, Verdict: "excluded",
			Reason: "Not selected; no specific fit identified for this advertiser."}
		out = append(out, applyGate(v, scoreByID[id].HardGate))
	}

	return out, nil
}

// applyGate is enforcement rule 1: a hard gate from stage 2 is an arithmetic
// certainty, and the model's verdict may not override it.
func applyGate(v model.FitVerdict, gate string) model.FitVerdict {
	if gate == GateNone {
		return v
	}
	v.Verdict = "excluded"
	v.Rank = 0
	if v.Reason == "" {
		v.Reason = gateReason(gate)
	}
	return v
}
