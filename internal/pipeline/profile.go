package pipeline

import (
	"context"
	"strings"

	"github.com/yashraj/disco/internal/model"
)

// Profile is stage 1. It turns the advertiser's free-text brief into a
// structured profile whose Confidence and IsConsumerDTC fields gate every
// stage after it.
func Profile(ctx context.Context, d Deps, brief string) (model.AdvertiserProfile, error) {
	p, err := callStage[model.AdvertiserProfile](ctx, d, "profile",
		fixtureKey("profile", brief),
		map[string]any{
			"brief":              brief,
			"catalog_categories": catalogCategories(d),
		})
	if err != nil {
		return model.AdvertiserProfile{}, err
	}

	p.RawBrief = brief
	p.PrimaryCategory = strings.ToLower(strings.TrimSpace(p.PrimaryCategory))
	for i, s := range p.Subcategories {
		p.Subcategories[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return p, nil
}

// catalogCategories lists the distinct publisher categories, so stage 1 lands
// its primary_category in the same vocabulary stage 2 scores against.
func catalogCategories(d Deps) []string {
	seen := map[string]bool{}
	for i := range d.Catalog.Publishers {
		seen[d.Catalog.Publishers[i].Category] = true
	}
	return sortedKeys(seen)
}
