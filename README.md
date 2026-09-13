# disco

An advertiser describes their business in a sentence. This drafts the campaign:
which publishers to run on and why, which ones to skip and why, 3–5 ad creatives
written for specific shopper personas, and a campaign config a downstream system
could act on.

## Run it

**Fixtures have not been recorded yet.** `evals/fixtures/` is empty — no
`GEMINI_API_KEY` was available while building this. A fresh clone will fail on
`disco run`, `disco eval`, and a submit from `disco serve` with an actionable
error saying exactly this. Fix it once:

```bash
export GEMINI_API_KEY=...     # free, no card required: aistudio.google.com
go run ./cmd/disco eval --provider gemini --record
```

That's about 120 calls (15 briefs × ~4 model-calling stages) against a 250/day
free-tier limit, written to `evals/fixtures/` as they come back. After this one
run, everything below replays offline forever and the key is never needed
again:

```bash
go run ./cmd/disco serve      # http://localhost:8080
go run ./cmd/disco run "We sell premium dog food for senior dogs, vet-formulated, subscription-based."
go run ./cmd/disco eval       # all 15 example briefs, offline, against the recorded fixtures
go test ./...
```

## How it works

Six stages. Four call a model; two are pure functions.

```
brief ─▶ profile ─▶ scoring ─▶ fit ─▶ personas ─▶ creative ─▶ campaign
          LLM        PURE       LLM     LLM         LLM        PURE
```

**Every number in the output comes from the pure stages.** Publisher scores,
CPMs, budget shares, impressions, and bid ranges are computed in
`internal/pipeline/{scoring,cpm,allocate,campaign}.go`. A model never returns a
figure that reaches the config; it supplies judgment and prose, arithmetic
supplies the money.

Three things the model is not trusted with, enforced in code rather than asked
for in a prompt: hard gates can't be overridden, publisher and persona IDs are
filtered against the catalog, and a publisher the model forgot is backfilled so
the ledger always covers all 20.

Two fields in the config exist only because the allocator ran into real
constraints I hadn't designed for up front. `unallocated_usd` is budget that
physically cannot be spent: at a $500k budget on the pet brief, one publisher
qualifies, its inventory caps out at 720,000 impressions, and the system
reports ~$488,857 unallocated rather than pretending the rest got placed.
`exceeds_max_share` fires when the 40% concentration cap has to relax because
it isn't satisfiable — that needs at least three qualifying publishers.

**The catalog has no CPM.** `EstimateCPM` derives one from income tier,
category, and AOV — a stand-in for a rate card, isolated in one function so
swapping it changes nothing else.

**Degraded modes.** A B2B brief (#7) returns `no_recommendation` and stops
after scoring rather than spending three more calls. A vague brief (#5, #8,
#15) returns `needs_clarification`: the inferred profile shown as a hypothesis,
the specific questions we'd need answered, and a provisional campaign clearly
labelled as such.

**The model.** This runs on Gemini 2.5 Flash's free tier — no API budget was
available. The ad copy is weaker than a frontier model would write, and copy
quality is one of the things this exercise grades, so I'd rather say that
plainly than let the demo hide it. The provider seam (`internal/llm`) means
regenerating the fixtures against a better model is a one-file change.

## What I'd do next week

1. **Measure creative quality.** The eval asserts copy is *grounded* — every
   messaging lever appears verbatim in its persona record — but not that it's
   *good*. A pairwise LLM-judge over held-out variants, validated against human
   spot-checks, is the first thing I'd build.
2. **Learn the scoring weights instead of asserting them.** The six weights in
   `internal/pipeline/scoring.go` are my judgment; with outcome data they'd be
   fit, and the pure layer is already shaped to accept that.
3. **Real inventory and pricing**, replacing `EstimateCPM` and the flat SOV cap
   with forecasts per placement, plus letting the user edit the derived
   profile and force a publisher in or out.

## What I cut, and why

**Image creative, auction simulation, multi-tenancy, auth, persistence.** All
real in production; none would have told you anything about how I think.

**Vector search.** The catalog is 10KB and fits in a prompt with room to
spare. Embedding 20 records would have been cargo-culted retrieval — the
interesting problem here is grounding, not recall.

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
