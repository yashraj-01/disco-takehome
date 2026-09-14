# disco

An advertiser describes their business in a sentence. This drafts the campaign:
which publishers to run on and why, which ones to skip and why, 3–5 ad creatives
written for specific shopper personas, and a campaign config a downstream system
could act on.

## Run it

Fixtures are committed, so everything below runs offline with **no API key**:

```bash
go run ./cmd/disco serve      # http://localhost:8080
go run ./cmd/disco run "We sell premium dog food for senior dogs, targeting owners who care about joint health and longevity. Grain-free, vet-formulated, subscription-based."
go run ./cmd/disco eval       # all 15 example briefs — 15/15 passing
go test ./...
```

To regenerate them against the live model, get a free key (no payment method:
<https://aistudio.google.com/apikey>), then:

```bash
cp .env.example .env          # put your key in it
set -a; source .env; set +a   # no dotenv loader: the shell loads it
go run ./cmd/disco eval --provider gemini --record
```

Mind the free-tier daily cap — it is per model and small (20/day on
20/day on some models). `--model` picks a different quota pool.

## How it works

Six stages — profile, scoring, fit, personas, creative, campaign. Four call a
model; scoring and campaign are pure functions.

**Every number comes from the pure stages** (`internal/pipeline/{scoring,cpm,allocate,campaign}.go`);
a model supplies judgment and prose, never a figure that reaches the config.
Hard gates can't be overridden, publisher/persona IDs are filtered against the
catalog, and a forgotten publisher is backfilled — all enforced in code, not
asked for in a prompt. `EstimateCPM` derives a rate from income tier, category,
and AOV, a one-function stand-in for a real rate card. `unallocated_usd`
reports budget that's physically inventory-capped rather than pretending it
got placed; `exceeds_max_share` marks a publisher whose share exceeds the 40%
concentration guideline, relaxed because it couldn't be satisfied for that
publisher set — worded as an effect, not a claimed cause, since more than one
condition can trigger it. A B2B brief (#7) returns `no_recommendation` after
scoring; a vague brief (#5, #8, #15) returns `needs_clarification` with an
inferred profile and the questions we'd ask.

**The model.** This runs on Gemini 3.5 Flash Lite's free tier — no API budget was
available. The ad copy is weaker than a frontier model would write, and copy
quality is one of the things this exercise grades, so I'd rather say that
plainly than let the demo hide it. The provider seam (`internal/llm`) means
regenerating the fixtures against a better model is a one-file change.

## What I'd do next week

1. **Measure creative quality** with a pairwise LLM-judge — the eval asserts
   copy is grounded, not that it's good.
2. **Learn the scoring weights** in `scoring.go` from outcome data instead of
   asserting them.
3. **Real inventory and pricing**, replacing `EstimateCPM` and the flat SOV cap.

## What I cut, and why

**Image creative, auction simulation, multi-tenancy, auth, persistence.** All
real in production; none would have told you anything about how I think. Same
for **vector search** — the catalog is 10KB and fits in a prompt, so embedding
20 records would have been cargo-culted retrieval.

**Eval snapshots, config schema validation, `retry-after`.** No golden-file
`--update-snapshots` mode; per-stage LLM output is schema-checked but the
final emitted campaign config isn't; the Gemini client backs off on a fixed
exponential schedule rather than honoring a 429's `retry-after`. The design
spec names the serve flag `--port` — the code (and this README) uses `--addr`.

## What's actually hard here

**Easy:** matching a pet brand to a pet publisher — any approach gets it. Also
easy: the structured-output plumbing, schemas, validation, retries. Fiddly,
not hard.

**Hard, and where the engineering actually lives:**

*Knowing when to say no.* Three of the 15 example briefs are deliberately
low-signal, and one (dental SaaS, B2B) has no valid answer in a consumer
catalog at all. A model asked to rank publishers will always rank publishers.
Making refusal a first-class outcome, gated in code upstream of the ranking so
it can't be argued out of, was the single highest-value decision in this
build.

*Exclusion reasoning.* Recommending is easy; explaining a non-obvious
rejection is not. A publisher gets excluded from an activewear brief because
its audience skews 50–70 against a 25–45 target — that's arithmetic, done
before the model sees anything, which is why it's reliable.

*The join between derived price, inventory ceiling, and budget.* A model will
fluently hallucinate past this: assert a budget split that overspends a
publisher's actual sellable inventory, or ignore that a derived CPM times a
capped impression volume caps the dollars too. Getting the allocator to hold
that join — target share, then inventory ceiling, then redistribute what
didn't fit, without ever exceeding a physical limit — is the least glamorous
code here and the part I'd defend hardest.

*Copy that isn't interchangeable.* Asking one call for five variants gets five
paraphrases of one idea. One call per persona, seeing only that persona, is
what makes them actually differ. It's also still the weakest output, because
whether a headline is *good* is a taste judgment the eval can't assert — it
checks structural and judgment invariants (grounding, refusal, exclusion
correctness), not taste, and that's an honest limitation, not an excuse.
