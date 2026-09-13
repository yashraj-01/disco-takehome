package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/model"
	"github.com/yashraj/disco/prompts"
)

func personaFixture(sel []string) map[string]any {
	var selected []map[string]any
	for _, id := range sel {
		selected = append(selected, map[string]any{
			"persona_id": id, "rationale": "because",
			"primary_publishers": []string{"pub_007"}})
	}
	return map[string]any{
		"selected": selected,
		"rejected": []map[string]any{{"persona_id": "persona_003", "reason": "no affinity"}},
	}
}

func TestPersonasKeepsValidSelection(t *testing.T) {
	brief := "premium dog food"
	d := fixtureDeps(t, "personas", brief,
		personaFixture([]string{"persona_004", "persona_002", "persona_001"}))

	got, err := Personas(context.Background(), d, withBrief(dogFood(), brief), nil)
	if err != nil {
		t.Fatalf("Personas: %v", err)
	}
	if len(got.Selected) != 3 {
		t.Fatalf("got %d personas, want 3", len(got.Selected))
	}
	if len(got.Rejected) == 0 {
		t.Error("want at least one recorded rejection")
	}
}

// Enforcement rule 1: an ID the model invented is not in the catalog and must
// never reach the campaign config.
func TestPersonasDropsUnknownAndDuplicateIDs(t *testing.T) {
	brief := "premium dog food"
	d := fixtureDeps(t, "personas", brief, personaFixture(
		[]string{"persona_004", "persona_999", "persona_004", "persona_002", "persona_001"}))

	got, err := Personas(context.Background(), d, withBrief(dogFood(), brief), nil)
	if err != nil {
		t.Fatalf("Personas: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range got.Selected {
		if p.PersonaID == "persona_999" {
			t.Error("persona_999 is not in the catalog and must be dropped")
		}
		if seen[p.PersonaID] {
			t.Errorf("%s selected twice", p.PersonaID)
		}
		seen[p.PersonaID] = true
	}
	// Non-vacuous: exactly 3 valid, unique IDs survive out of 5 raw entries
	// (persona_999 is unknown, the second persona_004 is a repeat).
	if len(got.Selected) != 3 {
		t.Fatalf("got %d personas, want 3 (persona_999 dropped, duplicate collapsed)", len(got.Selected))
	}
}

// Enforcement rule 2, isolated: on a duplicate persona_id, the FIRST occurrence
// must survive, not the last and not some merge of both. Distinguishable
// content on each occurrence proves which one won.
func TestPersonasKeepsFirstOnDuplicateID(t *testing.T) {
	brief := "premium dog food"
	dup := "persona_004"
	d := fixtureDeps(t, "personas", brief, map[string]any{
		"selected": []map[string]any{
			{"persona_id": dup, "rationale": "first: model committed here", "primary_publishers": []string{"pub_007"}},
			{"persona_id": "persona_002", "rationale": "because", "primary_publishers": []string{"pub_007"}},
			{"persona_id": "persona_001", "rationale": "because", "primary_publishers": []string{"pub_007"}},
			{"persona_id": dup, "rationale": "second: model contradicted itself", "primary_publishers": []string{"pub_003"}},
		},
		"rejected": []map[string]any{{"persona_id": "persona_003", "reason": "no affinity"}},
	})

	got, err := Personas(context.Background(), d, withBrief(dogFood(), brief), nil)
	if err != nil {
		t.Fatalf("Personas: %v", err)
	}
	var matches []model.PersonaPick
	for _, p := range got.Selected {
		if p.PersonaID == dup {
			matches = append(matches, p)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("got %d entries for %s, want exactly 1", len(matches), dup)
	}
	if matches[0].Rationale != "first: model committed here" {
		t.Errorf("kept rationale = %q, want the FIRST occurrence", matches[0].Rationale)
	}
}

// Enforcement rule 3: more usable personas than the cap must be truncated to
// exactly 5, not merely "not rejected outright". Seven valid, unique IDs are
// supplied so the cap actually has something to bite.
func TestPersonasCapsAtFive(t *testing.T) {
	brief := "premium dog food"
	d := fixtureDeps(t, "personas", brief, personaFixture([]string{
		"persona_001", "persona_002", "persona_004", "persona_005",
		"persona_006", "persona_008", "persona_009"}))

	got, err := Personas(context.Background(), d, withBrief(dogFood(), brief), nil)
	if err != nil {
		t.Fatalf("Personas: %v", err)
	}
	if len(got.Selected) != 5 {
		t.Fatalf("got %d personas, want exactly 5 (7 valid IDs supplied)", len(got.Selected))
	}
}

// Fewer than three usable picks is an error, not a silently short campaign:
// the exercise asks for 3 to 5 creative variants.
func TestPersonasTooFewIsAnError(t *testing.T) {
	brief := "premium dog food"
	d := fixtureDeps(t, "personas", brief, personaFixture([]string{"persona_004"}))
	if _, err := Personas(context.Background(), d, withBrief(dogFood(), brief), nil); err == nil {
		t.Fatal("want an error when fewer than 3 personas survive filtering")
	}
}

// Enforcement rule 4b (fewer than 3 is an error): filtering unknown/duplicate
// IDs down to under 3 must also error, not just a raw under-3 response. This
// exercises the interaction between rule 1/2 and rule 4, not just rule 4 in
// isolation.
func TestPersonasTooFewAfterFilteringIsAnError(t *testing.T) {
	brief := "premium dog food"
	d := fixtureDeps(t, "personas", brief, personaFixture(
		[]string{"persona_004", "persona_999", "persona_004", "persona_888"}))
	if _, err := Personas(context.Background(), d, withBrief(dogFood(), brief), nil); err == nil {
		t.Fatal("want an error: only 1 of 4 raw entries is a valid, unique catalog ID")
	}
}

// Enforcement rule 5: primary_publishers is filtered against the catalog too,
// not just persona_id.
func TestPersonasFiltersPrimaryPublishersAgainstCatalog(t *testing.T) {
	brief := "premium dog food"
	d := fixtureDeps(t, "personas", brief, map[string]any{
		"selected": []map[string]any{
			{"persona_id": "persona_004", "rationale": "because", "primary_publishers": []string{"pub_007", "pub_999"}},
			{"persona_id": "persona_002", "rationale": "because", "primary_publishers": []string{"pub_003"}},
			{"persona_id": "persona_001", "rationale": "because", "primary_publishers": []string{"pub_001"}},
		},
		"rejected": []map[string]any{{"persona_id": "persona_003", "reason": "no affinity"}},
	})

	got, err := Personas(context.Background(), d, withBrief(dogFood(), brief), nil)
	if err != nil {
		t.Fatalf("Personas: %v", err)
	}
	found := false
	for _, p := range got.Selected {
		if p.PersonaID != "persona_004" {
			continue
		}
		found = true
		for _, pub := range p.PrimaryPublishers {
			if pub == "pub_999" {
				t.Error("pub_999 is not in the catalog and must be dropped from primary_publishers")
			}
		}
		if len(p.PrimaryPublishers) != 1 || p.PrimaryPublishers[0] != "pub_007" {
			t.Errorf("PrimaryPublishers = %v, want [pub_007]", p.PrimaryPublishers)
		}
	}
	if !found {
		t.Fatal("persona_004 should have been selected")
	}
}

// Enforcement rule 6: a persona that made it into Selected must not also
// appear in Rejected, even if the model's own response contradicted itself.
func TestPersonasSelectedNeverAppearsInRejected(t *testing.T) {
	brief := "premium dog food"
	d := fixtureDeps(t, "personas", brief, map[string]any{
		"selected": []map[string]any{
			{"persona_id": "persona_004", "rationale": "because", "primary_publishers": []string{"pub_007"}},
			{"persona_id": "persona_002", "rationale": "because", "primary_publishers": []string{"pub_007"}},
			{"persona_id": "persona_001", "rationale": "because", "primary_publishers": []string{"pub_007"}},
		},
		"rejected": []map[string]any{
			{"persona_id": "persona_004", "reason": "contradiction: model also rejected what it selected"},
			{"persona_id": "persona_003", "reason": "no affinity"},
		},
	})

	got, err := Personas(context.Background(), d, withBrief(dogFood(), brief), nil)
	if err != nil {
		t.Fatalf("Personas: %v", err)
	}
	for _, r := range got.Rejected {
		if r.PersonaID == "persona_004" {
			t.Error("persona_004 was selected and must not also appear in Rejected")
		}
	}
	if len(got.Rejected) != 1 {
		t.Fatalf("got %d rejected, want 1 (persona_003 only)", len(got.Rejected))
	}
}

// A schema property name that drifts from its struct's json tag validates
// cleanly and decodes to a zero value with no error anywhere else in the
// suite. This test marshals a fully-populated model.PersonaSelection,
// validates it against the shipped schema, unmarshals it back, and checks
// every field survived with its exact value.
func TestPersonaSelectionSchemaRoundTrip(t *testing.T) {
	want := model.PersonaSelection{
		Selected: []model.PersonaPick{
			{
				PersonaID: "persona_004",
				Rationale: "The Weekend Warrior already pays a premium for vet-formulated " +
					"nutrition and this brand's science-backed positioning matches that affinity directly.",
				PrimaryPublishers: []string{"pub_007", "pub_012"},
			},
		},
		Rejected: []model.PersonaRejection{
			{
				PersonaID: "persona_003",
				Reason: "The Gifter buys for other people's pets, not their own, and this is a " +
					"subscription product the record lists as a disinterest.",
			},
		},
	}

	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, schema, err := prompts.Load("personas")
	if err != nil {
		t.Fatalf("prompts.Load: %v", err)
	}
	if err := llm.Validate(schema, json.RawMessage(b)); err != nil {
		t.Fatalf("PersonaSelection does not validate against personas.schema.json: %v", err)
	}

	var got model.PersonaSelection
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got.Selected) != 1 || len(got.Rejected) != 1 {
		t.Fatalf("got %d selected, %d rejected, want 1 and 1", len(got.Selected), len(got.Rejected))
	}
	gs, ws := got.Selected[0], want.Selected[0]
	if gs.PersonaID != ws.PersonaID {
		t.Errorf("PersonaID = %q, want %q", gs.PersonaID, ws.PersonaID)
	}
	if gs.Rationale != ws.Rationale {
		t.Errorf("Rationale = %q, want %q", gs.Rationale, ws.Rationale)
	}
	if len(gs.PrimaryPublishers) != len(ws.PrimaryPublishers) {
		t.Fatalf("PrimaryPublishers = %v, want %v", gs.PrimaryPublishers, ws.PrimaryPublishers)
	}
	for i := range ws.PrimaryPublishers {
		if gs.PrimaryPublishers[i] != ws.PrimaryPublishers[i] {
			t.Errorf("PrimaryPublishers[%d] = %q, want %q", i, gs.PrimaryPublishers[i], ws.PrimaryPublishers[i])
		}
	}
	gr, wr := got.Rejected[0], want.Rejected[0]
	if gr.PersonaID != wr.PersonaID {
		t.Errorf("Rejected PersonaID = %q, want %q", gr.PersonaID, wr.PersonaID)
	}
	if gr.Reason != wr.Reason {
		t.Errorf("Rejected Reason = %q, want %q", gr.Reason, wr.Reason)
	}
}
