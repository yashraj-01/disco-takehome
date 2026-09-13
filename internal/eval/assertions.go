// Package eval runs every example brief and checks the invariants that can
// actually be asserted.
//
// These are structural and judgment checks, not copy quality. Whether a
// headline is good is not measurable at this scale; whether the system refused
// a brief it could not serve, and excluded a publisher for the right stated
// reason, is binary and cheap to verify.
package eval

import (
	"fmt"
	"math"
	"strings"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
)

// Check returns the assertion failures for one brief's campaign. An empty slice
// means the brief passed.
func Check(b Brief, c model.Campaign, cat *catalog.Catalog) []string {
	var f []string
	add := func(format string, args ...any) { f = append(f, fmt.Sprintf(format, args...)) }

	// --- Universal invariants ---

	if n := len(c.PublisherLedger); n != len(cat.Publishers) {
		add("ledger has %d entries, want %d", n, len(cat.Publishers))
	}
	seen := map[string]bool{}
	for _, e := range c.PublisherLedger {
		if !cat.HasPublisher(e.PublisherID) {
			add("ledger contains unknown publisher %q", e.PublisherID)
		}
		if seen[e.PublisherID] {
			add("publisher %s appears twice in the ledger", e.PublisherID)
		}
		seen[e.PublisherID] = true
		if strings.TrimSpace(e.Reason) == "" {
			add("publisher %s has no reason", e.PublisherID)
		}
	}

	for _, a := range c.Budget.Allocation {
		if !cat.HasPublisher(a.PublisherID) {
			add("allocation to unknown publisher %q", a.PublisherID)
		}
	}
	if len(c.Budget.Allocation) > 0 {
		var sum float64
		for _, a := range c.Budget.Allocation {
			sum += a.Share
		}
		if math.Abs(sum-1) > 0.001 {
			add("allocation shares sum to %.4f, want 1.0", sum)
		}
	}

	personas := map[string]bool{}
	for _, cr := range c.Creatives {
		if !cat.HasPersona(cr.PersonaID) {
			add("creative for unknown persona %q", cr.PersonaID)
			continue
		}
		if personas[cr.PersonaID] {
			add("two creatives written for persona %s", cr.PersonaID)
		}
		personas[cr.PersonaID] = true

		if n := len([]rune(cr.Headline)); n == 0 || n > 60 {
			add("%s headline is %d runes, want 1 to 60", cr.PersonaID, n)
		}
		if n := len([]rune(cr.Body)); n == 0 || n > 200 {
			add("%s body is %d runes, want 1 to 200", cr.PersonaID, n)
		}

		// Every lever must appear verbatim in that persona's own record.
		p, _ := cat.Persona(cr.PersonaID)
		have := map[string]bool{}
		for _, m := range p.MessagingPreferences {
			have[strings.ToLower(m)] = true
		}
		if len(cr.MessagingLevers) == 0 {
			add("%s creative cites no messaging lever from the persona record", cr.PersonaID)
		}
		for _, l := range cr.MessagingLevers {
			if !have[strings.ToLower(l)] {
				add("%s cites lever %q, which is not in its persona record", cr.PersonaID, l)
			}
		}
	}

	if c.Status == model.StatusReady || c.Status == model.StatusNeedsClarification {
		if n := len(c.Creatives); n < 3 || n > 5 {
			add("got %d creatives, want 3 to 5", n)
		}
	}

	// --- Per-brief judgment invariants ---

	verdict := func(id string) model.LedgerEntry {
		for _, e := range c.PublisherLedger {
			if e.PublisherID == id {
				return e
			}
		}
		return model.LedgerEntry{}
	}
	rankOf := func(id string) int {
		for _, e := range c.PublisherLedger {
			if e.PublisherID == id && e.Verdict == "recommended" {
				return e.Rank
			}
		}
		return 0
	}
	allocatedTo := func(id string) bool {
		for _, a := range c.Budget.Allocation {
			if a.PublisherID == id {
				return true
			}
		}
		return false
	}

	switch b.N {
	case 7: // B2B SaaS for dental practices — nothing in this catalog can serve it
		if c.Status != model.StatusNoRecommendation {
			add("status = %q, want %q for a non-consumer brief", c.Status, model.StatusNoRecommendation)
		}
		if len(c.Budget.Allocation) != 0 {
			add("allocated budget for a brief this catalog cannot serve")
		}
	case 5, 8, 15: // deliberately low-signal briefs
		if c.Status != model.StatusNeedsClarification {
			add("status = %q, want %q for a low-signal brief", c.Status, model.StatusNeedsClarification)
		}
		if len(c.Advertiser.MissingSignals) == 0 {
			add("low-confidence brief names no missing signals")
		}
		if len(c.Clarifications) == 0 {
			add("low-confidence brief asks no clarifying questions")
		}
	case 2: // women's activewear, 25-45 — Linden Park skews 50-70
		e := verdict("pub_005")
		if e.Verdict != "excluded" {
			add("Linden Park verdict = %q, want excluded on demographics", e.Verdict)
		}
		if e.HardGate != "demographic_mismatch" {
			add("Linden Park gate = %q, want demographic_mismatch", e.HardGate)
		}
	case 1: // premium senior dog food
		if r := rankOf("pub_007"); r == 0 || r > 2 {
			add("Pawline rank = %d, want top 2 for a premium pet brief", r)
		}
	case 10: // $1,200 handbags — Swiftcart shoppers spend $28
		if allocatedTo("pub_001") {
			add("allocated budget to Swiftcart (AOV $28) for a $1,200 product")
		}
	case 6: // $650 ski shells — mid-income publishers are the wrong room
		for _, id := range []string{"pub_006", "pub_010"} {
			if allocatedTo(id) {
				add("allocated budget to mid-income publisher %s for a $650+ product", id)
			}
		}
	}

	return f
}
