package server

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/model"
	"github.com/yashraj/disco/internal/pipeline"
)

func loadCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load("../../data")
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return c
}

// fixtureKey mirrors the unexported pipeline.fixtureKey helper: stage name
// plus a stable hash of the brief. It has to be reproduced here rather than
// imported because pipeline does not export it, but the algorithm (and the
// llm.ShortHash it calls) is exported and stable.
func fixtureKey(stage, brief string) string {
	return stage + "-" + llm.ShortHash(brief)
}

// recordFullRun writes a complete, consistent set of fixture responses for
// brief to dir so a real pipeline.Run can execute against them without any
// network access. It mirrors internal/pipeline/run_test.go's
// fullRunProvider, trimmed to what the server's success-path test needs.
func recordFullRun(t *testing.T, dir, brief string) llm.Provider {
	t.Helper()
	c := loadCatalog(t)
	f := llm.NewFixture(dir)

	rec := func(stage, key string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Record(llm.Request{Stage: stage, FixtureKey: key}, b); err != nil {
			t.Fatal(err)
		}
	}

	rec("profile", fixtureKey("profile", brief), map[string]any{
		"primary_category": "pet", "subcategories": []string{"pet_food", "subscription"},
		"price_tier": "premium", "estimated_aov_usd": 70,
		"target_age_min": 30, "target_age_max": 55, "target_gender_skew": "balanced",
		"values": []string{"science_backed"}, "business_model": "subscription",
		"is_consumer_dtc": true, "confidence": "high",
		"assumptions": []string{}, "missing_signals": []string{},
	})

	var verdicts []map[string]any
	rank := 0
	for i := range c.Publishers {
		v, r := "excluded", 0
		if c.Publishers[i].Category == "pet" && rank < 3 {
			rank++
			v, r = "recommended", rank
		}
		verdicts = append(verdicts, map[string]any{
			"publisher_id": c.Publishers[i].ID, "verdict": v, "rank": r,
			"reason": "pet-category audience overlap",
		})
	}
	rec("fit", fixtureKey("fit", brief), map[string]any{"verdicts": verdicts})

	rec("personas", fixtureKey("personas", brief), map[string]any{
		"selected": []map[string]any{
			{"persona_id": "persona_004", "rationale": "pet parent", "primary_publishers": []string{"pub_007"}},
			{"persona_id": "persona_002", "rationale": "busy parent", "primary_publishers": []string{"pub_009"}},
			{"persona_id": "persona_001", "rationale": "optimizer", "primary_publishers": []string{"pub_007"}},
		},
		"rejected": []map[string]any{{"persona_id": "persona_003", "reason": "no pet affinity"}},
	})

	for _, id := range []string{"persona_004", "persona_002", "persona_001"} {
		rec("creative", fixtureKey("creative", brief+"|"+id), map[string]any{
			"headline": "Built for senior dogs", "body": "Vet-formulated meals, delivered monthly.",
			"rationale": "grounded", "messaging_levers": []string{}, "avoided": []string{},
		})
	}

	return f
}

// TestIndexIsServed catches the embed being wrong or missing: an empty
// web.FS, a bad go:embed pattern, or "/" not being routed to the file
// server at all would make this 404 or return a body without the page's
// title.
func TestIndexIsServed(t *testing.T) {
	h := New(pipeline.Options{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<title>disco") {
		t.Error("index.html was not served")
	}
}

// TestAPIRejectsEmptyBrief catches a missing (or TrimSpace-less) validation
// check: a whitespace-only brief reaching pipeline.Run would either panic
// downstream or silently produce a nonsense campaign instead of a clear
// 400 the page can render as an error.
func TestAPIRejectsEmptyBrief(t *testing.T) {
	h := New(pipeline.Options{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(`{"brief":"  "}`))
	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("error response was not valid JSON: %v", err)
	}
	if body["error"] == "" {
		t.Error("an error response should carry a non-empty error field")
	}
}

// TestAPIRejectsGET catches the route accepting any method (e.g. a plain
// mux.HandleFunc("/api/run", ...) instead of the method-scoped
// "POST /api/run") which would let a GET silently run the pipeline instead
// of returning 405.
func TestAPIRejectsGET(t *testing.T) {
	h := New(pipeline.Options{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/run", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// TestAPIRunSuccess catches the handler mangling pipeline.Run's result on
// the way out: wrong content-type, double-encoding, dropping fields, or
// writing something that is not the JSON shape of model.Campaign at all.
// It runs the real pipeline against recorded fixtures (no network) and
// round-trips the HTTP response body into model.Campaign.
func TestAPIRunSuccess(t *testing.T) {
	brief := "We sell premium dog food for senior dogs."
	provider := recordFullRun(t, t.TempDir(), brief)
	cat := loadCatalog(t)

	h := New(pipeline.Options{
		Provider: provider, Catalog: cat, Params: pipeline.DefaultAllocParams(), Model: "test",
	})

	body, err := json.Marshal(map[string]string{"brief": brief})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(string(body)))
	r.Header.Set("content-type", "application/json")
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("content-type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json", ct)
	}

	var got model.Campaign
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response body did not decode into model.Campaign: %v\nbody=%s", err, w.Body.String())
	}
	if got.Status != model.StatusReady {
		t.Errorf("status = %q, want %q", got.Status, model.StatusReady)
	}
	if len(got.PublisherLedger) != len(cat.Publishers) {
		t.Errorf("ledger has %d entries, want %d", len(got.PublisherLedger), len(cat.Publishers))
	}
	if len(got.Creatives) == 0 {
		t.Error("want at least one creative")
	}
}

// TestListenFallsBackWhenPortTaken catches a regression to the naive
// srv.ListenAndServe() path: if listen stopped retrying on EADDRINUSE (or
// started retrying on the same port instead of ":0"), this would either
// return a bind error where none is expected, or come back bound to the
// very port that was already occupied.
func TestListenFallsBackWhenPortTaken(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer occupied.Close()
	addr := occupied.Addr().String()

	ln, fellBack, err := listen(addr)
	if err != nil {
		t.Fatalf("listen(%q): %v", addr, err)
	}
	defer ln.Close()

	if !fellBack {
		t.Error("fellBack = false, want true when the requested port was already in use")
	}

	_, gotPort, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", ln.Addr().String(), err)
	}
	_, wantNotPort, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	if gotPort == "0" || gotPort == "" {
		t.Errorf("bound port = %q, want a real non-zero port", gotPort)
	}
	if gotPort == wantNotPort {
		t.Errorf("bound port = %q, want a port different from the occupied one %q", gotPort, wantNotPort)
	}
}

// TestListenPropagatesMalformedAddr catches listen swallowing a malformed
// address (one that doesn't even split into host:port) and retrying anyway
// instead of surfacing the original bind error.
func TestListenPropagatesMalformedAddr(t *testing.T) {
	_, _, err := listen("not-a-valid-address")
	if err == nil {
		t.Fatal("listen with a malformed address: got nil error, want non-nil")
	}
}

// TestListenPropagatesPermissionError catches listen retrying (and thus
// masking) a non-EADDRINUSE bind failure — here, binding a privileged port
// without permission — by checking the errors.Is(err, syscall.EADDRINUSE)
// gate specifically rather than treating every bind error as "port taken".
// A corrupted listen that always retries on ":0" regardless of error kind
// would pass this bind (root is never required for port 0) and wrongly
// report success instead of the permission error.
func TestListenPropagatesPermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: privileged ports are not denied")
	}
	_, _, err := listen("127.0.0.1:1")
	if err == nil {
		t.Fatal("listen(\"127.0.0.1:1\") as non-root: got nil error, want a permission error")
	}
	if !errors.Is(err, syscall.EACCES) {
		t.Errorf("err = %v, want one that errors.Is syscall.EACCES", err)
	}
}
