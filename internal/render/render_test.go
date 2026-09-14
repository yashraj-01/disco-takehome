package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yashraj/disco/internal/model"
)

// sample returns a fully-populated "ready" campaign: allocation, bid,
// targeting, creatives, and a mixed publisher ledger all set. Tests that need
// to prove something is *suppressed* start from this (not from an
// already-empty campaign), per the project's standing note on vacuous setups.
func sample() model.Campaign {
	return model.Campaign{
		Status:    model.StatusReady,
		Objective: "conversion",
		Advertiser: model.AdvertiserBlock{
			Brief: "premium dog food", Confidence: "high",
			Assumptions: []string{"subscription implies repeat purchase"},
		},
		Flight: model.Flight{Start: "2026-09-13", DurationDays: 30},
		Budget: model.Budget{
			TotalUSD: 25000, DailyCapUSD: 833.33,
			Allocation: []model.AllocationEntry{{
				PublisherID: "pub_007", PublisherName: "Pawline", Share: 0.378,
				AmountUSD: 9458, EstCPMUSD: 15.48, EstImpressions: 611124,
				Rationale: "Subscription-heavy premium pet buyers",
			}},
		},
		Bid: model.Bid{Model: "CPM", Strategy: "target_cpa_capped",
			FloorUSD: 12.38, TargetUSD: 15.48, CeilingUSD: 21.67, TargetCPAUSD: 24.50},
		Targeting: model.Targeting{AgeRange: "30-55", GenderSkew: "balanced",
			Geos: []string{"US-West"}, PersonaIDs: []string{"persona_004"}},
		Creatives: []model.Creative{{
			PersonaID: "persona_004", Headline: "Built for senior dogs",
			Body:            "Vet-formulated meals, delivered monthly.",
			Rationale:       "Leads with the vet formulation this persona screens for.",
			MessagingLevers: []string{"vet-recommended"},
		}},
		PublisherLedger: []model.LedgerEntry{
			{PublisherID: "pub_007", PublisherName: "Pawline", Verdict: "recommended",
				Rank: 1, Score: 0.84, Reason: "Premium pet audience"},
			{PublisherID: "pub_005", PublisherName: "Linden Park", Verdict: "excluded",
				Score: 0, HardGate: "demographic_mismatch",
				Reason: "Audience skews 50-70; this brief targets 30-55."},
		},
		Meta: model.Meta{Model: "gemini-2.5-flash", Provider: "fixture", PipelineVersion: "1"},
	}
}

// TestJSONRoundTrips catches: JSON that doesn't decode back into
// model.Campaign (wrong shape, broken marshaling), and JSON emitted as one
// compact line instead of indented for a human to read.
func TestJSONRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, sample()); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var back model.Campaign
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if back.Status != model.StatusReady {
		t.Errorf("status = %q after round trip", back.Status)
	}
	if !strings.Contains(buf.String(), "\n  ") {
		t.Error("JSON output should be indented for human reading")
	}
}

// TestJSONRoundTripsNewFields catches: the JSON encoder (or a hand-rolled
// projection instead of encoding model.Campaign directly) silently dropping
// UnallocatedUSD or ExceedsMaxShare, both of which are omitempty-tagged zero
// values and easy to lose without anyone noticing.
func TestJSONRoundTripsNewFields(t *testing.T) {
	c := sample()
	c.Budget.UnallocatedUSD = 4321.50
	c.Budget.Allocation[0].ExceedsMaxShare = true
	c.Budget.Allocation[0].Rationale += " This publisher's share exceeds the 40% concentration " +
		"guideline, which was relaxed because it could not be satisfied for this set of recommended publishers."

	var buf bytes.Buffer
	if err := JSON(&buf, c); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var back model.Campaign
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if back.Budget.UnallocatedUSD != 4321.50 {
		t.Errorf("unallocated_usd = %v after round trip, want 4321.50", back.Budget.UnallocatedUSD)
	}
	if len(back.Budget.Allocation) != 1 || !back.Budget.Allocation[0].ExceedsMaxShare {
		t.Error("exceeds_max_share did not survive the round trip")
	}
}

// TestTerminalShowsPicksAndExclusions catches: a renderer that only prints
// the winning allocation and drops the ledger entirely, or that renders
// numbers without thousands separators / loses the hard-gate label.
func TestTerminalShowsPicksAndExclusions(t *testing.T) {
	var buf bytes.Buffer
	if err := Terminal(&buf, sample()); err != nil {
		t.Fatalf("Terminal: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Pawline", "Linden Park", "demographic_mismatch",
		"9,458", "611,124", "Built for senior dogs", "persona_004",
		"30-55", "target_cpa_capped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("terminal output missing %q", want)
		}
	}
}

// TestTerminalShowsClarificationsWhenPresent catches: clarifying questions
// computed by the pipeline but never surfaced to the person reading the
// terminal output, which is the one place a needs-clarification result is
// actually actionable.
func TestTerminalShowsClarificationsWhenPresent(t *testing.T) {
	c := sample()
	c.Status = model.StatusNeedsClarification
	c.Clarifications = []string{"What exactly do you sell?"}

	var buf bytes.Buffer
	if err := Terminal(&buf, c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "What exactly do you sell?") {
		t.Error("clarifying questions must be shown")
	}
}

// TestTerminalNoRecommendationSuppressesBudgetAndCreative catches the
// vacuous-setup trap this project has hit before: starting from a campaign
// that has no budget/creative content anyway proves nothing about whether the
// renderer *suppresses* that content for a no-recommendation status. This
// test starts from the fully-populated sample() and only flips Status,
// deliberately leaving Budget.Allocation and Creatives populated, so a
// renderer that ignores Status and prints them anyway is caught.
func TestTerminalNoRecommendationSuppressesBudgetAndCreative(t *testing.T) {
	c := sample()
	c.Status = model.StatusNoRecommendation
	// Budget.Allocation and Creatives are deliberately left populated.

	var buf bytes.Buffer
	if err := Terminal(&buf, c); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	if !strings.Contains(out, "NO RECOMMENDATION") {
		t.Error("a no-recommendation result must say so unmistakably")
	}
	for _, unwanted := range []string{"9,458", "611,124", "Built for senior dogs", "target_cpa_capped"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("no-recommendation output should suppress budget/creative detail, but found %q", unwanted)
		}
	}
	// The ledger is the differentiator: it must still explain the outcome
	// even though nothing was spent.
	if !strings.Contains(out, "Linden Park") || !strings.Contains(out, "demographic_mismatch") {
		t.Error("no-recommendation output should still show the publisher ledger")
	}
}

// TestTerminalRendersUnallocatedProminently catches: UnallocatedUSD being
// dropped entirely, or being tucked away after the per-publisher table where
// a reader skimming the budget section would miss the headline "your budget
// exceeds what these publishers can deliver" signal.
func TestTerminalRendersUnallocatedProminently(t *testing.T) {
	c := sample()
	c.Budget.UnallocatedUSD = 4321.50

	var buf bytes.Buffer
	if err := Terminal(&buf, c); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	if !strings.Contains(out, "4,321.50") && !strings.Contains(out, "4,322") && !strings.Contains(out, "4,321") {
		t.Errorf("unallocated amount not rendered: %s", out)
	}
	budgetIdx := strings.Index(out, "BUDGET")
	unallocIdx := strings.Index(strings.ToUpper(out), "UNALLOCATED")
	tableIdx := strings.Index(out, "Pawline")
	if budgetIdx < 0 || unallocIdx < 0 || tableIdx < 0 {
		t.Fatalf("expected BUDGET, UNALLOCATED and a publisher row all present; got:\n%s", out)
	}
	if !(budgetIdx < unallocIdx && unallocIdx < tableIdx) {
		t.Errorf("unallocated budget line is not surfaced prominently between the budget header and the "+
			"per-publisher table; budgetIdx=%d unallocIdx=%d tableIdx=%d", budgetIdx, unallocIdx, tableIdx)
	}
}

// TestTerminalOmitsUnallocatedWhenZero catches a renderer that always prints
// an "unallocated" line (even $0), which would bury the real signal in noise
// on the common case where the budget was fully deployed.
func TestTerminalOmitsUnallocatedWhenZero(t *testing.T) {
	c := sample()
	c.Budget.UnallocatedUSD = 0

	var buf bytes.Buffer
	if err := Terminal(&buf, c); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToUpper(buf.String()), "UNALLOCATED") {
		t.Error("should not mention unallocated budget when it is zero")
	}
}

// TestTerminalDoesNotTruncateExceedsMaxShareRationale catches the exact trap
// called out for this task: a fixed-width truncation helper cutting off the
// ExceedsMaxShare explanation mid-sentence. The base rationale plus the
// appended sentence (copied verbatim from pipeline.Build's wording) is well
// over 100 characters, comfortably past any ~60-70 char fixed-width trunc.
func TestTerminalDoesNotTruncateExceedsMaxShareRationale(t *testing.T) {
	c := sample()
	const sentence = "This publisher's share exceeds the 40% concentration guideline, " +
		"which was relaxed because it could not be satisfied for this set of recommended publishers."
	c.Budget.Allocation[0].Rationale = "Subscription-heavy premium pet buyers with strong repeat purchase behavior. " + sentence
	c.Budget.Allocation[0].ExceedsMaxShare = true

	var buf bytes.Buffer
	if err := Terminal(&buf, c); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// Normalize whitespace so a renderer that word-wraps the rationale onto
	// several indented lines (a legitimate way to avoid truncation) still
	// compares equal to the single-line sentence — only word order and
	// completeness matter here, not literal line breaks. A renderer that
	// truncates instead of wrapping would still fail this: the tail words of
	// the sentence would be missing from the normalized output entirely.
	norm := strings.Join(strings.Fields(out), " ")
	wantNorm := strings.Join(strings.Fields(sentence), " ")
	if !strings.Contains(norm, wantNorm) {
		t.Errorf("ExceedsMaxShare rationale was truncated or dropped; full output:\n%s", out)
	}
	if strings.Contains(out, "…") {
		t.Errorf("rationale line was truncated with an ellipsis:\n%s", out)
	}
}

// TestTerminalLedgerShowsAllThreeVerdictSections catches a renderer that
// collapses "considered" into "excluded" (or vice versa), or that shows the
// hard-gate label on every row instead of only the ones that actually have
// one.
func TestTerminalLedgerShowsAllThreeVerdictSections(t *testing.T) {
	c := sample()
	c.PublisherLedger = append(c.PublisherLedger, model.LedgerEntry{
		PublisherID: "pub_009", PublisherName: "Borderline Co", Verdict: "considered",
		Score: 0.41, Reason: "Fits category but below the recommendation threshold.",
	})

	var buf bytes.Buffer
	if err := Terminal(&buf, c); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"RECOMMENDED", "CONSIDERED", "EXCLUDED", "Borderline Co", "below the recommendation threshold"} {
		if !strings.Contains(out, want) {
			t.Errorf("terminal output missing %q", want)
		}
	}
	// The considered row has no hard gate: its line must not pick up the
	// excluded row's gate label.
	lines := strings.Split(out, "\n")
	for _, l := range lines {
		if strings.Contains(l, "Borderline Co") && strings.Contains(l, "demographic_mismatch") {
			t.Errorf("considered entry incorrectly carries another row's hard-gate label: %q", l)
		}
	}
}

// TestTerminalShowsPersonaRejections catches PersonaRejections being read
// from BuildInput but never reaching the terminal renderer.
func TestTerminalShowsPersonaRejections(t *testing.T) {
	c := sample()
	c.PersonaRejections = []model.PersonaRejection{
		{PersonaID: "persona_003", Reason: "no pet affinity in this brief"},
	}

	var buf bytes.Buffer
	if err := Terminal(&buf, c); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "persona_003") || !strings.Contains(out, "no pet affinity in this brief") {
		t.Errorf("persona rejection not shown:\n%s", out)
	}
}

// TestTerminalNoRecommendationIsExplicit is kept close to the original brief
// wording but built on a campaign that never had allocation/creative data in
// the first place — the no-recommendation status alone must still say so.
func TestTerminalNoRecommendationIsExplicit(t *testing.T) {
	c := sample()
	c.Status = model.StatusNoRecommendation
	c.Budget.Allocation = nil
	c.Creatives = nil

	var buf bytes.Buffer
	if err := Terminal(&buf, c); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "NO RECOMMENDATION") {
		t.Error("a no-recommendation result must say so unmistakably")
	}
}
