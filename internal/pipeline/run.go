package pipeline

import (
	"context"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/model"
)

// Options configures a pipeline run.
type Options struct {
	Provider llm.Provider
	Catalog  *catalog.Catalog
	Params   AllocParams
	Model    string
}

// Run executes the six stages and returns the campaign config.
//
// A brief this catalog cannot serve short-circuits after stage 2: there is no
// point spending three more model calls to produce a recommendation that
// should not exist, and the ledger already explains why every publisher is
// out.
func Run(ctx context.Context, brief string, o Options) (model.Campaign, error) {
	d := Deps{Provider: o.Provider, Catalog: o.Catalog}

	profile, err := Profile(ctx, d, brief)
	if err != nil {
		return model.Campaign{}, err
	}

	scores := ScoreAll(profile, o.Catalog)

	in := BuildInput{
		Brief: brief, Profile: profile, Scores: scores,
		Catalog: o.Catalog, Params: o.Params,
		Model: o.Model, Provider: o.Provider.Name(),
	}

	if !profile.IsConsumerDTC {
		for _, s := range scores {
			in.Verdicts = append(in.Verdicts, model.FitVerdict{
				PublisherID: s.PublisherID, Verdict: "excluded",
				Reason: gateReason(GateNotConsumerDTC),
			})
		}
		return Build(in), nil
	}

	if in.Verdicts, err = Fit(ctx, d, profile, scores); err != nil {
		return model.Campaign{}, err
	}
	if in.Personas, err = Personas(ctx, d, profile, in.Verdicts); err != nil {
		return model.Campaign{}, err
	}
	if in.Creatives, err = Creatives(ctx, d, profile, in.Personas); err != nil {
		return model.Campaign{}, err
	}
	return Build(in), nil
}
