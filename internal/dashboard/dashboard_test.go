package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yashraj/disco/internal/measure"
)

// writeReport marshals r to a temp file and returns its path.
func writeReport(t *testing.T, dir, name string, r measure.Report) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(r); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	return path
}

func minimalReport(generatedAt time.Time) measure.Report {
	return measure.Report{
		GeneratedAt: generatedAt,
		Provider:    "fixture",
		Model:       "test-model",
		Metrics: map[string]measure.Metric{
			"reason_consistency": {
				Name:    "reason_consistency",
				Summary: "a summary",
				Values:  map[string]float64{"consistency_rate": 0.9, "contradiction_rate": 0.05},
			},
		},
	}
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// TestPageServedAt200 catches the handler not being wired to "/" at all (a
// bad mux registration, or a template that fails to parse and panics
// before ever writing a response).
func TestPageServedAt200(t *testing.T) {
	dir := t.TempDir()
	writeReport(t, dir, "latest.json", minimalReport(time.Now()))

	h := New(filepath.Join(dir, "latest.json"), filepath.Join(dir, "missing-baseline.json"))
	w := get(t, h, "/")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "<title>disco") {
		t.Error("metrics.html was not served")
	}
}

// TestMissingLatestProducesEmptyState catches a missing latest.json being
// treated as a zero-filled report (rendering an empty dashboard of zeros)
// or crashing the handler (a 500) instead of showing the documented empty
// state with the exact command to run.
func TestMissingLatestProducesEmptyState(t *testing.T) {
	dir := t.TempDir()
	h := New(filepath.Join(dir, "does-not-exist.json"), filepath.Join(dir, "also-missing.json"))
	w := get(t, h, "/")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a missing report is not a server error), body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "go run ./cmd/disco measure") {
		t.Error("empty state should give the exact command to run a measurement")
	}
	if strings.Contains(body, "reason_consistency") {
		t.Error("a missing report should not render any metric card, zero-filled or otherwise")
	}
}

// TestStalenessNotice catches the 24h staleness threshold being wrong or
// missing entirely: a report older than 24h must warn the reader it may
// not reflect current code, and a fresh report must not carry that warning
// (a false positive would train engineers to ignore it).
func TestStalenessNotice(t *testing.T) {
	const notice = "may not reflect the current code or prompts"

	t.Run("stale", func(t *testing.T) {
		dir := t.TempDir()
		writeReport(t, dir, "latest.json", minimalReport(time.Now().Add(-30*time.Hour)))
		h := New(filepath.Join(dir, "latest.json"), filepath.Join(dir, "no-baseline.json"))
		w := get(t, h, "/")
		if !strings.Contains(w.Body.String(), notice) {
			t.Error("a 30h-old report should show the staleness notice")
		}
	})

	t.Run("fresh", func(t *testing.T) {
		dir := t.TempDir()
		writeReport(t, dir, "latest.json", minimalReport(time.Now().Add(-1*time.Minute)))
		h := New(filepath.Join(dir, "latest.json"), filepath.Join(dir, "no-baseline.json"))
		w := get(t, h, "/")
		if strings.Contains(w.Body.String(), notice) {
			t.Error("a 1-minute-old report should not show the staleness notice")
		}
	})
}

// TestFindingDetailEscaped catches a raw string-concatenation render path
// (or a template executed with html/template's escaping accidentally
// disabled) that would let model-written finding text inject markup into
// the served page instead of being displayed as text.
func TestFindingDetailEscaped(t *testing.T) {
	dir := t.TempDir()
	r := minimalReport(time.Now())
	m := r.Metrics["reason_consistency"]
	m.Findings = []measure.Finding{{
		BriefN:      3,
		PublisherID: "pub_001",
		Detail:      `<script>alert(1)</script>`,
	}}
	r.Metrics["reason_consistency"] = m
	writeReport(t, dir, "latest.json", r)

	h := New(filepath.Join(dir, "latest.json"), filepath.Join(dir, "no-baseline.json"))
	body := get(t, h, "/").Body.String()

	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("finding detail was rendered unescaped — XSS via model-written text")
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("finding detail should appear HTML-escaped in the rendered page")
	}
}

// TestUnknownMetricRendersGenerically catches the hand-written
// interpretation table (or any per-metric special case) being required for
// a metric to render at all: a metric name absent from that table — a
// fifth metric added later — must still render its values and findings via
// the generic path, not panic or silently vanish.
func TestUnknownMetricRendersGenerically(t *testing.T) {
	dir := t.TempDir()
	r := minimalReport(time.Now())
	r.Metrics["brand_new_metric"] = measure.Metric{
		Name:    "brand_new_metric",
		Summary: "a metric nobody has written interpretation for yet",
		Values:  map[string]float64{"some_rate": 0.42},
		Findings: []measure.Finding{
			{BriefN: 1, PublisherID: "pub_009", Detail: "an example finding"},
		},
	}
	writeReport(t, dir, "latest.json", r)

	h := New(filepath.Join(dir, "latest.json"), filepath.Join(dir, "no-baseline.json"))
	w := get(t, h, "/")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an unrecognized metric name, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "brand_new_metric") {
		t.Error("an unrecognized metric name should still render")
	}
	if !strings.Contains(body, "some_rate") {
		t.Error("an unrecognized metric's values should still render via the generic path")
	}
	if !strings.Contains(body, "No hand-written interpretation recorded yet") {
		t.Error("an unrecognized metric should fall back to the generic interpretation note")
	}
}

// TestBaselineDeltaRendered catches the baseline comparison being wired up
// wrong: present but never actually diffed against latest.json, or
// rendered without any indication of direction (so a reader can't tell a
// regression from an improvement at a glance).
func TestBaselineDeltaRendered(t *testing.T) {
	dir := t.TempDir()
	latest := minimalReport(time.Now())
	latest.Metrics["reason_consistency"] = measure.Metric{
		Name:    "reason_consistency",
		Summary: "a summary",
		Values:  map[string]float64{"consistency_rate": 0.95, "contradiction_rate": 0.02},
	}
	baseline := minimalReport(time.Now().Add(-48 * time.Hour))
	baseline.Metrics["reason_consistency"] = measure.Metric{
		Name:    "reason_consistency",
		Summary: "a summary",
		Values:  map[string]float64{"consistency_rate": 0.80, "contradiction_rate": 0.10},
	}
	writeReport(t, dir, "latest.json", latest)
	writeReport(t, dir, "baseline.json", baseline)

	h := New(filepath.Join(dir, "latest.json"), filepath.Join(dir, "baseline.json"))
	body := get(t, h, "/").Body.String()

	if !strings.Contains(body, "0.800") {
		t.Error("baseline value for consistency_rate should be rendered")
	}
	if !strings.Contains(body, "▲") {
		t.Error("an increase (0.80 -> 0.95) should render an upward-direction marker")
	}
	// html/template HTML-escapes "+" as "&#43;" in text context (visible as
	// a literal "+" once the browser renders it), so check for the escaped
	// form rather than a raw "+0.150".
	if !strings.Contains(body, "&#43;0.150") {
		t.Error("the numeric delta (0.95 - 0.80 = +0.15) should be rendered")
	}
}

// TestBuildReasonBarSegmentsSumToTotal catches a bucketing mistake (a
// disposition double-counted or dropped) that would make the stacked bar's
// segments not add up to the citations they claim to represent, and checks
// that the two neutral buckets use the page's existing --mut/--line tokens
// rather than one of the three status colours.
func TestBuildReasonBarSegmentsSumToTotal(t *testing.T) {
	counts := map[string]int{
		"consistent": 320, "weak": 6, "contradicted": 14,
		"concessive": 6, "advertiser_term": 62, "unscored": 110,
	}
	values := map[string]float64{"coverage_rate": 0.6563706563706564}

	rb := buildReasonBar(counts, values)
	if rb == nil {
		t.Fatal("buildReasonBar returned nil for a complete count set")
	}
	if rb.Total != 518 {
		t.Errorf("Total = %d, want 518 (320+6+14+6+62+110)", rb.Total)
	}
	sum := 0
	for _, s := range rb.Segments {
		sum += s.Count
		if !s.Status && (s.ColorVar != "--mut" && s.ColorVar != "--line") {
			t.Errorf("neutral segment %q used colour %q, want --mut or --line", s.Label, s.ColorVar)
		}
		if s.Status && (s.ColorVar == "--mut" || s.ColorVar == "--line") {
			t.Errorf("status segment %q used a neutral colour %q", s.Label, s.ColorVar)
		}
	}
	if sum != rb.Total {
		t.Errorf("segment counts sum to %d, want %d (the bar's total)", sum, rb.Total)
	}
	if rb.CoverageRatePct != "65.6%" {
		t.Errorf("CoverageRatePct = %q, want 65.6%%", rb.CoverageRatePct)
	}
}

// TestBuildReasonBarMissingCountsFallsBackGenerically catches a report that
// has reason_consistency's Values but not its Counts (an older report
// format, or a hand-edited fixture) crashing instead of falling back to the
// generic values table.
func TestBuildReasonBarMissingCountsFallsBackGenerically(t *testing.T) {
	if rb := buildReasonBar(map[string]int{}, map[string]float64{"coverage_rate": 0.5}); rb != nil {
		t.Errorf("buildReasonBar = %+v, want nil when required counts are absent", rb)
	}
}

// TestBuildAblationChartOrderAndIndependentScales catches the two charts
// losing their shared category order (which is the whole point — it's
// what lets a reader see the two measures disagree) or accidentally being
// scaled off a shared max (a dual axis by another name).
func TestBuildAblationChartOrderAndIndependentScales(t *testing.T) {
	values := map[string]float64{
		"top5_change_rate.A": 0.30, "mean_rank_shift.A": 0.10,
		"top5_change_rate.B": 0.30, "mean_rank_shift.B": 0.90, // ties A on top5, wins on rank
		"top5_change_rate.C": 0.10, "mean_rank_shift.C": 0.05,
		"mean_ungated_per_brief": 7,
	}
	ac := buildAblationChart(values)
	if ac == nil {
		t.Fatal("buildAblationChart returned nil")
	}
	if len(ac.Rows) != 3 {
		t.Fatalf("len(Rows) = %d, want 3", len(ac.Rows))
	}
	// B ties A on top5_change_rate (0.30); tie-break is mean_rank_shift
	// descending, so B (0.90) sorts before A (0.10).
	got := []string{ac.Rows[0].SubScore, ac.Rows[1].SubScore, ac.Rows[2].SubScore}
	want := []string{"B", "A", "C"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Rows[%d] order = %v, want %v", i, got, want)
			break
		}
	}
	// C's rank shift (0.05) is the smallest of the three, so its
	// mean_rank_shift bar must be the narrowest — but its top5 bar (0.10)
	// is also the narrowest on that independent scale, which alone
	// wouldn't prove the axes are independent, so instead check that A and
	// B, tied on top5_change_rate, get equal-width top5 bars but very
	// different mean_rank_shift bars.
	var rowA, rowB AblationRow
	for _, r := range ac.Rows {
		if r.SubScore == "A" {
			rowA = r
		}
		if r.SubScore == "B" {
			rowB = r
		}
	}
	if rowA.Top5W != rowB.Top5W {
		t.Errorf("A and B tie on top5_change_rate but got different bar widths: %s vs %s", rowA.Top5W, rowB.Top5W)
	}
	if rowA.RankW == rowB.RankW {
		t.Errorf("A (0.10) and B (0.90) differ sharply on mean_rank_shift but got equal bar widths: %s", rowA.RankW)
	}
	if ac.MeanUngated != "7" {
		t.Errorf("MeanUngated = %q, want 7", ac.MeanUngated)
	}
}

// TestBuildDistinctivenessParsesFindingsAndSkipsMalformed catches the
// Detail-string regex silently mis-parsing a finding (plotting the wrong
// number) or panicking on one that doesn't match the expected
// "copy_overlap=X lever_overlap=Y" shape, instead of just skipping it.
func TestBuildDistinctivenessParsesFindingsAndSkipsMalformed(t *testing.T) {
	findings := []measure.Finding{
		{BriefN: 4, PublisherID: "persona_006 vs persona_010", Detail: `copy_overlap=0.250 lever_overlap=0.000 — "a" vs "b"`},
		{BriefN: 9, PublisherID: "persona_x vs persona_y", Detail: "not in the expected format at all"},
	}
	values := map[string]float64{"mean_copy_overlap": 0.109}

	dv := buildDistinctiveness(values, findings)
	if dv == nil {
		t.Fatal("buildDistinctiveness returned nil")
	}
	if len(dv.Points) != 1 {
		t.Fatalf("len(Points) = %d, want 1 (the malformed finding should be skipped, not plotted or crash)", len(dv.Points))
	}
	if dv.Points[0].CopyLabel != "0.250" {
		t.Errorf("CopyLabel = %q, want 0.250", dv.Points[0].CopyLabel)
	}
	if dv.Points[0].LeverLabel != "0.000" {
		t.Errorf("LeverLabel = %q, want 0.000", dv.Points[0].LeverLabel)
	}
}

// TestBuildAttributionRequiresAccuracy catches persona_attribution's chart
// being built (and a bogus 0% hero rendered) from a report that lacks
// attribution_accuracy entirely, rather than falling back to the generic
// table.
func TestBuildAttributionRequiresAccuracy(t *testing.T) {
	if av := buildAttribution(map[string]float64{"chance_baseline": 0.33}); av != nil {
		t.Errorf("buildAttribution = %+v, want nil without attribution_accuracy", av)
	}
	av := buildAttribution(map[string]float64{
		"attribution_accuracy": 1, "chance_baseline": 0.3333333333333333,
		"high_confidence_rate_real": 1, "high_confidence_rate_control": 0.16666666666666666,
	})
	if av == nil {
		t.Fatal("buildAttribution returned nil with attribution_accuracy present")
	}
	if av.AccuracyPct != "100%" {
		t.Errorf("AccuracyPct = %q, want 100%%", av.AccuracyPct)
	}
}

// TestChartsRenderWithoutNaNOrClippedSVG loads the repo's real baseline
// fixture (which exercises every one of the four hand-built chart forms at
// once) as both latest and baseline, and checks the served page has an
// <svg> per known metric, every one of them carrying viewBox + role="img"
// + aria-label, and none of the classic float-formatting failure modes
// (NaN, +Inf/-Inf) leaking into the markup.
func TestChartsRenderWithoutNaNOrClippedSVG(t *testing.T) {
	real, err := loadReport("../../evals/measurements/baseline.json")
	if err != nil {
		t.Skipf("repo fixture evals/measurements/baseline.json not readable: %v", err)
	}

	dir := t.TempDir()
	writeReport(t, dir, "latest.json", real)
	writeReport(t, dir, "baseline.json", real)

	h := New(filepath.Join(dir, "latest.json"), filepath.Join(dir, "baseline.json"))
	body := get(t, h, "/").Body.String()

	if strings.Contains(body, "NaN") || strings.Contains(body, "Infinity") {
		t.Error("rendered page contains NaN or Infinity")
	}

	svgCount := strings.Count(body, "<svg")
	if svgCount < 4 {
		t.Errorf("found %d <svg> elements, want at least 4 (one per known metric's chart)", svgCount)
	}
	for _, tag := range strings.Split(body, "<svg")[1:] {
		end := strings.Index(tag, ">")
		if end == -1 {
			t.Fatal("unterminated <svg tag in rendered page")
		}
		attrs := tag[:end]
		if !strings.Contains(attrs, "viewBox=") {
			t.Errorf("<svg %s...> is missing viewBox", attrs)
		}
		if !strings.Contains(attrs, `role="img"`) {
			t.Errorf("<svg %s...> is missing role=\"img\"", attrs)
		}
		if !strings.Contains(attrs, "aria-label=") {
			t.Errorf("<svg %s...> is missing aria-label", attrs)
		}
	}

	for _, name := range []string{"reason_consistency", "scoring_ablation", "creative_distinctiveness", "persona_attribution"} {
		if !strings.Contains(body, "<h3>"+name+"</h3>") {
			t.Errorf("metric section %q not found in rendered page", name)
		}
	}
}
