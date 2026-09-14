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
package dashboard

import (
	"fmt"
	"html/template"
	"log"
	"math"
	"net"
	"net/http"
	"os"
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

// MetricView is one metric.Name's worth of rendering-ready data.
type MetricView struct {
	Name           string
	Interpretation string
	Summary        string
	Ablation       []AblationRow // only for "scoring_ablation"; nil otherwise
	ExtraValues    []ValueRow
	Findings       []measure.Finding
}

// AblationRow is one scoring sub-score's influence, ready to draw as a bar.
type AblationRow struct {
	SubScore      string
	Top5Pct       string
	BarStyle      template.CSS
	MeanRankShift string
}

// ValueRow is one metric.Values entry plus its baseline comparison, if any.
type ValueRow struct {
	Key        string
	Value      string
	HasBase    bool
	Base       string
	Arrow      string
	Delta      string
	ColorClass string
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
// m.Values, sorted, with a baseline delta when available), with one
// special case: "scoring_ablation" additionally gets its six
// top5_change_rate.<SubScore> values drawn as a ranked bar comparison
// instead of buried in the flat value table.
func buildMetricView(name string, m measure.Metric, baseValues map[string]float64) MetricView {
	mv := MetricView{
		Name:           name,
		Interpretation: interpretation[name],
		Summary:        m.Summary,
		Findings:       m.Findings,
	}

	skip := map[string]bool{}
	if name == "scoring_ablation" {
		mv.Ablation = buildAblation(m.Values)
		for k := range m.Values {
			if strings.HasPrefix(k, "top5_change_rate.") {
				skip[k] = true
			}
		}
	}

	keys := make([]string, 0, len(m.Values))
	for k := range m.Values {
		if !skip[k] {
			keys = append(keys, k)
		}
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

func buildAblation(values map[string]float64) []AblationRow {
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
	sort.Slice(aggs, func(i, j int) bool {
		if aggs[i].top5 != aggs[j].top5 {
			return aggs[i].top5 > aggs[j].top5
		}
		if aggs[i].rank != aggs[j].rank {
			return aggs[i].rank > aggs[j].rank
		}
		return aggs[i].name < aggs[j].name
	})

	maxTop5 := 0.0
	for _, a := range aggs {
		if a.top5 > maxTop5 {
			maxTop5 = a.top5
		}
	}

	rows := make([]AblationRow, 0, len(aggs))
	for _, a := range aggs {
		pct := 0.0
		if maxTop5 > 0 {
			pct = a.top5 / maxTop5 * 100
		}
		rows = append(rows, AblationRow{
			SubScore:      a.name,
			Top5Pct:       fmt.Sprintf("%.1f%%", a.top5*100),
			BarStyle:      template.CSS(fmt.Sprintf("width:%.1f%%", pct)),
			MeanRankShift: formatValue(a.rank),
		})
	}
	return rows
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
