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
