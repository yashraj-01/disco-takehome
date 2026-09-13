# Disco Take-Home — Ad Placement & Creative Generation: Design

**Date:** 2026-09-13
**Status:** Approved, pending implementation plan
**Stack:** Go 1.25, `anthropic-sdk-go`, `claude-opus-5`

---

## 1. Objective

One advertiser sentence in. Three artifacts out:

1. Ranked publishers from a fixed catalog of 20, with reasons for inclusion **and exclusion**
2. 3–5 ad creatives, each written for a named shopper persona, with the persona choice justified
3. A structured campaign config a downstream system could act on

Delivered as a Go CLI with an embedded web view, plus an eval harness over the 15 supplied example briefs.

The exercise is not testing ad-tech knowledge. It is testing whether an LLM pipeline can make **defensible decisions on fuzzy input and show its work**.

## 2. Scope

**In:**
- 6-stage pipeline: 4 LLM stages, 2 pure-function stages
- Exclusion ledger covering all 20 publishers on every run
- Degraded modes for non-addressable and low-signal briefs
- `disco run` (terminal), `disco serve` (embedded HTML), `disco eval` (assertions over 15 briefs)
- Prompts and JSON Schemas as reviewable files under `prompts/`

**Out (deliberate cuts, to be stated in README):**
image creative · auction simulation · vector search / RAG (catalog is ~10KB, it fits in a prompt) · persistence · auth · multi-tenancy · real rate card · A/B measurement · frequency capping · personas beyond the 10 supplied

## 3. Architecture

```
brief ─▶ (1) profile ─▶ (2) scoring ─▶ (3) fit ─▶ (4) personas ─▶ (5) creative ─▶ (6) campaign ─▶ output
          LLM            PURE           LLM        LLM             LLM            PURE
```

Stage 2 and 6 have no network dependency and are unit-tested. Every dollar figure in the output originates in stage 6, never in an LLM response.

### Repo layout

```
cmd/disco/main.go                run | serve | eval
internal/catalog/                load + validate publishers.json / shopper_personas.json; ID allow-lists
internal/llm/                    Anthropic client: structured output, schema repair, prompt cache, disk cache
internal/pipeline/
    profile.go    stage 1  LLM
    scoring.go    stage 2  PURE   <- unit tested
    fit.go        stage 3  LLM
    personas.go   stage 4  LLM
    creative.go   stage 5  LLM (parallel per persona)
    campaign.go   stage 6  PURE   <- unit tested
internal/render/                 terminal + JSON renderers
web/index.html                   go:embed, served by `disco serve`
prompts/<stage>.md               prompt text
prompts/<stage>.schema.json      output contract, loaded at runtime, fed to output_config.format
evals/briefs.txt
evals/assertions.go
evals/snapshots/
data/                            supplied catalog, unmodified
```

## 4. Data contracts

```go
type AdvertiserProfile struct {
    RawBrief         string
    PrimaryCategory  string   // publisher category vocabulary, or "unknown"
    Subcategories    []string
    PriceTier        string   // budget | mid | premium | luxury
    EstimatedAOVUSD  int
    TargetAgeMin     int
    TargetAgeMax     int
    TargetGenderSkew string   // female | male | balanced | unknown
    Values           []string // sustainability | craftsmanship | science_backed | convenience | value | aesthetic
    BusinessModel    string   // subscription | one_off | b2b | service
    IsConsumerDTC    bool
    Confidence       string   // high | medium | low
    Assumptions      []string
    MissingSignals   []string
}

type SubScores struct {
    Category, AOVAlignment, AgeOverlap, ValuesMatch, GenderFit, IncomeTier float64
}

type PublisherScore struct {
    PublisherID string
    Score       float64   // weighted sum, 0..1
    Sub         SubScores
    HardGate    string    // "" | not_consumer_dtc | category_mismatch | demographic_mismatch
}

type FitVerdict struct {
    PublisherID string
    Verdict     string // recommended | considered | excluded
    Rank        int    // recommended only
    Reason      string
}

type PersonaPick struct {
    PersonaID         string
    Rationale         string
    PrimaryPublishers []string
}

type Creative struct {
    PersonaID           string
    Headline            string   // <= 60 chars
    Body                string   // <= 200 chars
    Rationale           string
    SuggestedPublishers []string
    MessagingLevers     []string // must cite >=1 entry from that persona's messaging_preferences
    Avoided             []string // entries from that persona's disinterested_in
}
```

`MessagingLevers` / `Avoided` force copy to ground itself in the persona record rather than gesture at it, and give the eval something checkable.

## 5. Stage specifications

### Stage 1 — `profile` (LLM)

Brief text → `AdvertiserProfile`. Sets `Confidence` and `IsConsumerDTC`, which gate everything downstream. Catalog categories are supplied in the prompt so `PrimaryCategory` lands in the same vocabulary the publishers use.

### Stage 2 — `scoring` (pure)

For each of the 20 publishers, six sub-scores in 0..1, weighted:

| Signal | Weight | Computation |
|---|---:|---|
| `Category` | 0.30 | exact category = 1.0; else subcategory overlap = `0.4 + 0.5×jaccard`; else adjacency-map hit = 0.5; else 0.0 |
| `AOVAlignment` | 0.20 | `r = min(a,p)/max(a,p)`; `clamp((r−0.25)/0.75, 0, 1)` |
| `AgeOverlap` | 0.15 | `overlap / min(profileRange, publisherRange)` |
| `ValuesMatch` | 0.15 | `\|profile.Values ∩ publisherKeywords\| / \|profile.Values\|`; 0.5 when profile has no values |
| `GenderFit` | 0.10 | `clamp((pubShare − 0.3)/0.5, 0, 1)`; 0.5 when skew unknown |
| `IncomeTier` | 0.10 | `1 − 0.5 × ordinalDistance(priceTier, incomeTier)` |

`publisherKeywords` is precomputed once per publisher from `notes` + `subcategories`. Keyword matching on free text is crude by design — stage 3 receives the raw `notes` string and can override the *verdict* with a written reason.

**Adjacency map** (explicit, in code; symmetric — stored one-way, closed on load): pet↔subscription_boxes · wellness_dtc↔{wellness_services, beauty, groceries} · wellness_services↔{wellness_dtc, apparel, beauty} · apparel↔{beauty, home} · groceries↔{meal_kits, beverages, wellness_dtc} · beverages↔{groceries, wellness_dtc, instant_delivery} · home↔{apparel, groceries} · beauty↔{apparel, wellness_dtc} · meal_kits↔{groceries, instant_delivery} · instant_delivery↔{groceries, meal_kits, beverages}

**Hard gates** — force `excluded`, not overridable by the LLM:
1. `!IsConsumerDTC` → every publisher excluded (brief #7, dental SaaS)
2. `Category == 0` → `category_mismatch`
3. `AgeOverlap == 0` → `demographic_mismatch` (this is what excludes Linden Park, 50–70, from a 25–45 activewear brief)

### Stage 3 — `fit` (LLM)

Receives all 20 publishers with their full records, sub-scores, and gate status. Emits a verdict, rank, and reason for each. The LLM may move a publisher between `recommended` / `considered` / `excluded` and order the recommended set. It may **not** override a hard gate, and it does **not** produce the fit number used for budget math — allocation always uses the deterministic stage-2 score. A publisher the LLM promotes against a low score therefore appears with a small allocation plus the LLM's stated reason, which is visible rather than hidden.

### Stage 4 — `personas` (LLM)

Picks 3–5 of the 10 personas with rationale, and records why the others were rejected. Persona IDs constrained to the catalog allow-list.

### Stage 5 — `creative` (LLM, parallel)

One goroutine per selected persona via `errgroup`. Each call sees only that persona's record plus the advertiser profile, so copy can't blur across personas. Must populate `MessagingLevers` from the persona's actual `messaging_preferences`.

### Stage 6 — `campaign` (pure)

Assembles the config. Owns all arithmetic — CPM derivation and budget allocation, both below.

## 6. Derived CPM

The catalog has no price field. Rather than have the LLM invent one (non-reproducible, indefensible), CPM is computed from the three fields that proxy audience value:

```
base      = 8 + income_premium + category_premium
            income:   mid +0 · mid-high +4 · high +9
            category: groceries/instant_delivery/meal_kits +0 · beverages +1 ·
                      apparel +2 · pet/wellness_dtc/wellness_services/beauty +3 · home +4

aov_index = (publisher_aov − 28) / (198 − 28)        // catalog min/max
est_cpm   = base × (1 + 0.15 × aov_index)
```

AOV is capped at a +15% effect so it modifies rather than drives the number — at full weight the ranking would collapse into "sort by AOV".

Spot values: Swiftcart $8.00 · Ruffco $11.44 · Pawline $15.48 · Pantrygood $17.87 · Hearthstone $24.15. Range $8–$24, consistent with real display CPMs.

**This is a stand-in for a rate card, isolated in one function.** The README says so plainly; swapping in real rates changes nothing else.

## 7. Budget allocation

Four steps, all thresholds exposed as CLI flags.

1. **Raw share** `∝ fit^γ`, γ = 1.5 (`--gamma`). Plain proportional splitting is too flat: fits of 0.84 and 0.44 differ 1.9× proportionally, 2.6× at γ=1.5. γ=1 is proportional, γ→∞ is winner-take-all.
2. **Concentration cap** 40% per publisher (`--max-share`). Media-buying practice: concentration risk plus frequency saturation. Surplus redistributes pro-rata.
3. **Deliverability cap** 15% share-of-voice (`--sov-cap`). `impressions_needed = dollars / est_cpm × 1000` compared against `monthly_impressions × 0.15`. Surplus redistributes.
4. **Dust floor** 5% (`--min-share`). Sub-floor publishers are dropped and their money redistributed; a $750 slice buys ~50k impressions, below useful frequency and too thin to learn from.

Steps 2–4 are **iterated to a fixed point** (max 5 passes), because redistribution can push a survivor back over the 40% cap or a new publisher under the floor. Final shares are renormalized to exactly 1.0. If no publisher clears the floor, the single highest-fit publisher takes 100%.

Worked, dog-food brief, four surviving publishers:

| | fit | share | $ @ 25k | est_cpm | impressions | 15% SOV cap | |
|---|---:|---:|---:|---:|---:|---:|:--|
| Pawline | 0.84 | 37.8% | 9,458 | 15.48 | 611,124 | 720,000 | ok |
| Ruffco | 0.71 | 29.4% | 7,350 | 11.44 | 642,638 | 9,300,000 | ok |
| Pantrygood | 0.52 | 18.4% | 4,607 | 17.87 | 257,788 | 1,560,000 | ok |
| Tailcrate | 0.44 | 14.3% | 3,586 | 11.07 | 323,962 | 1,260,000 | ok |

At a $50,000 budget Pawline's 37.8% needs 1,222,247 impressions against a 720,000 cap — it clips to $11,146 and $7,770 redistributes. The system can then state something an LLM alone would not: *the best-matched publisher cannot absorb this budget*. That conclusion requires joining a derived price against an inventory constraint.

## 8. Campaign config shape

```json
{
  "status": "ready | needs_clarification | no_recommendation",
  "advertiser": { "brief", "derived_profile", "confidence", "assumptions[]", "missing_signals[]" },
  "objective": "awareness | consideration | conversion",
  "flight": { "start", "duration_days" },
  "budget": {
    "total_usd", "daily_cap_usd",
    "allocation": [{ "publisher_id", "share", "amount_usd", "est_cpm_usd", "est_impressions", "rationale" }]
  },
  "bid": { "model": "CPM", "strategy", "floor_usd", "target_usd", "ceiling_usd", "target_cpa_usd", "reasoning" },
  "targeting": { "age_range", "gender_skew", "geos[]", "income_tiers[]", "persona_ids[]", "contextual_categories[]" },
  "creatives": [{ "persona_id", "headline", "body", "rationale", "suggested_publishers[]", "messaging_levers[]", "avoided[]" }],
  "publisher_ledger": [ { "publisher_id", "verdict", "score", "sub_scores", "reason" } ],
  "meta": { "generated_at", "model", "pipeline_version" }
}
```

`publisher_ledger` always contains exactly 20 entries.

**`objective`** derives deterministically: `b2b` or `service` business model → `consideration`; `subscription` → `conversion`; `luxury` price tier → `awareness`; otherwise `conversion`.

**`bid`** derives from the allocation's share-weighted `est_cpm` (call it `w`): `floor = 0.8w`, `target = w`, `ceiling = 1.4w`. `strategy` is `target_cpa_capped` when `EstimatedAOVUSD > 0`, else `even_pacing`. `target_cpa_usd = 0.35 × EstimatedAOVUSD`, a 35%-of-AOV acquisition ceiling stated as an assumption in the README rather than presented as a derived truth.

## 9. Degraded modes

| Trigger | `status` | Behavior |
|---|---|---|
| `!IsConsumerDTC` (#7 dental SaaS) | `no_recommendation` | All 20 excluded with one honest reason. No creatives, no budget. |
| `Confidence == low` (#5, #8, #15) | `needs_clarification` | Inferred profile presented as a hypothesis; `missing_signals` listed; 2–3 specific questions; a clearly-labeled provisional campaign still emitted |
| `Confidence == medium` | `ready` | Runs with an assumptions banner |

Refusing `"idk just try it"` outright is as wrong as confidently campaigning on it. The system shows the guess *and* the uncertainty.

## 10. LLM integration

- **Model** `claude-opus-5`, adaptive thinking. `output_config.effort`: `medium` for stages 1 and 4, `high` for 3 and 5.
- **Structured output** via `output_config.format` fed from `prompts/<stage>.schema.json` — one file serves as the LLM contract, the runtime validator (`santhosh-tekuri/jsonschema`), and the `prompts/` deliverable.
- **Schema repair**: on validation failure, one retry feeding the validator error back. Second failure is a hard error, not a silent default.
- **Grounding**: after unmarshal, any `publisher_id` / `persona_id` outside the catalog allow-list is dropped. An empty required field after filtering fails loudly.
- **Prompt caching**: the ~10KB catalog is an identical prefix across all stages and all 15 eval briefs — cached with a breakpoint after the catalog block. Verify via `usage.cache_read_input_tokens`.
- **Disk cache** keyed by `sha256(stage + prompt + schema + input)` under `.cache/`, so `serve` and repeated `eval` runs are fast and cheap. `--no-cache` bypasses.

## 11. Eval

`disco eval` runs all 15 briefs and asserts.

**Universal:** every publisher/persona ID exists in the catalog · ledger has exactly 20 entries · 3–5 creatives · no duplicate persona · allocation shares sum to 1.0 ± 0.001 · output validates against the campaign schema · every creative cites ≥1 real `messaging_preference` from its persona.

**Per-brief:**

| Brief | Assertion |
|---|---|
| #7 dental SaaS | `status == no_recommendation` |
| #5, #8, #15 | `status == needs_clarification`, `missing_signals` non-empty |
| #2 activewear | Linden Park (pub_005) excluded, reason cites demographics |
| #1 dog food | Pawline (pub_007) in top 2 |
| #10 $1,200 handbags | zero allocation to Swiftcart (pub_001, AOV 28) |
| #6 $650 ski shells | excluded from mid-income publishers |

Plus snapshot diffs under `evals/snapshots/` so a prompt edit shows its blast radius.

The eval asserts **structural and judgment invariants**, not copy quality. Whether a headline is good is not measurable in this budget; whether the system refused when it should have refused is binary and cheap. The README says this explicitly — creative quality is the genuinely hard part precisely because it cannot be asserted.

## 12. CLI surface

```
disco run "<brief>" [--budget 25000] [--days 30] [--json]
                    [--gamma 1.5] [--max-share 0.40] [--sov-cap 0.15] [--min-share 0.05]
                    [--no-cache]
disco serve [--port 8080]
disco eval  [--brief N] [--update-snapshots]
```

`serve` renders the identical pipeline output in one `go:embed`-ed HTML page — no npm, no build step, same binary.

## 13. Testing

- `internal/pipeline/scoring_test.go` — table-driven, no network. Covers each sub-score, each hard gate, and the Linden Park / Swiftcart exclusion cases.
- `internal/pipeline/campaign_test.go` — table-driven. Covers CPM derivation against the spot values above, all four allocation steps, the $50k clip case, and sum-to-1.0.
- `internal/catalog/catalog_test.go` — golden load, ID allow-list construction.
- `go test ./...` requires no API key. `disco eval` requires network and a key.

## 14. Risks

| Risk | Mitigation |
|---|---|
| Creative quality is unmeasurable | Stated openly in README; eval asserts grounding, not taste |
| Keyword matching on free-text `notes` is brittle | Only 15% of the score; stage 3 sees raw notes and can override the verdict |
| CPM proxy is invented | Isolated in one function, labeled as a stand-in, spot values sanity-checked against real display ranges |
| LLM stage non-determinism | Disk cache; assertions are structural, never exact-text |
| No `ANTHROPIC_API_KEY` in the dev environment | Must be resolved before any LLM stage runs |

## 15. Prerequisite

`ANTHROPIC_API_KEY` is not set and the `ant` CLI is not installed. One of the two is required before stages 1, 3, 4, or 5 can execute. Stages 2 and 6 and their tests run without it.
