package pipeline

import (
	"context"
	"fmt"

	"github.com/yashraj/disco/internal/model"
)

const (
	minPersonas = 3
	maxPersonas = 5
)

// Personas is stage 4. It selects the 3 to 5 shopper personas the creatives
// will be written for, and records why the rest were passed over.
//
// The count bounds and the ID allow-list are enforced here, not trusted to the
// prompt: an invented or duplicate persona_id is dropped, primary_publishers is
// filtered the same way, and a persona that survived into Selected is scrubbed
// out of Rejected. Fewer than 3 usable picks after filtering is an error — the
// exercise requires 3 to 5 creative variants, so silently returning fewer would
// produce a deliverable that fails its own spec.
func Personas(ctx context.Context, d Deps, p model.AdvertiserProfile, verdicts []model.FitVerdict) (model.PersonaSelection, error) {
	var recommended []map[string]any
	for _, v := range verdicts {
		if v.Verdict != "recommended" {
			continue
		}
		pub, ok := d.Catalog.Publisher(v.PublisherID)
		if !ok {
			continue
		}
		recommended = append(recommended, map[string]any{
			"id": pub.ID, "name": pub.Name, "category": pub.Category,
			"subcategories": pub.Subcategories, "notes": pub.Notes,
			"audience": pub.Audience, "reason": v.Reason,
		})
	}

	sel, err := callStage[model.PersonaSelection](ctx, d, "personas",
		fixtureKey("personas", p.RawBrief),
		map[string]any{
			"advertiser":             p,
			"recommended_publishers": recommended,
			"personas":               d.Catalog.Personas,
		})
	if err != nil {
		return model.PersonaSelection{}, err
	}

	// Enforcement rules 1-3: unknown persona IDs are dropped, a repeated ID
	// keeps only its first occurrence, and the surviving list is capped at 5.
	seen := make(map[string]bool, len(sel.Selected))
	kept := make([]model.PersonaPick, 0, maxPersonas)
	for _, pick := range sel.Selected {
		if !d.Catalog.HasPersona(pick.PersonaID) || seen[pick.PersonaID] {
			continue
		}
		seen[pick.PersonaID] = true
		// Enforcement rule 5: primary_publishers is filtered against the
		// catalog too, not just persona_id.
		pick.PrimaryPublishers = filterPersonaPublisherIDs(d, pick.PrimaryPublishers)
		kept = append(kept, pick)
		if len(kept) == maxPersonas {
			break
		}
	}

	// Enforcement rule 4: always end with at least minPersonas picks.
	//
	// The model under-delivers on a vague brief: "We help people feel better"
	// returned two picks against a prompt asking for three to five, both valid
	// and neither filtered. Failing there would crash on exactly the low-signal
	// input this pipeline advertises handling gracefully, and would make vague
	// briefs succeed or fail on whether the model happened to return three.
	// Backfill deterministically instead, in catalog order, and say plainly in
	// the rationale that the pick was added — the output must never imply the
	// model endorsed a persona it did not choose.
	for i := range d.Catalog.Personas {
		if len(kept) >= minPersonas {
			break
		}
		p := &d.Catalog.Personas[i]
		if seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		kept = append(kept, model.PersonaPick{
			PersonaID: p.ID,
			Rationale: "Added to reach the three-variant minimum. The brief was too " +
				"vague for the model to segment on, so this is a placeholder, not a " +
				"judgement that this persona fits.",
		})
	}

	// Structurally impossible with the supplied catalog of ten, but a catalog
	// smaller than the minimum cannot satisfy the contract.
	if len(kept) < minPersonas {
		return model.PersonaSelection{}, fmt.Errorf(
			"pipeline: personas: catalog has %d personas, need at least %d",
			len(d.Catalog.Personas), minPersonas)
	}

	// Enforcement rule 6: a persona that was selected must not also appear in
	// Rejected, and an unknown ID has no business in Rejected either.
	rejected := make([]model.PersonaRejection, 0, len(sel.Rejected))
	for _, r := range sel.Rejected {
		if !d.Catalog.HasPersona(r.PersonaID) || seen[r.PersonaID] {
			continue
		}
		rejected = append(rejected, r)
	}

	return model.PersonaSelection{Selected: kept, Rejected: rejected}, nil
}

// filterPersonaPublisherIDs removes any publisher ID that is not in the
// catalog. Applied to PersonaPick.PrimaryPublishers.
func filterPersonaPublisherIDs(d Deps, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if d.Catalog.HasPublisher(id) {
			out = append(out, id)
		}
	}
	return out
}
