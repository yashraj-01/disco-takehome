package pipeline

import (
	"context"
	"log/slog"
	"time"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/logging"
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

	// Every line from this run carries the brief hash. "disco measure" runs
	// fifteen briefs back to back and stage 5 is concurrent, so without a
	// correlation key the interleaved output cannot be read.
	log := logging.L().With("brief", llm.ShortHash(brief))
	started := time.Now()
	log.Info("run started", "text", logging.Brief(brief),
		"provider", o.Provider.Name(), "model", o.Model)

	profile, err := Profile(ctx, d, brief)
	if err != nil {
		return model.Campaign{}, err
	}
	log.Debug("profiled", "category", profile.PrimaryCategory,
		"price_tier", profile.PriceTier, "confidence", profile.Confidence,
		"consumer_dtc", profile.IsConsumerDTC)

	scores := ScoreAll(profile, o.Catalog)
	log.Debug("scored", "publishers", len(scores), "hard_gated", gatedCount(scores))

	in := BuildInput{
		Brief: brief, Profile: profile, Scores: scores,
		Catalog: o.Catalog, Params: o.Params,
		Model: o.Model, Provider: o.Provider.Name(),
	}

	if !profile.IsConsumerDTC {
		// The single most important log line in the system: it records that
		// the pipeline chose to return nothing, and that the choice was made
		// in code before any ranking model saw the brief.
		log.Warn("refusing to recommend", "reason", GateNotConsumerDTC,
			"business_model", profile.BusinessModel, "stages_skipped", 3)
		for _, s := range scores {
			in.Verdicts = append(in.Verdicts, model.FitVerdict{
				PublisherID: s.PublisherID, Verdict: "excluded",
				Reason: gateReason(GateNotConsumerDTC),
			})
		}
		return finish(log, Build(in), started), nil
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
	return finish(log, Build(in), started), nil
}

// finish logs the one-line outcome of a run and returns the campaign unchanged,
// so both exits from Run report themselves identically.
func finish(log *slog.Logger, c model.Campaign, started time.Time) model.Campaign {
	recommended := 0
	for _, e := range c.PublisherLedger {
		if e.Verdict == "recommended" {
			recommended++
		}
	}
	log.Info("run complete", "status", c.Status,
		"recommended", recommended, "of", len(c.PublisherLedger),
		"creatives", len(c.Creatives),
		"budget_usd", int64(c.Budget.TotalUSD),
		"took", logging.Elapsed(time.Since(started)))

	// Nonzero unallocated budget means the recommended set physically cannot
	// absorb the money. That is a real finding about the campaign, not a bug,
	// and it is invisible in the terminal summary unless you go looking.
	if c.Budget.UnallocatedUSD > 0 {
		log.Warn("budget exceeds inventory",
			"unallocated_usd", int64(c.Budget.UnallocatedUSD),
			"of_total_usd", int64(c.Budget.TotalUSD))
	}
	// Gate on the status, not merely on having clarifications: a
	// no_recommendation campaign also carries questions ("do you have a
	// consumer product?"), and calling that brief vague is wrong — the B2B
	// refusal is a confident answer, not an uncertain one.
	if c.Status == model.StatusNeedsClarification {
		log.Warn("brief needs clarifying",
			"questions", len(c.Clarifications), "confidence", c.Advertiser.Confidence)
	}
	return c
}

func gatedCount(scores []model.PublisherScore) int {
	n := 0
	for _, s := range scores {
		if s.HardGate != GateNone {
			n++
		}
	}
	return n
}
