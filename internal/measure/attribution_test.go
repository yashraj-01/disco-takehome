package measure

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/model"
)

// stubProvider is a canned-response llm.Provider for tests: never a real
// client, per the constraint that go test ./... must pass with no API key
// and no network. Responses are served in call order (PersonaAttribution
// always calls real then control, per judged campaign, in campaign order),
// and every request is recorded so a test can inspect exactly what prompt
// was sent.
type stubProvider struct {
	responses []json.RawMessage
	calls     []llm.Request
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) Complete(_ context.Context, r llm.Request) (json.RawMessage, error) {
	s.calls = append(s.calls, r)
	i := len(s.calls) - 1
	if i >= len(s.responses) {
		return nil, fmt.Errorf("stubProvider: no response queued for call %d", i)
	}
	return s.responses[i], nil
}

// emptyJudgeResponse returns a schema-valid empty response: Assignments must
// be a non-nil empty slice, since the judge schema requires an array and a
// nil slice marshals to JSON null.
func emptyJudgeResponse() judgeResponse {
	return judgeResponse{Assignments: []judgeAssignment{}}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

// oneCampaign builds a single-campaign []Campaign from creatives, ready to
// pass to PersonaAttribution.
func oneCampaign(briefN int, creatives ...model.Creative) []Campaign {
	return []Campaign{{BriefN: briefN, Campaign: model.Campaign{Creatives: creatives}}}
}

// groundTruthFor replicates PersonaAttribution's own creative shuffle so a
// test can build a real-task response that names the correct persona_id for
// each SHOWN creative_index, without duplicating the metric's internals
// beyond the one deterministic step (the shuffle) a test must know to
// construct a "perfect" or "all-wrong" response.
func groundTruthFor(briefN int, creatives []model.Creative) []string {
	n := len(creatives)
	order := shuffledOrder(n, seedFor("creatives", fmt.Sprint(briefN)))
	gt := make([]string, n)
	for pos, orig := range order {
		gt[pos] = creatives[orig].PersonaID
	}
	return gt
}

// TestPersonaAttribution_PerfectJudgeScoresOne catches the corruption: an
// accuracy computation that can't reach 1.0 even when every assignment is
// exactly right (e.g. comparing against the wrong ground-truth index because
// the shuffle was applied inconsistently between the creative shown to the
// judge and the truth checked against its answer).
func TestPersonaAttribution_PerfectJudgeScoresOne(t *testing.T) {
	cat := testCatalog(t)
	creatives := []model.Creative{
		{PersonaID: "persona_001", Headline: "Track every rep", Body: "Built for people who measure everything."},
		{PersonaID: "persona_002", Headline: "Dinner in ten minutes", Body: "Because the kids need feeding, now."},
	}
	campaigns := oneCampaign(1, creatives...)
	gt := groundTruthFor(1, creatives)

	var real judgeResponse
	for pos, pid := range gt {
		real.Assignments = append(real.Assignments, judgeAssignment{
			CreativeIndex: pos, PersonaID: pid, Confidence: "high", Rationale: "matches",
		})
	}
	stub := &stubProvider{responses: []json.RawMessage{mustJSON(t, real), mustJSON(t, emptyJudgeResponse())}}

	m, err := PersonaAttribution(context.Background(), stub, campaigns, cat)
	if err != nil {
		t.Fatalf("PersonaAttribution: %v", err)
	}
	if got := m.Values["attribution_accuracy"]; got != 1.0 {
		t.Errorf("attribution_accuracy = %v, want 1.0", got)
	}
	if got := m.Counts["correct"]; got != 2 {
		t.Errorf("correct = %d, want 2", got)
	}
	if got := m.Counts["creatives_matched"]; got != 2 {
		t.Errorf("creatives_matched = %d, want 2", got)
	}
}

// TestPersonaAttribution_AllWrongScoresZero catches the corruption: a
// correctness check that gives credit it shouldn't (e.g. matching on
// creative_index alone, or on set membership rather than exact persona_id
// equality).
func TestPersonaAttribution_AllWrongScoresZero(t *testing.T) {
	cat := testCatalog(t)
	creatives := []model.Creative{
		{PersonaID: "persona_001", Headline: "Track every rep", Body: "Built for people who measure everything."},
		{PersonaID: "persona_002", Headline: "Dinner in ten minutes", Body: "Because the kids need feeding, now."},
	}
	campaigns := oneCampaign(2, creatives...)
	gt := groundTruthFor(2, creatives)

	// Swap: every assignment names the OTHER shown creative's real persona.
	real := judgeResponse{Assignments: []judgeAssignment{
		{CreativeIndex: 0, PersonaID: gt[1], Confidence: "medium", Rationale: "wrong on purpose"},
		{CreativeIndex: 1, PersonaID: gt[0], Confidence: "medium", Rationale: "wrong on purpose"},
	}}
	stub := &stubProvider{responses: []json.RawMessage{mustJSON(t, real), mustJSON(t, emptyJudgeResponse())}}

	m, err := PersonaAttribution(context.Background(), stub, campaigns, cat)
	if err != nil {
		t.Fatalf("PersonaAttribution: %v", err)
	}
	if got := m.Values["attribution_accuracy"]; got != 0 {
		t.Errorf("attribution_accuracy = %v, want 0", got)
	}
	if got := m.Counts["correct"]; got != 0 {
		t.Errorf("correct = %d, want 0", got)
	}
	if got := m.Counts["creatives_matched"]; got != 2 {
		t.Errorf("creatives_matched = %d, want 2 (both assignments were valid, just wrong)", got)
	}
	if len(m.Findings) != 2 {
		t.Errorf("findings = %d, want 2 misattributions", len(m.Findings))
	}
}

// TestPersonaAttribution_ChanceBaselineAveragedPerCampaign catches the
// corruption: chance_baseline computed as one global constant (e.g. 1 over
// the total creative count across every campaign, or 1 over the number of
// campaigns) instead of the mean, across judged campaigns, of that
// campaign's own 1/N. Two campaigns of different size (N=2 and N=4) make the
// two computations diverge, so a global-constant bug is caught rather than
// coincidentally matching.
func TestPersonaAttribution_ChanceBaselineAveragedPerCampaign(t *testing.T) {
	cat := testCatalog(t)
	campA := Campaign{BriefN: 1, Campaign: model.Campaign{Creatives: []model.Creative{
		{PersonaID: "persona_001", Headline: "A1", Body: "b1"},
		{PersonaID: "persona_002", Headline: "A2", Body: "b2"},
	}}}
	campB := Campaign{BriefN: 2, Campaign: model.Campaign{Creatives: []model.Creative{
		{PersonaID: "persona_003", Headline: "B1", Body: "b1"},
		{PersonaID: "persona_004", Headline: "B2", Body: "b2"},
		{PersonaID: "persona_005", Headline: "B3", Body: "b3"},
		{PersonaID: "persona_006", Headline: "B4", Body: "b4"},
	}}}
	campaigns := []Campaign{campA, campB}

	empty := mustJSON(t, emptyJudgeResponse())
	stub := &stubProvider{responses: []json.RawMessage{empty, empty, empty, empty}}

	m, err := PersonaAttribution(context.Background(), stub, campaigns, cat)
	if err != nil {
		t.Fatalf("PersonaAttribution: %v", err)
	}
	want := (1.0/2.0 + 1.0/4.0) / 2.0 // 0.375
	if got := m.Values["chance_baseline"]; math.Abs(got-want) > 1e-9 {
		t.Errorf("chance_baseline = %v, want %v (mean of per-campaign 1/N)", got, want)
	}
	// Guard against the two most tempting wrong constants.
	if got := m.Values["chance_baseline"]; math.Abs(got-1.0/6.0) < 1e-9 {
		t.Errorf("chance_baseline = %v looks like 1/total_creatives (a global constant), want the per-campaign mean %v", got, want)
	}
	if got := m.Values["chance_baseline"]; math.Abs(got-0.5) < 1e-9 {
		t.Errorf("chance_baseline = %v looks like 1/campaigns (a global constant), want the per-campaign mean %v", got, want)
	}
}

// TestPersonaAttribution_OutOfRangeIndexDroppedNotPanic catches the
// corruption: indexing shown creatives (or the ground-truth slice) directly
// with an untrusted creative_index from the judge's response, which panics
// on an out-of-range value instead of dropping and counting it.
func TestPersonaAttribution_OutOfRangeIndexDroppedNotPanic(t *testing.T) {
	cat := testCatalog(t)
	creatives := []model.Creative{
		{PersonaID: "persona_001", Headline: "A1", Body: "b1"},
		{PersonaID: "persona_002", Headline: "A2", Body: "b2"},
	}
	campaigns := oneCampaign(3, creatives...)
	gt := groundTruthFor(3, creatives)

	real := judgeResponse{Assignments: []judgeAssignment{
		{CreativeIndex: 99, PersonaID: gt[0], Confidence: "high", Rationale: "out of range"},
		{CreativeIndex: 0, PersonaID: gt[0], Confidence: "high", Rationale: "valid"},
	}}
	stub := &stubProvider{responses: []json.RawMessage{mustJSON(t, real), mustJSON(t, emptyJudgeResponse())}}

	m, err := PersonaAttribution(context.Background(), stub, campaigns, cat) // must not panic
	if err != nil {
		t.Fatalf("PersonaAttribution: %v", err)
	}
	if got := m.Counts["dropped_out_of_range_index"]; got != 1 {
		t.Errorf("dropped_out_of_range_index = %d, want 1", got)
	}
	if got := m.Counts["creatives_matched"]; got != 1 {
		t.Errorf("creatives_matched = %d, want 1 (only the valid assignment)", got)
	}
}

// TestPersonaAttribution_UnknownPersonaDroppedAndCounted catches the
// corruption: trusting a persona_id the judge invented (or one belonging to
// a different task's candidate list) instead of validating it against the
// set actually shown for this task.
func TestPersonaAttribution_UnknownPersonaDroppedAndCounted(t *testing.T) {
	cat := testCatalog(t)
	creatives := []model.Creative{
		{PersonaID: "persona_001", Headline: "A1", Body: "b1"},
		{PersonaID: "persona_002", Headline: "A2", Body: "b2"},
	}
	campaigns := oneCampaign(4, creatives...)
	gt := groundTruthFor(4, creatives)

	real := judgeResponse{Assignments: []judgeAssignment{
		{CreativeIndex: 0, PersonaID: "persona_999", Confidence: "high", Rationale: "not a real id"},
		{CreativeIndex: 1, PersonaID: gt[1], Confidence: "high", Rationale: "valid"},
	}}
	stub := &stubProvider{responses: []json.RawMessage{mustJSON(t, real), mustJSON(t, emptyJudgeResponse())}}

	m, err := PersonaAttribution(context.Background(), stub, campaigns, cat)
	if err != nil {
		t.Fatalf("PersonaAttribution: %v", err)
	}
	if got := m.Counts["dropped_unknown_persona"]; got != 1 {
		t.Errorf("dropped_unknown_persona = %d, want 1", got)
	}
	if got := m.Counts["creatives_matched"]; got != 1 {
		t.Errorf("creatives_matched = %d, want 1 (only the valid assignment)", got)
	}
}

// TestPersonaAttribution_DuplicatePersonaDropped catches the corruption:
// counting a persona twice toward creatives_matched (and possibly toward
// correct) when the judge assigns it to more than one creative, instead of
// keeping only the first occurrence and dropping the rest.
func TestPersonaAttribution_DuplicatePersonaDropped(t *testing.T) {
	cat := testCatalog(t)
	creatives := []model.Creative{
		{PersonaID: "persona_001", Headline: "A1", Body: "b1"},
		{PersonaID: "persona_002", Headline: "A2", Body: "b2"},
	}
	campaigns := oneCampaign(5, creatives...)
	gt := groundTruthFor(5, creatives)

	// Same persona_id assigned to both creatives.
	real := judgeResponse{Assignments: []judgeAssignment{
		{CreativeIndex: 0, PersonaID: gt[0], Confidence: "high", Rationale: "first"},
		{CreativeIndex: 1, PersonaID: gt[0], Confidence: "high", Rationale: "duplicate"},
	}}
	stub := &stubProvider{responses: []json.RawMessage{mustJSON(t, real), mustJSON(t, emptyJudgeResponse())}}

	m, err := PersonaAttribution(context.Background(), stub, campaigns, cat)
	if err != nil {
		t.Fatalf("PersonaAttribution: %v", err)
	}
	if got := m.Counts["dropped_duplicate_persona"]; got != 1 {
		t.Errorf("dropped_duplicate_persona = %d, want 1", got)
	}
	if got := m.Counts["creatives_matched"]; got != 1 {
		t.Errorf("creatives_matched = %d, want 1 (the duplicate must not also count)", got)
	}
}

// TestPersonaAttribution_PromptLeaksNoAnswerFields is the test that
// validates the whole metric isn't self-defeating: it asserts on the
// RENDERED prompt text sent to the provider (not on the code that builds
// it), so a future refactor that accidentally starts marshaling the full
// model.Creative — persona_id, messaging_levers, rationale and all — into
// the judge's input is caught immediately, rather than silently making
// attribution trivial and every accuracy number meaningless.
func TestPersonaAttribution_PromptLeaksNoAnswerFields(t *testing.T) {
	cat := testCatalog(t)
	creatives := []model.Creative{
		{
			PersonaID: "persona_001", Headline: "Track every rep", Body: "Built for people who measure everything.",
			Rationale: "UNIQUE_RATIONALE_MARKER_ONE", MessagingLevers: []string{"UNIQUE_LEVER_MARKER_ONE"},
			Avoided: []string{"UNIQUE_AVOIDED_MARKER_ONE"},
		},
		{
			PersonaID: "persona_002", Headline: "Dinner in ten minutes", Body: "Because the kids need feeding, now.",
			Rationale: "UNIQUE_RATIONALE_MARKER_TWO", MessagingLevers: []string{"UNIQUE_LEVER_MARKER_TWO"},
			Avoided: []string{"UNIQUE_AVOIDED_MARKER_TWO"},
		},
	}
	campaigns := oneCampaign(6, creatives...)

	empty := mustJSON(t, emptyJudgeResponse())
	stub := &stubProvider{responses: []json.RawMessage{empty, empty}}

	if _, err := PersonaAttribution(context.Background(), stub, campaigns, cat); err != nil {
		t.Fatalf("PersonaAttribution: %v", err)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("calls = %d, want 2 (real + control)", len(stub.calls))
	}

	// Note: "persona_001"/"persona_002" are deliberately NOT banned — they
	// legitimately appear as the shown persona candidates' own "id" field.
	// What must never appear is a "persona_id" KEY (which would mean a
	// creative is carrying its answer), or any of the creative-specific
	// rationale/lever/avoided text.
	banned := []string{
		"UNIQUE_RATIONALE_MARKER_ONE", "UNIQUE_RATIONALE_MARKER_TWO",
		"UNIQUE_LEVER_MARKER_ONE", "UNIQUE_LEVER_MARKER_TWO",
		"UNIQUE_AVOIDED_MARKER_ONE", "UNIQUE_AVOIDED_MARKER_TWO",
		`"persona_id"`, `"messaging_levers"`, `"rationale"`, `"avoided"`,
	}
	for i, call := range stub.calls {
		for _, b := range banned {
			if strings.Contains(call.Prompt, b) {
				t.Errorf("call %d prompt leaked %q:\n%s", i, b, call.Prompt)
			}
		}
	}
}

// TestShuffledOrderDeterministicForSeed catches the corruption: a shuffle
// seeded from something non-deterministic (time, map iteration order) or
// seeded inconsistently between calls, which would make every rerun a
// fixture cache miss and burn quota (see the metric's Cost control
// requirement).
func TestShuffledOrderDeterministicForSeed(t *testing.T) {
	seed := seedFor("creatives", "7")
	a := shuffledOrder(6, seed)
	b := shuffledOrder(6, seed)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("shuffledOrder(6, seed) not deterministic: %v vs %v", a, b)
	}
}

// TestPersonaAttribution_ShuffleDeterministicAcrossReruns is the
// integration-level version of the same requirement: running the exact same
// campaign data through PersonaAttribution twice (as a real rerun of "disco
// measure" would) must render byte-identical prompts and fixture keys both
// times, so the second run replays every call from its recorded fixture
// instead of missing and calling the model again.
func TestPersonaAttribution_ShuffleDeterministicAcrossReruns(t *testing.T) {
	cat := testCatalog(t)
	creatives := []model.Creative{
		{PersonaID: "persona_001", Headline: "H1", Body: "B1"},
		{PersonaID: "persona_002", Headline: "H2", Body: "B2"},
		{PersonaID: "persona_003", Headline: "H3", Body: "B3"},
	}
	campaigns := oneCampaign(9, creatives...)

	run := func() []llm.Request {
		empty := mustJSON(t, emptyJudgeResponse())
		stub := &stubProvider{responses: []json.RawMessage{empty, empty}}
		if _, err := PersonaAttribution(context.Background(), stub, campaigns, cat); err != nil {
			t.Fatalf("PersonaAttribution: %v", err)
		}
		return stub.calls
	}

	first := run()
	second := run()
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("expected 2 calls per run, got %d and %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Prompt != second[i].Prompt {
			t.Errorf("call %d prompt differs across reruns", i)
		}
		if first[i].FixtureKey != second[i].FixtureKey {
			t.Errorf("call %d fixture key differs across reruns: %q vs %q", i, first[i].FixtureKey, second[i].FixtureKey)
		}
	}
}
