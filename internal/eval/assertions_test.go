package eval

import (
	"strings"
	"testing"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
)

func cat(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load("../../data")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// fullLedger returns one excluded ledger entry per catalog publisher, the
// minimum a compliant campaign must carry. Tests mutate a copy of this to
// introduce exactly one corruption each.
func fullLedger(c *catalog.Catalog) []model.LedgerEntry {
	var out []model.LedgerEntry
	for i := range c.Publishers {
		out = append(out, model.LedgerEntry{
			PublisherID: c.Publishers[i].ID, Verdict: "excluded", Reason: "r"})
	}
	return out
}

func TestLoadBriefsReadsFifteen(t *testing.T) {
	bs, err := LoadBriefs("../../evals/briefs.txt")
	if err != nil {
		t.Fatalf("LoadBriefs: %v", err)
	}
	if len(bs) != 15 {
		t.Fatalf("got %d briefs, want 15", len(bs))
	}
	if bs[6].N != 7 || !strings.Contains(bs[6].Text, "dental") {
		t.Errorf("brief 7 = %+v, want the dental SaaS brief", bs[6])
	}
}

// A compliant campaign (the baseline every other test corrupts one field of)
// must itself report zero failures. Without this, a Check that always
// returns some fixed non-empty slice would still pass every other test in
// this file — this is the counterpart to the vacuity trap, not the trap
// itself.
func TestCheckPassesACompliantCampaign(t *testing.T) {
	c := cat(t)
	camp := model.Campaign{Status: model.StatusNoRecommendation, PublisherLedger: fullLedger(c)}

	got := Check(Brief{N: 7}, camp, c)
	if len(got) != 0 {
		t.Errorf("failures = %v, want none for a compliant no_recommendation campaign", got)
	}
}

// Corruption this catches: a ledger entry naming a publisher not in the
// catalog (e.g. a stale or hand-edited ID) silently passing through Check
// unnoticed.
func TestCheckFlagsUnknownPublisherInLedger(t *testing.T) {
	c := cat(t)
	camp := model.Campaign{Status: model.StatusNoRecommendation, PublisherLedger: fullLedger(c)}
	camp.PublisherLedger[0].PublisherID = "pub_999"

	got := Check(Brief{N: 7}, camp, c)
	if !containsSubstr(got, "unknown publisher") {
		t.Errorf("failures = %v, want an unknown-publisher failure", got)
	}
}

// Corruption this catches: the ledger missing an entry for one catalog
// publisher (e.g. Build skipping a publisher, or a caller truncating the
// slice) — a shorter-than-expected ledger must be flagged even though every
// entry it does contain is individually well-formed.
func TestCheckFlagsShortLedger(t *testing.T) {
	c := cat(t)
	full := fullLedger(c)
	if len(full) < 2 {
		t.Fatal("catalog needs at least 2 publishers for this test")
	}
	camp := model.Campaign{Status: model.StatusNoRecommendation, PublisherLedger: full[:len(full)-1]}

	got := Check(Brief{N: 7}, camp, c)
	if !containsSubstr(got, "ledger has") {
		t.Errorf("failures = %v, want a short-ledger failure", got)
	}
}

// Corruption this catches: a brief the catalog cannot serve (brief #7, B2B
// dental SaaS) nonetheless getting a "ready" status and a live allocation —
// i.e. the pipeline's non-consumer gate being bypassed or ignored.
func TestCheckFlagsNonConsumerBriefThatRecommends(t *testing.T) {
	c := cat(t)
	camp := model.Campaign{Status: model.StatusReady, PublisherLedger: fullLedger(c)}
	camp.Budget.Allocation = []model.AllocationEntry{{PublisherID: "pub_007", Share: 1}}

	got := Check(Brief{N: 7}, camp, c)
	if !containsSubstr(got, "non-consumer") {
		t.Errorf("failures = %v, want a non-consumer status failure", got)
	}
	if !containsSubstr(got, "cannot serve") {
		t.Errorf("failures = %v, want a budget-allocated failure", got)
	}
}

// Corruption this catches: a creative citing a messaging lever that does not
// appear anywhere in its persona's own messaging_preferences — i.e. the
// creative stage inventing a justification instead of grounding it in the
// catalog record.
func TestCheckFlagsUngroundedMessagingLever(t *testing.T) {
	c := cat(t)
	camp := model.Campaign{Status: model.StatusReady, PublisherLedger: fullLedger(c)}
	camp.Creatives = []model.Creative{
		{PersonaID: "persona_004", Headline: "h", Body: "b",
			MessagingLevers: []string{"totally invented"}},
		{PersonaID: "persona_002", Headline: "h", Body: "b",
			MessagingLevers: []string{"time-saving"}},
		{PersonaID: "persona_001", Headline: "h", Body: "b",
			MessagingLevers: []string{"science-backed claims"}},
	}

	got := Check(Brief{N: 1}, camp, c)
	if !containsSubstr(got, "not in its persona record") {
		t.Errorf("failures = %v, want an ungrounded-lever failure", got)
	}
}

// Corruption this catches: two creatives written for the same persona (e.g.
// the persona-selection stage returning a duplicate pick, or the creative
// stage fanning out twice) instead of one per selected persona.
func TestCheckFlagsDuplicatePersonaCreative(t *testing.T) {
	c := cat(t)
	camp := model.Campaign{Status: model.StatusReady, PublisherLedger: fullLedger(c)}
	camp.Creatives = []model.Creative{
		{PersonaID: "persona_004", Headline: "h", Body: "b", MessagingLevers: []string{}},
		{PersonaID: "persona_004", Headline: "h2", Body: "b2", MessagingLevers: []string{}},
		{PersonaID: "persona_001", Headline: "h3", Body: "b3", MessagingLevers: []string{}},
	}

	got := Check(Brief{N: 1}, camp, c)
	if !containsSubstr(got, "twice") && !containsSubstr(got, "two creatives") {
		t.Errorf("failures = %v, want a duplicate-persona failure", got)
	}
}

// Corruption this catches: allocation shares that do not sum to 1.0 (e.g. an
// allocator bug that drops a remainder or double-counts a publisher).
func TestCheckFlagsAllocationSharesNotSummingToOne(t *testing.T) {
	c := cat(t)
	camp := model.Campaign{Status: model.StatusReady, PublisherLedger: fullLedger(c)}
	camp.Budget.Allocation = []model.AllocationEntry{
		{PublisherID: "pub_007", Share: 0.5},
		{PublisherID: "pub_009", Share: 0.3},
	}

	got := Check(Brief{N: 1}, camp, c)
	if !containsSubstr(got, "sum to") {
		t.Errorf("failures = %v, want an allocation-sum failure", got)
	}
}

// Corruption this catches: brief #2 (women's activewear) failing to exclude
// Linden Park (pub_005, age skew 50-70) on the demographic mismatch gate —
// e.g. the gate firing but not being recorded, or firing for the wrong
// reason.
func TestCheckFlagsLindenParkNotExcludedForBrief2(t *testing.T) {
	c := cat(t)
	camp := model.Campaign{Status: model.StatusReady, PublisherLedger: fullLedger(c)}
	// pub_005 is present but recorded as recommended instead of excluded.
	for i := range camp.PublisherLedger {
		if camp.PublisherLedger[i].PublisherID == "pub_005" {
			camp.PublisherLedger[i].Verdict = "recommended"
			camp.PublisherLedger[i].HardGate = ""
		}
	}

	got := Check(Brief{N: 2}, camp, c)
	if !containsSubstr(got, "Linden Park") {
		t.Errorf("failures = %v, want a Linden Park exclusion failure", got)
	}
}

// Corruption this catches: brief #1 (premium senior dog food) failing to
// rank Pawline (pub_007) in the top 2 recommended publishers.
func TestCheckFlagsPawlineNotTopRankedForBrief1(t *testing.T) {
	c := cat(t)
	camp := model.Campaign{Status: model.StatusReady, PublisherLedger: fullLedger(c)}
	for i := range camp.PublisherLedger {
		if camp.PublisherLedger[i].PublisherID == "pub_007" {
			camp.PublisherLedger[i].Verdict = "excluded"
		}
	}

	got := Check(Brief{N: 1}, camp, c)
	if !containsSubstr(got, "Pawline") {
		t.Errorf("failures = %v, want a Pawline rank failure", got)
	}
}

// Corruption this catches: brief #10 ($1,200 handbags) allocating budget to
// Swiftcart (pub_001, $28 AOV) — a price-tier mismatch that should never
// receive spend.
func TestCheckFlagsSwiftcartAllocatedForBrief10(t *testing.T) {
	c := cat(t)
	camp := model.Campaign{Status: model.StatusReady, PublisherLedger: fullLedger(c)}
	camp.Budget.Allocation = []model.AllocationEntry{{PublisherID: "pub_001", Share: 1}}

	got := Check(Brief{N: 10}, camp, c)
	if !containsSubstr(got, "Swiftcart") {
		t.Errorf("failures = %v, want a Swiftcart allocation failure", got)
	}
}

// Corruption this catches: brief #6 ($650+ ski shells) allocating budget to a
// mid-income publisher (pub_006 or pub_010) — the wrong income tier for a
// premium product.
func TestCheckFlagsMidIncomeAllocatedForBrief6(t *testing.T) {
	c := cat(t)
	camp := model.Campaign{Status: model.StatusReady, PublisherLedger: fullLedger(c)}
	camp.Budget.Allocation = []model.AllocationEntry{{PublisherID: "pub_006", Share: 1}}

	got := Check(Brief{N: 6}, camp, c)
	if !containsSubstr(got, "mid-income") {
		t.Errorf("failures = %v, want a mid-income allocation failure", got)
	}
}

// Corruption this catches: a deliberately low-signal brief (#5, #8, #15)
// getting a "ready" status with no missing signals or clarifying questions
// recorded, instead of being flagged for more input.
func TestCheckFlagsLowSignalBriefThatSkipsClarification(t *testing.T) {
	c := cat(t)
	camp := model.Campaign{Status: model.StatusReady, PublisherLedger: fullLedger(c)}

	got := Check(Brief{N: 5}, camp, c)
	if !containsSubstr(got, "needs_clarification") {
		t.Errorf("failures = %v, want a status failure", got)
	}
	if !containsSubstr(got, "missing signals") {
		t.Errorf("failures = %v, want a missing-signals failure", got)
	}
	if !containsSubstr(got, "clarifying questions") {
		t.Errorf("failures = %v, want a clarifying-questions failure", got)
	}
}

func containsSubstr(xs []string, want string) bool {
	for _, x := range xs {
		if strings.Contains(x, want) {
			return true
		}
	}
	return false
}
