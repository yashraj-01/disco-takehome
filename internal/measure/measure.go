// Package measure builds quality metrics for campaign output, measured
// against arithmetic the pipeline itself already computed rather than human
// judgment, an LLM judge, or invented ground truth.
//
// This is a different concern from internal/eval. eval is a gate: binary
// invariant checks ("every catalog publisher gets a ledger entry") that a
// build can fail on. measure is instrumentation: rates tracked over time so
// a regression in generation quality is visible even when every invariant
// still holds.
//
// The first metric, ReasonConsistency (see reasons.go), is a heuristic
// detector with high precision and unknown recall. When it flags a
// contradiction, that is a real finding: the model's written reason cites a
// sub-score the pipeline itself computed, and the two disagree. But a reason
// that expresses a real concept in words the phrase map does not contain is
// silently counted as "vague" rather than checked, so a high vagueness rate
// is ambiguous — it may mean the reasons are genuinely empty, or it may mean
// the phrase map is too narrow. Report both possibilities; the number alone
// does not distinguish them.
package measure

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Report is one measurement run across every metric computed.
type Report struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Provider    string            `json:"provider"`
	Model       string            `json:"model"`
	Metrics     map[string]Metric `json:"metrics"`
}

// Metric is one named measurement: headline rates, the counts that back
// them, and concrete findings an engineer can quote back without re-deriving
// them.
type Metric struct {
	Name     string             `json:"name"`
	Summary  string             `json:"summary"`
	Values   map[string]float64 `json:"values"`
	Counts   map[string]int     `json:"counts"`
	Findings []Finding          `json:"findings,omitempty"`
}

// Finding is one concrete, quotable example backing a metric.
type Finding struct {
	BriefN      int    `json:"brief_n"`
	PublisherID string `json:"publisher_id"`
	Detail      string `json:"detail"`
}

// WriteJSON writes the report as indented JSON.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteText writes a human-readable rendering of every metric: its summary,
// its values and counts (sorted so output is stable across runs), and its
// findings.
func (r Report) WriteText(w io.Writer) error {
	fmt.Fprintf(w, "measured %s  provider=%s  model=%s\n",
		r.GeneratedAt.Format(time.RFC3339), r.Provider, r.Model)

	for _, name := range sortedMetricNames(r.Metrics) {
		m := r.Metrics[name]
		fmt.Fprintf(w, "\n=== %s ===\n", name)
		if m.Summary != "" {
			fmt.Fprintf(w, "%s\n", m.Summary)
		}

		if len(m.Values) > 0 {
			fmt.Fprintln(w, "\nvalues:")
			for _, k := range sortedFloatKeys(m.Values) {
				fmt.Fprintf(w, "  %-22s %.4f\n", k, m.Values[k])
			}
		}

		if len(m.Counts) > 0 {
			fmt.Fprintln(w, "\ncounts:")
			for _, k := range sortedIntKeys(m.Counts) {
				fmt.Fprintf(w, "  %-22s %d\n", k, m.Counts[k])
			}
		}

		if len(m.Findings) > 0 {
			fmt.Fprintf(w, "\nfindings (%d):\n", len(m.Findings))
			for _, f := range m.Findings {
				fmt.Fprintf(w, "  [brief %d] %s: %s\n", f.BriefN, f.PublisherID, f.Detail)
			}
		}
	}
	return nil
}

// Diff compares two reports metric by metric and value by value, returning a
// human-readable rendering of the deltas. A metric or a value present on
// only one side is reported as missing rather than causing a crash or being
// silently dropped, so a baseline recorded before a metric existed (or one
// retired since) is still safe to diff against.
func Diff(current, baseline Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "current:  %s\n", current.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "baseline: %s\n", baseline.GeneratedAt.Format(time.RFC3339))

	for _, name := range sortedMetricNames(unionMetrics(current, baseline)) {
		cur, curOK := current.Metrics[name]
		base, baseOK := baseline.Metrics[name]
		switch {
		case !curOK:
			fmt.Fprintf(&b, "\n%s: present in baseline only\n", name)
			continue
		case !baseOK:
			fmt.Fprintf(&b, "\n%s: present in current only\n", name)
			continue
		}

		fmt.Fprintf(&b, "\n%s:\n", name)
		keys := unionFloatKeys(cur.Values, base.Values)
		for _, k := range keys {
			cv, cok := cur.Values[k]
			bv, bok := base.Values[k]
			switch {
			case !cok:
				fmt.Fprintf(&b, "  %-22s missing in current (baseline %.4f)\n", k, bv)
			case !bok:
				fmt.Fprintf(&b, "  %-22s missing in baseline (current %.4f)\n", k, cv)
			default:
				delta := cv - bv
				sign := "+"
				if delta < 0 {
					sign = ""
				}
				fmt.Fprintf(&b, "  %-22s %.4f -> %.4f (%s%.4f)\n", k, bv, cv, sign, delta)
			}
		}
	}
	return b.String()
}

func unionMetrics(a, b Report) map[string]Metric {
	out := make(map[string]Metric, len(a.Metrics)+len(b.Metrics))
	for k, v := range a.Metrics {
		out[k] = v
	}
	for k, v := range b.Metrics {
		if _, ok := out[k]; !ok {
			out[k] = v
		}
	}
	return out
}

func sortedMetricNames(m map[string]Metric) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedFloatKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedIntKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func unionFloatKeys(a, b map[string]float64) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, m := range []map[string]float64{a, b} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}
