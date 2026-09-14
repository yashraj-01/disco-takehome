# Stage 3 — Publisher fit

You are given an advertiser profile and every publisher in the catalog, each
with a deterministic fit score and its sub-scores. Your job is to assign each
publisher a verdict and write the reason a human would want to read.

## Verdicts

- **recommended** — worth spending money on. Assign `rank` starting at 1.
  Recommend between 2 and 6 publishers when any are suitable.
- **considered** — plausible but not funded. A real near-miss.
- **excluded** — not a fit.

## Rules

1. **Return a verdict for every publisher in the input. All of them.** A
   publisher you leave out is a recommendation a human cannot audit.
2. **A publisher with a non-empty `hard_gate` is always `excluded`.** You may
   write a better reason than the gate's, but you may not promote it. What each
   gate value means: `not_consumer_dtc` — this catalog cannot reach this
   advertiser's buyers at all; `category_mismatch` — no category or subcategory
   overlap; `demographic_mismatch` — the audience age bands do not overlap.

   **Never write the gate's name in your reason.** `category_mismatch` is an
   internal label, not an explanation — an advertiser reading "Excluded due to
   hard gate category_mismatch" learns nothing they could act on. Say the
   substance in plain words instead: *"Sells kitchenware and home goods; nothing
   in the assortment reaches people shopping for pet food."* Do not mention
   gates, scores, or any other machinery of this system.
3. The `score` is a starting point, not a verdict. You see each publisher's
   `notes`, which the score only crudely approximates. Promote or demote against
   the score when the notes justify it, and say so in the reason.

   **What the sub-scores actually mean.** These terms are narrower than their
   everyday sense, and a reason that uses them loosely will contradict the data
   it sits next to:
   - `values_match` is overlap with the advertiser's **stated** values — the
     ones in `advertiser.values`. A publisher being sustainable is not a values
     match for an advertiser who talked about craftsmanship. It scores 0.
   - `aov_alignment` is how **close** the publisher's average order value is to
     the advertiser's, not how large it is. The catalog's highest-AOV publisher
     scores 0 against a $1,200 product if its shoppers spend $128.
   - `age_overlap` is the overlap of the two age bands, not whether the audience
     is desirable.

   You may absolutely recommend on grounds the scores do not capture — that is
   what rule 3 is for. But when you do, **name the real reason** rather than
   borrowing a sub-score's vocabulary for it. Write *"its shoppers already buy
   sustainably, which this brand can lead with"*, not *"strong values
   alignment"*, when `values_match` is 0.
4. **Reasons must be specific to this pairing.** "Good audience fit" is not a
   reason. "Subscription-heavy pet buyers who already pay a premium for health
   positioning" is.
5. When you exclude, say what specifically disqualifies it — the age band, the
   order value gap, the category distance — not that it "scored low".
6. If no publisher is suitable, recommend none. An empty recommendation with
   honest reasons beats a confident bad placement.
