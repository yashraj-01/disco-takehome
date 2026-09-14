package measure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/prompts"
)

// judgeCreative is what the judge model sees for one creative: a numbered
// index, headline, and body — nothing else. persona_id, rationale,
// messaging_levers and avoided are deliberately withheld: those are drawn
// verbatim from the persona record the creative was written for, so showing
// them would hand the judge the answer and make the whole metric worthless.
// See PersonaAttribution's doc comment.
type judgeCreative struct {
	Index    int    `json:"index"`
	Headline string `json:"headline"`
	Body     string `json:"body"`
}

// judgePersona is a catalog persona record rendered for the judge: enough to
// let it reason about fit (name, demographics, description, affinities,
// price sensitivity, messaging preferences, disinterests), plus id, which the
// judge must return in its assignment.
type judgePersona struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	AgeRange             string   `json:"age_range"`
	GenderSkew           string   `json:"gender_skew"`
	Description          string   `json:"description"`
	CategoryAffinities   []string `json:"category_affinities"`
	PriceSensitivity     string   `json:"price_sensitivity"`
	MessagingPreferences []string `json:"messaging_preferences"`
	DisinterestedIn      []string `json:"disinterested_in"`
}

// judgeInput is the full payload rendered as the judge stage's "## Input"
// JSON block.
type judgeInput struct {
	Creatives []judgeCreative `json:"creatives"`
	Personas  []judgePersona  `json:"personas"`
}

// judgeAssignment is one entry of the judge's response.
type judgeAssignment struct {
	CreativeIndex int    `json:"creative_index"`
	PersonaID     string `json:"persona_id"`
	Confidence    string `json:"confidence"`
	Rationale     string `json:"rationale"`
}

// judgeResponse is the judge stage's full decoded response.
type judgeResponse struct {
	Assignments []judgeAssignment `json:"assignments"`
}

func personaToJudge(p *catalog.Persona) judgePersona {
	return judgePersona{
		ID:                   p.ID,
		Name:                 p.Name,
		AgeRange:             p.AgeRange,
		GenderSkew:           p.GenderSkew,
		Description:          p.Description,
		CategoryAffinities:   p.CategoryAffinities,
		PriceSensitivity:     p.PriceSensitivity,
		MessagingPreferences: p.MessagingPreferences,
		DisinterestedIn:      p.DisinterestedIn,
	}
}

// seedFor derives a stable int64 seed from parts (joined with a NUL
// separator so "a","bc" and "ab","c" can never collide). Used to make the
// judge prompt's shuffles deterministic per brief: the same brief number
// always produces the same permutation, so a rerun renders byte-identical
// prompt text and replays from its recorded fixture instead of missing it
// and burning quota.
func seedFor(parts ...string) int64 {
	h := fnv.New64a()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return int64(h.Sum64())
}

// shuffledOrder returns a permutation of 0..n-1 (order[shown] = original
// index to place at that shown position), deterministic for a given seed.
func shuffledOrder(n int, seed int64) []int {
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	rand.New(rand.NewSource(seed)).Shuffle(n, func(i, j int) {
		order[i], order[j] = order[j], order[i]
	})
	return order
}

// controlPersonaIDs picks n persona IDs to pair with campaigns[idx]'s
// creatives for the control run: drawn from OTHER campaigns' actual persona
// selections (not invented ad hoc), preferring IDs that do not appear in
// real (the current campaign's own persona set) so that, by construction, no
// correct matching exists in the control task. Iterates other campaigns in a
// fixed, deterministic order (starting the one after idx, wrapping around),
// and only falls back to the raw catalog list if the other campaigns don't
// between them supply n distinct IDs — which cannot happen with the shipped
// catalog (10 personas total, campaigns of at most 5), but is guarded rather
// than assumed.
func controlPersonaIDs(campaigns []Campaign, idx, n int, real map[string]bool, cat *catalog.Catalog) []string {
	var preferred, fallback []string
	seen := make(map[string]bool, n*2)
	add := func(id string) {
		if id == "" || seen[id] || !cat.HasPersona(id) {
			return
		}
		seen[id] = true
		if real[id] {
			fallback = append(fallback, id)
		} else {
			preferred = append(preferred, id)
		}
	}
	for step := 1; step <= len(campaigns) && len(preferred) < n; step++ {
		other := (idx + step) % len(campaigns)
		if other == idx {
			continue
		}
		for _, cr := range campaigns[other].Campaign.Creatives {
			add(cr.PersonaID)
		}
	}

	ids := append(append([]string{}, preferred...), fallback...)
	if len(ids) > n {
		ids = ids[:n]
	}
	// Top up from the raw catalog if other campaigns didn't supply enough
	// distinct IDs. Two passes: IDs outside the real set first, then (only
	// if still short) IDs from the real set — never invent an ID outside the
	// catalog.
	for _, pass := range []bool{false, true} { // pass: include real-set IDs?
		for _, p := range cat.Personas {
			if len(ids) >= n {
				break
			}
			if seen[p.ID] {
				continue
			}
			if !pass && real[p.ID] {
				continue
			}
			ids = append(ids, p.ID)
			seen[p.ID] = true
		}
	}
	return ids
}

// fixtureKeyFor names the recorded judge response for one campaign and task
// kind ("real" or "control"). Deliberately keyed on the brief number and
// kind alone (not the rendered prompt), so it is stable across prompt edits —
// mirroring pipeline's own fixtureKey convention (see internal/pipeline/stage.go).
func fixtureKeyFor(kind string, briefN int) string {
	return "judge-" + llm.ShortHash(fmt.Sprintf("%s-%d", kind, briefN))
}

// callJudge renders the judge prompt, calls provider, validates the response
// against the judge schema, and decodes it. Mirrors pipeline.callStage,
// reimplemented here rather than exported from pipeline, since measure must
// not depend on (or modify) internal/pipeline.
func callJudge(ctx context.Context, provider llm.Provider, fixtureKey string, input judgeInput) (judgeResponse, error) {
	text, schema, err := prompts.Load("judge")
	if err != nil {
		return judgeResponse{}, fmt.Errorf("measure: judge: %w", err)
	}
	payload, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return judgeResponse{}, fmt.Errorf("measure: judge: encoding input: %w", err)
	}
	prompt := text + "\n\n## Input\n\n```json\n" + string(payload) + "\n```\n"

	out, err := provider.Complete(ctx, llm.Request{
		Stage: "judge", Prompt: prompt, Schema: schema, FixtureKey: fixtureKey,
	})
	if err != nil {
		return judgeResponse{}, err
	}
	if err := llm.Validate(schema, out); err != nil {
		return judgeResponse{}, fmt.Errorf("measure: judge: %w", err)
	}
	var resp judgeResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return judgeResponse{}, fmt.Errorf("measure: judge: decoding response: %w", err)
	}
	return resp, nil
}

// misattributionCap bounds how many misattribution findings the metric
// reports, so a bad run doesn't produce an unreadable wall of findings.
const misattributionCap = 8

// minConfidenceSeparation is the smallest real-vs-control gap in
// high-confidence rate this metric treats as evidence the judge is actually
// discriminating rather than pattern-matching noise. Below it, every number
// this metric produced (accuracy, lift included) is unreliable, and the
// Summary says so plainly rather than reporting a number that looks
// meaningful and isn't.
const minConfidenceSeparation = 0.10

// PersonaAttribution asks the one question CreativeDistinctiveness cannot
// answer: not "are these creatives different from each other" but "is each
// one actually right for the persona it claims to be written for." A judge
// model is shown each campaign's creatives — headline and body only, stripped
// of persona_id, rationale, messaging_levers and avoided, since those are
// drawn verbatim from the persona record and would make the task trivial —
// alongside that campaign's persona records with the labels removed, and
// asked to put the labels back. If the copy is genuinely persona-specific,
// the judge should do meaningfully better than chance; if it is generic copy
// dressed up with a persona's name, the judge has nothing to go on and
// should do no better than guessing.
//
// Every campaign judged also runs a control: the same creatives, paired with
// persona records drawn from a DIFFERENT campaign (see controlPersonaIDs),
// preferring ones that don't appear in the real set so that no correct
// matching exists by construction. Correctness is never scored on the
// control — there is nothing to score. What is scored is whether the judge
// answers "high" confidence there about as often as on the real task. If it
// does, it is not reading the copy at all; it is pattern-matching, and
// attribution_accuracy and lift must be discarded regardless of what they
// say — confidence_separation and the Summary's verdict say plainly whether
// that happened. This is why the control is not optional: without it, a
// judge that always guesses "high" and gets some fraction right by chance
// would look like a working measurement instead of a broken one.
//
// Both the creative list and each task's persona list are shuffled with a
// seed derived only from the brief number (see seedFor, shuffledOrder), so a
// rerun renders byte-identical prompt text and replays from its recorded
// fixture — the metric is exercised for free after the one recording run
// (see "Cost control" in the task brief).
//
// A campaign with fewer than 2 creatives is skipped (skipped_too_few): with
// N=1 there is exactly one persona to assign and chance is already 100%, so
// there is nothing to measure.
//
// Guarding the arithmetic: an assignment naming a creative_index outside
// [0,N) is dropped (dropped_out_of_range_index); one naming a persona_id
// that was not among those actually shown for that task is dropped
// (dropped_unknown_persona); and if the judge assigns the same persona (or
// the same creative_index) twice, every occurrence after the first is
// dropped (dropped_duplicate_persona). All three are counted, never a panic
// and never silently accepted.
func PersonaAttribution(ctx context.Context, provider llm.Provider, campaigns []Campaign, cat *catalog.Catalog) (Metric, error) {
	var (
		campaignsJudged, skippedTooFew, judgeCalls                        int
		creativesMatched, correct                                         int
		droppedOutOfRange, droppedUnknownPersona, droppedDuplicatePersona int
		highConfReal, realValid                                           int
		highConfControl, controlValid                                     int
		chanceSum                                                         float64
		findings                                                          []Finding
	)

	for i, c := range campaigns {
		n := len(c.Campaign.Creatives)
		if n < 2 {
			skippedTooFew++
			continue
		}

		// One shared creative shuffle for both the real and control task, so
		// the two are directly comparable index-for-index.
		order := shuffledOrder(n, seedFor("creatives", fmt.Sprint(c.BriefN)))
		shown := make([]judgeCreative, n)
		groundTruth := make([]string, n) // groundTruth[shown index] -> real persona_id
		for pos, orig := range order {
			cr := c.Campaign.Creatives[orig]
			shown[pos] = judgeCreative{Index: pos, Headline: cr.Headline, Body: cr.Body}
			groundTruth[pos] = cr.PersonaID
		}

		real := make(map[string]bool, n)
		var realIDs []string
		missingPersona := false
		for _, id := range groundTruth {
			if !real[id] {
				real[id] = true
				realIDs = append(realIDs, id)
			}
			if _, ok := cat.Persona(id); !ok {
				missingPersona = true
			}
		}
		if missingPersona {
			// Data integrity problem, not a judge problem: a creative names a
			// persona_id the loaded catalog doesn't have. Skip rather than
			// hand the judge an incomplete persona set it cannot possibly
			// match against.
			skippedTooFew++
			continue
		}

		realOrder := shuffledOrder(len(realIDs), seedFor("personas-real", fmt.Sprint(c.BriefN)))
		realPersonas := make([]judgePersona, len(realIDs))
		for pos, orig := range realOrder {
			p, _ := cat.Persona(realIDs[orig])
			realPersonas[pos] = personaToJudge(p)
		}

		controlIDs := controlPersonaIDs(campaigns, i, n, real, cat)
		controlValidIDs := make(map[string]bool, len(controlIDs))
		for _, id := range controlIDs {
			controlValidIDs[id] = true
		}
		controlOrder := shuffledOrder(len(controlIDs), seedFor("personas-control", fmt.Sprint(c.BriefN)))
		controlPersonas := make([]judgePersona, len(controlIDs))
		for pos, orig := range controlOrder {
			p, _ := cat.Persona(controlIDs[orig])
			controlPersonas[pos] = personaToJudge(p)
		}

		realResp, err := callJudge(ctx, provider, fixtureKeyFor("real", c.BriefN),
			judgeInput{Creatives: shown, Personas: realPersonas})
		if err != nil {
			if isNoFixture(err) {
				return skippedMetric(judgeCalls, campaignsJudged, skippedTooFew), nil
			}
			return Metric{}, fmt.Errorf("measure: persona_attribution: brief %d: real task: %w", c.BriefN, err)
		}
		judgeCalls++

		controlResp, err := callJudge(ctx, provider, fixtureKeyFor("control", c.BriefN),
			judgeInput{Creatives: shown, Personas: controlPersonas})
		if err != nil {
			if isNoFixture(err) {
				return skippedMetric(judgeCalls, campaignsJudged, skippedTooFew), nil
			}
			return Metric{}, fmt.Errorf("measure: persona_attribution: brief %d: control task: %w", c.BriefN, err)
		}
		judgeCalls++

		campaignsJudged++
		chanceSum += 1.0 / float64(n)

		usedPersona := make(map[string]bool, n)
		usedIndex := make(map[int]bool, n)
		for _, a := range realResp.Assignments {
			if a.CreativeIndex < 0 || a.CreativeIndex >= n {
				droppedOutOfRange++
				continue
			}
			if !real[a.PersonaID] {
				droppedUnknownPersona++
				continue
			}
			if usedPersona[a.PersonaID] || usedIndex[a.CreativeIndex] {
				droppedDuplicatePersona++
				continue
			}
			usedPersona[a.PersonaID] = true
			usedIndex[a.CreativeIndex] = true

			realValid++
			creativesMatched++
			if a.Confidence == "high" {
				highConfReal++
			}

			truth := groundTruth[a.CreativeIndex]
			if a.PersonaID == truth {
				correct++
			} else if len(findings) < misattributionCap {
				findings = append(findings, misattributionFinding(c.BriefN, shown[a.CreativeIndex].Headline, truth, a, cat))
			}
		}

		// Control task: confidence rate only. No correctness is scored here —
		// by construction there is no correct matching to score.
		usedPersonaControl := make(map[string]bool, n)
		usedIndexControl := make(map[int]bool, n)
		for _, a := range controlResp.Assignments {
			if a.CreativeIndex < 0 || a.CreativeIndex >= n {
				continue
			}
			if !controlValidIDs[a.PersonaID] {
				continue
			}
			if usedPersonaControl[a.PersonaID] || usedIndexControl[a.CreativeIndex] {
				continue
			}
			usedPersonaControl[a.PersonaID] = true
			usedIndexControl[a.CreativeIndex] = true

			controlValid++
			if a.Confidence == "high" {
				highConfControl++
			}
		}
	}

	values := map[string]float64{
		"attribution_accuracy":         safeDivF(float64(correct), float64(creativesMatched)),
		"chance_baseline":              safeDivF(chanceSum, float64(campaignsJudged)),
		"high_confidence_rate_real":    safeDivF(float64(highConfReal), float64(realValid)),
		"high_confidence_rate_control": safeDivF(float64(highConfControl), float64(controlValid)),
	}
	values["lift"] = values["attribution_accuracy"] - values["chance_baseline"]
	values["confidence_separation"] = values["high_confidence_rate_real"] - values["high_confidence_rate_control"]

	valid := values["confidence_separation"] >= minConfidenceSeparation
	verdict, explanation := "INVALID", "the judge is about as confident on the impossible control as on the "+
		"real task, which means it is pattern-matching rather than reading the copy — attribution_accuracy "+
		"and lift above must be discarded; they do not mean what they appear to."
	if valid {
		verdict, explanation = "VALID", "the judge is meaningfully more confident on the real task than on "+
			"the control it cannot possibly solve, so the accuracy numbers above reflect real signal."
	}

	summary := fmt.Sprintf(
		"%d campaigns judged (%d skipped for fewer than 2 creatives): attribution_accuracy=%.3f vs "+
			"chance_baseline=%.3f (lift=%+.3f). high_confidence_rate real=%.3f vs control=%.3f "+
			"(confidence_separation=%.3f, threshold=%.2f). Judge validity: %s — %s",
		campaignsJudged, skippedTooFew,
		values["attribution_accuracy"], values["chance_baseline"], values["lift"],
		values["high_confidence_rate_real"], values["high_confidence_rate_control"],
		values["confidence_separation"], minConfidenceSeparation, verdict, explanation)

	return Metric{
		Name:    "persona_attribution",
		Summary: summary,
		Values:  values,
		Counts: map[string]int{
			"campaigns_judged":           campaignsJudged,
			"creatives_matched":          creativesMatched,
			"correct":                    correct,
			"skipped_too_few":            skippedTooFew,
			"judge_calls":                judgeCalls,
			"dropped_out_of_range_index": droppedOutOfRange,
			"dropped_unknown_persona":    droppedUnknownPersona,
			"dropped_duplicate_persona":  droppedDuplicatePersona,
		},
		Findings: findings,
	}, nil
}

// misattributionFinding renders one incorrect real-task assignment as a
// quotable finding: which brief, the headline, the persona it was actually
// written for, which persona the judge chose instead, and the judge's own
// rationale — so a reader can judge the miss for themselves.
func misattributionFinding(briefN int, headline, truthID string, a judgeAssignment, cat *catalog.Catalog) Finding {
	truthName := truthID
	if p, ok := cat.Persona(truthID); ok {
		truthName = p.Name
	}
	chosenName := a.PersonaID
	if p, ok := cat.Persona(a.PersonaID); ok {
		chosenName = p.Name
	}
	return Finding{
		BriefN:      briefN,
		PublisherID: fmt.Sprintf("creative %d", a.CreativeIndex),
		Detail: fmt.Sprintf(
			"%q was written for %s, judge assigned %s at %s confidence — %q",
			headline, truthName, chosenName, a.Confidence, a.Rationale),
	}
}

// isNoFixture reports whether err is (or wraps) llm.ErrNoFixture — a fixture
// MISS, as opposed to a real failure (bad schema, decoding error, network
// error from a live provider). Chain wraps a miss with fmt.Errorf("%w", ...)
// when it has no fallback, so errors.As is required rather than a type
// assertion.
func isNoFixture(err error) bool {
	var missing *llm.ErrNoFixture
	return errors.As(err, &missing)
}

// skippedMetric reports that PersonaAttribution could not run at all: under
// --provider=fixture with nothing recorded yet for stage "judge", the first
// call misses. Returned with a nil error so a fresh checkout's "disco
// measure --provider=fixture" reports one metric as skipped rather than
// failing the whole run.
func skippedMetric(judgeCalls, campaignsJudged, skippedTooFew int) Metric {
	return Metric{
		Name: "persona_attribution",
		Summary: fmt.Sprintf(
			"skipped: no recorded fixture for stage \"judge\" (--provider=fixture with nothing recorded "+
				"yet). Record it with a live provider (GEMINI_API_KEY set, --provider=gemini --record) so "+
				"evals/fixtures/judge-*.json exists, then this metric replays offline for free. "+
				"(%d judge calls completed before the miss, %d campaigns fully judged, %d skipped for "+
				"fewer than 2 creatives.)",
			judgeCalls, campaignsJudged, skippedTooFew),
		Values: map[string]float64{
			"attribution_accuracy": 0, "chance_baseline": 0, "lift": 0,
			"high_confidence_rate_real": 0, "high_confidence_rate_control": 0, "confidence_separation": 0,
		},
		Counts: map[string]int{
			"campaigns_judged": campaignsJudged, "creatives_matched": 0, "correct": 0,
			"skipped_too_few": skippedTooFew, "judge_calls": judgeCalls,
			"dropped_out_of_range_index": 0, "dropped_unknown_persona": 0, "dropped_duplicate_persona": 0,
		},
	}
}
