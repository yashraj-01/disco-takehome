package render

import (
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/yashraj/disco/internal/model"
)

// rationaleWrapWidth is the wrap width used for free-text explanations
// (allocation rationale, ledger reasons, creative rationale). These strings
// can carry a full sentence — including the ExceedsMaxShare explanation
// appended in pipeline.Build — so they are wrapped onto multiple indented
// lines rather than truncated. A truncated explanation is worse than none:
// it looks like the system had a reason and lost it.
const rationaleWrapWidth = 76

// Terminal writes a human-readable campaign summary.
//
// Exclusions are printed as prominently as picks. A ranked list alone is a
// black box; the reason a publisher was left out is usually the more
// interesting half of the answer.
func Terminal(w io.Writer, c model.Campaign) error {
	p := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\n", args...)
	}
	rule := func() { p("%s", strings.Repeat("─", 72)) }
	// wrapped prints s wrapped at rationaleWrapWidth, every line (including
	// the first) prefixed with indent. Used anywhere a rationale/reason
	// string might be long enough to need it instead of being cut off.
	wrapped := func(indent, s string) {
		for _, line := range wrapLines(s, rationaleWrapWidth) {
			p("%s%s", indent, line)
		}
	}

	rule()
	switch c.Status {
	case model.StatusNoRecommendation:
		p("NO RECOMMENDATION")
	case model.StatusNeedsClarification:
		p("PROVISIONAL — LOW CONFIDENCE")
	default:
		p("CAMPAIGN DRAFT")
	}
	p("%s", c.Advertiser.Brief)
	rule()

	if len(c.Clarifications) > 0 {
		p("\nBefore this is worth running, we need to know:")
		for _, q := range c.Clarifications {
			p("  ? %s", q)
		}
	}
	if len(c.Advertiser.Assumptions) > 0 {
		p("\nAssumptions made:")
		for _, a := range c.Advertiser.Assumptions {
			p("  · %s", a)
		}
	}

	if c.Status != model.StatusNoRecommendation {
		p("\nObjective: %s   Flight: %s for %d days   Confidence: %s",
			c.Objective, c.Flight.Start, c.Flight.DurationDays, c.Advertiser.Confidence)

		p("\nBUDGET  $%s total, $%s/day", comma(c.Budget.TotalUSD), comma(c.Budget.DailyCapUSD))
		// The budget these publishers can absorb before the allocation stops
		// following fit. Stated as the basis when the advertiser named no
		// budget, and as a comparison when they named one.
		if r := c.Budget.RecommendedUSD; r > 0 {
			switch {
			case math.Abs(c.Budget.TotalUSD-r) < 1:
				p("  no budget given — sized to $%s, the most these publishers absorb before inventory limits start redirecting spend", comma(r))
			case c.Budget.TotalUSD > r:
				p("  recommended $%s — beyond it a publisher hits its inventory ceiling and spend shifts to whoever has room", comma(r))
			default:
				p("  recommended $%s — room for $%s more before inventory limits bind", comma(r), comma(r-c.Budget.TotalUSD))
			}
		}
		// UnallocatedUSD is a headline product signal — the budget exceeds
		// what these recommended publishers can physically deliver this
		// flight — so it is printed right under the budget header, before
		// the per-publisher table, not buried after it or omitted.
		if c.Budget.UnallocatedUSD > 0 {
			p("  ** UNALLOCATED: $%s cannot be spent — the recommended publishers' combined "+
				"deliverable inventory falls short of the budget for this flight **",
				dollars2(c.Budget.UnallocatedUSD))
		}
		if len(c.Budget.Allocation) > 0 {
			p("  %-20s %7s %11s %8s %13s", "PUBLISHER", "SHARE", "SPEND", "CPM", "IMPRESSIONS")
			for _, a := range c.Budget.Allocation {
				gate := ""
				if a.ExceedsMaxShare {
					gate = "  [exceeds 40% share guideline]"
				}
				p("  %-20s %6.1f%% %11s %8.2f %13s%s",
					trunc(a.PublisherName, 20), a.Share*100,
					"$"+comma(a.AmountUSD), a.EstCPMUSD, commaInt(a.EstImpressions), gate)
				wrapped("      ", a.Rationale)
			}
		}

		p("\nBID  %s, %s   floor $%.2f · target $%.2f · ceiling $%.2f",
			c.Bid.Model, c.Bid.Strategy, c.Bid.FloorUSD, c.Bid.TargetUSD, c.Bid.CeilingUSD)
		if c.Bid.TargetCPAUSD > 0 {
			p("     target CPA $%.2f", c.Bid.TargetCPAUSD)
		}

		p("\nTARGETING  age %s · %s · %s",
			c.Targeting.AgeRange, c.Targeting.GenderSkew, strings.Join(c.Targeting.Geos, ", "))
		if len(c.Targeting.PersonaIDs) > 0 {
			p("           personas: %s", strings.Join(c.Targeting.PersonaIDs, ", "))
		}

		if len(c.Creatives) > 0 {
			p("\nCREATIVE")
			for i, cr := range c.Creatives {
				p("\n  [%d] %s", i+1, cr.PersonaID)
				p("      %s", cr.Headline)
				p("      %s", cr.Body)
				// Rationale is wrapped with a "why: " lead-in on the first
				// line and matching indent on continuations, rather than
				// truncated.
				lines := wrapLines(cr.Rationale, rationaleWrapWidth-5)
				for i, line := range lines {
					if i == 0 {
						p("      why: %s", line)
					} else {
						p("           %s", line)
					}
				}
				if len(cr.MessagingLevers) > 0 {
					p("      levers: %s", strings.Join(cr.MessagingLevers, ", "))
				}
			}
		}
	}

	var rec, con, exc []model.LedgerEntry
	for _, e := range c.PublisherLedger {
		switch e.Verdict {
		case "recommended":
			rec = append(rec, e)
		case "considered":
			con = append(con, e)
		default:
			exc = append(exc, e)
		}
	}

	p("\nPUBLISHER LEDGER  %d recommended · %d considered · %d excluded  (%d total)",
		len(rec), len(con), len(exc), len(c.PublisherLedger))
	section := func(title string, es []model.LedgerEntry) {
		if len(es) == 0 {
			return
		}
		p("\n  %s", title)
		for _, e := range es {
			gate := ""
			if e.HardGate != "" {
				gate = "  [" + e.HardGate + "]"
			}
			p("    %-20s %.2f%s", trunc(e.PublisherName, 20), e.Score, gate)
			wrapped("      ", e.Reason)
		}
	}
	section("RECOMMENDED", rec)
	section("CONSIDERED", con)
	section("EXCLUDED", exc)

	if len(c.PersonaRejections) > 0 {
		p("\nPERSONAS NOT SELECTED")
		for _, r := range c.PersonaRejections {
			p("    %-14s", r.PersonaID)
			wrapped("      ", r.Reason)
		}
	}

	p("\n%s · %s · pipeline v%s", c.Meta.Provider, c.Meta.Model, c.Meta.PipelineVersion)
	return nil
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// wrapLines word-wraps s to width, returning one entry per line. Unlike a
// fixed-width truncation, no text is ever dropped — this is what keeps a long
// rationale (e.g. the ExceedsMaxShare explanation appended in
// pipeline.Build) intact instead of cut off mid-sentence.
func wrapLines(s string, width int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	words := strings.Fields(s)
	var lines []string
	var cur strings.Builder
	for _, word := range words {
		switch {
		case cur.Len() == 0:
			cur.WriteString(word)
		case cur.Len()+1+len(word) > width:
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(word)
		default:
			cur.WriteString(" ")
			cur.WriteString(word)
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// comma formats a dollar amount with thousands separators, no decimals.
func comma(v float64) string { return commaInt(int64(v + 0.5)) }

// dollars2 formats a dollar amount with thousands separators and cents. Used
// for UnallocatedUSD, which is worth showing precisely since it is a
// headline shortfall figure rather than a rounded summary number.
func dollars2(v float64) string {
	cents := int64(v*100 + 0.5)
	whole := cents / 100
	frac := cents % 100
	if frac < 0 {
		frac = -frac
	}
	return fmt.Sprintf("%s.%02d", commaInt(whole), frac)
}

func commaInt(v int64) string {
	s := fmt.Sprintf("%d", v)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
