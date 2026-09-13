package pipeline

import (
	"context"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/yashraj/disco/internal/model"
)

const (
	maxHeadlineRunes = 60
	maxBodyRunes     = 200
)

// Creatives is stage 5. It writes one ad per selected persona, concurrently.
//
// Each call sees exactly one persona record. Asking a single call for five
// variants produces five paraphrases of one idea; isolating the persona is what
// makes the copy actually differ. The provider's shared rate limiter (Task 7)
// is what keeps this burst inside the free-tier quota.
func Creatives(ctx context.Context, d Deps, p model.AdvertiserProfile, sel model.PersonaSelection) ([]model.Creative, error) {
	// Pre-sized and index-written: each goroutine below owns exactly one
	// index, so there is no shared mutable state between them and no need for
	// a mutex around this slice.
	out := make([]model.Creative, len(sel.Selected))

	g, gctx := errgroup.WithContext(ctx)
	for i, pick := range sel.Selected {
		i, pick := i, pick // avoid the classic loop-variable capture bug
		g.Go(func() error {
			persona, ok := d.Catalog.Persona(pick.PersonaID)
			if !ok {
				// Not in the catalog: skip rather than produce an empty
				// creative. out[i] stays the zero value and is filtered out
				// below.
				return nil
			}

			var pubs []map[string]any
			for _, id := range pick.PrimaryPublishers {
				if pub, ok := d.Catalog.Publisher(id); ok {
					pubs = append(pubs, map[string]any{
						"id": pub.ID, "name": pub.Name,
						"category": pub.Category, "notes": pub.Notes,
					})
				}
			}

			// gctx, not ctx: a failure in any other persona's call must
			// cancel this in-flight call too, instead of letting it run (or
			// hang) uselessly.
			c, err := callStage[model.Creative](gctx, d, "creative",
				fixtureKey("creative", p.RawBrief+"|"+pick.PersonaID),
				map[string]any{
					"advertiser":         p,
					"persona":            persona,
					"persona_rationale":  pick.Rationale,
					"primary_publishers": pubs,
				})
			if err != nil {
				return err
			}

			c.PersonaID = pick.PersonaID
			c.SuggestedPublishers = pick.PrimaryPublishers
			c.Headline = truncateRunes(c.Headline, maxHeadlineRunes)
			c.Body = truncateRunes(c.Body, maxBodyRunes)
			c.MessagingLevers = groundedIn(c.MessagingLevers, persona.MessagingPreferences)
			c.Avoided = groundedIn(c.Avoided, persona.DisinterestedIn)

			out[i] = c
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Selection order is exactly the order of sel.Selected, which is the
	// order out was indexed by above, so this pass over out preserves it.
	// A skipped (unknown) persona left its slot as the zero value, identified
	// by an empty PersonaID, and is dropped here rather than emitted empty.
	kept := make([]model.Creative, 0, len(out))
	for _, c := range out {
		if c.PersonaID != "" {
			kept = append(kept, c)
		}
	}
	return kept, nil
}

// groundedIn keeps only the claims that appear (case-insensitively, ignoring
// surrounding whitespace) in the persona's own record. The field exists to
// prove the copy is grounded, so an entry that cannot be checked against the
// record is worth nothing and is dropped rather than trusted.
func groundedIn(claimed, actual []string) []string {
	have := make(map[string]bool, len(actual))
	for _, a := range actual {
		have[strings.ToLower(strings.TrimSpace(a))] = true
	}
	out := make([]string, 0, len(claimed))
	for _, c := range claimed {
		if have[strings.ToLower(strings.TrimSpace(c))] {
			out = append(out, c)
		}
	}
	return out
}

// truncateRunes hard-caps s at n runes (not bytes), so multi-byte characters
// are never split mid-character.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}
