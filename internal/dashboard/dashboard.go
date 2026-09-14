// Package dashboard serves a read-only metrics page for "disco metrics".
//
// It renders whatever measure.Report "disco measure" last wrote to disk. It
// never runs the pipeline and never calls a model: there is no refresh
// button here, because a refresh that silently re-measures would either
// burn API quota or take minutes to answer a click. Staleness is instead
// made visible (an absolute + relative "generated" timestamp, and a notice
// past 24h) so the reader can judge for themselves whether the numbers are
// still trustworthy.
//
// Every string that could originate from the model or the report body
// (finding text, publisher/persona ids, the report's own free-text summary)
// is rendered through html/template, which HTML-escapes it contextually —
// the same discipline web/index.html applies by hand with its JS esc().
//
// The page is primarily visual: each of the four known metrics gets a
// hand-authored inline-SVG (or CSS-grid) chart built from that metric's
// values/counts/findings. A metric name the chart-building code doesn't
// recognize (a fifth metric added later, a typo'd name in a test) still
// renders — see buildMetricView — via the generic values table alone,
// which is also kept, collapsed behind a <details>, for every metric as
// the exact-number fallback.
package dashboard

import (
	"fmt"
	"html/template"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"encoding/json"

	"github.com/yashraj/disco/internal/measure"
	"github.com/yashraj/disco/internal/server"
	"github.com/yashraj/disco/web"
)

// page is parsed once at package init: a bad template is a build-time
// mistake, not something that should surface as a 500 on first request.
var page = template.Must(template.New("metrics.html").ParseFS(web.FS, "metrics.html"))

// direction records, for the value keys we have an actual opinion about,
// which way is "better". A key absent from this table renders its delta in
// neutral colour: we do not guess whether e.g. more copy overlap is good or
// bad without knowing what's being tested, and a perfect attribution
// accuracy has no headroom, so a couple of these are listed only for the
// arrow, not because up is unambiguously good.
type direction int

const (
	neutral direction = iota
	higherBetter
	lowerBetter
)

var knownDirection = map[string]direction{
	"consistency_rate":          higherBetter,
	"contradiction_rate":        lowerBetter,
	"vagueness_rate":            lowerBetter,
	"coverage_rate":             higherBetter,
	"attribution_accuracy":      higherBetter,
	"lift":                      higherBetter,
	"confidence_separation":     higherBetter,
	"high_confidence_rate_real": higherBetter,
	// chance_baseline and high_confidence_rate_control are properties of the
	// judge/control, not a quality signal to chase. mean_copy_overlap,
	// mean_lever_overlap, max_copy_overlap, campaigns_measured,
	// mean_ungated_per_brief, and every top5_change_rate./mean_rank_shift.
	// key are deliberately left out: "better" depends on what's being
	// tested, or the number isn't a quality signal at all.
}

// interpretation is the hand-written, per-metric explanation the task asks
// for: what a metric measures and, just as important, what it does not. A
// metric name absent from this table (a fifth metric added later, or a
// name typo'd in a test) still renders — see MetricView.Interpretation and
// the template's generic fallback — it just gets a generic note instead of
// bespoke prose.
var interpretation = map[string]string{
	"reason_consistency":       "Checks whether a stated reason is TRUE against the sub-score it cites — not whether it is persuasive. `coverage_rate` is the share of citations the metric could adjudicate; the rest are deliberate abstentions where the referent was ambiguous.",
	"scoring_ablation":         "Whether removing a sub-score changes the top-5 ranking. Measures whether a weight is load-bearing, not whether it is correct. Note `mean_ungated_per_brief` — hard gates eliminate most of the catalog before any weight applies.",
	"creative_distinctiveness": "Whether the per-persona creatives differ from each other. Low overlap does NOT mean the copy is good or on-target — two texts can be entirely distinct and both wrong. Shared product nouns put a non-zero floor under every pair.",
	"persona_attribution":      "Whether a blind judge can match copy back to the persona it was written for. `confidence_separation` validates the judge: high confidence on real matchings versus impossible controls. A perfect accuracy has no headroom — it shows the copy is targeted, not that it is excellent.",
}

const staleAfter = 24 * time.Hour

// PageData is everything the template needs, precomputed so the template
// itself stays free of formatting logic.
type PageData struct {
	HasLatest    bool
	LoadErr      string
	GeneratedAbs string
	GeneratedRel string
	Stale        bool
	Provider     string
	Model        string

	HasBaseline bool
	BaselineAbs string
	RawDiff     string

	Metrics []MetricView
}

// MetricView is one metric.Name's worth of rendering-ready data. Exactly
// one of the four chart pointers below is non-nil for a recognized metric
// name; all four are nil for anything else, and the template falls back to
// the generic summary + values table + findings presentation.
type MetricView struct {
	Name           string
	Interpretation string
	Summary        string
	ExtraValues    []ValueRow
	Findings       []measure.Finding

	ReasonBar       *ReasonBarView       // reason_consistency only
	AblationChart   *AblationChartView   // scoring_ablation only
	Distinctiveness *DistinctivenessView // creative_distinctiveness only
	Attribution     *AttributionView     // persona_attribution only
}

// ValueRow is one metric.Values entry plus its baseline comparison, if any.
// This is the "table view" fallback the task requires for every chart —
// the precise-number path — kept in full (no keys filtered out) even for
// metrics that also get a chart.
type ValueRow struct {
	Key        string
	Value      string
	HasBase    bool
	Base       string
	Arrow      string
	Delta      string
	ColorClass string
}

// ---- reason_consistency: one horizontal stacked bar (part-to-whole) ----

// ReasonBarView draws citation dispositions as a single stacked bar.
// Colour: consistent=good, weak=warning, contradicted=critical; the
// abstentions (concessive + advertiser_term, merged into one bucket) and
// unscored are neutral — drawn in the page's existing --mut/--line tokens,
// never a status colour, and their labels are always placed outside the
// fill (a caption under the bar) rather than risk low contrast text-on-fill
// once the reader is in dark mode, where --mut/--line swap relative
// lightness.
type ReasonBarView struct {
	Segments        []ReasonSegment
	Total           int
	CoverageRatePct string
	AriaLabel       string
}

// ReasonSegment is one disposition bucket's share of the bar, in SVG
// viewBox units (0-1000 wide) precomputed so the template does no math.
type ReasonSegment struct {
	Label     string
	Count     int
	PctLabel  string
	X         string // formatted viewBox x
	W         string // formatted viewBox width
	CenterX   string // formatted viewBox center, for labels
	ColorVar  string // CSS variable the segment fills with
	Status    bool   // true = good/warn/crit segment; false = neutral grey
	Inside    bool   // wide enough for a centered label directly on the fill
	LeaderTop string // formatted y for an outside leader label (status, !Inside only)
	LeaderMid string // formatted y for the leader connector line's label end
}

const reasonBarViewW = 1000.0

func buildReasonBar(counts map[string]int, values map[string]float64) *ReasonBarView {
	need := []string{"consistent", "weak", "contradicted", "concessive", "advertiser_term", "unscored"}
	for _, k := range need {
		if _, ok := counts[k]; !ok {
			return nil
		}
	}
	type bucket struct {
		label    string
		count    int
		colorVar string
		status   bool
	}
	buckets := []bucket{
		{"consistent", counts["consistent"], "--c-good", true},
		{"weak", counts["weak"], "--c-warn", true},
		{"contradicted", counts["contradicted"], "--c-crit", true},
		{"abstained", counts["concessive"] + counts["advertiser_term"], "--mut", false},
		{"unscored", counts["unscored"], "--line", false},
	}
	total := 0
	for _, b := range buckets {
		total += b.count
	}
	if total <= 0 {
		return nil
	}

	// gap is the surface-coloured gap between adjacent segments, in viewBox
	// units — inset from each segment's true (gapless) share so the fills
	// never touch, while the outer bar (x=0 to reasonBarViewW) still meets
	// the clip-path exactly for its rounded corners.
	const gap = 4.0

	segs := make([]ReasonSegment, 0, len(buckets))
	x := 0.0
	tier := 0
	for i, b := range buckets {
		pct := float64(b.count) / float64(total) * 100
		w := pct / 100 * reasonBarViewW
		rectX, rectW := x, w
		if i > 0 {
			rectX += gap / 2
			rectW -= gap / 2
		}
		if i < len(buckets)-1 {
			rectW -= gap / 2
		}
		if rectW < 0 {
			rectW = 0
		}
		seg := ReasonSegment{
			Label:    b.label,
			Count:    b.count,
			PctLabel: fmt.Sprintf("%.1f%%", pct),
			X:        f2(rectX),
			W:        f2(rectW),
			CenterX:  f2(x + w/2),
			ColorVar: b.colorVar,
			Status:   b.status,
			Inside:   b.status && pct >= 8,
		}
		if b.status && !seg.Inside {
			// Alternate the leader-label row so two small adjacent
			// segments (weak, contradicted here) don't collide.
			if tier%2 == 0 {
				seg.LeaderTop, seg.LeaderMid = "34", "44"
			} else {
				seg.LeaderTop, seg.LeaderMid = "54", "64"
			}
			tier++
		}
		segs = append(segs, seg)
		x += w
	}

	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		parts = append(parts, fmt.Sprintf("%s %d (%s)", s.Label, s.Count, s.PctLabel))
	}
	aria := fmt.Sprintf("Stacked bar of %d citation dispositions: %s", total, strings.Join(parts, ", "))

	return &ReasonBarView{
		Segments:        segs,
		Total:           total,
		CoverageRatePct: fmt.Sprintf("%.1f%%", values["coverage_rate"]*100),
		AriaLabel:       aria,
	}
}

// ---- scoring_ablation: two small-multiple horizontal bar charts ----

// AblationChartView is scoring_ablation's six sub-scores drawn as two
// separate bar charts (top5_change_rate, mean_rank_shift), sharing one
// category order — sorted by top5_change_rate descending — so a reader can
// see where the two measures disagree. Each chart scales to its own max:
// never a dual axis.
type AblationChartView struct {
	Rows          []AblationRow
	Height        string // shared viewBox height for both charts
	MeanUngated   string
	Top5AriaLabel string
	RankAriaLabel string
}

// AblationRow is one sub-score's two bar widths, precomputed in SVG
// viewBox units, plus its shared row centerline Y.
type AblationRow struct {
	SubScore  string
	Y         string
	Top5Label string
	Top5W     string
	RankLabel string
	RankW     string
}

const ablationRowH = 46.0
const ablationPadTop = 16.0
const ablationBarAreaW = 560.0
const ablationLabelX = 250.0

func buildAblationChart(values map[string]float64) *AblationChartView {
	type agg struct {
		name string
		top5 float64
		rank float64
	}
	var aggs []agg
	for k, v := range values {
		sub, ok := strings.CutPrefix(k, "top5_change_rate.")
		if !ok {
			continue
		}
		aggs = append(aggs, agg{name: sub, top5: v, rank: values["mean_rank_shift."+sub]})
	}
	if len(aggs) == 0 {
		return nil
	}
	sort.Slice(aggs, func(i, j int) bool {
		if aggs[i].top5 != aggs[j].top5 {
			return aggs[i].top5 > aggs[j].top5
		}
		if aggs[i].rank != aggs[j].rank {
			return aggs[i].rank > aggs[j].rank
		}
		return aggs[i].name < aggs[j].name
	})

	maxTop5, maxRank := 0.0, 0.0
	for _, a := range aggs {
		if a.top5 > maxTop5 {
			maxTop5 = a.top5
		}
		if a.rank > maxRank {
			maxRank = a.rank
		}
	}

	rows := make([]AblationRow, 0, len(aggs))
	for i, a := range aggs {
		y := ablationPadTop + float64(i)*ablationRowH + ablationRowH/2
		top5W := 0.0
		if maxTop5 > 0 {
			top5W = a.top5 / maxTop5 * ablationBarAreaW
		}
		rankW := 0.0
		if maxRank > 0 {
			rankW = a.rank / maxRank * ablationBarAreaW
		}
		rows = append(rows, AblationRow{
			SubScore:  a.name,
			Y:         f2(y),
			Top5Label: fmt.Sprintf("%.1f%%", a.top5*100),
			Top5W:     f2(top5W),
			RankLabel: formatValue(a.rank),
			RankW:     f2(rankW),
		})
	}

	top5Parts := make([]string, 0, len(rows))
	rankParts := make([]string, 0, len(rows))
	for _, r := range rows {
		top5Parts = append(top5Parts, fmt.Sprintf("%s %s", r.SubScore, r.Top5Label))
		rankParts = append(rankParts, fmt.Sprintf("%s %s", r.SubScore, r.RankLabel))
	}

	height := ablationPadTop*2 + float64(len(aggs))*ablationRowH
	return &AblationChartView{
		Rows:          rows,
		Height:        f2(height),
		MeanUngated:   formatValue(values["mean_ungated_per_brief"]),
		Top5AriaLabel: "top5_change_rate by sub-score, sorted descending: " + strings.Join(top5Parts, ", "),
		RankAriaLabel: "mean_rank_shift by the same sub-score order: " + strings.Join(rankParts, ", "),
	}
}

// ---- creative_distinctiveness: horizontal strip/dot plot ----

// copyLeverRe pulls the two overlap numbers back out of a
// creative_distinctiveness finding's free-text Detail (e.g.
// "copy_overlap=0.250 lever_overlap=0.000 — ..."). measure.Finding only
// carries a rendered string, not structured fields, and internal/measure is
// off-limits to change, so the dashboard parses its own metric's
// well-known detail format instead. A finding whose detail doesn't match
// (a hand-edited fixture, a future format change) is simply skipped rather
// than plotted with garbage numbers.
var copyLeverRe = regexp.MustCompile(`copy_overlap=([0-9.]+)\s+lever_overlap=([0-9.]+)`)

// DistinctivenessView plots each finding's copy_overlap as a dot on a
// shared 0-1 axis, with a lighter second series for lever_overlap and the
// campaign mean drawn as a labelled reference line.
type DistinctivenessView struct {
	Points        []DistinctivenessPoint
	MeanCopyLabel string
	MeanCopyX     string
	Ticks         []AxisTick
	Height        string
	AxisX0        string
	AxisX1        string
	AriaLabel     string
}

// DistinctivenessPoint is one worst-pair finding's two dots.
type DistinctivenessPoint struct {
	Label      string
	CopyLabel  string
	CopyX      string
	LeverLabel string
	LeverX     string
	Y          string
}

// AxisTick is one 0/0.25/0.5/0.75/1 gridline of the 0-1 axis.
type AxisTick struct {
	X     string
	Label string
}

const distRowH = 40.0
const distPadTop = 26.0
const distAxisX0 = 280.0
const distAxisW = 820.0

func buildDistinctiveness(values map[string]float64, findings []measure.Finding) *DistinctivenessView {
	pts := make([]DistinctivenessPoint, 0, len(findings))
	i := 0
	for _, f := range findings {
		m := copyLeverRe.FindStringSubmatch(f.Detail)
		if m == nil {
			continue
		}
		copyV, err1 := strconv.ParseFloat(m[1], 64)
		leverV, err2 := strconv.ParseFloat(m[2], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		y := distPadTop + float64(i)*distRowH + distRowH/2
		pts = append(pts, DistinctivenessPoint{
			Label:      fmt.Sprintf("brief %d: %s", f.BriefN, f.PublisherID),
			CopyLabel:  fmt.Sprintf("%.3f", copyV),
			CopyX:      f2(distAxisX0 + copyV*distAxisW),
			LeverLabel: fmt.Sprintf("%.3f", leverV),
			LeverX:     f2(distAxisX0 + leverV*distAxisW),
			Y:          f2(y),
		})
		i++
	}
	if len(pts) == 0 {
		return nil
	}

	mean := values["mean_copy_overlap"]
	ticks := make([]AxisTick, 0, 5)
	for _, t := range []float64{0, 0.25, 0.5, 0.75, 1} {
		ticks = append(ticks, AxisTick{X: f2(distAxisX0 + t*distAxisW), Label: fmt.Sprintf("%.2g", t)})
	}

	ptParts := make([]string, 0, len(pts))
	for _, p := range pts {
		ptParts = append(ptParts, fmt.Sprintf("%s copy overlap %s, lever overlap %s", p.Label, p.CopyLabel, p.LeverLabel))
	}
	aria := fmt.Sprintf("Dot plot of the %d worst creative pairs by copy overlap, mean copy overlap %.3f: %s",
		len(pts), mean, strings.Join(ptParts, "; "))

	height := distPadTop + float64(len(pts))*distRowH + 30
	return &DistinctivenessView{
		Points:        pts,
		MeanCopyLabel: fmt.Sprintf("mean %.3f", mean),
		MeanCopyX:     f2(distAxisX0 + mean*distAxisW),
		Ticks:         ticks,
		Height:        f2(height),
		AxisX0:        f2(distAxisX0),
		AxisX1:        f2(distAxisX0 + distAxisW),
		AriaLabel:     aria,
	}
}

// ---- persona_attribution: hero number + baseline comparison ----

// AttributionView is attribution_accuracy as a hero figure, plus two
// paired-bar comparisons (accuracy vs. chance, and the judge-validity
// check) on a shared 0-1 axis so the lift is visible rather than asserted.
type AttributionView struct {
	AccuracyPct string
	Bars        []PairedBar
	ConfBars    []PairedBar
}

// PairedBar is one labelled bar on the 0-1 axis.
type PairedBar struct {
	Label      string
	ValueLabel string
	WidthPct   string // 0-100, for a CSS-grid bar
}

func buildAttribution(values map[string]float64) *AttributionView {
	acc, ok := values["attribution_accuracy"]
	if !ok {
		return nil
	}
	mk := func(label string, v float64) PairedBar {
		return PairedBar{Label: label, ValueLabel: fmt.Sprintf("%.1f%%", v*100), WidthPct: f2(v * 100)}
	}
	return &AttributionView{
		AccuracyPct: fmt.Sprintf("%.0f%%", acc*100),
		Bars: []PairedBar{
			mk("attribution accuracy", acc),
			mk("chance baseline", values["chance_baseline"]),
		},
		ConfBars: []PairedBar{
			mk("high-confidence rate — real", values["high_confidence_rate_real"]),
			mk("high-confidence rate — control", values["high_confidence_rate_control"]),
		},
	}
}

// f2 formats a viewBox coordinate to two decimal places — enough precision
// for smooth rendering, without the noisy full float64 string a bare %v
// would produce.
func f2(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// New returns the dashboard handler. latestPath and baselinePath are read
// fresh on every request — the files are small, and re-reading means a
// fresh "disco measure" run shows up without restarting this process.
func New(latestPath, baselinePath string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data := buildPageData(latestPath, baselinePath, time.Now())
		w.Header().Set("content-type", "text/html; charset=utf-8")
		if err := page.Execute(w, data); err != nil {
			log.Printf("dashboard: rendering page: %v", err)
		}
	})
	return mux
}

// Run serves the dashboard until the process is stopped, following the
// same bind-with-fallback behaviour as "disco serve" (internal/server).
func Run(addr, latestPath, baselinePath string) error {
	ln, fellBack, err := server.Listen(addr)
	if err != nil {
		return err
	}
	defer ln.Close()

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		port = ln.Addr().String()
	}
	if fellBack {
		fmt.Printf("disco: address %s already in use; listening on http://localhost:%s instead\n", addr, port)
	} else {
		fmt.Printf("disco metrics listening on http://localhost:%s\n", port)
	}

	srv := &http.Server{
		Handler:           New(latestPath, baselinePath),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.Serve(ln)
}

// buildPageData loads latestPath and baselinePath and turns them into
// PageData. now is passed in (rather than read internally) so callers —
// tests especially — can control what "stale" and "4 hours ago" mean
// without racing the clock.
func buildPageData(latestPath, baselinePath string, now time.Time) PageData {
	var data PageData

	latest, err := loadReport(latestPath)
	if err != nil {
		if !os.IsNotExist(err) {
			data.LoadErr = err.Error()
		}
		return data
	}
	data.HasLatest = true
	data.GeneratedAbs = latest.GeneratedAt.UTC().Format("Jan 2, 2006 15:04 MST")
	data.GeneratedRel = humanizeAge(now.Sub(latest.GeneratedAt))
	data.Stale = now.Sub(latest.GeneratedAt) > staleAfter
	data.Provider = latest.Provider
	data.Model = latest.Model

	var baseline measure.Report
	if b, err := loadReport(baselinePath); err == nil {
		baseline = b
		data.HasBaseline = true
		data.BaselineAbs = baseline.GeneratedAt.UTC().Format("Jan 2, 2006 15:04 MST")
		data.RawDiff = measure.Diff(latest, baseline)
	}

	names := make([]string, 0, len(latest.Metrics))
	for name := range latest.Metrics {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		m := latest.Metrics[name]
		var baseValues map[string]float64
		if data.HasBaseline {
			if bm, ok := baseline.Metrics[name]; ok {
				baseValues = bm.Values
			}
		}
		data.Metrics = append(data.Metrics, buildMetricView(name, m, baseValues))
	}

	return data
}

// buildMetricView renders one metric generically (every value in
// m.Values, sorted, with a baseline delta when available — this is the
// table-view fallback kept for every metric), with a hand-built visual
// form layered on top for the four metrics this dashboard knows how to
// draw. A metric name absent from both the chart-building code and the
// interpretation table (a fifth metric added later, or a name typo'd in a
// test) still renders via the generic values table alone.
func buildMetricView(name string, m measure.Metric, baseValues map[string]float64) MetricView {
	mv := MetricView{
		Name:           name,
		Interpretation: interpretation[name],
		Summary:        m.Summary,
		Findings:       m.Findings,
	}

	switch name {
	case "reason_consistency":
		mv.ReasonBar = buildReasonBar(m.Counts, m.Values)
	case "scoring_ablation":
		mv.AblationChart = buildAblationChart(m.Values)
	case "creative_distinctiveness":
		mv.Distinctiveness = buildDistinctiveness(m.Values, m.Findings)
	case "persona_attribution":
		mv.Attribution = buildAttribution(m.Values)
	}

	keys := make([]string, 0, len(m.Values))
	for k := range m.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		cur := m.Values[k]
		row := ValueRow{Key: k, Value: formatValue(cur)}
		if bv, ok := baseValues[k]; ok {
			row.HasBase = true
			row.Base = formatValue(bv)
			delta := cur - bv
			row.Arrow, row.ColorClass = classify(knownDirection[k], delta)
			row.Delta = formatDelta(delta)
		}
		mv.ExtraValues = append(mv.ExtraValues, row)
	}

	return mv
}

func classify(dir direction, delta float64) (arrow, color string) {
	const eps = 1e-9
	switch {
	case delta > eps:
		arrow = "▲"
	case delta < -eps:
		arrow = "▼"
	default:
		arrow = "–"
	}

	switch dir {
	case higherBetter:
		switch {
		case delta > eps:
			color = "good"
		case delta < -eps:
			color = "bad"
		default:
			color = "neutral"
		}
	case lowerBetter:
		switch {
		case delta < -eps:
			color = "good"
		case delta > eps:
			color = "bad"
		default:
			color = "neutral"
		}
	default:
		color = "neutral"
	}
	return arrow, color
}

func formatValue(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e9 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 3, 64)
}

func formatDelta(v float64) string {
	if v == 0 {
		return formatValue(0)
	}
	sign := "+"
	x := v
	if v < 0 {
		sign = "-"
		x = -v
	}
	return sign + formatValue(x)
}

// humanizeAge renders a duration as a short, human relative age. Negative
// durations (a clock skew, or a "generated_at" in the future) are floored
// to "just now" rather than printed as nonsense like "-3 hours ago".
func humanizeAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour") + " ago"
	case d < 30*24*time.Hour:
		return plural(int(d/(24*time.Hour)), "day") + " ago"
	default:
		return plural(int(d/(30*24*time.Hour)), "month") + " ago"
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// loadReport reads and decodes a measure.Report from path. A missing file
// is returned as-is (os.IsNotExist) so the caller can tell "never measured"
// apart from "measured, but the file is corrupt".
func loadReport(path string) (measure.Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return measure.Report{}, err
	}
	defer f.Close()
	var r measure.Report
	if err := json.NewDecoder(f).Decode(&r); err != nil {
		return measure.Report{}, fmt.Errorf("decoding %s: %w", path, err)
	}
	return r, nil
}
