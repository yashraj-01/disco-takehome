# disco

An advertiser describes their business in a sentence. This drafts the campaign:
which publishers to run on and why, which ones to skip and why, 3–5 ad creatives
written for specific shopper personas, and a campaign config a downstream system
could act on.

## Run it

Get a Gemini key — free, no payment method: <https://aistudio.google.com/apikey>.

```bash
cp .env.example .env          # put your key in it
set -a; source .env; set +a   # no dotenv loader: the shell loads it
```

```bash
go run ./cmd/disco serve      # campaign UI      → http://localhost:8080
go run ./cmd/disco measure    # recompute the four quality metrics
go run ./cmd/disco metrics    # metrics dashboard → http://localhost:8090

# both UIs together
go run ./cmd/disco serve & go run ./cmd/disco metrics; kill %1
```

In VS Code, hit F5 on the committed **disco — campaign UI + metrics** compound.

Both servers pick a free port if the default is taken and print the one they
bound. `measure` is what refreshes the dashboard — drafting a campaign doesn't.
Mind the free-tier daily request cap; it's per model, and `--model` picks a
different pool.

## How it works

Six stages — profile, scoring, fit, personas, creative, campaign. Four call a
model; scoring and campaign are pure functions.

**Every number comes from the pure stages**
(`internal/pipeline/{scoring,cpm,allocate,campaign}.go`). A model supplies
judgment and prose, never a figure that reaches the config. Hard gates can't be
overridden, publisher and persona IDs are filtered against the catalog, and a
forgotten publisher is backfilled — enforced in code, not asked for in a prompt.
`unallocated_usd` reports budget that's physically inventory-capped rather than
pretending it got placed. A B2B brief (#7) returns `no_recommendation` after
scoring; a vague brief (#5, #8, #15) returns `needs_clarification` with the
questions we'd ask.

**Measured, not asserted** (`internal/measure`): whether a stated exclusion
reason is *true* against the sub-score it cites, whether each scoring weight is
load-bearing, whether the per-persona creatives actually differ, and whether a
blind judge can match copy back to its persona — validated against a control it
cannot possibly solve.

**The model.** Gemini Flash Lite's free tier; no API budget was available. The
copy is weaker than a frontier model would write, and copy quality is graded
here, so I'd rather say that than let the demo hide it. The provider seam
(`internal/llm`) makes the model a flag.

## What I'd do next week

1. **Fix the AOV sub-score.** It measures proximity to the advertiser's price,
   which goes completely flat on 3 of 15 briefs — every publisher scores 0 for a
   $1,200 product in a $28–198 catalog. Purchase-mode banding is the candidate.
2. **Learn the scoring weights** from outcome data instead of asserting them.
3. **Real inventory and pricing**, replacing `EstimateCPM` and the flat SOV cap.

## What I cut, and why

**Image creative, auction simulation, multi-tenancy, auth, persistence.** All
real in production; none would have told you anything about how I think. Same
for **vector search** — the catalog is 10KB and fits in a prompt, so embedding
20 records would have been cargo-culted retrieval.

**Config schema validation and `retry-after`.** Per-stage model output is
schema-checked; the final emitted config isn't. The client backs off on a fixed
exponential schedule rather than honouring a 429's `retry-after`.

## What's actually hard here

**Easy:** matching a pet brand to a pet publisher — any approach gets it. Also
easy: structured-output plumbing, schemas, validation, retries.

**Hard, and where the engineering lives:**

*Knowing when to say no.* Three of the 15 briefs are deliberately low-signal and
one (B2B dental SaaS) has no valid answer in a consumer catalog. A model asked
to rank publishers will always rank publishers. Making refusal a first-class
outcome, gated in code upstream of the ranking so it can't be argued out of, was
the highest-value decision here.

*Exclusion reasoning.* Recommending is easy; explaining a non-obvious rejection
is not. A publisher is excluded from an activewear brief because its audience
skews 50–70 against a 25–45 target — arithmetic, done before the model sees
anything, which is why it's reliable.

*The join between derived price, inventory ceiling and budget.* A model will
fluently hallucinate past this: assert a split that overspends a publisher's
sellable inventory, or ignore that a capped impression volume caps the dollars
too. Holding that join — target share, then inventory ceiling, then redistribute
what didn't fit, never exceeding a physical limit — is the least glamorous code
here and the part I'd defend hardest.

*Copy that isn't interchangeable.* One call per persona, each seeing only that
persona, is what makes the variants differ. Measured: mean lever overlap 0.005,
and a blind judge matched all 42 creatives to their persona. Still the weakest
output, because whether a headline is *good* is a taste judgment none of the
metrics can assert.
