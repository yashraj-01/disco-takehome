# Disco Ad Placement & Creative Generation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go CLI that turns one advertiser sentence into a ranked publisher list with an exclusion ledger, 3–5 persona-tuned ad creatives, and a structured campaign config — runnable with no API key via a fixture provider.

**Architecture:** Six-stage pipeline. Stages 1, 3, 4, 5 call an LLM through a two-implementation `Provider` interface. Stages 2 and 6 are pure functions that own every number in the output — publisher scoring, CPM derivation, and budget allocation never come from a model response. A `fixture` provider replays recorded JSON so the whole test suite and a reviewer's first run need no key.

**Tech Stack:** Go 1.25 · `google.golang.org/genai` (pin `< 2.0.0`) · `github.com/santhosh-tekuri/jsonschema/v6` · `golang.org/x/sync/errgroup` · `gemini-2.5-flash` · stdlib `net/http` + `embed` for the web view

**Spec:** `docs/superpowers/specs/2026-09-13-disco-ad-pipeline-design.md`

## Global Constraints

- Go 1.25. Module path `github.com/yashraj/disco`.
- Dependencies limited to: `google.golang.org/genai` (pinned `< 2.0.0`), `github.com/santhosh-tekuri/jsonschema/v6`, `golang.org/x/sync`. Nothing else. No CLI framework — stdlib `flag` only.
- `data/` is supplied input and is **never modified**.
- `go test ./...` must pass with no API key and no network. LLM stages under test use the `fixture` provider.
- Every dollar, share, CPM, and impression figure originates in `internal/pipeline` pure functions. An LLM response never carries a number that reaches the campaign config.
- Publisher and persona IDs in any LLM output are filtered against the catalog allow-list before use. IDs outside it are dropped.
- Scoring weights, fixed: Category 0.30 · AOVAlignment 0.20 · AgeOverlap 0.15 · ValuesMatch 0.15 · GenderFit 0.10 · IncomeTier 0.10.
- Allocation defaults, fixed: `--gamma 1.5` · `--max-share 0.40` · `--sov-cap 0.15` · `--min-share 0.05` · `--budget 25000` · `--days 30`.
- Catalog AOV bounds are 28 (min) and 198 (max), read from the data at load time, not hard-coded.
- Prompts live in `prompts/` at repo root as `<stage>.md` + `<stage>.schema.json`. Each schema file is used three ways: the model's `ResponseSchema`, the runtime validator, and the `prompts/` deliverable.
- Commit after every task.

**Deviation from spec, deliberate:** the spec's repo layout put eval code at `evals/assertions.go`. Go packages read better with code under `internal/`, so eval *code* lives in `internal/eval/` and eval *data* (`briefs.txt`, `fixtures/`, `snapshots/`) stays in `evals/`.

---
## File Structure

| Path | Responsibility |
|---|---|
| `go.mod` | module `github.com/yashraj/disco`, Go 1.25 |
| `internal/catalog/types.go` | `Publisher`, `Persona`, `Audience` — JSON shapes of the supplied data |
| `internal/catalog/catalog.go` | `Load`, ID allow-lists, AOV bounds, per-publisher keyword precompute, `ParseAgeRange` |
| `internal/model/types.go` | Pipeline data contracts shared across packages: `AdvertiserProfile`, `SubScores`, `PublisherScore`, `FitVerdict`, `PersonaSelection`, `Creative`, `Campaign` |
| `internal/pipeline/adjacency.go` | Category adjacency map, closed symmetrically on init |
| `internal/pipeline/scoring.go` | Stage 2. `ScoreAll` — six sub-scores, weights, three hard gates |
| `internal/pipeline/cpm.go` | `EstimateCPM` — income + category premium, AOV index |
| `internal/pipeline/allocate.go` | `Allocate` — γ share, three caps, fixed-point iteration |
| `internal/pipeline/campaign.go` | Stage 6. `Build` — objective, bid, targeting, 20-entry ledger |
| `internal/llm/provider.go` | `Provider` interface, `CacheKey` |
| `internal/llm/fixture.go` | Replay provider — reads `evals/fixtures/<key>.json` |
| `internal/llm/gemini.go` | Gemini provider, token-bucket rate limiter, disk cache, 429 backoff, schema repair |
| `internal/llm/schema.go` | JSON Schema compile + validate via `santhosh-tekuri/jsonschema/v6` |
| `prompts/embed.go` | `package prompts` — `go:embed` of the `.md` and `.schema.json` files beside it |
| `internal/pipeline/profile.go` | Stage 1 |
| `internal/pipeline/fit.go` | Stage 3 |
| `internal/pipeline/personas.go` | Stage 4 |
| `internal/pipeline/creative.go` | Stage 5, parallel via `errgroup` |
| `internal/pipeline/run.go` | Orchestrator + the three `status` degraded modes |
| `internal/render/terminal.go` | Human-readable terminal output |
| `internal/render/json.go` | `--json` output |
| `internal/server/server.go` | `disco serve` |
| `web/index.html` | Single embedded page |
| `internal/eval/eval.go` | Eval runner over `evals/briefs.txt` |
| `internal/eval/assertions.go` | Universal + per-brief assertions |
| `cmd/disco/main.go` | Subcommand dispatch and flag parsing |

Prompts are a package so `go:embed` can reach them — `embed` cannot read outside its own directory tree, and the brief requires `prompts/` at the repo root.

---

### Task 1: Module bootstrap, catalog loader, shared types

**Files:**
- Create: `go.mod`, `internal/catalog/types.go`, `internal/catalog/catalog.go`, `internal/model/types.go`
- Test: `internal/catalog/catalog_test.go`

**Interfaces:**
- Consumes: nothing (first task)
- Produces:
  - `catalog.Load(dir string) (*catalog.Catalog, error)` — `dir` is the path containing `publishers.json` and `shopper_personas.json`
  - `(*catalog.Catalog).Publisher(id string) (*catalog.Publisher, bool)`, `.Persona(id string) (*catalog.Persona, bool)`
  - fields `(*catalog.Catalog).Publishers []Publisher`, `.Personas []Persona`, `.MinAOV int`, `.MaxAOV int`
  - `catalog.ParseAgeRange(s string) (lo, hi int, err error)`
  - `catalog.Publisher.Keywords []string` — precomputed, populated by `Load`
  - all `model` structs listed below

- [ ] **Step 1: Initialize the module**

```bash
cd /Users/yash.raj/Downloads/disco-takehome-candidate
go mod init github.com/yashraj/disco
```

- [ ] **Step 2: Write `internal/catalog/types.go`**

```go
package catalog

// Audience mirrors the audience object in publishers.json.
type Audience struct {
	AgeSkew     string             `json:"age_skew"`
	GenderSplit map[string]float64 `json:"gender_split"`
	TopGeos     []string           `json:"top_geos"`
	IncomeTier  string             `json:"income_tier"`
}

// Publisher mirrors one entry of publishers.json. Keywords is derived at load
// time from Notes and Subcategories; it is not present in the source data.
type Publisher struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Category           string   `json:"category"`
	Subcategories      []string `json:"subcategories"`
	MonthlyImpressions int64    `json:"monthly_impressions"`
	AvgOrderValueUSD   int      `json:"avg_order_value_usd"`
	Audience           Audience `json:"audience"`
	Notes              string   `json:"notes"`

	Keywords []string `json:"-"`
}

// Persona mirrors one entry of shopper_personas.json.
type Persona struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	AgeRange             string   `json:"age_range"`
	GenderSkew           string   `json:"gender_skew"`
	Description          string   `json:"description"`
	CategoryAffinities   []string `json:"category_affinities"`
	PriceSensitivity     string   `json:"price_sensitivity"`
	MessagingPreferences []string `json:"messaging_preferences"`
	DisinterestedIn      []string `json:"disinterested_in"`
	TypicalAOVUSD        int      `json:"typical_aov_usd"`
}
```

- [ ] **Step 3: Write the failing test**

```go
package catalog

import "testing"

func TestLoadReadsBothFiles(t *testing.T) {
	c, err := Load("../../data")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(c.Publishers); got != 20 {
		t.Errorf("publishers = %d, want 20", got)
	}
	if got := len(c.Personas); got != 10 {
		t.Errorf("personas = %d, want 10", got)
	}
	if c.MinAOV != 28 || c.MaxAOV != 198 {
		t.Errorf("AOV bounds = %d/%d, want 28/198", c.MinAOV, c.MaxAOV)
	}
}

func TestLookupByID(t *testing.T) {
	c, _ := Load("../../data")
	p, ok := c.Publisher("pub_007")
	if !ok {
		t.Fatal("pub_007 not found")
	}
	if p.Name != "Pawline" {
		t.Errorf("name = %q, want Pawline", p.Name)
	}
	if _, ok := c.Publisher("pub_999"); ok {
		t.Error("pub_999 should not exist")
	}
	if _, ok := c.Persona("persona_004"); !ok {
		t.Error("persona_004 should exist")
	}
}

func TestKeywordsPrecomputed(t *testing.T) {
	c, _ := Load("../../data")
	p, _ := c.Publisher("pub_008") // Pantrygood: "Values-driven shoppers. Responsive to
	// clean-ingredient, sustainability, and wellness claims." + organic/natural subcategories
	if !contains(p.Keywords, "sustainability") {
		t.Errorf("Pantrygood keywords = %v, want sustainability", p.Keywords)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestParseAgeRange(t *testing.T) {
	for _, tc := range []struct {
		in       string
		lo, hi   int
		wantErr  bool
	}{
		{"18-34", 18, 34, false},
		{"50-70", 50, 70, false},
		{"28-40", 28, 40, false},
		{"garbage", 0, 0, true},
	} {
		lo, hi, err := ParseAgeRange(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseAgeRange(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if err == nil && (lo != tc.lo || hi != tc.hi) {
			t.Errorf("ParseAgeRange(%q) = %d,%d want %d,%d", tc.in, lo, hi, tc.lo, tc.hi)
		}
	}
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `go test ./internal/catalog/ -v`
Expected: FAIL — `undefined: Load`, `undefined: ParseAgeRange`

- [ ] **Step 5: Write `internal/catalog/catalog.go`**

```go
package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Catalog is the loaded, validated publisher and persona data plus the
// derived lookups the pipeline needs. Construct it with Load.
type Catalog struct {
	Publishers []Publisher
	Personas   []Persona

	MinAOV int
	MaxAOV int

	pubByID     map[string]*Publisher
	personaByID map[string]*Persona
}

// valueKeywords maps the profile value vocabulary to substrings that signal
// that value in a publisher's free-text notes and subcategories. Matching on
// free text is deliberately crude; stage 3 sees the raw notes and can override
// the verdict with a written reason.
var valueKeywords = map[string][]string{
	"sustainability": {"sustainab", "eco", "recycl", "refill", "organic", "natural", "clean", "values-driven", "ethical"},
	"craftsmanship":  {"quality", "craft", "heritage", "artisan", "small-batch", "handmade", "premium", "durable"},
	"science_backed": {"vet", "clinical", "science", "ingredient", "formulat", "evidence", "performance", "technical"},
	"convenience":    {"convenien", "subscription", "fast", "instant", "quick", "easy", "delivery", "impulse"},
	"value":          {"value", "affordable", "budget", "deal", "discount", "inclusive"},
	"aesthetic":      {"aesthetic", "design", "decor", "style", "visual", "brand"},
}

// Load reads publishers.json and shopper_personas.json from dir, builds the ID
// lookups, records the catalog-wide AOV bounds, and precomputes each
// publisher's value keywords.
func Load(dir string) (*Catalog, error) {
	var c Catalog

	if err := readJSON(filepath.Join(dir, "publishers.json"), &c.Publishers); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(dir, "shopper_personas.json"), &c.Personas); err != nil {
		return nil, err
	}
	if len(c.Publishers) == 0 {
		return nil, fmt.Errorf("catalog: no publishers loaded from %s", dir)
	}
	if len(c.Personas) == 0 {
		return nil, fmt.Errorf("catalog: no personas loaded from %s", dir)
	}

	c.pubByID = make(map[string]*Publisher, len(c.Publishers))
	c.MinAOV, c.MaxAOV = c.Publishers[0].AvgOrderValueUSD, c.Publishers[0].AvgOrderValueUSD
	for i := range c.Publishers {
		p := &c.Publishers[i]
		if _, dup := c.pubByID[p.ID]; dup {
			return nil, fmt.Errorf("catalog: duplicate publisher id %q", p.ID)
		}
		c.pubByID[p.ID] = p
		if p.AvgOrderValueUSD < c.MinAOV {
			c.MinAOV = p.AvgOrderValueUSD
		}
		if p.AvgOrderValueUSD > c.MaxAOV {
			c.MaxAOV = p.AvgOrderValueUSD
		}
		p.Keywords = deriveKeywords(p)
	}

	c.personaByID = make(map[string]*Persona, len(c.Personas))
	for i := range c.Personas {
		p := &c.Personas[i]
		if _, dup := c.personaByID[p.ID]; dup {
			return nil, fmt.Errorf("catalog: duplicate persona id %q", p.ID)
		}
		c.personaByID[p.ID] = p
	}

	return &c, nil
}

func readJSON(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("catalog: parsing %s: %w", path, err)
	}
	return nil
}

// deriveKeywords returns the value-vocabulary terms this publisher's notes and
// subcategories signal.
func deriveKeywords(p *Publisher) []string {
	hay := strings.ToLower(p.Notes + " " + strings.Join(p.Subcategories, " ") + " " + p.Category)
	var out []string
	for value, needles := range valueKeywords {
		for _, n := range needles {
			if strings.Contains(hay, n) {
				out = append(out, value)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// Publisher returns the publisher with the given ID.
func (c *Catalog) Publisher(id string) (*Publisher, bool) {
	p, ok := c.pubByID[id]
	return p, ok
}

// Persona returns the persona with the given ID.
func (c *Catalog) Persona(id string) (*Persona, bool) {
	p, ok := c.personaByID[id]
	return p, ok
}

// HasPublisher reports whether id is in the catalog. Used to filter model
// output before it reaches the campaign config.
func (c *Catalog) HasPublisher(id string) bool { _, ok := c.pubByID[id]; return ok }

// HasPersona reports whether id is in the catalog.
func (c *Catalog) HasPersona(id string) bool { _, ok := c.personaByID[id]; return ok }

// ParseAgeRange parses the "28-40" form used by both age_skew and age_range.
func ParseAgeRange(s string) (int, int, error) {
	lo, hi, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return 0, 0, fmt.Errorf("catalog: age range %q: want LO-HI", s)
	}
	l, err := strconv.Atoi(strings.TrimSpace(lo))
	if err != nil {
		return 0, 0, fmt.Errorf("catalog: age range %q: %w", s, err)
	}
	h, err := strconv.Atoi(strings.TrimSpace(hi))
	if err != nil {
		return 0, 0, fmt.Errorf("catalog: age range %q: %w", s, err)
	}
	if l > h {
		return 0, 0, fmt.Errorf("catalog: age range %q: low > high", s)
	}
	return l, h, nil
}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/catalog/ -v`
Expected: PASS, all four tests

- [ ] **Step 7: Write `internal/model/types.go`**

```go
// Package model holds the data contracts passed between pipeline stages.
// It has no behavior and no dependencies, so every other package can import it.
package model

// AdvertiserProfile is stage 1 output. Confidence and IsConsumerDTC gate
// everything downstream.
type AdvertiserProfile struct {
	RawBrief         string   `json:"raw_brief"`
	PrimaryCategory  string   `json:"primary_category"`
	Subcategories    []string `json:"subcategories"`
	PriceTier        string   `json:"price_tier"`  // budget | mid | premium | luxury
	EstimatedAOVUSD  int      `json:"estimated_aov_usd"`
	TargetAgeMin     int      `json:"target_age_min"`
	TargetAgeMax     int      `json:"target_age_max"`
	TargetGenderSkew string   `json:"target_gender_skew"` // female | male | balanced | unknown
	Values           []string `json:"values"`
	BusinessModel    string   `json:"business_model"` // subscription | one_off | b2b | service
	IsConsumerDTC    bool     `json:"is_consumer_dtc"`
	Confidence       string   `json:"confidence"` // high | medium | low
	Assumptions      []string `json:"assumptions"`
	MissingSignals   []string `json:"missing_signals"`
}

// SubScores are the six components of a publisher's fit, each in [0,1].
type SubScores struct {
	Category     float64 `json:"category"`
	AOVAlignment float64 `json:"aov_alignment"`
	AgeOverlap   float64 `json:"age_overlap"`
	ValuesMatch  float64 `json:"values_match"`
	GenderFit    float64 `json:"gender_fit"`
	IncomeTier   float64 `json:"income_tier"`
}

// PublisherScore is stage 2 output for one publisher. Score is the weighted sum
// and is the only fit number that reaches budget allocation.
type PublisherScore struct {
	PublisherID string    `json:"publisher_id"`
	Score       float64   `json:"score"`
	Sub         SubScores `json:"sub_scores"`
	HardGate    string    `json:"hard_gate,omitempty"`
}

// FitVerdict is stage 3 output for one publisher.
type FitVerdict struct {
	PublisherID string `json:"publisher_id"`
	Verdict     string `json:"verdict"` // recommended | considered | excluded
	Rank        int    `json:"rank,omitempty"`
	Reason      string `json:"reason"`
}

// PersonaPick is one selected persona with its justification.
type PersonaPick struct {
	PersonaID         string   `json:"persona_id"`
	Rationale         string   `json:"rationale"`
	PrimaryPublishers []string `json:"primary_publishers"`
}

// PersonaRejection records why a persona was not selected.
type PersonaRejection struct {
	PersonaID string `json:"persona_id"`
	Reason    string `json:"reason"`
}

// PersonaSelection is stage 4 output.
type PersonaSelection struct {
	Selected []PersonaPick      `json:"selected"`
	Rejected []PersonaRejection `json:"rejected"`
}

// Creative is one ad variant, written for exactly one persona.
type Creative struct {
	PersonaID           string   `json:"persona_id"`
	Headline            string   `json:"headline"`
	Body                string   `json:"body"`
	Rationale           string   `json:"rationale"`
	SuggestedPublishers []string `json:"suggested_publishers"`
	MessagingLevers     []string `json:"messaging_levers"`
	Avoided             []string `json:"avoided"`
}
```

- [ ] **Step 8: Verify the whole module builds**

Run: `go build ./... && go vet ./...`
Expected: no output

- [ ] **Step 9: Commit**

```bash
git add go.mod internal/catalog internal/model
git commit -m "feat: catalog loader and shared pipeline types

Loads the supplied publisher and persona data, builds ID allow-lists used
later to filter model output, records catalog-wide AOV bounds for the CPM
index, and precomputes per-publisher value keywords from free-text notes."
```

---

### Task 2: Stage 2 — publisher scoring and hard gates

**Files:**
- Create: `internal/pipeline/adjacency.go`, `internal/pipeline/scoring.go`
- Test: `internal/pipeline/scoring_test.go`

**Interfaces:**
- Consumes: `catalog.Catalog`, `catalog.Publisher`, `catalog.ParseAgeRange`, `model.AdvertiserProfile`, `model.PublisherScore`, `model.SubScores` (Task 1)
- Produces:
  - `pipeline.ScoreAll(p model.AdvertiserProfile, c *catalog.Catalog) []model.PublisherScore` — returns exactly one entry per catalog publisher, in catalog order
  - gate constants `pipeline.GateNone`, `GateNotConsumerDTC`, `GateCategoryMismatch`, `GateDemographicMismatch`

- [ ] **Step 1: Write `internal/pipeline/adjacency.go`**

```go
package pipeline

// adjacent maps a category to the categories it is a plausible neighbour of.
// Declared one-way and closed symmetrically by init, so a single edge written
// here applies in both directions.
var adjacent = map[string]map[string]bool{}

var adjacencyEdges = map[string][]string{
	"pet":               {"subscription_boxes"},
	"wellness_dtc":      {"wellness_services", "beauty", "groceries"},
	"wellness_services": {"apparel"},
	"apparel":           {"beauty", "home"},
	"groceries":         {"meal_kits", "beverages"},
	"beverages":         {"wellness_dtc", "instant_delivery"},
	"home":              {"groceries"},
	"beauty":            {"wellness_dtc"},
	"meal_kits":         {"instant_delivery"},
	"instant_delivery":  {"groceries"},
}

func init() {
	add := func(a, b string) {
		if adjacent[a] == nil {
			adjacent[a] = map[string]bool{}
		}
		adjacent[a][b] = true
	}
	for a, bs := range adjacencyEdges {
		for _, b := range bs {
			add(a, b)
			add(b, a)
		}
	}
}

// isAdjacent reports whether two categories are neighbours.
func isAdjacent(a, b string) bool { return adjacent[a][b] }
```

- [ ] **Step 2: Write the failing test**

```go
package pipeline

import (
	"math"
	"testing"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
)

func loadCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load("../../data")
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return c
}

func scoreFor(t *testing.T, scores []model.PublisherScore, id string) model.PublisherScore {
	t.Helper()
	for _, s := range scores {
		if s.PublisherID == id {
			return s
		}
	}
	t.Fatalf("no score for %s", id)
	return model.PublisherScore{}
}

// dogFood is example brief #1: premium senior dog food, vet-formulated,
// subscription, owners who care about joint health.
func dogFood() model.AdvertiserProfile {
	return model.AdvertiserProfile{
		RawBrief:         "premium dog food for senior dogs",
		PrimaryCategory:  "pet",
		Subcategories:    []string{"pet_food", "subscription"},
		PriceTier:        "premium",
		EstimatedAOVUSD:  70,
		TargetAgeMin:     30,
		TargetAgeMax:     55,
		TargetGenderSkew: "balanced",
		Values:           []string{"science_backed"},
		BusinessModel:    "subscription",
		IsConsumerDTC:    true,
		Confidence:       "high",
	}
}

// activewear is example brief #2: sustainable women's activewear, recycled
// ocean plastic, priced between Lululemon and Girlfriend Collective.
func activewear() model.AdvertiserProfile {
	return model.AdvertiserProfile{
		RawBrief:         "sustainable activewear for women",
		PrimaryCategory:  "apparel",
		Subcategories:    []string{"activewear", "women"},
		PriceTier:        "premium",
		EstimatedAOVUSD:  95,
		TargetAgeMin:     25,
		TargetAgeMax:     45,
		TargetGenderSkew: "female",
		Values:           []string{"sustainability"},
		BusinessModel:    "one_off",
		IsConsumerDTC:    true,
		Confidence:       "high",
	}
}

func TestScoreAllCoversEveryPublisher(t *testing.T) {
	c := loadCatalog(t)
	got := ScoreAll(dogFood(), c)
	if len(got) != len(c.Publishers) {
		t.Fatalf("got %d scores, want %d", len(got), len(c.Publishers))
	}
	for i, s := range got {
		if s.PublisherID != c.Publishers[i].ID {
			t.Errorf("score[%d] id = %s, want %s", i, s.PublisherID, c.Publishers[i].ID)
		}
		if s.Score < 0 || s.Score > 1 {
			t.Errorf("%s score = %v, want [0,1]", s.PublisherID, s.Score)
		}
	}
}

func TestPetPublishersOutrankUnrelatedOnes(t *testing.T) {
	c := loadCatalog(t)
	got := ScoreAll(dogFood(), c)
	pawline := scoreFor(t, got, "pub_007")  // pet, mid-high, AOV 64
	velvetline := scoreFor(t, got, "pub_013") // beauty, mid, AOV 61
	if pawline.Score <= velvetline.Score {
		t.Errorf("Pawline %v should outscore Velvetline %v", pawline.Score, velvetline.Score)
	}
	if pawline.HardGate != GateNone {
		t.Errorf("Pawline gate = %q, want none", pawline.HardGate)
	}
}

// Linden Park skews 50-70; a 25-45 activewear brief has zero age overlap, so it
// must be gated out on demographics rather than merely scored low.
func TestZeroAgeOverlapGatesDemographically(t *testing.T) {
	c := loadCatalog(t)
	got := ScoreAll(activewear(), c)
	linden := scoreFor(t, got, "pub_005")
	if linden.HardGate != GateDemographicMismatch {
		t.Errorf("Linden Park gate = %q, want %q", linden.HardGate, GateDemographicMismatch)
	}
	if linden.Sub.AgeOverlap != 0 {
		t.Errorf("Linden Park age overlap = %v, want 0", linden.Sub.AgeOverlap)
	}
	movewell := scoreFor(t, got, "pub_002") // apparel, activewear, 25-44, 82% female
	if movewell.HardGate != GateNone {
		t.Errorf("Movewell gate = %q, want none", movewell.HardGate)
	}
}

// Brief #7 is B2B SaaS for dental practices. No publisher in a DTC consumer
// catalog can serve it, and the system must say so rather than pick a winner.
func TestNonConsumerBriefGatesEveryPublisher(t *testing.T) {
	c := loadCatalog(t)
	p := dogFood()
	p.IsConsumerDTC = false
	p.PrimaryCategory = "b2b_saas"
	p.BusinessModel = "b2b"
	for _, s := range ScoreAll(p, c) {
		if s.HardGate != GateNotConsumerDTC {
			t.Errorf("%s gate = %q, want %q", s.PublisherID, s.HardGate, GateNotConsumerDTC)
		}
	}
}

// Brief #10 is $1,200 Italian handbags. Swiftcart's shoppers spend $28 a time.
func TestAOVAlignmentPunishesMismatch(t *testing.T) {
	c := loadCatalog(t)
	p := dogFood()
	p.EstimatedAOVUSD = 1200
	got := ScoreAll(p, c)
	swiftcart := scoreFor(t, got, "pub_001") // AOV 28
	if swiftcart.Sub.AOVAlignment != 0 {
		t.Errorf("Swiftcart AOV alignment = %v, want 0", swiftcart.Sub.AOVAlignment)
	}
}

func TestSubScoresAreWeightedIntoScore(t *testing.T) {
	c := loadCatalog(t)
	got := ScoreAll(dogFood(), c)
	s := scoreFor(t, got, "pub_007")
	want := 0.30*s.Sub.Category + 0.20*s.Sub.AOVAlignment + 0.15*s.Sub.AgeOverlap +
		0.15*s.Sub.ValuesMatch + 0.10*s.Sub.GenderFit + 0.10*s.Sub.IncomeTier
	if math.Abs(s.Score-want) > 1e-9 {
		t.Errorf("score = %v, weighted sub-scores = %v", s.Score, want)
	}
}

func TestGatedPublisherScoresZero(t *testing.T) {
	c := loadCatalog(t)
	got := ScoreAll(activewear(), c)
	linden := scoreFor(t, got, "pub_005")
	if linden.Score != 0 {
		t.Errorf("gated publisher score = %v, want 0", linden.Score)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/pipeline/ -run TestScore -v`
Expected: FAIL — `undefined: ScoreAll`, `undefined: GateNone`

- [ ] **Step 4: Write `internal/pipeline/scoring.go`**

```go
package pipeline

import (
	"strings"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
)

// Hard gate reasons. A gated publisher is excluded regardless of its
// sub-scores, and stage 3 may not override the exclusion.
const (
	GateNone                = ""
	GateNotConsumerDTC      = "not_consumer_dtc"
	GateCategoryMismatch    = "category_mismatch"
	GateDemographicMismatch = "demographic_mismatch"
)

// Scoring weights. These sum to 1.0.
const (
	wCategory = 0.30
	wAOV      = 0.20
	wAge      = 0.15
	wValues   = 0.15
	wGender   = 0.10
	wIncome   = 0.10
)

// ScoreAll scores every publisher in the catalog against the profile and
// returns the results in catalog order. It performs no I/O and calls no model,
// so its output is reproducible for a given profile.
func ScoreAll(p model.AdvertiserProfile, c *catalog.Catalog) []model.PublisherScore {
	out := make([]model.PublisherScore, 0, len(c.Publishers))
	for i := range c.Publishers {
		out = append(out, scoreOne(p, &c.Publishers[i]))
	}
	return out
}

func scoreOne(p model.AdvertiserProfile, pub *catalog.Publisher) model.PublisherScore {
	s := model.PublisherScore{PublisherID: pub.ID}

	s.Sub = model.SubScores{
		Category:     categoryScore(p, pub),
		AOVAlignment: aovScore(p.EstimatedAOVUSD, pub.AvgOrderValueUSD),
		AgeOverlap:   ageScore(p, pub),
		ValuesMatch:  valuesScore(p.Values, pub.Keywords),
		GenderFit:    genderScore(p.TargetGenderSkew, pub.Audience.GenderSplit),
		IncomeTier:   incomeScore(p.PriceTier, pub.Audience.IncomeTier),
	}

	switch {
	case !p.IsConsumerDTC:
		s.HardGate = GateNotConsumerDTC
	case s.Sub.Category == 0:
		s.HardGate = GateCategoryMismatch
	case s.Sub.AgeOverlap == 0:
		s.HardGate = GateDemographicMismatch
	}
	if s.HardGate != GateNone {
		return s // Score stays 0; a gated publisher never competes for budget.
	}

	s.Score = wCategory*s.Sub.Category + wAOV*s.Sub.AOVAlignment + wAge*s.Sub.AgeOverlap +
		wValues*s.Sub.ValuesMatch + wGender*s.Sub.GenderFit + wIncome*s.Sub.IncomeTier
	return s
}

// categoryScore prefers an exact category match, falls back to subcategory
// overlap, then to the adjacency map.
func categoryScore(p model.AdvertiserProfile, pub *catalog.Publisher) float64 {
	if p.PrimaryCategory != "" && p.PrimaryCategory == pub.Category {
		return 1.0
	}
	if j := jaccard(p.Subcategories, pub.Subcategories); j > 0 {
		return 0.4 + 0.5*j
	}
	if isAdjacent(p.PrimaryCategory, pub.Category) {
		return 0.5
	}
	return 0.0
}

// jaccard is the intersection-over-union of two lowercase token sets.
func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]bool, len(a))
	for _, x := range a {
		set[strings.ToLower(x)] = true
	}
	inter := 0
	seen := make(map[string]bool, len(b))
	for _, y := range b {
		y = strings.ToLower(y)
		if seen[y] {
			continue
		}
		seen[y] = true
		if set[y] {
			inter++
		}
	}
	union := len(set) + len(seen) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// aovScore rewards advertisers whose order value is in the same ballpark as the
// publisher's shoppers. A $1,200 handbag on a $28-AOV publisher scores 0.
func aovScore(advertiser, publisher int) float64 {
	if advertiser <= 0 || publisher <= 0 {
		return 0.5 // no signal either way
	}
	a, b := float64(advertiser), float64(publisher)
	r := min(a, b) / max(a, b)
	return clamp((r-0.25)/0.75, 0, 1)
}

// ageScore is the overlap of the two age ranges as a fraction of the narrower
// one. Zero overlap triggers the demographic hard gate.
func ageScore(p model.AdvertiserProfile, pub *catalog.Publisher) float64 {
	plo, phi, err := catalog.ParseAgeRange(pub.Audience.AgeSkew)
	if err != nil {
		return 0.5
	}
	alo, ahi := p.TargetAgeMin, p.TargetAgeMax
	if alo == 0 && ahi == 0 {
		return 0.5 // profile gave no age signal
	}
	lo, hi := max(alo, plo), min(ahi, phi)
	overlap := hi - lo
	if overlap <= 0 {
		return 0
	}
	narrower := min(ahi-alo, phi-plo)
	if narrower <= 0 {
		return 0
	}
	return clamp(float64(overlap)/float64(narrower), 0, 1)
}

// valuesScore is the fraction of the advertiser's stated values that the
// publisher's notes and subcategories signal.
func valuesScore(profileValues, pubKeywords []string) float64 {
	if len(profileValues) == 0 {
		return 0.5
	}
	have := make(map[string]bool, len(pubKeywords))
	for _, k := range pubKeywords {
		have[k] = true
	}
	hit := 0
	for _, v := range profileValues {
		if have[v] {
			hit++
		}
	}
	return clamp(float64(hit)/float64(len(profileValues)), 0, 1)
}

// genderScore rewards publishers whose audience leans the way the advertiser
// needs. A balanced or unknown target is neutral.
func genderScore(skew string, split map[string]float64) float64 {
	if skew != "female" && skew != "male" {
		return 0.5
	}
	share, ok := split[skew]
	if !ok {
		return 0.5
	}
	return clamp((share-0.3)/0.5, 0, 1)
}

// priceTierIncome and incomeIndex place price tiers and audience income tiers
// on one ordinal axis so the distance between them is meaningful.
var priceTierIncome = map[string]float64{"budget": 0, "mid": 0.5, "premium": 1.5, "luxury": 2}
var incomeIndex = map[string]float64{"mid": 0, "mid-high": 1, "high": 2}

func incomeScore(priceTier, incomeTier string) float64 {
	a, okA := priceTierIncome[priceTier]
	b, okB := incomeIndex[incomeTier]
	if !okA || !okB {
		return 0.5
	}
	d := a - b
	if d < 0 {
		d = -d
	}
	return clamp(1-0.5*d, 0, 1)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/pipeline/ -run TestScore -v` then `go test ./internal/pipeline/ -v`
Expected: PASS, all seven tests

- [ ] **Step 6: Commit**

```bash
git add internal/pipeline/adjacency.go internal/pipeline/scoring.go internal/pipeline/scoring_test.go
git commit -m "feat: deterministic publisher scoring with hard gates

Six weighted sub-scores plus three non-overridable gates. The demographic
gate is what excludes a 50-70 publisher from a 25-45 brief on arithmetic
rather than on model judgement."
```

---

### Task 3: Derived CPM

**Files:**
- Create: `internal/pipeline/cpm.go`
- Test: `internal/pipeline/cpm_test.go`

**Interfaces:**
- Consumes: `catalog.Publisher`, `(*catalog.Catalog).MinAOV`, `.MaxAOV` (Task 1)
- Produces: `pipeline.EstimateCPM(pub *catalog.Publisher, minAOV, maxAOV int) float64` — dollars per thousand impressions

The catalog has no price field. Rather than let a model invent one, CPM is computed from the three fields that proxy audience value. This function is the single place a real rate card would replace.

- [ ] **Step 1: Write the failing test**

```go
package pipeline

import (
	"math"
	"testing"
)

// Spot values fixed by the design doc. If these move, the budget figures in
// every demo and every eval snapshot move with them.
func TestEstimateCPMSpotValues(t *testing.T) {
	c := loadCatalog(t)
	for _, tc := range []struct {
		id   string
		name string
		want float64
	}{
		{"pub_001", "Swiftcart", 8.00},
		{"pub_009", "Ruffco", 11.44},
		{"pub_007", "Pawline", 15.48},
		{"pub_008", "Pantrygood", 17.87},
		{"pub_014", "Hearthstone Goods", 24.15},
	} {
		pub, ok := c.Publisher(tc.id)
		if !ok {
			t.Fatalf("%s missing", tc.id)
		}
		got := EstimateCPM(pub, c.MinAOV, c.MaxAOV)
		if math.Abs(got-tc.want) > 0.005 {
			t.Errorf("%s CPM = %.4f, want %.2f", tc.name, got, tc.want)
		}
	}
}

// Every publisher should land in a range a real display buyer would recognise.
func TestEstimateCPMStaysInPlausibleRange(t *testing.T) {
	c := loadCatalog(t)
	for i := range c.Publishers {
		got := EstimateCPM(&c.Publishers[i], c.MinAOV, c.MaxAOV)
		if got < 8 || got > 25 {
			t.Errorf("%s CPM = %.2f, want [8,25]", c.Publishers[i].ID, got)
		}
	}
}

// AOV is capped at a 15% effect so it modifies the number without driving it.
func TestAOVIsAModifierNotADriver(t *testing.T) {
	c := loadCatalog(t)
	lowAOVHighTier, _ := c.Publisher("pub_003")  // wellness_services, high, AOV 42
	highAOVLowTier, _ := c.Publisher("pub_006")  // apparel, mid, AOV 89
	a := EstimateCPM(lowAOVHighTier, c.MinAOV, c.MaxAOV)
	b := EstimateCPM(highAOVLowTier, c.MinAOV, c.MaxAOV)
	if a <= b {
		t.Errorf("high-tier low-AOV (%.2f) should exceed mid-tier high-AOV (%.2f); "+
			"AOV is overpowering income tier", a, b)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/pipeline/ -run TestEstimateCPM -v`
Expected: FAIL — `undefined: EstimateCPM`

- [ ] **Step 3: Write `internal/pipeline/cpm.go`**

```go
package pipeline

import "github.com/yashraj/disco/internal/catalog"

// The supplied catalog carries no CPM, CPC, or rate card. EstimateCPM derives a
// stand-in from the three fields that proxy how valuable an audience is to
// advertisers: income tier, category demand, and order value.
//
// This is a stand-in, not a measurement. Swapping in a real rate card means
// replacing this one function and changing nothing else.
const (
	cpmFloor    = 8.0  // dollars per thousand impressions
	aovMaxLift  = 0.15 // AOV can move the number by at most 15%
)

var incomePremium = map[string]float64{
	"mid":      0,
	"mid-high": 4,
	"high":     9,
}

var categoryPremium = map[string]float64{
	"groceries":         0,
	"instant_delivery":  0,
	"meal_kits":         0,
	"beverages":         1,
	"apparel":           2,
	"pet":               3,
	"wellness_dtc":      3,
	"wellness_services": 3,
	"beauty":            3,
	"home":              4,
}

// EstimateCPM returns the modelled cost per thousand impressions for a
// publisher. minAOV and maxAOV are the catalog-wide bounds, used to place this
// publisher's order value on a 0..1 index.
func EstimateCPM(pub *catalog.Publisher, minAOV, maxAOV int) float64 {
	base := cpmFloor + incomePremium[pub.Audience.IncomeTier] + categoryPremium[pub.Category]

	var idx float64
	if maxAOV > minAOV {
		idx = clamp(float64(pub.AvgOrderValueUSD-minAOV)/float64(maxAOV-minAOV), 0, 1)
	}
	return base * (1 + aovMaxLift*idx)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/pipeline/ -run TestEstimateCPM -v && go test ./internal/pipeline/ -run TestAOVIs -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/pipeline/cpm.go internal/pipeline/cpm_test.go
git commit -m "feat: derive a stand-in CPM from income tier, category, and AOV

The catalog carries no price field, so budget allocation needs a modelled
one. Isolated in a single function with spot values pinned by test."
```

---

### Task 4: Budget allocation with fixed-point capping

**Files:**
- Create: `internal/pipeline/allocate.go`
- Test: `internal/pipeline/allocate_test.go`

**Interfaces:**
- Consumes: nothing beyond stdlib
- Produces:
  - `pipeline.AllocParams{Gamma, MaxShare, SOVCap, MinShare float64; TotalUSD float64; Days int}`
  - `pipeline.Candidate{PublisherID string; Fit, EstCPM float64; MonthlyImpressions int64}`
  - `pipeline.Allocation{PublisherID string; Share, AmountUSD, EstCPMUSD float64; EstImpressions int64}`
  - `pipeline.DefaultAllocParams() AllocParams`
  - `pipeline.Allocate(cands []Candidate, p AllocParams) []Allocation` — sorted by share descending, shares sum to 1.0

Three caps interact through redistribution: dropping a sub-floor publisher can push a survivor back over the concentration cap, and relaxing a clip can pull another under the floor. A single pass would emit allocations that violate the plan's own stated limits, so the caps are iterated to a fixed point.

- [ ] **Step 1: Write the failing test**

```go
package pipeline

import (
	"math"
	"testing"
)

// The dog-food shortlist, with CPMs as produced by EstimateCPM.
func dogFoodCandidates() []Candidate {
	return []Candidate{
		{PublisherID: "pub_007", Fit: 0.84, EstCPM: 15.4765, MonthlyImpressions: 4_800_000},  // Pawline
		{PublisherID: "pub_009", Fit: 0.71, EstCPM: 11.4368, MonthlyImpressions: 62_000_000}, // Ruffco
		{PublisherID: "pub_008", Fit: 0.52, EstCPM: 17.8700, MonthlyImpressions: 10_400_000}, // Pantrygood
		{PublisherID: "pub_018", Fit: 0.44, EstCPM: 11.0679, MonthlyImpressions: 8_400_000},  // Tailcrate
	}
}

func shareOf(t *testing.T, as []Allocation, id string) float64 {
	t.Helper()
	for _, a := range as {
		if a.PublisherID == id {
			return a.Share
		}
	}
	return 0
}

func sumShares(as []Allocation) float64 {
	var s float64
	for _, a := range as {
		s += a.Share
	}
	return s
}

// At $25k no cap binds, so the result is pure fit^gamma. This pins the gamma
// maths; every other case builds on it.
func TestAllocateUncappedFollowsGamma(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 25_000
	got := Allocate(dogFoodCandidates(), p)

	for _, tc := range []struct {
		id   string
		want float64
	}{
		{"pub_007", 0.37832},
		{"pub_009", 0.29399},
		{"pub_008", 0.18427},
		{"pub_018", 0.14342},
	} {
		if g := shareOf(t, got, tc.id); math.Abs(g-tc.want) > 0.0005 {
			t.Errorf("%s share = %.5f, want %.5f", tc.id, g, tc.want)
		}
	}
	if s := sumShares(got); math.Abs(s-1) > 1e-9 {
		t.Errorf("shares sum to %v, want 1", s)
	}
}

// Gamma concentrates spend on better fits relative to a plain proportional
// split, without abandoning the tail.
func TestGammaConcentratesRelativeToProportional(t *testing.T) {
	c := dogFoodCandidates()
	p := DefaultAllocParams()
	p.TotalUSD = 25_000

	sharpened := Allocate(c, p)
	p.Gamma = 1.0
	flat := Allocate(c, p)

	ratio := func(as []Allocation) float64 {
		return shareOf(t, as, "pub_007") / shareOf(t, as, "pub_018")
	}
	if ratio(sharpened) <= ratio(flat) {
		t.Errorf("gamma=1.5 ratio %.2f should exceed gamma=1.0 ratio %.2f",
			ratio(sharpened), ratio(flat))
	}
}

// At $50k Pawline is still the best fit but cannot absorb its share: 37.8% of
// the budget buys 1.22M impressions against a 720k deliverable ceiling. It
// clips to exactly the ceiling and drops from first place to third.
func TestAllocateClipsOnDeliverableInventory(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 50_000
	got := Allocate(dogFoodCandidates(), p)

	for _, tc := range []struct {
		id   string
		want float64
	}{
		{"pub_009", 0.36750},
		{"pub_008", 0.23035},
		{"pub_007", 0.22286},
		{"pub_018", 0.17929},
	} {
		if g := shareOf(t, got, tc.id); math.Abs(g-tc.want) > 0.0005 {
			t.Errorf("%s share = %.5f, want %.5f", tc.id, g, tc.want)
		}
	}
	if got[0].PublisherID != "pub_009" {
		t.Errorf("top allocation = %s, want pub_009 (Ruffco) after Pawline clips",
			got[0].PublisherID)
	}
	for _, a := range got {
		if a.PublisherID == "pub_007" && a.EstImpressions > 720_000 {
			t.Errorf("Pawline impressions = %d, exceeds 15%% of 4.8M", a.EstImpressions)
		}
	}
}

// Every declared cap must hold in the emitted result, not merely in an
// intermediate pass.
func TestAllocateRespectsAllCapsAfterRedistribution(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 50_000
	for _, got := range [][]Allocation{
		Allocate(dogFoodCandidates(), p),
		Allocate(dogFoodCandidates()[:2], p),
		Allocate(dogFoodCandidates()[:1], p),
	} {
		for _, a := range got {
			if a.Share < p.MinShare-1e-9 && len(got) > 1 {
				t.Errorf("%s share %.4f below floor %.2f", a.PublisherID, a.Share, p.MinShare)
			}
			if a.Share > p.MaxShare+1e-9 && len(got) > 1 {
				t.Errorf("%s share %.4f above cap %.2f", a.PublisherID, a.Share, p.MaxShare)
			}
		}
		if s := sumShares(got); math.Abs(s-1) > 1e-9 {
			t.Errorf("shares sum to %v, want 1", s)
		}
	}
}

// A lone survivor takes the whole budget rather than being dropped by the floor.
func TestAllocateSingleCandidateTakesEverything(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 25_000
	got := Allocate(dogFoodCandidates()[:1], p)
	if len(got) != 1 || math.Abs(got[0].Share-1) > 1e-9 {
		t.Fatalf("got %+v, want a single 100%% allocation", got)
	}
}

func TestAllocateEmptyInput(t *testing.T) {
	if got := Allocate(nil, DefaultAllocParams()); len(got) != 0 {
		t.Errorf("got %d allocations, want 0", len(got))
	}
}

func TestAllocateComputesDollarsAndImpressions(t *testing.T) {
	p := DefaultAllocParams()
	p.TotalUSD = 25_000
	got := Allocate(dogFoodCandidates(), p)
	for _, a := range got {
		wantUSD := p.TotalUSD * a.Share
		if math.Abs(a.AmountUSD-wantUSD) > 0.01 {
			t.Errorf("%s amount = %.2f, want %.2f", a.PublisherID, a.AmountUSD, wantUSD)
		}
		wantImp := int64(a.AmountUSD / a.EstCPMUSD * 1000)
		if a.EstImpressions != wantImp {
			t.Errorf("%s impressions = %d, want %d", a.PublisherID, a.EstImpressions, wantImp)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/pipeline/ -run TestAllocate -v`
Expected: FAIL — `undefined: DefaultAllocParams`, `undefined: Allocate`, `undefined: Candidate`

- [ ] **Step 3: Write `internal/pipeline/allocate.go`**

```go
package pipeline

import (
	"math"
	"sort"
)

// AllocParams are the budget-splitting knobs. Every one is exposed as a CLI
// flag so the shape of an allocation can be interrogated and changed live.
type AllocParams struct {
	Gamma    float64 // share exponent; 1.0 is proportional, higher concentrates
	MaxShare float64 // concentration cap per publisher
	SOVCap   float64 // share of a publisher's monthly impressions we will buy
	MinShare float64 // below this a slice is dust and is redistributed
	TotalUSD float64
	Days     int
}

// DefaultAllocParams returns the documented defaults.
func DefaultAllocParams() AllocParams {
	return AllocParams{Gamma: 1.5, MaxShare: 0.40, SOVCap: 0.15, MinShare: 0.05,
		TotalUSD: 25_000, Days: 30}
}

// Candidate is one publisher competing for budget. Fit is the deterministic
// stage-2 score; a model never supplies it.
type Candidate struct {
	PublisherID        string
	Fit                float64
	EstCPM             float64
	MonthlyImpressions int64
}

// Allocation is one publisher's resulting slice of the budget.
type Allocation struct {
	PublisherID    string
	Share          float64
	AmountUSD      float64
	EstCPMUSD      float64
	EstImpressions int64
}

const maxAllocPasses = 5

// Allocate splits TotalUSD across candidates in proportion to Fit^Gamma, then
// applies three caps: a per-publisher concentration cap, a deliverability cap
// derived from the publisher's inventory at its modelled CPM, and a floor below
// which a slice is too small to be worth buying.
//
// The caps interact — releasing money from a clipped publisher can push a
// survivor over the concentration cap, and dropping a sub-floor publisher
// redistributes money that can do the same — so they are iterated to a fixed
// point rather than applied in a single pass.
func Allocate(cands []Candidate, p AllocParams) []Allocation {
	if len(cands) == 0 {
		return nil
	}

	share := make(map[string]float64, len(cands))
	byID := make(map[string]Candidate, len(cands))
	var total float64
	for _, c := range cands {
		w := math.Pow(math.Max(c.Fit, 0), p.Gamma)
		share[c.PublisherID] = w
		byID[c.PublisherID] = c
		total += w
	}
	if total == 0 { // no candidate has any fit; split evenly
		for id := range share {
			share[id] = 1 / float64(len(share))
		}
	} else {
		for id := range share {
			share[id] /= total
		}
	}

	locked := map[string]float64{} // publishers pinned at their cap

	for pass := 0; pass < maxAllocPasses; pass++ {
		changed := false

		// Redistribute whatever is not locked across the unlocked survivors.
		var freeTotal, lockedTotal float64
		for id, v := range share {
			if _, isLocked := locked[id]; isLocked {
				lockedTotal += v
			} else {
				freeTotal += v
			}
		}
		if freeTotal > 0 {
			avail := 1 - lockedTotal
			for id := range share {
				if _, isLocked := locked[id]; !isLocked {
					share[id] = share[id] / freeTotal * avail
				}
			}
		}

		// Concentration and deliverability caps.
		for id, v := range share {
			if _, isLocked := locked[id]; isLocked {
				continue
			}
			cap := p.MaxShare
			if c := byID[id]; c.EstCPM > 0 && p.TotalUSD > 0 {
				deliverableUSD := float64(c.MonthlyImpressions) * p.SOVCap / 1000 * c.EstCPM
				if s := deliverableUSD / p.TotalUSD; s < cap {
					cap = s
				}
			}
			if v > cap+1e-12 {
				share[id] = cap
				locked[id] = cap
				changed = true
			}
		}

		// Dust floor. Never drop the last survivor.
		if len(share) > 1 {
			for id, v := range share {
				if _, isLocked := locked[id]; isLocked {
					continue
				}
				if v < p.MinShare-1e-12 && len(share) > 1 {
					delete(share, id)
					changed = true
				}
			}
		}

		if !changed {
			break
		}
	}

	// Renormalise so the emitted shares sum to exactly 1.
	var sum float64
	for _, v := range share {
		sum += v
	}
	out := make([]Allocation, 0, len(share))
	for id, v := range share {
		c := byID[id]
		s := v / sum
		amount := p.TotalUSD * s
		var impressions int64
		if c.EstCPM > 0 {
			impressions = int64(amount / c.EstCPM * 1000)
		}
		out = append(out, Allocation{
			PublisherID: id, Share: s, AmountUSD: amount,
			EstCPMUSD: c.EstCPM, EstImpressions: impressions,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Share != out[j].Share {
			return out[i].Share > out[j].Share
		}
		return out[i].PublisherID < out[j].PublisherID // stable for equal shares
	})
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/pipeline/ -run TestAllocate -v && go test ./internal/pipeline/ -run TestGamma -v`
Expected: PASS, all seven tests

- [ ] **Step 5: Run the whole suite and vet**

Run: `go test ./... && go vet ./...`
Expected: PASS, no vet output

- [ ] **Step 6: Commit**

```bash
git add internal/pipeline/allocate.go internal/pipeline/allocate_test.go
git commit -m "feat: budget allocation with fixed-point capping

Share proportional to fit^gamma, then concentration, deliverability, and
dust-floor caps iterated to a fixed point. A single pass would emit
allocations violating the caps it just applied, because releasing money
from a clipped publisher pushes survivors back over the limit.

Pinned by test: at 50k the best-fitting publisher clips to its inventory
ceiling and drops from first place to third."
```

---

### Task 5: Stage 6 — campaign assembly

**Files:**
- Modify: `internal/model/types.go` (append the `Campaign` type and its sub-structs)
- Create: `internal/pipeline/campaign.go`
- Test: `internal/pipeline/campaign_test.go`

**Interfaces:**
- Consumes: `pipeline.Allocate`, `pipeline.EstimateCPM`, `pipeline.AllocParams`, `pipeline.Candidate` (Tasks 3–4); `model.PublisherScore`, `FitVerdict`, `PersonaSelection`, `Creative`, `AdvertiserProfile` (Tasks 1–2); `catalog.Catalog`
- Produces:
  - `model.Campaign` and sub-structs `AdvertiserBlock`, `Flight`, `Budget`, `Bid`, `Targeting`, `LedgerEntry`, `Meta`
  - status constants `model.StatusReady`, `StatusNeedsClarification`, `StatusNoRecommendation`
  - `pipeline.BuildInput{Brief, Profile, Scores, Verdicts, Personas, Creatives, Catalog, Params, Model, Provider}`
  - `pipeline.Build(in BuildInput) model.Campaign`

- [ ] **Step 1: Append the campaign types to `internal/model/types.go`**

```go
// Campaign status values.
const (
	StatusReady              = "ready"
	StatusNeedsClarification = "needs_clarification"
	StatusNoRecommendation   = "no_recommendation"
)

// AdvertiserBlock carries the brief and what stage 1 made of it.
type AdvertiserBlock struct {
	Brief          string            `json:"brief"`
	DerivedProfile AdvertiserProfile `json:"derived_profile"`
	Confidence     string            `json:"confidence"`
	Assumptions    []string          `json:"assumptions"`
	MissingSignals []string          `json:"missing_signals"`
}

// Flight is when the campaign runs.
type Flight struct {
	Start        string `json:"start"` // RFC 3339 date
	DurationDays int    `json:"duration_days"`
}

// AllocationEntry is one publisher's budget slice as it appears in the config.
type AllocationEntry struct {
	PublisherID    string  `json:"publisher_id"`
	PublisherName  string  `json:"publisher_name"`
	Share          float64 `json:"share"`
	AmountUSD      float64 `json:"amount_usd"`
	EstCPMUSD      float64 `json:"est_cpm_usd"`
	EstImpressions int64   `json:"est_impressions"`
	Rationale      string  `json:"rationale"`
}

// Budget is the total and its split.
type Budget struct {
	TotalUSD    float64           `json:"total_usd"`
	DailyCapUSD float64           `json:"daily_cap_usd"`
	Allocation  []AllocationEntry `json:"allocation"`
}

// Bid is the bidding posture, derived from the allocation's weighted CPM.
type Bid struct {
	Model        string  `json:"model"`
	Strategy     string  `json:"strategy"`
	FloorUSD     float64 `json:"floor_usd"`
	TargetUSD    float64 `json:"target_usd"`
	CeilingUSD   float64 `json:"ceiling_usd"`
	TargetCPAUSD float64 `json:"target_cpa_usd"`
	Reasoning    string  `json:"reasoning"`
}

// Targeting is the audience definition a downstream system would act on.
type Targeting struct {
	AgeRange             string   `json:"age_range"`
	GenderSkew           string   `json:"gender_skew"`
	Geos                 []string `json:"geos"`
	IncomeTiers          []string `json:"income_tiers"`
	PersonaIDs           []string `json:"persona_ids"`
	ContextualCategories []string `json:"contextual_categories"`
}

// LedgerEntry is one publisher's verdict. Every catalog publisher gets one on
// every run, including the ones that were never in contention.
type LedgerEntry struct {
	PublisherID   string    `json:"publisher_id"`
	PublisherName string    `json:"publisher_name"`
	Verdict       string    `json:"verdict"`
	Rank          int       `json:"rank,omitempty"`
	Score         float64   `json:"score"`
	SubScores     SubScores `json:"sub_scores"`
	HardGate      string    `json:"hard_gate,omitempty"`
	Reason        string    `json:"reason"`
}

// Meta records how this config was produced.
type Meta struct {
	GeneratedAt     string `json:"generated_at"`
	Model           string `json:"model"`
	Provider        string `json:"provider"`
	PipelineVersion string `json:"pipeline_version"`
}

// Campaign is the complete output artifact.
type Campaign struct {
	Status            string             `json:"status"`
	Advertiser        AdvertiserBlock    `json:"advertiser"`
	Objective         string             `json:"objective"`
	Flight            Flight             `json:"flight"`
	Budget            Budget             `json:"budget"`
	Bid               Bid                `json:"bid"`
	Targeting         Targeting          `json:"targeting"`
	Creatives         []Creative         `json:"creatives"`
	PublisherLedger   []LedgerEntry      `json:"publisher_ledger"`
	PersonaRejections []PersonaRejection `json:"persona_rejections,omitempty"`
	Clarifications    []string           `json:"clarifications,omitempty"`
	Meta              Meta               `json:"meta"`
}
```

- [ ] **Step 2: Write the failing test**

```go
package pipeline

import (
	"math"
	"testing"

	"github.com/yashraj/disco/internal/model"
)

func buildInput(t *testing.T, p model.AdvertiserProfile) BuildInput {
	t.Helper()
	c := loadCatalog(t)
	scores := ScoreAll(p, c)

	// Stand in for stage 3: recommend the four best-scoring ungated publishers.
	var verdicts []model.FitVerdict
	rank := 0
	for _, s := range scores {
		v := model.FitVerdict{PublisherID: s.PublisherID, Verdict: "excluded", Reason: "low fit"}
		if s.HardGate == GateNone && s.Score > 0.45 && rank < 4 {
			rank++
			v = model.FitVerdict{PublisherID: s.PublisherID, Verdict: "recommended",
				Rank: rank, Reason: "strong category and audience match"}
		}
		verdicts = append(verdicts, v)
	}

	return BuildInput{
		Brief:   p.RawBrief,
		Profile: p,
		Scores:  scores,
		Verdicts: verdicts,
		Personas: model.PersonaSelection{
			Selected: []model.PersonaPick{
				{PersonaID: "persona_004", Rationale: "pet parent"},
				{PersonaID: "persona_002", Rationale: "busy parent"},
				{PersonaID: "persona_001", Rationale: "wellness optimizer"},
			},
			Rejected: []model.PersonaRejection{{PersonaID: "persona_003", Reason: "no pet affinity"}},
		},
		Creatives: []model.Creative{
			{PersonaID: "persona_004", Headline: "a", Body: "b"},
			{PersonaID: "persona_002", Headline: "c", Body: "d"},
			{PersonaID: "persona_001", Headline: "e", Body: "f"},
		},
		Catalog:  c,
		Params:   DefaultAllocParams(),
		Model:    "gemini-2.5-flash",
		Provider: "fixture",
	}
}

func TestBuildLedgerCoversEveryPublisher(t *testing.T) {
	c := loadCatalog(t)
	got := Build(buildInput(t, dogFood()))
	if len(got.PublisherLedger) != len(c.Publishers) {
		t.Fatalf("ledger has %d entries, want %d", len(got.PublisherLedger), len(c.Publishers))
	}
	seen := map[string]bool{}
	for _, e := range got.PublisherLedger {
		if e.PublisherName == "" {
			t.Errorf("%s has no publisher name", e.PublisherID)
		}
		if e.Reason == "" {
			t.Errorf("%s has no reason", e.PublisherID)
		}
		seen[e.PublisherID] = true
	}
	for _, p := range c.Publishers {
		if !seen[p.ID] {
			t.Errorf("%s missing from ledger", p.ID)
		}
	}
}

func TestBuildAllocatesOnlyToRecommended(t *testing.T) {
	got := Build(buildInput(t, dogFood()))
	rec := map[string]bool{}
	for _, e := range got.PublisherLedger {
		if e.Verdict == "recommended" {
			rec[e.PublisherID] = true
		}
	}
	if len(got.Budget.Allocation) == 0 {
		t.Fatal("no allocation produced")
	}
	var sum float64
	for _, a := range got.Budget.Allocation {
		if !rec[a.PublisherID] {
			t.Errorf("%s got budget but is not recommended", a.PublisherID)
		}
		sum += a.Share
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Errorf("allocation shares sum to %v, want 1", sum)
	}
	if got.Budget.DailyCapUSD <= 0 {
		t.Error("daily cap not set")
	}
}

func TestBuildDerivesObjective(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*model.AdvertiserProfile)
		want    string
	}{
		{"b2b", func(p *model.AdvertiserProfile) { p.BusinessModel = "b2b" }, "consideration"},
		{"service", func(p *model.AdvertiserProfile) { p.BusinessModel = "service" }, "consideration"},
		{"subscription", func(p *model.AdvertiserProfile) { p.BusinessModel = "subscription" }, "conversion"},
		{"luxury", func(p *model.AdvertiserProfile) {
			p.BusinessModel = "one_off"
			p.PriceTier = "luxury"
		}, "awareness"},
		{"default", func(p *model.AdvertiserProfile) {
			p.BusinessModel = "one_off"
			p.PriceTier = "mid"
		}, "conversion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := dogFood()
			tc.mutate(&p)
			if got := Build(buildInput(t, p)).Objective; got != tc.want {
				t.Errorf("objective = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildDerivesBidFromWeightedCPM(t *testing.T) {
	got := Build(buildInput(t, dogFood()))
	var w float64
	for _, a := range got.Budget.Allocation {
		w += a.Share * a.EstCPMUSD
	}
	if math.Abs(got.Bid.TargetUSD-w) > 0.01 {
		t.Errorf("bid target = %.2f, want weighted CPM %.2f", got.Bid.TargetUSD, w)
	}
	if math.Abs(got.Bid.FloorUSD-0.8*w) > 0.01 {
		t.Errorf("bid floor = %.2f, want %.2f", got.Bid.FloorUSD, 0.8*w)
	}
	if math.Abs(got.Bid.CeilingUSD-1.4*w) > 0.01 {
		t.Errorf("bid ceiling = %.2f, want %.2f", got.Bid.CeilingUSD, 1.4*w)
	}
	if got.Bid.Strategy != "target_cpa_capped" {
		t.Errorf("strategy = %q, want target_cpa_capped", got.Bid.Strategy)
	}
	if want := 0.35 * float64(dogFood().EstimatedAOVUSD); math.Abs(got.Bid.TargetCPAUSD-want) > 0.01 {
		t.Errorf("target CPA = %.2f, want %.2f", got.Bid.TargetCPAUSD, want)
	}
}

// A non-consumer brief produces no budget, no creatives, and a ledger in which
// every publisher is excluded for the same honest reason.
func TestBuildNoRecommendationForNonConsumerBrief(t *testing.T) {
	p := dogFood()
	p.IsConsumerDTC = false
	in := buildInput(t, p)
	in.Creatives = nil
	in.Personas = model.PersonaSelection{}
	got := Build(in)

	if got.Status != model.StatusNoRecommendation {
		t.Errorf("status = %q, want %q", got.Status, model.StatusNoRecommendation)
	}
	if len(got.Budget.Allocation) != 0 {
		t.Errorf("got %d allocations, want 0", len(got.Budget.Allocation))
	}
	if len(got.Creatives) != 0 {
		t.Errorf("got %d creatives, want 0", len(got.Creatives))
	}
	for _, e := range got.PublisherLedger {
		if e.Verdict != "excluded" {
			t.Errorf("%s verdict = %q, want excluded", e.PublisherID, e.Verdict)
		}
	}
}

func TestBuildLowConfidenceAsksForClarification(t *testing.T) {
	p := dogFood()
	p.Confidence = "low"
	p.MissingSignals = []string{"price point", "who buys it"}
	got := Build(buildInput(t, p))
	if got.Status != model.StatusNeedsClarification {
		t.Errorf("status = %q, want %q", got.Status, model.StatusNeedsClarification)
	}
	if len(got.Clarifications) == 0 {
		t.Error("no clarifying questions produced")
	}
	if len(got.Budget.Allocation) == 0 {
		t.Error("a provisional campaign should still be emitted at low confidence")
	}
}

func TestBuildTargetingUnionsRecommendedPublishers(t *testing.T) {
	got := Build(buildInput(t, dogFood()))
	if len(got.Targeting.Geos) == 0 {
		t.Error("no geos derived")
	}
	if len(got.Targeting.PersonaIDs) != 3 {
		t.Errorf("got %d persona IDs, want 3", len(got.Targeting.PersonaIDs))
	}
	if got.Targeting.AgeRange == "" {
		t.Error("no age range derived")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/pipeline/ -run TestBuild -v`
Expected: FAIL — `undefined: BuildInput`, `undefined: Build`

- [ ] **Step 4: Write `internal/pipeline/campaign.go`**

```go
package pipeline

import (
	"fmt"
	"sort"
	"time"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
)

// PipelineVersion is stamped into every campaign so a stored config can be
// traced back to the logic that produced it.
const PipelineVersion = "1"

// BuildInput is everything stage 6 needs. Scores and Verdicts must each cover
// every catalog publisher.
type BuildInput struct {
	Brief    string
	Profile  model.AdvertiserProfile
	Scores   []model.PublisherScore
	Verdicts []model.FitVerdict
	Personas model.PersonaSelection
	Creatives []model.Creative
	Catalog  *catalog.Catalog
	Params   AllocParams
	Model    string
	Provider string
}

// Build assembles the campaign config. It owns every number in the output:
// allocation, CPM, impressions, bid range, and daily cap are all computed here
// from the deterministic stage-2 scores, never taken from a model response.
func Build(in BuildInput) model.Campaign {
	scoreByID := make(map[string]model.PublisherScore, len(in.Scores))
	for _, s := range in.Scores {
		scoreByID[s.PublisherID] = s
	}
	verdictByID := make(map[string]model.FitVerdict, len(in.Verdicts))
	for _, v := range in.Verdicts {
		verdictByID[v.PublisherID] = v
	}

	c := model.Campaign{
		Status: statusFor(in.Profile),
		Advertiser: model.AdvertiserBlock{
			Brief:          in.Brief,
			DerivedProfile: in.Profile,
			Confidence:     in.Profile.Confidence,
			Assumptions:    in.Profile.Assumptions,
			MissingSignals: in.Profile.MissingSignals,
		},
		Objective: objectiveFor(in.Profile),
		Flight: model.Flight{
			Start:        time.Now().UTC().Format("2006-01-02"),
			DurationDays: in.Params.Days,
		},
		Meta: model.Meta{
			GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
			Model:           in.Model,
			Provider:        in.Provider,
			PipelineVersion: PipelineVersion,
		},
	}

	// Ledger: one entry per catalog publisher, always.
	for i := range in.Catalog.Publishers {
		pub := &in.Catalog.Publishers[i]
		s := scoreByID[pub.ID]
		v, ok := verdictByID[pub.ID]
		if !ok {
			v = model.FitVerdict{Verdict: "excluded", Reason: "not evaluated"}
		}
		if s.HardGate != GateNone {
			v.Verdict = "excluded" // a hard gate is not overridable
			v.Rank = 0
			if v.Reason == "" {
				v.Reason = gateReason(s.HardGate)
			}
		}
		c.PublisherLedger = append(c.PublisherLedger, model.LedgerEntry{
			PublisherID: pub.ID, PublisherName: pub.Name,
			Verdict: v.Verdict, Rank: v.Rank,
			Score: s.Score, SubScores: s.Sub, HardGate: s.HardGate,
			Reason: v.Reason,
		})
	}

	if c.Status == model.StatusNoRecommendation {
		c.Budget = model.Budget{TotalUSD: in.Params.TotalUSD}
		c.Bid = model.Bid{Model: "CPM", Strategy: "none",
			Reasoning: "No publisher in this catalog reaches this advertiser's buyers."}
		c.Clarifications = []string{
			"This catalog is consumer DTC only. Which consumer-facing product should we place?",
		}
		return c
	}

	// Budget: only recommended, ungated publishers compete.
	var cands []Candidate
	for _, e := range c.PublisherLedger {
		if e.Verdict != "recommended" || e.HardGate != GateNone {
			continue
		}
		pub, _ := in.Catalog.Publisher(e.PublisherID)
		cands = append(cands, Candidate{
			PublisherID:        e.PublisherID,
			Fit:                e.Score,
			EstCPM:             EstimateCPM(pub, in.Catalog.MinAOV, in.Catalog.MaxAOV),
			MonthlyImpressions: pub.MonthlyImpressions,
		})
	}

	reasonByID := make(map[string]string, len(c.PublisherLedger))
	for _, e := range c.PublisherLedger {
		reasonByID[e.PublisherID] = e.Reason
	}
	var weightedCPM float64
	for _, a := range Allocate(cands, in.Params) {
		pub, _ := in.Catalog.Publisher(a.PublisherID)
		c.Budget.Allocation = append(c.Budget.Allocation, model.AllocationEntry{
			PublisherID: a.PublisherID, PublisherName: pub.Name,
			Share: a.Share, AmountUSD: a.AmountUSD,
			EstCPMUSD: a.EstCPMUSD, EstImpressions: a.EstImpressions,
			Rationale: reasonByID[a.PublisherID],
		})
		weightedCPM += a.Share * a.EstCPMUSD
	}
	c.Budget.TotalUSD = in.Params.TotalUSD
	if in.Params.Days > 0 {
		c.Budget.DailyCapUSD = in.Params.TotalUSD / float64(in.Params.Days)
	}

	c.Bid = buildBid(in.Profile, weightedCPM)
	c.Targeting = buildTargeting(in, c.Budget.Allocation)
	c.Creatives = in.Creatives
	c.PersonaRejections = in.Personas.Rejected

	if c.Status == model.StatusNeedsClarification {
		c.Clarifications = clarificationsFor(in.Profile)
	}
	return c
}

func statusFor(p model.AdvertiserProfile) string {
	switch {
	case !p.IsConsumerDTC:
		return model.StatusNoRecommendation
	case p.Confidence == "low":
		return model.StatusNeedsClarification
	default:
		return model.StatusReady
	}
}

func objectiveFor(p model.AdvertiserProfile) string {
	switch {
	case p.BusinessModel == "b2b" || p.BusinessModel == "service":
		return "consideration"
	case p.BusinessModel == "subscription":
		return "conversion"
	case p.PriceTier == "luxury":
		return "awareness"
	default:
		return "conversion"
	}
}

func gateReason(gate string) string {
	switch gate {
	case GateNotConsumerDTC:
		return "This catalog reaches consumer DTC shoppers; this advertiser does not sell to them."
	case GateCategoryMismatch:
		return "No category or subcategory overlap with this advertiser."
	case GateDemographicMismatch:
		return "Audience age range does not overlap the advertiser's target at all."
	default:
		return "Excluded."
	}
}

func buildBid(p model.AdvertiserProfile, weightedCPM float64) model.Bid {
	b := model.Bid{
		Model:      "CPM",
		Strategy:   "even_pacing",
		FloorUSD:   0.8 * weightedCPM,
		TargetUSD:  weightedCPM,
		CeilingUSD: 1.4 * weightedCPM,
	}
	if p.EstimatedAOVUSD > 0 {
		b.Strategy = "target_cpa_capped"
		b.TargetCPAUSD = 0.35 * float64(p.EstimatedAOVUSD)
		b.Reasoning = fmt.Sprintf(
			"Target bid tracks the share-weighted CPM of the selected publishers ($%.2f). "+
				"CPA ceiling assumes 35%% of a $%d order value is an acceptable acquisition cost.",
			weightedCPM, p.EstimatedAOVUSD)
	} else {
		b.Reasoning = fmt.Sprintf(
			"Target bid tracks the share-weighted CPM of the selected publishers ($%.2f). "+
				"No order value was inferable, so pacing is even rather than CPA-capped.",
			weightedCPM)
	}
	return b
}

func buildTargeting(in BuildInput, alloc []model.AllocationEntry) model.Targeting {
	t := model.Targeting{
		GenderSkew: in.Profile.TargetGenderSkew,
	}
	if in.Profile.TargetAgeMin > 0 || in.Profile.TargetAgeMax > 0 {
		t.AgeRange = fmt.Sprintf("%d-%d", in.Profile.TargetAgeMin, in.Profile.TargetAgeMax)
	}
	for _, p := range in.Personas.Selected {
		t.PersonaIDs = append(t.PersonaIDs, p.PersonaID)
	}

	geos, tiers, cats := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, a := range alloc {
		pub, ok := in.Catalog.Publisher(a.PublisherID)
		if !ok {
			continue
		}
		for _, g := range pub.Audience.TopGeos {
			geos[g] = true
		}
		tiers[pub.Audience.IncomeTier] = true
		cats[pub.Category] = true
		for _, s := range pub.Subcategories {
			cats[s] = true
		}
	}
	t.Geos, t.IncomeTiers, t.ContextualCategories = sortedKeys(geos), sortedKeys(tiers), sortedKeys(cats)
	return t
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// clarificationsFor turns the profile's missing signals into questions a person
// can actually answer. The campaign below them is still emitted, clearly marked
// provisional: refusing a vague brief outright is as unhelpful as pretending it
// was clear.
func clarificationsFor(p model.AdvertiserProfile) []string {
	if len(p.MissingSignals) == 0 {
		return []string{"What exactly do you sell, and roughly what does it cost?"}
	}
	out := make([]string, 0, len(p.MissingSignals))
	for _, m := range p.MissingSignals {
		out = append(out, fmt.Sprintf("We had to guess at %s. What is it actually?", m))
	}
	return out
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/pipeline/ -run TestBuild -v`
Expected: PASS, all seven tests

- [ ] **Step 6: Run the whole suite**

Run: `go test ./... && go vet ./...`
Expected: PASS, no vet output

- [ ] **Step 7: Commit**

```bash
git add internal/model/types.go internal/pipeline/campaign.go internal/pipeline/campaign_test.go
git commit -m "feat: assemble the campaign config

Stage 6 owns every number in the output. The ledger always carries one
entry per catalog publisher so exclusions are as visible as picks, and a
hard gate overrides whatever verdict stage 3 assigned."
```

---

### Task 6: Provider interface, schema validation, prompt embedding, fixture provider

**Files:**
- Create: `internal/llm/provider.go`, `internal/llm/schema.go`, `internal/llm/fixture.go`, `prompts/embed.go`
- Test: `internal/llm/llm_test.go`

**Interfaces:**
- Consumes: nothing beyond stdlib and `github.com/santhosh-tekuri/jsonschema/v6`
- Produces:
  - `llm.Request{Stage, Prompt string; Schema json.RawMessage; FixtureKey string}`
  - `llm.Provider` interface: `Name() string`, `Complete(ctx context.Context, r Request) (json.RawMessage, error)`
  - `llm.CacheKey(provider, model string, r Request) string`
  - `llm.Validate(schema, doc json.RawMessage) error`
  - `llm.NewFixture(dir string) *llm.Fixture`, `(*Fixture).Record(r Request, out json.RawMessage) error`
  - `prompts.Load(stage string) (text string, schema json.RawMessage, err error)`

Two different keys, on purpose. The **disk cache** is keyed by the full rendered prompt, so editing a prompt correctly invalidates it. **Fixtures** are keyed by `FixtureKey` — stage plus a hash of the brief — so a reviewer's committed demo data survives prompt edits instead of going stale on every wording change.

- [ ] **Step 1: Add the dependencies**

```bash
go get github.com/santhosh-tekuri/jsonschema/v6
go get golang.org/x/sync/errgroup
```

- [ ] **Step 2: Write `internal/llm/provider.go`**

```go
// Package llm wraps model access behind a two-implementation interface: a real
// provider for live runs and a fixture replayer so the test suite and a
// reviewer's first run need no API key.
package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Request is one structured-output call.
type Request struct {
	Stage  string          // profile | fit | personas | creative
	Prompt string          // fully rendered prompt text
	Schema json.RawMessage // JSON Schema the response must satisfy

	// FixtureKey identifies this call for fixture replay. It is deliberately
	// coarser than the prompt hash — stage plus a hash of the advertiser brief —
	// so committed demo fixtures survive prompt edits.
	FixtureKey string
}

// Provider returns JSON conforming to the request's schema.
type Provider interface {
	Name() string
	Complete(ctx context.Context, r Request) (json.RawMessage, error)
}

// CacheKey identifies a call exactly. Any change to the provider, model,
// prompt, or schema produces a different key, so the disk cache never serves a
// stale response after a prompt edit.
func CacheKey(provider, model string, r Request) string {
	h := sha256.New()
	for _, part := range []string{provider, model, r.Stage, r.Prompt, string(r.Schema)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// ShortHash is a stable 8-character hash, used to build fixture keys from a
// brief.
func ShortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}
```

- [ ] **Step 3: Write `internal/llm/schema.go`**

```go
package llm

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Validate checks doc against schema. The same schema file is the model's
// response contract and this validator, so a drifting prompt cannot quietly
// produce a shape the pipeline does not expect.
func Validate(schema, doc json.RawMessage) error {
	compiler := jsonschema.NewCompiler()

	sch, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return fmt.Errorf("llm: parsing schema: %w", err)
	}
	if err := compiler.AddResource("schema.json", sch); err != nil {
		return fmt.Errorf("llm: adding schema: %w", err)
	}
	compiled, err := compiler.Compile("schema.json")
	if err != nil {
		return fmt.Errorf("llm: compiling schema: %w", err)
	}

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return fmt.Errorf("llm: parsing response: %w", err)
	}
	if err := compiled.Validate(inst); err != nil {
		return fmt.Errorf("llm: response does not satisfy schema: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Write `prompts/embed.go`**

```go
// Package prompts embeds the prompt text and response schemas. It lives inside
// prompts/ because go:embed cannot read outside its own directory, and the
// exercise asks for every prompt to be reviewable at prompts/ in the repo root.
package prompts

import (
	"embed"
	"encoding/json"
	"fmt"
)

//go:embed *.md *.schema.json
var files embed.FS

// Load returns the prompt text and response schema for a pipeline stage.
func Load(stage string) (string, json.RawMessage, error) {
	text, err := files.ReadFile(stage + ".md")
	if err != nil {
		return "", nil, fmt.Errorf("prompts: %w", err)
	}
	schema, err := files.ReadFile(stage + ".schema.json")
	if err != nil {
		return "", nil, fmt.Errorf("prompts: %w", err)
	}
	return string(text), json.RawMessage(schema), nil
}
```

- [ ] **Step 5: Write the failing test**

```go
package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var personSchema = json.RawMessage(`{
  "type": "object",
  "properties": {"name": {"type": "string"}, "age": {"type": "integer"}},
  "required": ["name", "age"],
  "additionalProperties": false
}`)

func TestValidateAcceptsConformingDocument(t *testing.T) {
	if err := Validate(personSchema, json.RawMessage(`{"name":"a","age":3}`)); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestValidateRejectsMissingField(t *testing.T) {
	err := Validate(personSchema, json.RawMessage(`{"name":"a"}`))
	if err == nil {
		t.Fatal("want error for missing required field")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("error %q should mention the schema", err)
	}
}

func TestValidateRejectsWrongType(t *testing.T) {
	if err := Validate(personSchema, json.RawMessage(`{"name":"a","age":"three"}`)); err == nil {
		t.Fatal("want error for wrong type")
	}
}

func TestCacheKeyChangesWithEveryInput(t *testing.T) {
	base := Request{Stage: "fit", Prompt: "p", Schema: personSchema}
	k := CacheKey("gemini", "m", base)

	other := base
	other.Prompt = "p2"
	if CacheKey("gemini", "m", other) == k {
		t.Error("prompt change should change the key")
	}
	if CacheKey("gemini", "m2", base) == k {
		t.Error("model change should change the key")
	}
	if CacheKey("fixture", "m", base) == k {
		t.Error("provider change should change the key")
	}
	if CacheKey("gemini", "m", base) != k {
		t.Error("identical inputs should produce identical keys")
	}
}

func TestFixtureRoundTrip(t *testing.T) {
	dir := t.TempDir()
	f := NewFixture(dir)

	r := Request{Stage: "profile", Prompt: "anything", Schema: personSchema,
		FixtureKey: "profile-abc12345"}
	want := json.RawMessage(`{"name":"a","age":3}`)

	if err := f.Record(r, want); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := f.Complete(context.Background(), r)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("got %s, want %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "profile-abc12345.json")); err != nil {
		t.Errorf("fixture file not written: %v", err)
	}
}

// A fixture replay is stable across prompt edits: the same brief keeps working
// after the prompt wording changes.
func TestFixtureIgnoresPromptChanges(t *testing.T) {
	dir := t.TempDir()
	f := NewFixture(dir)
	r := Request{Stage: "fit", Prompt: "v1", Schema: personSchema, FixtureKey: "fit-deadbeef"}
	if err := f.Record(r, json.RawMessage(`{"name":"a","age":3}`)); err != nil {
		t.Fatal(err)
	}
	r.Prompt = "v2 — reworded"
	if _, err := f.Complete(context.Background(), r); err != nil {
		t.Errorf("prompt edit should not invalidate a fixture: %v", err)
	}
}

func TestFixtureMissingIsAClearError(t *testing.T) {
	f := NewFixture(t.TempDir())
	_, err := f.Complete(context.Background(),
		Request{Stage: "fit", FixtureKey: "fit-nothere"})
	if err == nil {
		t.Fatal("want error for a missing fixture")
	}
	if !strings.Contains(err.Error(), "--record") {
		t.Errorf("error %q should tell the user how to create it", err)
	}
}

func TestFixtureName(t *testing.T) {
	if got := NewFixture(t.TempDir()).Name(); got != "fixture" {
		t.Errorf("Name = %q, want fixture", got)
	}
}
```

- [ ] **Step 6: Run the test to verify it fails**

Run: `go test ./internal/llm/ -v`
Expected: FAIL — `undefined: NewFixture`

- [ ] **Step 7: Write `internal/llm/fixture.go`**

```go
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Fixture replays recorded responses from disk. It makes the test suite
// deterministic and lets someone clone the repo and see the whole pipeline run
// without an API key or a billing account.
type Fixture struct{ dir string }

// NewFixture returns a replayer reading from dir.
func NewFixture(dir string) *Fixture { return &Fixture{dir: dir} }

// Name identifies the provider in campaign metadata.
func (f *Fixture) Name() string { return "fixture" }

func (f *Fixture) path(r Request) string {
	key := r.FixtureKey
	if key == "" {
		key = r.Stage
	}
	return filepath.Join(f.dir, key+".json")
}

// Complete returns the recorded response for this request.
func (f *Fixture) Complete(_ context.Context, r Request) (json.RawMessage, error) {
	b, err := os.ReadFile(f.path(r))
	if err != nil {
		return nil, fmt.Errorf(
			"llm: no fixture for stage %q at %s — run once with --provider gemini --record to create it",
			r.Stage, f.path(r))
	}
	return json.RawMessage(b), nil
}

// Record writes a response so later runs can replay it.
func (f *Fixture) Record(r Request, out json.RawMessage) error {
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out, "", "  "); err != nil {
		pretty.Reset()
		pretty.Write(out)
	}
	if err := os.WriteFile(f.path(r), pretty.Bytes(), 0o644); err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	return nil
}
```

Add `"bytes"` to that file's imports.

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./internal/llm/ -v`
Expected: PASS, all eight tests

- [ ] **Step 9: Create placeholder prompt files so the embed compiles**

The four real prompts arrive in Tasks 8–11. `go:embed` fails on an empty match, so create stubs now and replace them per stage.

```bash
mkdir -p prompts
for s in profile fit personas creative; do
  printf 'Placeholder. Replaced in the task that implements this stage.\n' > "prompts/$s.md"
  printf '{"type":"object"}\n' > "prompts/$s.schema.json"
done
```

- [ ] **Step 10: Verify the module builds and commit**

Run: `go build ./... && go test ./... && go vet ./...`
Expected: PASS

```bash
git add go.mod go.sum internal/llm prompts
git commit -m "feat: provider interface, schema validation, fixture replay

Fixtures key on stage plus brief rather than on the prompt hash, so
committed demo data survives prompt edits; the disk cache keys on the full
prompt so it correctly goes stale when one changes."
```

---

### Task 7: Gemini provider — rate limiter, disk cache, backoff, schema repair

**Files:**
- Create: `internal/llm/ratelimit.go`, `internal/llm/gemini.go`
- Test: `internal/llm/ratelimit_test.go`, `internal/llm/geminischema_test.go`

**Interfaces:**
- Consumes: `llm.Request`, `llm.CacheKey`, `llm.Validate`, `llm.Fixture` (Task 6)
- Produces:
  - `llm.NewGemini(opts GeminiOptions) (*Gemini, error)`
  - `llm.GeminiOptions{APIKey, Model, CacheDir string; RPM int; Recorder *Fixture}`
  - `llm.NewLimiter(rpm int) *Limiter`, `(*Limiter).Wait(ctx) error`

Two things here are not optional. The free tier is 10 requests per minute on `gemini-2.5-flash`, and stage 5 fires one call per persona concurrently — that burst is exactly what trips a 429, so every call goes through one shared limiter. And `genai.Schema` is not JSON Schema: its `Type` is an uppercase enum, so the schema files need an explicit converter rather than a hopeful `json.Unmarshal`.

- [ ] **Step 1: Write the rate limiter test**

```go
package llm

import (
	"context"
	"testing"
	"time"
)

func TestLimiterSpacesCalls(t *testing.T) {
	l := NewLimiter(600) // 600/min = one per 100ms
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := l.Wait(ctx); err != nil {
			t.Fatalf("Wait: %v", err)
		}
	}
	// First call is free; two more cost ~100ms each.
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("3 calls took %v, want at least 150ms", elapsed)
	}
}

func TestLimiterUnlimitedWhenRPMZero(t *testing.T) {
	l := NewLimiter(0)
	start := time.Now()
	for i := 0; i < 50; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("unlimited limiter took %v", elapsed)
	}
}

func TestLimiterHonorsContextCancellation(t *testing.T) {
	l := NewLimiter(1) // one per minute
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_ = l.Wait(ctx) // first is free
	if err := l.Wait(ctx); err == nil {
		t.Error("want a context error on the second call")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/llm/ -run TestLimiter -v`
Expected: FAIL — `undefined: NewLimiter`

- [ ] **Step 3: Write `internal/llm/ratelimit.go`**

```go
package llm

import (
	"context"
	"sync"
	"time"
)

// Limiter spaces calls to stay inside a requests-per-minute quota. Stage 5
// fires one call per persona concurrently, so a single shared limiter is what
// keeps that burst from tripping the provider's rate limit.
type Limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

// NewLimiter returns a limiter permitting rpm requests per minute. rpm <= 0
// disables limiting.
func NewLimiter(rpm int) *Limiter {
	if rpm <= 0 {
		return &Limiter{}
	}
	return &Limiter{interval: time.Minute / time.Duration(rpm)}
}

// Wait blocks until the caller may issue a request, or until ctx is done.
func (l *Limiter) Wait(ctx context.Context) error {
	if l.interval == 0 {
		return ctx.Err()
	}

	l.mu.Lock()
	now := time.Now()
	slot := l.next
	if slot.Before(now) {
		slot = now
	}
	l.next = slot.Add(l.interval)
	l.mu.Unlock()

	delay := time.Until(slot)
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/llm/ -run TestLimiter -v`
Expected: PASS, all three tests

- [ ] **Step 5: Write the schema-converter test**

```go
package llm

import (
	"encoding/json"
	"testing"

	"google.golang.org/genai"
)

func TestToGenaiSchemaConvertsTypes(t *testing.T) {
	raw := json.RawMessage(`{
	  "type": "object",
	  "properties": {
	    "name": {"type": "string", "description": "who"},
	    "count": {"type": "integer"},
	    "score": {"type": "number"},
	    "ok": {"type": "boolean"},
	    "verdict": {"type": "string", "enum": ["a","b"]},
	    "tags": {"type": "array", "items": {"type": "string"}}
	  },
	  "required": ["name","count"],
	  "additionalProperties": false
	}`)

	got, err := toGenaiSchema(raw)
	if err != nil {
		t.Fatalf("toGenaiSchema: %v", err)
	}
	if got.Type != genai.TypeObject {
		t.Errorf("type = %v, want OBJECT", got.Type)
	}
	if got.Properties["name"].Type != genai.TypeString {
		t.Errorf("name type = %v, want STRING", got.Properties["name"].Type)
	}
	if got.Properties["count"].Type != genai.TypeInteger {
		t.Errorf("count type = %v, want INTEGER", got.Properties["count"].Type)
	}
	if got.Properties["score"].Type != genai.TypeNumber {
		t.Errorf("score type = %v, want NUMBER", got.Properties["score"].Type)
	}
	if got.Properties["ok"].Type != genai.TypeBoolean {
		t.Errorf("ok type = %v, want BOOLEAN", got.Properties["ok"].Type)
	}
	if got.Properties["tags"].Items == nil ||
		got.Properties["tags"].Items.Type != genai.TypeString {
		t.Error("tags items should be STRING")
	}
	if len(got.Properties["verdict"].Enum) != 2 {
		t.Errorf("verdict enum = %v, want 2 values", got.Properties["verdict"].Enum)
	}
	if len(got.Required) != 2 {
		t.Errorf("required = %v, want 2", got.Required)
	}
}

func TestToGenaiSchemaRejectsUnknownType(t *testing.T) {
	if _, err := toGenaiSchema(json.RawMessage(`{"type":"tuple"}`)); err == nil {
		t.Fatal("want error for an unsupported type")
	}
}

// Every shipped schema file must survive conversion, or a stage fails at
// runtime rather than at build time.
func TestShippedSchemasConvert(t *testing.T) {
	for _, stage := range []string{"profile", "fit", "personas", "creative"} {
		_, schema, err := prompts.Load(stage)
		if err != nil {
			t.Fatalf("prompts.Load(%q): %v", stage, err)
		}
		if _, err := toGenaiSchema(schema); err != nil {
			t.Errorf("%s.schema.json does not convert: %v", stage, err)
		}
	}
}
```

Import `"github.com/yashraj/disco/prompts"` in that test file.

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./internal/llm/ -run TestToGenaiSchema -v`
Expected: FAIL — `undefined: toGenaiSchema`

- [ ] **Step 7: Write `internal/llm/gemini.go`**

```go
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/genai"
)

// GeminiOptions configures the live provider.
type GeminiOptions struct {
	APIKey   string
	Model    string // e.g. gemini-2.5-flash
	CacheDir string // "" disables the disk cache
	RPM      int    // provider requests-per-minute quota; 0 disables limiting
	Recorder *Fixture
}

// Gemini calls the Gemini API for structured output, spacing requests to stay
// inside the free-tier quota and caching responses on disk so repeated runs of
// the same brief cost nothing.
type Gemini struct {
	client   *genai.Client
	model    string
	cacheDir string
	limiter  *Limiter
	recorder *Fixture
}

// NewGemini constructs the live provider.
func NewGemini(o GeminiOptions) (*Gemini, error) {
	if o.APIKey == "" {
		return nil, errors.New("llm: GEMINI_API_KEY is not set — " +
			"get a free key at aistudio.google.com, or use --provider fixture")
	}
	c, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  o.APIKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("llm: %w", err)
	}
	return &Gemini{
		client: c, model: o.Model, cacheDir: o.CacheDir,
		limiter: NewLimiter(o.RPM), recorder: o.Recorder,
	}, nil
}

// Name identifies the provider in campaign metadata.
func (g *Gemini) Name() string { return "gemini" }

const (
	maxAttempts  = 3
	backoffBase  = 2 * time.Second
)

// Complete returns schema-valid JSON for the request. On a schema violation it
// retries once with the validator's complaint appended to the prompt; a second
// failure is a hard error rather than a silently defaulted value.
func (g *Gemini) Complete(ctx context.Context, r Request) (json.RawMessage, error) {
	if cached, ok := g.readCache(r); ok {
		return cached, nil
	}

	schema, err := toGenaiSchema(r.Schema)
	if err != nil {
		return nil, fmt.Errorf("llm: stage %s: %w", r.Stage, err)
	}

	prompt := r.Prompt
	var lastErr error

	for repair := 0; repair < 2; repair++ {
		out, err := g.generate(ctx, prompt, schema)
		if err != nil {
			return nil, err
		}
		if err := Validate(r.Schema, out); err == nil {
			g.writeCache(r, out)
			if g.recorder != nil {
				if err := g.recorder.Record(r, out); err != nil {
					return nil, fmt.Errorf("llm: recording fixture: %w", err)
				}
			}
			return out, nil
		} else {
			lastErr = err
			prompt = r.Prompt + "\n\nYour previous response was rejected: " + err.Error() +
				"\nReturn JSON that satisfies the schema exactly."
		}
	}
	return nil, fmt.Errorf("llm: stage %s failed schema validation twice: %w", r.Stage, lastErr)
}

// generate issues one request, waiting for a rate-limit slot and backing off on
// 429 responses.
func (g *Gemini) generate(ctx context.Context, prompt string, schema *genai.Schema) (json.RawMessage, error) {
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := g.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		resp, err := g.client.Models.GenerateContent(ctx, g.model,
			genai.Text(prompt),
			&genai.GenerateContentConfig{
				ResponseMIMEType: "application/json",
				ResponseSchema:   schema,
			})
		if err == nil {
			text := resp.Text()
			if strings.TrimSpace(text) == "" {
				return nil, errors.New("llm: empty response")
			}
			return json.RawMessage(text), nil
		}
		if !isRateLimited(err) || attempt == maxAttempts-1 {
			return nil, fmt.Errorf("llm: %w", err)
		}
		wait := backoffBase * time.Duration(1<<attempt)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("llm: exhausted attempts")
}

func isRateLimited(err error) bool {
	s := err.Error()
	return strings.Contains(s, "429") ||
		strings.Contains(s, "RESOURCE_EXHAUSTED") ||
		strings.Contains(strings.ToLower(s), "rate limit")
}

func (g *Gemini) cachePath(r Request) string {
	return filepath.Join(g.cacheDir, CacheKey(g.Name(), g.model, r)+".json")
}

func (g *Gemini) readCache(r Request) (json.RawMessage, bool) {
	if g.cacheDir == "" {
		return nil, false
	}
	b, err := os.ReadFile(g.cachePath(r))
	if err != nil {
		return nil, false
	}
	return json.RawMessage(b), true
}

func (g *Gemini) writeCache(r Request, out json.RawMessage) {
	if g.cacheDir == "" {
		return
	}
	if err := os.MkdirAll(g.cacheDir, 0o755); err != nil {
		return // the cache is an optimisation; failing to write it is not fatal
	}
	_ = os.WriteFile(g.cachePath(r), out, 0o644)
}

// jsonSchema is the subset of JSON Schema the prompt files use.
type jsonSchema struct {
	Type        string                `json:"type"`
	Description string                `json:"description"`
	Properties  map[string]jsonSchema `json:"properties"`
	Items       *jsonSchema           `json:"items"`
	Required    []string              `json:"required"`
	Enum        []string              `json:"enum"`
}

var genaiTypes = map[string]genai.Type{
	"object":  genai.TypeObject,
	"array":   genai.TypeArray,
	"string":  genai.TypeString,
	"integer": genai.TypeInteger,
	"number":  genai.TypeNumber,
	"boolean": genai.TypeBoolean,
}

// toGenaiSchema converts a JSON Schema document into the SDK's schema type.
// genai.Schema is not JSON Schema — its Type is an uppercase enum — so the
// conversion is explicit rather than a hopeful unmarshal. Schema files are
// restricted to the subset handled here: no $ref, no oneOf, no allOf.
func toGenaiSchema(raw json.RawMessage) (*genai.Schema, error) {
	var js jsonSchema
	if err := json.Unmarshal(raw, &js); err != nil {
		return nil, fmt.Errorf("parsing schema: %w", err)
	}
	return convert(&js)
}

func convert(js *jsonSchema) (*genai.Schema, error) {
	t, ok := genaiTypes[js.Type]
	if !ok {
		return nil, fmt.Errorf("unsupported schema type %q", js.Type)
	}
	out := &genai.Schema{Type: t, Description: js.Description, Required: js.Required}
	if len(js.Enum) > 0 {
		out.Enum = js.Enum
	}
	if len(js.Properties) > 0 {
		out.Properties = make(map[string]*genai.Schema, len(js.Properties))
		for name, sub := range js.Properties {
			s := sub
			c, err := convert(&s)
			if err != nil {
				return nil, fmt.Errorf("property %q: %w", name, err)
			}
			out.Properties[name] = c
		}
	}
	if js.Items != nil {
		c, err := convert(js.Items)
		if err != nil {
			return nil, fmt.Errorf("items: %w", err)
		}
		out.Items = c
	}
	return out, nil
}
```

- [ ] **Step 8: Run the tests**

Run: `go test ./internal/llm/ -v`
Expected: `TestToGenaiSchemaConvertsTypes` and `TestToGenaiSchemaRejectsUnknownType` PASS. `TestShippedSchemasConvert` FAILS against the Task 6 placeholder schemas (`{"type":"object"}` converts fine, so it should actually pass) — if it fails, the placeholder is malformed; fix the placeholder, not the converter.

- [ ] **Step 9: Commit**

```bash
git add internal/llm/gemini.go internal/llm/ratelimit.go internal/llm/ratelimit_test.go internal/llm/geminischema_test.go go.mod go.sum
git commit -m "feat: Gemini provider with rate limiting, disk cache, and schema repair

One shared limiter covers the concurrent stage-5 calls, which are the only
place a burst can exceed the free tier's 10 rpm. genai.Schema is not JSON
Schema, so schema files go through an explicit converter and every shipped
schema is checked at test time rather than failing mid-run."
```

---

### Task 8: Shared stage helper and Stage 1 — profile

**Files:**
- Create: `internal/pipeline/stage.go`, `internal/pipeline/profile.go`
- Replace: `prompts/profile.md`, `prompts/profile.schema.json`
- Test: `internal/pipeline/profile_test.go`
- Create: `evals/fixtures/` (test fixtures written by the test itself)

**Interfaces:**
- Consumes: `llm.Provider`, `llm.Request`, `llm.Validate`, `llm.ShortHash` (Tasks 6–7); `prompts.Load`; `catalog.Catalog`; `model.AdvertiserProfile`
- Produces:
  - `pipeline.Deps{Provider llm.Provider; Catalog *catalog.Catalog}`
  - `pipeline.callStage[T any](ctx, d Deps, stage, fixtureKey string, input any) (T, error)`
  - `pipeline.Profile(ctx context.Context, d Deps, brief string) (model.AdvertiserProfile, error)`

- [ ] **Step 1: Write `internal/pipeline/stage.go`**

```go
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/prompts"
)

// Deps is what every LLM stage needs.
type Deps struct {
	Provider llm.Provider
	Catalog  *catalog.Catalog
}

// callStage renders a stage's prompt with its input appended as JSON, calls the
// provider, validates the response against the stage's schema, and decodes it.
//
// Validation runs here as well as inside the live provider's repair loop, so a
// stale or hand-edited fixture fails as loudly as a bad model response.
func callStage[T any](ctx context.Context, d Deps, stage, fixtureKey string, input any) (T, error) {
	var zero T

	text, schema, err := prompts.Load(stage)
	if err != nil {
		return zero, err
	}
	payload, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return zero, fmt.Errorf("pipeline: %s: encoding input: %w", stage, err)
	}
	prompt := text + "\n\n## Input\n\n```json\n" + string(payload) + "\n```\n"

	out, err := d.Provider.Complete(ctx, llm.Request{
		Stage: stage, Prompt: prompt, Schema: schema, FixtureKey: fixtureKey,
	})
	if err != nil {
		return zero, err
	}
	if err := llm.Validate(schema, out); err != nil {
		return zero, fmt.Errorf("pipeline: %s: %w", stage, err)
	}

	var v T
	if err := json.Unmarshal(out, &v); err != nil {
		return zero, fmt.Errorf("pipeline: %s: decoding response: %w", stage, err)
	}
	return v, nil
}

// fixtureKey names the recorded response for a stage and brief. It excludes the
// prompt so committed fixtures survive prompt edits.
func fixtureKey(stage, brief string) string {
	return stage + "-" + llm.ShortHash(brief)
}
```

- [ ] **Step 2: Write `prompts/profile.md`**

```markdown
# Stage 1 — Advertiser profile

You read one or two sentences from an advertiser describing their business, and
turn them into a structured profile that the rest of an ad-placement pipeline
can reason over.

You are working against a fixed catalog of consumer DTC publishers. The
categories in that catalog are:

`apparel`, `beauty`, `beverages`, `groceries`, `home`, `instant_delivery`,
`meal_kits`, `pet`, `wellness_dtc`, `wellness_services`

## What to produce

- **primary_category** — the catalog category this advertiser belongs in. If
  none fits, say what it actually is (for example `b2b_saas`) rather than
  forcing it into the list.
- **subcategories** — specific product or audience terms, lowercase with
  underscores, for example `pet_food`, `activewear`, `women`.
- **price_tier** — `budget`, `mid`, `premium`, or `luxury`.
- **estimated_aov_usd** — typical single order value in dollars. Infer it from
  any stated price. A $650 jacket is not a $650 order value only if the brief
  says otherwise.
- **target_age_min / target_age_max** — the age band that actually buys this.
  Be specific; a range of 18-75 is not an answer.
- **target_gender_skew** — `female`, `male`, `balanced`, or `unknown`.
- **values** — zero or more of: `sustainability`, `craftsmanship`,
  `science_backed`, `convenience`, `value`, `aesthetic`. Only include one the
  brief actually supports.
- **business_model** — `subscription`, `one_off`, `b2b`, or `service`.
- **is_consumer_dtc** — false when this business does not sell to individual
  consumers. B2B software, wholesale, and professional services are all false.
  This matters: the catalog cannot serve them, and the honest answer is to say
  so rather than to pick the closest-looking publisher.
- **confidence** — `high`, `medium`, or `low`.
  - `high`: the category, rough price, and buyer are all clear.
  - `medium`: one of those is a reasonable inference rather than stated.
  - `low`: the brief is too vague to place. "We help people feel better" and
    "idk just try it" are `low`.
- **assumptions** — every inference you made that the brief did not state.
- **missing_signals** — what you would need to ask about. At `low` confidence
  this must not be empty.

## Rules

- Guess, but label the guess. An unlabelled inference is worse than a question.
- Do not inflate confidence to seem useful. A vague brief handled honestly is a
  better outcome than a confident campaign built on nothing.
- Never invent a price point the brief does not support.
```

- [ ] **Step 3: Write `prompts/profile.schema.json`**

```json
{
  "type": "object",
  "description": "Structured advertiser profile derived from a free-text brief.",
  "properties": {
    "primary_category": { "type": "string" },
    "subcategories": { "type": "array", "items": { "type": "string" } },
    "price_tier": { "type": "string", "enum": ["budget", "mid", "premium", "luxury"] },
    "estimated_aov_usd": { "type": "integer" },
    "target_age_min": { "type": "integer" },
    "target_age_max": { "type": "integer" },
    "target_gender_skew": { "type": "string", "enum": ["female", "male", "balanced", "unknown"] },
    "values": { "type": "array", "items": { "type": "string" } },
    "business_model": { "type": "string", "enum": ["subscription", "one_off", "b2b", "service"] },
    "is_consumer_dtc": { "type": "boolean" },
    "confidence": { "type": "string", "enum": ["high", "medium", "low"] },
    "assumptions": { "type": "array", "items": { "type": "string" } },
    "missing_signals": { "type": "array", "items": { "type": "string" } }
  },
  "required": [
    "primary_category", "subcategories", "price_tier", "estimated_aov_usd",
    "target_age_min", "target_age_max", "target_gender_skew", "values",
    "business_model", "is_consumer_dtc", "confidence", "assumptions", "missing_signals"
  ]
}
```

- [ ] **Step 4: Write the failing test**

```go
package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yashraj/disco/internal/llm"
)

// fixtureDeps builds a pipeline that replays canned responses from a temp dir.
func fixtureDeps(t *testing.T, stage, brief string, response any) Deps {
	t.Helper()
	dir := t.TempDir()
	f := llm.NewFixture(dir)

	b, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := f.Record(llm.Request{Stage: stage, FixtureKey: fixtureKey(stage, brief)}, b); err != nil {
		t.Fatalf("record fixture: %v", err)
	}
	return Deps{Provider: f, Catalog: loadCatalog(t)}
}

func TestProfileDecodesAndStampsBrief(t *testing.T) {
	brief := "We sell premium dog food for senior dogs, vet-formulated, subscription."
	d := fixtureDeps(t, "profile", brief, map[string]any{
		"primary_category":   "pet",
		"subcategories":      []string{"pet_food", "subscription"},
		"price_tier":         "premium",
		"estimated_aov_usd":  70,
		"target_age_min":     30,
		"target_age_max":     55,
		"target_gender_skew": "balanced",
		"values":             []string{"science_backed"},
		"business_model":     "subscription",
		"is_consumer_dtc":    true,
		"confidence":         "high",
		"assumptions":        []string{"subscription implies repeat purchase"},
		"missing_signals":    []string{},
	})

	got, err := Profile(context.Background(), d, brief)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if got.PrimaryCategory != "pet" {
		t.Errorf("category = %q, want pet", got.PrimaryCategory)
	}
	if got.RawBrief != brief {
		t.Errorf("RawBrief = %q, want the original brief", got.RawBrief)
	}
	if !got.IsConsumerDTC {
		t.Error("IsConsumerDTC = false, want true")
	}
}

// A response missing a required field must fail loudly rather than decode into
// a zero-valued profile that silently drives the rest of the pipeline.
func TestProfileRejectsSchemaViolation(t *testing.T) {
	brief := "anything"
	d := fixtureDeps(t, "profile", brief, map[string]any{"primary_category": "pet"})
	if _, err := Profile(context.Background(), d, brief); err == nil {
		t.Fatal("want a schema validation error")
	}
}

func TestProfileLowConfidenceCarriesMissingSignals(t *testing.T) {
	brief := "idk just try it"
	d := fixtureDeps(t, "profile", brief, map[string]any{
		"primary_category": "unknown", "subcategories": []string{},
		"price_tier": "mid", "estimated_aov_usd": 0,
		"target_age_min": 25, "target_age_max": 55,
		"target_gender_skew": "unknown", "values": []string{},
		"business_model": "one_off", "is_consumer_dtc": true,
		"confidence":  "low",
		"assumptions": []string{"assumed a consumer product"},
		"missing_signals": []string{"what is being sold", "price point", "who buys it"},
	})

	got, err := Profile(context.Background(), d, brief)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if got.Confidence != "low" {
		t.Errorf("confidence = %q, want low", got.Confidence)
	}
	if len(got.MissingSignals) == 0 {
		t.Error("a low-confidence profile must name what it is missing")
	}
}
```

- [ ] **Step 5: Run it to verify it fails**

Run: `go test ./internal/pipeline/ -run TestProfile -v`
Expected: FAIL — `undefined: Profile`

- [ ] **Step 6: Write `internal/pipeline/profile.go`**

```go
package pipeline

import (
	"context"
	"strings"

	"github.com/yashraj/disco/internal/model"
)

// Profile is stage 1. It turns the advertiser's free-text brief into a
// structured profile whose Confidence and IsConsumerDTC fields gate every
// stage after it.
func Profile(ctx context.Context, d Deps, brief string) (model.AdvertiserProfile, error) {
	p, err := callStage[model.AdvertiserProfile](ctx, d, "profile",
		fixtureKey("profile", brief),
		map[string]any{
			"brief":              brief,
			"catalog_categories": catalogCategories(d),
		})
	if err != nil {
		return model.AdvertiserProfile{}, err
	}

	p.RawBrief = brief
	p.PrimaryCategory = strings.ToLower(strings.TrimSpace(p.PrimaryCategory))
	for i, s := range p.Subcategories {
		p.Subcategories[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return p, nil
}

// catalogCategories lists the distinct publisher categories, so stage 1 lands
// its primary_category in the same vocabulary stage 2 scores against.
func catalogCategories(d Deps) []string {
	seen := map[string]bool{}
	for i := range d.Catalog.Publishers {
		seen[d.Catalog.Publishers[i].Category] = true
	}
	return sortedKeys(seen)
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/pipeline/ -run TestProfile -v`
Expected: PASS, all three tests

- [ ] **Step 8: Run the whole suite and commit**

Run: `go test ./... && go vet ./...`

```bash
git add internal/pipeline/stage.go internal/pipeline/profile.go internal/pipeline/profile_test.go prompts/profile.md prompts/profile.schema.json
git commit -m "feat: stage 1 profile extraction and the shared stage helper

Responses are validated against the stage schema in the helper as well as
in the live provider's repair loop, so a stale fixture fails as loudly as a
bad model response rather than decoding into a zero value."
```

---

### Task 9: Stage 3 — publisher fit verdicts

**Files:**
- Create: `internal/pipeline/fit.go`
- Replace: `prompts/fit.md`, `prompts/fit.schema.json`
- Test: `internal/pipeline/fit_test.go`

**Interfaces:**
- Consumes: `pipeline.Deps`, `callStage`, `fixtureKey` (Task 8); `model.PublisherScore`, `model.FitVerdict`, `model.AdvertiserProfile`
- Produces: `pipeline.Fit(ctx context.Context, d Deps, p model.AdvertiserProfile, scores []model.PublisherScore) ([]model.FitVerdict, error)` — always returns one verdict per catalog publisher

- [ ] **Step 1: Write `prompts/fit.md`**

```markdown
# Stage 3 — Publisher fit

You are given an advertiser profile and every publisher in the catalog, each
with a deterministic fit score and its sub-scores. Your job is to assign each
publisher a verdict and write the reason a human would want to read.

## Verdicts

- **recommended** — worth spending money on. Assign `rank` starting at 1.
  Recommend between 2 and 6 publishers when any are suitable.
- **considered** — plausible but not funded. A real near-miss.
- **excluded** — not a fit.

## Rules

1. **Return a verdict for every publisher in the input. All of them.** A
   publisher you leave out is a recommendation a human cannot audit.
2. **A publisher with a non-empty `hard_gate` is always `excluded`.** You may
   write a better reason than the gate's, but you may not promote it. The gates
   mean: `not_consumer_dtc` — this catalog cannot reach this advertiser's buyers
   at all; `category_mismatch` — no category or subcategory overlap;
   `demographic_mismatch` — the audience age bands do not overlap.
3. The `score` is a starting point, not a verdict. You see each publisher's
   `notes`, which the score only crudely approximates. Promote or demote against
   the score when the notes justify it, and say so in the reason.
4. **Reasons must be specific to this pairing.** "Good audience fit" is not a
   reason. "Subscription-heavy pet buyers who already pay a premium for health
   positioning" is.
5. When you exclude, say what specifically disqualifies it — the age band, the
   order value gap, the category distance — not that it "scored low".
6. If no publisher is suitable, recommend none. An empty recommendation with
   honest reasons beats a confident bad placement.
```

- [ ] **Step 2: Write `prompts/fit.schema.json`**

```json
{
  "type": "object",
  "description": "A verdict for every publisher in the catalog.",
  "properties": {
    "verdicts": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "publisher_id": { "type": "string" },
          "verdict": { "type": "string", "enum": ["recommended", "considered", "excluded"] },
          "rank": { "type": "integer", "description": "1-based, recommended only; 0 otherwise" },
          "reason": { "type": "string" }
        },
        "required": ["publisher_id", "verdict", "rank", "reason"]
      }
    }
  },
  "required": ["verdicts"]
}
```

- [ ] **Step 3: Write the failing test**

```go
package pipeline

import (
	"context"
	"testing"

	"github.com/yashraj/disco/internal/model"
)

func TestFitReturnsOneVerdictPerPublisher(t *testing.T) {
	brief := "premium dog food"
	c := loadCatalog(t)
	scores := ScoreAll(dogFood(), c)

	var vs []map[string]any
	for i, s := range scores {
		verdict, rank := "excluded", 0
		if i < 3 && s.HardGate == GateNone {
			verdict, rank = "recommended", i+1
		}
		vs = append(vs, map[string]any{
			"publisher_id": s.PublisherID, "verdict": verdict,
			"rank": rank, "reason": "because"})
	}
	d := fixtureDeps(t, "fit", brief, map[string]any{"verdicts": vs})
	d.Catalog = c

	got, err := Fit(context.Background(), d, withBrief(dogFood(), brief), scores)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if len(got) != len(c.Publishers) {
		t.Fatalf("got %d verdicts, want %d", len(got), len(c.Publishers))
	}
}

// The model does not get to promote a hard-gated publisher, whatever it returns.
func TestFitCannotPromoteAHardGatedPublisher(t *testing.T) {
	brief := "sustainable activewear for women"
	c := loadCatalog(t)
	scores := ScoreAll(activewear(), c)

	var vs []map[string]any
	for _, s := range scores {
		// Deliberately try to recommend everything, including gated publishers.
		vs = append(vs, map[string]any{
			"publisher_id": s.PublisherID, "verdict": "recommended",
			"rank": 1, "reason": "model says yes"})
	}
	d := fixtureDeps(t, "fit", brief, map[string]any{"verdicts": vs})
	d.Catalog = c

	got, err := Fit(context.Background(), d, withBrief(activewear(), brief), scores)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	byID := map[string]model.FitVerdict{}
	for _, v := range got {
		byID[v.PublisherID] = v
	}
	if v := byID["pub_005"]; v.Verdict != "excluded" { // Linden Park, age-gated
		t.Errorf("gated publisher verdict = %q, want excluded", v.Verdict)
	}
}

// An invented publisher ID must not reach the campaign config.
func TestFitDropsUnknownPublisherIDs(t *testing.T) {
	brief := "premium dog food"
	c := loadCatalog(t)
	scores := ScoreAll(dogFood(), c)

	vs := []map[string]any{
		{"publisher_id": "pub_999", "verdict": "recommended", "rank": 1, "reason": "invented"},
	}
	for _, s := range scores {
		vs = append(vs, map[string]any{
			"publisher_id": s.PublisherID, "verdict": "considered", "rank": 0, "reason": "ok"})
	}
	d := fixtureDeps(t, "fit", brief, map[string]any{"verdicts": vs})
	d.Catalog = c

	got, err := Fit(context.Background(), d, withBrief(dogFood(), brief), scores)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	for _, v := range got {
		if v.PublisherID == "pub_999" {
			t.Error("pub_999 is not in the catalog and must be dropped")
		}
	}
}

// A publisher the model forgot still gets an entry, so the ledger stays complete.
func TestFitBackfillsOmittedPublishers(t *testing.T) {
	brief := "premium dog food"
	c := loadCatalog(t)
	scores := ScoreAll(dogFood(), c)

	vs := []map[string]any{
		{"publisher_id": scores[0].PublisherID, "verdict": "recommended", "rank": 1, "reason": "ok"},
	}
	d := fixtureDeps(t, "fit", brief, map[string]any{"verdicts": vs})
	d.Catalog = c

	got, err := Fit(context.Background(), d, withBrief(dogFood(), brief), scores)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if len(got) != len(c.Publishers) {
		t.Fatalf("got %d verdicts, want %d", len(got), len(c.Publishers))
	}
	for _, v := range got {
		if v.Reason == "" {
			t.Errorf("%s has no reason", v.PublisherID)
		}
	}
}

func withBrief(p model.AdvertiserProfile, brief string) model.AdvertiserProfile {
	p.RawBrief = brief
	return p
}
```

- [ ] **Step 4: Run it to verify it fails**

Run: `go test ./internal/pipeline/ -run TestFit -v`
Expected: FAIL — `undefined: Fit`

- [ ] **Step 5: Write `internal/pipeline/fit.go`**

```go
package pipeline

import (
	"context"

	"github.com/yashraj/disco/internal/model"
)

type fitResponse struct {
	Verdicts []model.FitVerdict `json:"verdicts"`
}

// Fit is stage 3. It hands the model every publisher with its deterministic
// sub-scores and gate status, and takes back a verdict and a written reason for
// each.
//
// Three things are enforced here rather than trusted to the prompt: a hard gate
// is never overridable, an unknown publisher ID is dropped, and a publisher the
// model omitted is backfilled — so the ledger always covers the whole catalog.
func Fit(ctx context.Context, d Deps, p model.AdvertiserProfile, scores []model.PublisherScore) ([]model.FitVerdict, error) {
	type pubView struct {
		ID                 string          `json:"id"`
		Name               string          `json:"name"`
		Category           string          `json:"category"`
		Subcategories      []string        `json:"subcategories"`
		MonthlyImpressions int64           `json:"monthly_impressions"`
		AvgOrderValueUSD   int             `json:"avg_order_value_usd"`
		Audience           any             `json:"audience"`
		Notes              string          `json:"notes"`
		Score              float64         `json:"score"`
		SubScores          model.SubScores `json:"sub_scores"`
		HardGate           string          `json:"hard_gate"`
	}

	scoreByID := make(map[string]model.PublisherScore, len(scores))
	for _, s := range scores {
		scoreByID[s.PublisherID] = s
	}

	views := make([]pubView, 0, len(d.Catalog.Publishers))
	for i := range d.Catalog.Publishers {
		pub := &d.Catalog.Publishers[i]
		s := scoreByID[pub.ID]
		views = append(views, pubView{
			ID: pub.ID, Name: pub.Name, Category: pub.Category,
			Subcategories: pub.Subcategories, MonthlyImpressions: pub.MonthlyImpressions,
			AvgOrderValueUSD: pub.AvgOrderValueUSD, Audience: pub.Audience, Notes: pub.Notes,
			Score: s.Score, SubScores: s.Sub, HardGate: s.HardGate,
		})
	}

	resp, err := callStage[fitResponse](ctx, d, "fit", fixtureKey("fit", p.RawBrief),
		map[string]any{"advertiser": p, "publishers": views})
	if err != nil {
		return nil, err
	}

	got := make(map[string]model.FitVerdict, len(resp.Verdicts))
	for _, v := range resp.Verdicts {
		if !d.Catalog.HasPublisher(v.PublisherID) {
			continue // invented ID
		}
		got[v.PublisherID] = v
	}

	out := make([]model.FitVerdict, 0, len(d.Catalog.Publishers))
	for i := range d.Catalog.Publishers {
		id := d.Catalog.Publishers[i].ID
		v, ok := got[id]
		if !ok {
			v = model.FitVerdict{PublisherID: id, Verdict: "excluded",
				Reason: "Not selected; no specific fit identified for this advertiser."}
		}
		if gate := scoreByID[id].HardGate; gate != GateNone {
			v.Verdict = "excluded"
			v.Rank = 0
			if v.Reason == "" {
				v.Reason = gateReason(gate)
			}
		}
		v.PublisherID = id
		out = append(out, v)
	}
	return out, nil
}
```

- [ ] **Step 6: Run the tests and commit**

Run: `go test ./internal/pipeline/ -run TestFit -v`
Expected: PASS, all four tests

```bash
git add internal/pipeline/fit.go internal/pipeline/fit_test.go prompts/fit.md prompts/fit.schema.json
git commit -m "feat: stage 3 publisher fit verdicts

Hard gates, unknown IDs, and omitted publishers are handled in code rather
than trusted to the prompt, so the ledger covers the whole catalog on every
run regardless of what the model returns."
```

---

### Task 10: Stage 4 — persona selection

**Files:**
- Create: `internal/pipeline/personas.go`
- Replace: `prompts/personas.md`, `prompts/personas.schema.json`
- Test: `internal/pipeline/personas_test.go`

**Interfaces:**
- Consumes: `pipeline.Deps`, `callStage`, `fixtureKey`; `model.PersonaSelection`, `model.PersonaPick`, `model.PersonaRejection`
- Produces: `pipeline.Personas(ctx context.Context, d Deps, p model.AdvertiserProfile, verdicts []model.FitVerdict) (model.PersonaSelection, error)` — between 3 and 5 selected personas, IDs guaranteed to exist

- [ ] **Step 1: Write `prompts/personas.md`**

```markdown
# Stage 4 — Persona selection

You are given an advertiser profile, the publishers recommended for them, and a
fixed set of ten shopper personas. Choose the 3 to 5 personas whose ads should
be written for, and say why each one — and why the others were passed over.

## Rules

1. Choose **at least 3 and at most 5** personas. Use only persona IDs from the
   input.
2. Pick personas who plausibly shop on the **recommended publishers**, not
   personas who merely like the product category in the abstract.
3. `primary_publishers` lists which of the recommended publishers each persona
   is most likely to be reached on. Use publisher IDs from the input.
4. Rationale must reference something concrete from the persona record — a
   category affinity, a price sensitivity, a messaging preference — and connect
   it to something concrete about this advertiser.
5. Give a reason for **every** persona you did not choose. "The Gifter buys for
   others and this is a subscription, which the record explicitly lists as a
   disinterest" is a reason. "Not a fit" is not.
6. Personas are overlapping lenses, not exclusive segments. Prefer a set that
   covers genuinely different buying motivations over three variations on one.
```

- [ ] **Step 2: Write `prompts/personas.schema.json`**

```json
{
  "type": "object",
  "description": "Selected shopper personas and the reasons the rest were passed over.",
  "properties": {
    "selected": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "persona_id": { "type": "string" },
          "rationale": { "type": "string" },
          "primary_publishers": { "type": "array", "items": { "type": "string" } }
        },
        "required": ["persona_id", "rationale", "primary_publishers"]
      }
    },
    "rejected": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "persona_id": { "type": "string" },
          "reason": { "type": "string" }
        },
        "required": ["persona_id", "reason"]
      }
    }
  },
  "required": ["selected", "rejected"]
}
```

- [ ] **Step 3: Write the failing test**

```go
package pipeline

import (
	"context"
	"testing"
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
}

func TestPersonasCapsAtFive(t *testing.T) {
	brief := "premium dog food"
	d := fixtureDeps(t, "personas", brief, personaFixture([]string{
		"persona_001", "persona_002", "persona_004", "persona_005",
		"persona_006", "persona_008", "persona_009"}))

	got, err := Personas(context.Background(), d, withBrief(dogFood(), brief), nil)
	if err != nil {
		t.Fatalf("Personas: %v", err)
	}
	if len(got.Selected) > 5 {
		t.Errorf("got %d personas, want at most 5", len(got.Selected))
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
```

- [ ] **Step 4: Run it to verify it fails**

Run: `go test ./internal/pipeline/ -run TestPersonas -v`
Expected: FAIL — `undefined: Personas`

- [ ] **Step 5: Write `internal/pipeline/personas.go`**

```go
package pipeline

import (
	"context"
	"fmt"

	"github.com/yashraj/disco/internal/model"
)

const (
	minPersonas = 3
	maxPersonas = 5
)

// Personas is stage 4. It selects the 3 to 5 shopper personas the creatives
// will be written for, and records why the rest were passed over.
//
// The count bounds and the ID allow-list are enforced here. A short or invented
// selection would otherwise surface as a campaign with too few creatives.
func Personas(ctx context.Context, d Deps, p model.AdvertiserProfile, verdicts []model.FitVerdict) (model.PersonaSelection, error) {
	var recommended []map[string]any
	for _, v := range verdicts {
		if v.Verdict != "recommended" {
			continue
		}
		if pub, ok := d.Catalog.Publisher(v.PublisherID); ok {
			recommended = append(recommended, map[string]any{
				"id": pub.ID, "name": pub.Name, "category": pub.Category,
				"subcategories": pub.Subcategories, "notes": pub.Notes,
				"audience": pub.Audience, "reason": v.Reason,
			})
		}
	}

	sel, err := callStage[model.PersonaSelection](ctx, d, "personas",
		fixtureKey("personas", p.RawBrief),
		map[string]any{
			"advertiser":               p,
			"recommended_publishers":   recommended,
			"personas":                 d.Catalog.Personas,
		})
	if err != nil {
		return model.PersonaSelection{}, err
	}

	seen := make(map[string]bool, len(sel.Selected))
	kept := make([]model.PersonaPick, 0, maxPersonas)
	for _, pick := range sel.Selected {
		if !d.Catalog.HasPersona(pick.PersonaID) || seen[pick.PersonaID] {
			continue
		}
		seen[pick.PersonaID] = true
		pick.PrimaryPublishers = filterPublisherIDs(d, pick.PrimaryPublishers)
		kept = append(kept, pick)
		if len(kept) == maxPersonas {
			break
		}
	}
	if len(kept) < minPersonas {
		return model.PersonaSelection{}, fmt.Errorf(
			"pipeline: personas: %d usable personas, want at least %d", len(kept), minPersonas)
	}

	rejected := make([]model.PersonaRejection, 0, len(sel.Rejected))
	for _, r := range sel.Rejected {
		if d.Catalog.HasPersona(r.PersonaID) && !seen[r.PersonaID] {
			rejected = append(rejected, r)
		}
	}
	return model.PersonaSelection{Selected: kept, Rejected: rejected}, nil
}

// filterPublisherIDs removes any ID that is not in the catalog.
func filterPublisherIDs(d Deps, ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if d.Catalog.HasPublisher(id) {
			out = append(out, id)
		}
	}
	return out
}
```

- [ ] **Step 6: Run the tests and commit**

Run: `go test ./internal/pipeline/ -run TestPersonas -v`
Expected: PASS, all four tests

```bash
git add internal/pipeline/personas.go internal/pipeline/personas_test.go prompts/personas.md prompts/personas.schema.json
git commit -m "feat: stage 4 persona selection

Count bounds and the persona allow-list are enforced in code. Fewer than
three usable picks is an error rather than a quietly short campaign, since
the brief asks for 3 to 5 creative variants."
```

---

### Task 11: Stage 5 — creative generation, one call per persona

**Files:**
- Create: `internal/pipeline/creative.go`
- Replace: `prompts/creative.md`, `prompts/creative.schema.json`
- Test: `internal/pipeline/creative_test.go`

**Interfaces:**
- Consumes: `pipeline.Deps`, `callStage`, `fixtureKey`; `model.Creative`, `model.PersonaSelection`; `golang.org/x/sync/errgroup`
- Produces: `pipeline.Creatives(ctx context.Context, d Deps, p model.AdvertiserProfile, sel model.PersonaSelection) ([]model.Creative, error)` — one creative per selected persona, returned in selection order

One call per persona, concurrently. Each call sees exactly one persona record, so the copy cannot blur across audiences — which is what happens when a single call is asked for five variants at once. The shared rate limiter from Task 7 is what keeps the burst inside the free tier.

- [ ] **Step 1: Write `prompts/creative.md`**

```markdown
# Stage 5 — Ad creative for one persona

You write one ad — a headline and body — for exactly one shopper persona. You
are given the advertiser's profile, the persona's full record, and the
publishers this persona is likely to be reached on.

## Constraints

- **headline**: 60 characters or fewer. Hard limit.
- **body**: 200 characters or fewer. Hard limit.
- **messaging_levers**: the entries from this persona's `messaging_preferences`
  that your copy actually uses. At least one, and only ones that appear
  verbatim in the persona record.
- **avoided**: the entries from this persona's `disinterested_in` that you
  deliberately steered around.
- **rationale**: why this copy is right for this persona specifically.

## Rules

1. Write for **this** persona. If the same copy would work for a different
   persona in the set, it is too generic — rewrite it.
2. `messaging_preferences` is your brief, not decoration. A persona whose
   preferences are "science-backed claims, ingredient transparency" should get
   copy carrying a claim and an ingredient, not a mood.
3. `disinterested_in` is a prohibition. A persona disinterested in "luxury
   positioning" must not be sold to with the word "indulge".
4. Respect `price_sensitivity`. A high-sensitivity persona wants the value made
   explicit; a low-sensitivity one does not want to hear about discounts.
5. No invented facts. No statistics, awards, endorsements, or claims the
   advertiser's brief does not support.
6. Write like a person. No em-dash-and-colon ad-speak, no "Introducing", no
   stacked rhetorical questions.
```

- [ ] **Step 2: Write `prompts/creative.schema.json`**

```json
{
  "type": "object",
  "description": "One ad creative written for a single shopper persona.",
  "properties": {
    "headline": { "type": "string", "description": "60 characters or fewer" },
    "body": { "type": "string", "description": "200 characters or fewer" },
    "rationale": { "type": "string" },
    "messaging_levers": { "type": "array", "items": { "type": "string" } },
    "avoided": { "type": "array", "items": { "type": "string" } }
  },
  "required": ["headline", "body", "rationale", "messaging_levers", "avoided"]
}
```

- [ ] **Step 3: Write the failing test**

```go
package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/model"
)

// multiFixtureDeps records one response per persona.
func multiFixtureDeps(t *testing.T, brief string, byPersona map[string]any) Deps {
	t.Helper()
	f := llm.NewFixture(t.TempDir())
	for personaID, resp := range byPersona {
		b, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		key := fixtureKey("creative", brief+"|"+personaID)
		if err := f.Record(llm.Request{Stage: "creative", FixtureKey: key}, b); err != nil {
			t.Fatal(err)
		}
	}
	return Deps{Provider: f, Catalog: loadCatalog(t)}
}

func creativeBody(levers []string) map[string]any {
	return map[string]any{
		"headline": "Joint support your senior dog can feel",
		"body":     "Vet-formulated, grain-free meals built for older dogs. Delivered monthly.",
		"rationale": "Leads with the vet formulation this persona screens for.",
		"messaging_levers": levers,
		"avoided":          []string{"generic pet brands"},
	}
}

func selection(ids ...string) model.PersonaSelection {
	var s model.PersonaSelection
	for _, id := range ids {
		s.Selected = append(s.Selected, model.PersonaPick{PersonaID: id, Rationale: "r"})
	}
	return s
}

func TestCreativesOnePerPersonaInOrder(t *testing.T) {
	brief := "premium dog food"
	sel := selection("persona_004", "persona_002", "persona_001")
	d := multiFixtureDeps(t, brief, map[string]any{
		"persona_004": creativeBody([]string{"vet-recommended"}),
		"persona_002": creativeBody([]string{"trusted by parents"}),
		"persona_001": creativeBody([]string{"science-backed claims"}),
	})

	got, err := Creatives(context.Background(), d, withBrief(dogFood(), brief), sel)
	if err != nil {
		t.Fatalf("Creatives: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d creatives, want 3", len(got))
	}
	for i, want := range []string{"persona_004", "persona_002", "persona_001"} {
		if got[i].PersonaID != want {
			t.Errorf("creative[%d] persona = %s, want %s", i, got[i].PersonaID, want)
		}
	}
}

func TestCreativesEnforceLengthLimits(t *testing.T) {
	brief := "premium dog food"
	long := creativeBody([]string{"vet-recommended"})
	long["headline"] = strings.Repeat("x", 90)
	long["body"] = strings.Repeat("y", 300)

	d := multiFixtureDeps(t, brief, map[string]any{
		"persona_004": long,
		"persona_002": creativeBody([]string{"trusted by parents"}),
		"persona_001": creativeBody([]string{"science-backed claims"}),
	})

	got, err := Creatives(context.Background(), d,
		withBrief(dogFood(), brief), selection("persona_004", "persona_002", "persona_001"))
	if err != nil {
		t.Fatalf("Creatives: %v", err)
	}
	for _, c := range got {
		if len([]rune(c.Headline)) > 60 {
			t.Errorf("%s headline is %d runes, want <= 60", c.PersonaID, len([]rune(c.Headline)))
		}
		if len([]rune(c.Body)) > 200 {
			t.Errorf("%s body is %d runes, want <= 200", c.PersonaID, len([]rune(c.Body)))
		}
	}
}

// A lever the persona record does not actually contain is dropped: the field
// exists to prove the copy is grounded, so an unverifiable entry is worthless.
func TestCreativesDropUngroundedMessagingLevers(t *testing.T) {
	brief := "premium dog food"
	d := multiFixtureDeps(t, brief, map[string]any{
		"persona_004": creativeBody([]string{"vet-recommended", "invented lever"}),
		"persona_002": creativeBody([]string{"trusted by parents"}),
		"persona_001": creativeBody([]string{"science-backed claims"}),
	})

	got, err := Creatives(context.Background(), d,
		withBrief(dogFood(), brief), selection("persona_004", "persona_002", "persona_001"))
	if err != nil {
		t.Fatalf("Creatives: %v", err)
	}
	for _, c := range got {
		if c.PersonaID != "persona_004" {
			continue
		}
		for _, l := range c.MessagingLevers {
			if l == "invented lever" {
				t.Error("an ungrounded messaging lever must be dropped")
			}
		}
		if len(c.MessagingLevers) == 0 {
			t.Error("the grounded lever should have survived")
		}
	}
}
```

- [ ] **Step 4: Run it to verify it fails**

Run: `go test ./internal/pipeline/ -run TestCreatives -v`
Expected: FAIL — `undefined: Creatives`

- [ ] **Step 5: Write `internal/pipeline/creative.go`**

```go
package pipeline

import (
	"context"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
)

const (
	maxHeadlineRunes = 60
	maxBodyRunes     = 200
)

// Creatives is stage 5. It writes one ad per selected persona, concurrently.
//
// Each call sees exactly one persona record. Asking a single call for five
// variants produces five paraphrases of one idea; isolating the persona is what
// makes the copy actually differ. The provider's shared rate limiter is what
// keeps this burst inside the free-tier quota.
func Creatives(ctx context.Context, d Deps, p model.AdvertiserProfile, sel model.PersonaSelection) ([]model.Creative, error) {
	out := make([]model.Creative, len(sel.Selected))

	g, gctx := errgroup.WithContext(ctx)
	for i, pick := range sel.Selected {
		i, pick := i, pick
		g.Go(func() error {
			persona, ok := d.Catalog.Persona(pick.PersonaID)
			if !ok {
				return nil // filtered upstream; nothing to write
			}

			var pubs []map[string]any
			for _, id := range pick.PrimaryPublishers {
				if pub, ok := d.Catalog.Publisher(id); ok {
					pubs = append(pubs, map[string]any{
						"id": pub.ID, "name": pub.Name,
						"category": pub.Category, "notes": pub.Notes,
					})
				}
			}

			c, err := callStage[model.Creative](gctx, d, "creative",
				fixtureKey("creative", p.RawBrief+"|"+pick.PersonaID),
				map[string]any{
					"advertiser":          p,
					"persona":             persona,
					"persona_rationale":   pick.Rationale,
					"primary_publishers":  pubs,
				})
			if err != nil {
				return err
			}

			c.PersonaID = pick.PersonaID
			c.SuggestedPublishers = pick.PrimaryPublishers
			c.Headline = truncateRunes(c.Headline, maxHeadlineRunes)
			c.Body = truncateRunes(c.Body, maxBodyRunes)
			c.MessagingLevers = groundedIn(c.MessagingLevers, persona.MessagingPreferences)
			c.Avoided = groundedIn(c.Avoided, persona.DisinterestedIn)

			out[i] = c
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	kept := make([]model.Creative, 0, len(out))
	for _, c := range out {
		if c.PersonaID != "" {
			kept = append(kept, c)
		}
	}
	return kept, nil
}

// groundedIn keeps only the claims that appear in the persona's own record.
// The field exists to prove the copy is grounded, so an entry that cannot be
// checked against the record is worth nothing and is dropped.
func groundedIn(claimed, actual []string) []string {
	have := make(map[string]bool, len(actual))
	for _, a := range actual {
		have[strings.ToLower(strings.TrimSpace(a))] = true
	}
	out := make([]string, 0, len(claimed))
	for _, c := range claimed {
		if have[strings.ToLower(strings.TrimSpace(c))] {
			out = append(out, c)
		}
	}
	return out
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}

var _ = catalog.Persona{} // keep the import explicit for readers of this file
```

Drop that last line if `catalog` is already referenced; it is there only to make the dependency obvious and `go vet` will flag it as unnecessary if so.

- [ ] **Step 6: Run the tests and commit**

Run: `go test ./internal/pipeline/ -run TestCreatives -v && go test ./... && go vet ./...`
Expected: PASS

```bash
git add internal/pipeline/creative.go internal/pipeline/creative_test.go prompts/creative.md prompts/creative.schema.json
git commit -m "feat: stage 5 creative generation, one call per persona

Isolating each persona in its own call is what makes the variants actually
differ; a single call asked for five produces five paraphrases of one idea.
Messaging levers are checked against the persona record and dropped when
they are not in it."
```

---

### Task 12: Orchestrator and degraded modes

**Files:**
- Create: `internal/pipeline/run.go`
- Test: `internal/pipeline/run_test.go`

**Interfaces:**
- Consumes: every stage from Tasks 2–11
- Produces:
  - `pipeline.Options{Provider llm.Provider; Catalog *catalog.Catalog; Params AllocParams; Model string}`
  - `pipeline.Run(ctx context.Context, brief string, o Options) (model.Campaign, error)`

The short-circuit matters more than the happy path. A brief the catalog cannot serve must stop after stage 2 — running persona selection and creative generation for a dental-software company would burn quota to produce an answer that should not exist.

- [ ] **Step 1: Write the failing test**

```go
package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/model"
)

// countingProvider wraps a fixture provider and records which stages ran.
type countingProvider struct {
	inner  llm.Provider
	stages []string
}

func (c *countingProvider) Name() string { return "counting" }
func (c *countingProvider) Complete(ctx context.Context, r llm.Request) (json.RawMessage, error) {
	c.stages = append(c.stages, r.Stage)
	return c.inner.Complete(ctx, r)
}
func (c *countingProvider) ran(stage string) bool {
	for _, s := range c.stages {
		if s == stage {
			return true
		}
	}
	return false
}

// fullRunProvider records a complete, consistent set of fixtures for one brief.
func fullRunProvider(t *testing.T, brief string, profile map[string]any) *countingProvider {
	t.Helper()
	c := loadCatalog(t)
	f := llm.NewFixture(t.TempDir())

	rec := func(stage, key string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Record(llm.Request{Stage: stage, FixtureKey: key}, b); err != nil {
			t.Fatal(err)
		}
	}

	rec("profile", fixtureKey("profile", brief), profile)

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
			"reason": "pet-category audience overlap"})
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
			"rationale": "grounded", "messaging_levers": []string{}, "avoided": []string{}})
	}

	return &countingProvider{inner: f}
}

func consumerProfile() map[string]any {
	return map[string]any{
		"primary_category": "pet", "subcategories": []string{"pet_food", "subscription"},
		"price_tier": "premium", "estimated_aov_usd": 70,
		"target_age_min": 30, "target_age_max": 55, "target_gender_skew": "balanced",
		"values": []string{"science_backed"}, "business_model": "subscription",
		"is_consumer_dtc": true, "confidence": "high",
		"assumptions": []string{}, "missing_signals": []string{},
	}
}

func TestRunProducesACompleteCampaign(t *testing.T) {
	brief := "We sell premium dog food for senior dogs."
	p := fullRunProvider(t, brief, consumerProfile())
	c := loadCatalog(t)

	got, err := Run(context.Background(), brief, Options{
		Provider: p, Catalog: c, Params: DefaultAllocParams(), Model: "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != model.StatusReady {
		t.Errorf("status = %q, want ready", got.Status)
	}
	if len(got.PublisherLedger) != len(c.Publishers) {
		t.Errorf("ledger has %d entries, want %d", len(got.PublisherLedger), len(c.Publishers))
	}
	if n := len(got.Creatives); n < 3 || n > 5 {
		t.Errorf("got %d creatives, want 3 to 5", n)
	}
	if len(got.Budget.Allocation) == 0 {
		t.Error("no budget allocated")
	}
	for _, stage := range []string{"profile", "fit", "personas", "creative"} {
		if !p.ran(stage) {
			t.Errorf("stage %q never ran", stage)
		}
	}
}

// Brief #7: B2B SaaS for dental practices. The run must stop after scoring.
func TestRunShortCircuitsNonConsumerBrief(t *testing.T) {
	brief := "B2B SaaS for dental practices. We automate patient recall."
	prof := consumerProfile()
	prof["is_consumer_dtc"] = false
	prof["primary_category"] = "b2b_saas"
	prof["business_model"] = "b2b"

	p := fullRunProvider(t, brief, prof)
	got, err := Run(context.Background(), brief, Options{
		Provider: p, Catalog: loadCatalog(t), Params: DefaultAllocParams(), Model: "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got.Status != model.StatusNoRecommendation {
		t.Errorf("status = %q, want %q", got.Status, model.StatusNoRecommendation)
	}
	if len(got.Creatives) != 0 {
		t.Errorf("got %d creatives, want 0", len(got.Creatives))
	}
	if len(got.Budget.Allocation) != 0 {
		t.Errorf("got %d allocations, want 0", len(got.Budget.Allocation))
	}
	for _, stage := range []string{"fit", "personas", "creative"} {
		if p.ran(stage) {
			t.Errorf("stage %q ran for a brief this catalog cannot serve", stage)
		}
	}
	if len(got.PublisherLedger) == 0 {
		t.Error("the ledger should still explain every publisher")
	}
}

func TestRunLowConfidenceStillProducesAProvisionalCampaign(t *testing.T) {
	brief := "idk just try it"
	prof := consumerProfile()
	prof["confidence"] = "low"
	prof["missing_signals"] = []string{"what is being sold", "price point"}

	p := fullRunProvider(t, brief, prof)
	got, err := Run(context.Background(), brief, Options{
		Provider: p, Catalog: loadCatalog(t), Params: DefaultAllocParams(), Model: "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != model.StatusNeedsClarification {
		t.Errorf("status = %q, want %q", got.Status, model.StatusNeedsClarification)
	}
	if len(got.Clarifications) == 0 {
		t.Error("want clarifying questions")
	}
	if len(got.Creatives) == 0 {
		t.Error("a provisional campaign should still carry creatives")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/pipeline/ -run TestRun -v`
Expected: FAIL — `undefined: Options`, `undefined: Run`

- [ ] **Step 3: Write `internal/pipeline/run.go`**

```go
package pipeline

import (
	"context"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/model"
)

// Options configures a pipeline run.
type Options struct {
	Provider llm.Provider
	Catalog  *catalog.Catalog
	Params   AllocParams
	Model    string
}

// Run executes the six stages and returns the campaign config.
//
// A brief this catalog cannot serve short-circuits after stage 2: there is no
// point spending three more model calls to produce a recommendation that should
// not exist, and the ledger already explains why every publisher is out.
func Run(ctx context.Context, brief string, o Options) (model.Campaign, error) {
	d := Deps{Provider: o.Provider, Catalog: o.Catalog}

	profile, err := Profile(ctx, d, brief)
	if err != nil {
		return model.Campaign{}, err
	}

	scores := ScoreAll(profile, o.Catalog)

	in := BuildInput{
		Brief: brief, Profile: profile, Scores: scores,
		Catalog: o.Catalog, Params: o.Params,
		Model: o.Model, Provider: o.Provider.Name(),
	}

	if !profile.IsConsumerDTC {
		for _, s := range scores {
			in.Verdicts = append(in.Verdicts, model.FitVerdict{
				PublisherID: s.PublisherID, Verdict: "excluded",
				Reason: gateReason(GateNotConsumerDTC),
			})
		}
		return Build(in), nil
	}

	if in.Verdicts, err = Fit(ctx, d, profile, scores); err != nil {
		return model.Campaign{}, err
	}
	if in.Personas, err = Personas(ctx, d, profile, in.Verdicts); err != nil {
		return model.Campaign{}, err
	}
	if in.Creatives, err = Creatives(ctx, d, profile, in.Personas); err != nil {
		return model.Campaign{}, err
	}
	return Build(in), nil
}
```

- [ ] **Step 4: Run the tests and commit**

Run: `go test ./... && go vet ./...`
Expected: PASS

```bash
git add internal/pipeline/run.go internal/pipeline/run_test.go
git commit -m "feat: pipeline orchestrator with degraded-mode short circuit

A brief the catalog cannot serve stops after scoring rather than spending
three more model calls producing a recommendation that should not exist.
The ledger still explains every publisher."
```

---

### Task 13: Renderers

**Files:**
- Create: `internal/render/json.go`, `internal/render/terminal.go`
- Test: `internal/render/render_test.go`

**Interfaces:**
- Consumes: `model.Campaign`
- Produces: `render.JSON(w io.Writer, c model.Campaign) error`, `render.Terminal(w io.Writer, c model.Campaign) error`

The terminal view leads with the ledger summary and the exclusions, not just the picks — the thing the exercise asks for that a recommendation list alone does not show.

- [ ] **Step 1: Write the failing test**

```go
package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yashraj/disco/internal/model"
)

func sample() model.Campaign {
	return model.Campaign{
		Status:    model.StatusReady,
		Objective: "conversion",
		Advertiser: model.AdvertiserBlock{
			Brief: "premium dog food", Confidence: "high",
			Assumptions: []string{"subscription implies repeat purchase"},
		},
		Flight: model.Flight{Start: "2026-09-13", DurationDays: 30},
		Budget: model.Budget{
			TotalUSD: 25000, DailyCapUSD: 833.33,
			Allocation: []model.AllocationEntry{{
				PublisherID: "pub_007", PublisherName: "Pawline", Share: 0.378,
				AmountUSD: 9458, EstCPMUSD: 15.48, EstImpressions: 611124,
				Rationale: "Subscription-heavy premium pet buyers",
			}},
		},
		Bid: model.Bid{Model: "CPM", Strategy: "target_cpa_capped",
			FloorUSD: 12.38, TargetUSD: 15.48, CeilingUSD: 21.67, TargetCPAUSD: 24.50},
		Targeting: model.Targeting{AgeRange: "30-55", GenderSkew: "balanced",
			Geos: []string{"US-West"}, PersonaIDs: []string{"persona_004"}},
		Creatives: []model.Creative{{
			PersonaID: "persona_004", Headline: "Built for senior dogs",
			Body: "Vet-formulated meals, delivered monthly.",
			Rationale: "Leads with the vet formulation this persona screens for.",
			MessagingLevers: []string{"vet-recommended"},
		}},
		PublisherLedger: []model.LedgerEntry{
			{PublisherID: "pub_007", PublisherName: "Pawline", Verdict: "recommended",
				Rank: 1, Score: 0.84, Reason: "Premium pet audience"},
			{PublisherID: "pub_005", PublisherName: "Linden Park", Verdict: "excluded",
				Score: 0, HardGate: "demographic_mismatch",
				Reason: "Audience skews 50-70; this brief targets 30-55."},
		},
		Meta: model.Meta{Model: "gemini-2.5-flash", Provider: "fixture", PipelineVersion: "1"},
	}
}

func TestJSONRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, sample()); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var back model.Campaign
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if back.Status != model.StatusReady {
		t.Errorf("status = %q after round trip", back.Status)
	}
	if !strings.Contains(buf.String(), "\n  ") {
		t.Error("JSON output should be indented for human reading")
	}
}

func TestTerminalShowsPicksAndExclusions(t *testing.T) {
	var buf bytes.Buffer
	if err := Terminal(&buf, sample()); err != nil {
		t.Fatalf("Terminal: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Pawline", "Linden Park", "demographic_mismatch",
		"9,458", "611,124", "Built for senior dogs", "persona_004",
		"30-55", "target_cpa_capped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("terminal output missing %q", want)
		}
	}
}

func TestTerminalShowsClarificationsWhenPresent(t *testing.T) {
	c := sample()
	c.Status = model.StatusNeedsClarification
	c.Clarifications = []string{"What exactly do you sell?"}

	var buf bytes.Buffer
	if err := Terminal(&buf, c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "What exactly do you sell?") {
		t.Error("clarifying questions must be shown")
	}
}

func TestTerminalNoRecommendationIsExplicit(t *testing.T) {
	c := sample()
	c.Status = model.StatusNoRecommendation
	c.Budget.Allocation = nil
	c.Creatives = nil

	var buf bytes.Buffer
	if err := Terminal(&buf, c); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "NO RECOMMENDATION") {
		t.Error("a no-recommendation result must say so unmistakably")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/render/ -v`
Expected: FAIL — `undefined: JSON`

- [ ] **Step 3: Write `internal/render/json.go`**

```go
// Package render turns a campaign into terminal text or JSON.
package render

import (
	"encoding/json"
	"io"

	"github.com/yashraj/disco/internal/model"
)

// JSON writes the campaign as indented JSON.
func JSON(w io.Writer, c model.Campaign) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(c)
}
```

- [ ] **Step 4: Write `internal/render/terminal.go`**

```go
package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/yashraj/disco/internal/model"
)

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
		p("  %-20s %7s %11s %8s %13s", "PUBLISHER", "SHARE", "SPEND", "CPM", "IMPRESSIONS")
		for _, a := range c.Budget.Allocation {
			p("  %-20s %6.1f%% %11s %8.2f %13s",
				trunc(a.PublisherName, 20), a.Share*100,
				"$"+comma(a.AmountUSD), a.EstCPMUSD, commaInt(a.EstImpressions))
			p("  %-20s %s", "", trunc(a.Rationale, 66))
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
	}

	if len(c.Creatives) > 0 {
		p("\nCREATIVE")
		for i, cr := range c.Creatives {
			p("\n  [%d] %s", i+1, cr.PersonaID)
			p("      %s", cr.Headline)
			p("      %s", cr.Body)
			p("      why: %s", trunc(cr.Rationale, 62))
			if len(cr.MessagingLevers) > 0 {
				p("      levers: %s", strings.Join(cr.MessagingLevers, ", "))
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
			p("    %-20s %s", "", trunc(e.Reason, 64))
		}
	}
	section("RECOMMENDED", rec)
	section("CONSIDERED", con)
	section("EXCLUDED", exc)

	if len(c.PersonaRejections) > 0 {
		p("\nPERSONAS NOT SELECTED")
		for _, r := range c.PersonaRejections {
			p("    %-14s %s", r.PersonaID, trunc(r.Reason, 56))
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

// comma formats a dollar amount with thousands separators, no decimals.
func comma(v float64) string { return commaInt(int64(v + 0.5)) }

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
```

- [ ] **Step 5: Run the tests and commit**

Run: `go test ./internal/render/ -v`
Expected: PASS, all four tests

```bash
git add internal/render
git commit -m "feat: terminal and JSON renderers

The terminal view prints the exclusion ledger as prominently as the picks;
a ranked list on its own is the black box the exercise asks us to avoid."
```

---

### Task 14: CLI

**Files:**
- Create: `cmd/disco/main.go`
- Test: manual, plus `go build`

**Interfaces:**
- Consumes: `pipeline.Run`, `pipeline.Options`, `pipeline.DefaultAllocParams`, `catalog.Load`, `llm.NewGemini`, `llm.NewFixture`, `render.Terminal`, `render.JSON`
- Produces: `disco run` / `disco serve` / `disco eval`, and `buildProvider(...)` shared by all three

- [ ] **Step 1: Write `cmd/disco/main.go`**

```go
// Command disco turns an advertiser's one-line brief into a draft ad campaign:
// ranked publishers with reasons, persona-tuned creatives, and a campaign config.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/pipeline"
	"github.com/yashraj/disco/internal/render"
)

const usage = `disco — draft an ad campaign from a one-line brief

  disco run "<brief>"   generate a campaign
  disco serve           browse results at http://localhost:8080
  disco eval            run every example brief and check the invariants

Run "disco <command> -h" for the flags of a command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "eval":
		err = cmdEval(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// commonFlags are shared by every subcommand.
type commonFlags struct {
	provider    string
	model       string
	dataDir     string
	fixtureDir  string
	cacheDir    string
	rpm         int
	record      bool
	noCache     bool
	params      pipeline.AllocParams
}

func registerCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{params: pipeline.DefaultAllocParams()}
	fs.StringVar(&c.provider, "provider", "fixture",
		"llm provider: fixture (replays recorded responses, no key needed) or gemini")
	fs.StringVar(&c.model, "model", "gemini-2.5-flash", "model id when --provider=gemini")
	fs.StringVar(&c.dataDir, "data", "data", "directory holding publishers.json and shopper_personas.json")
	fs.StringVar(&c.fixtureDir, "fixtures", "evals/fixtures", "recorded provider responses")
	fs.StringVar(&c.cacheDir, "cache", ".cache", "disk cache for live provider responses")
	fs.IntVar(&c.rpm, "rpm", 10, "provider requests per minute (free tier is 10 on gemini-2.5-flash)")
	fs.BoolVar(&c.record, "record", false, "write each live response to the fixtures directory")
	fs.BoolVar(&c.noCache, "no-cache", false, "bypass the disk cache")

	fs.Float64Var(&c.params.TotalUSD, "budget", c.params.TotalUSD, "total campaign budget in USD")
	fs.IntVar(&c.params.Days, "days", c.params.Days, "flight length in days")
	fs.Float64Var(&c.params.Gamma, "gamma", c.params.Gamma,
		"budget share exponent; 1.0 is proportional to fit, higher concentrates")
	fs.Float64Var(&c.params.MaxShare, "max-share", c.params.MaxShare, "maximum budget share for one publisher")
	fs.Float64Var(&c.params.SOVCap, "sov-cap", c.params.SOVCap, "maximum share of a publisher's monthly impressions to buy")
	fs.Float64Var(&c.params.MinShare, "min-share", c.params.MinShare, "budget shares below this are redistributed")
	return c
}

// buildProvider returns the configured provider and the loaded catalog.
func buildProvider(c *commonFlags) (llm.Provider, *catalog.Catalog, error) {
	cat, err := catalog.Load(c.dataDir)
	if err != nil {
		return nil, nil, err
	}

	switch c.provider {
	case "fixture":
		return llm.NewFixture(c.fixtureDir), cat, nil
	case "gemini":
		opts := llm.GeminiOptions{
			APIKey: os.Getenv("GEMINI_API_KEY"),
			Model:  c.model,
			RPM:    c.rpm,
		}
		if !c.noCache {
			opts.CacheDir = c.cacheDir
		}
		if c.record {
			opts.Recorder = llm.NewFixture(c.fixtureDir)
		}
		p, err := llm.NewGemini(opts)
		return p, cat, err
	default:
		return nil, nil, fmt.Errorf("unknown provider %q: want fixture or gemini", c.provider)
	}
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	common := registerCommon(fs)
	asJSON := fs.Bool("json", false, "emit the campaign config as JSON instead of a summary")
	if err := fs.Parse(args); err != nil {
		return err
	}

	brief := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if brief == "" {
		return fmt.Errorf(`no brief given; try: disco run "We sell premium dog food for senior dogs"`)
	}

	provider, cat, err := buildProvider(common)
	if err != nil {
		return err
	}

	campaign, err := pipeline.Run(context.Background(), brief, pipeline.Options{
		Provider: provider, Catalog: cat, Params: common.params, Model: common.model,
	})
	if err != nil {
		return err
	}

	if *asJSON {
		return render.JSON(os.Stdout, campaign)
	}
	return render.Terminal(os.Stdout, campaign)
}
```

- [ ] **Step 2: Add temporary stubs so the package compiles**

`cmdServe` and `cmdEval` arrive in Tasks 15 and 16. Add them at the bottom of `main.go` now:

```go
func cmdServe(args []string) error { return fmt.Errorf("serve: not implemented yet") }
func cmdEval(args []string) error  { return fmt.Errorf("eval: not implemented yet") }
```

- [ ] **Step 3: Verify it builds and the help works**

```bash
go build ./... && go run ./cmd/disco --help
go run ./cmd/disco run -h
```
Expected: usage text; `run -h` lists every flag including `--gamma` and `--sov-cap`

- [ ] **Step 4: Verify the missing-fixture error is actionable**

```bash
go run ./cmd/disco run "We sell premium dog food for senior dogs"
```
Expected: an error naming the missing fixture path and telling you to run with `--provider gemini --record`. This is the correct behaviour before any fixture exists.

- [ ] **Step 5: Commit**

```bash
git add cmd/disco
git commit -m "feat: disco CLI with run subcommand

Every allocation threshold is a flag, so the shape of a budget split can be
interrogated and changed without editing code."
```

---

### Task 15: `disco serve` and the embedded web view

**Files:**
- Create: `internal/server/server.go`, `web/index.html`, `web/embed.go`
- Modify: `cmd/disco/main.go` (replace the `cmdServe` stub)
- Test: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `pipeline.Run`, `pipeline.Options`, `model.Campaign`
- Produces: `server.New(o pipeline.Options) http.Handler`, `server.Run(addr string, o pipeline.Options) error`

- [ ] **Step 1: Write `web/embed.go`**

```go
// Package web holds the single-page view served by "disco serve".
package web

import "embed"

//go:embed index.html
var FS embed.FS
```

- [ ] **Step 2: Write `web/index.html`**

```html
<!doctype html>
<meta charset="utf-8">
<title>disco — campaign drafter</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
  :root { --bg:#fbfbf9; --fg:#1c1c1a; --mut:#6b6b66; --line:#e3e3dd; --accent:#1a5c3a; --warn:#8a5a12; --bad:#8a2a2a; }
  @media (prefers-color-scheme:dark){ :root{ --bg:#16161a; --fg:#e8e8e4; --mut:#9a9a94; --line:#2e2e34; --accent:#6fbf90; --warn:#d8a94a; --bad:#e08080; } }
  *{box-sizing:border-box}
  body{margin:0;padding:24px 16px;background:var(--bg);color:var(--fg);
       font:15px/1.5 ui-sans-serif,system-ui,-apple-system,Segoe UI,sans-serif}
  main{max-width:900px;margin:0 auto}
  h1{font-size:20px;margin:0 0 4px} .sub{color:var(--mut);margin:0 0 20px}
  form{display:flex;gap:8px;flex-wrap:wrap;margin-bottom:20px}
  textarea{flex:1 1 420px;min-height:70px;padding:10px;border:1px solid var(--line);
           border-radius:8px;background:transparent;color:inherit;font:inherit;resize:vertical}
  button{padding:10px 18px;border:0;border-radius:8px;background:var(--accent);
         color:var(--bg);font:600 15px inherit;cursor:pointer}
  button[disabled]{opacity:.5;cursor:progress}
  .ex{display:flex;gap:6px;flex-wrap:wrap;margin-bottom:20px}
  .ex button{background:transparent;color:var(--mut);border:1px solid var(--line);
             font-weight:400;font-size:13px;padding:5px 10px}
  section{border-top:1px solid var(--line);padding-top:16px;margin-top:20px}
  h2{font-size:12px;letter-spacing:.08em;text-transform:uppercase;color:var(--mut);margin:0 0 12px}
  .banner{padding:10px 14px;border-radius:8px;margin-bottom:16px;font-weight:600}
  .ready{background:color-mix(in srgb,var(--accent) 15%,transparent)}
  .needs_clarification{background:color-mix(in srgb,var(--warn) 18%,transparent)}
  .no_recommendation{background:color-mix(in srgb,var(--bad) 18%,transparent)}
  table{width:100%;border-collapse:collapse;font-size:14px}
  th{text-align:left;font-weight:600;color:var(--mut);font-size:12px;padding:4px 8px 4px 0}
  td{padding:6px 8px 6px 0;border-top:1px solid var(--line);vertical-align:top}
  td.n{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}
  .why{color:var(--mut);font-size:13px}
  .card{border:1px solid var(--line);border-radius:8px;padding:12px;margin-bottom:10px}
  .card h3{margin:0 0 6px;font-size:16px}
  .card p{margin:0 0 6px}
  .tag{display:inline-block;font-size:11px;color:var(--mut);border:1px solid var(--line);
       border-radius:99px;padding:1px 8px;margin:2px 4px 0 0}
  .v-recommended{color:var(--accent);font-weight:600}
  .v-considered{color:var(--warn)}
  .v-excluded{color:var(--mut)}
  details summary{cursor:pointer;color:var(--mut);font-size:13px;margin-top:8px}
  .err{color:var(--bad)}
  @media(max-width:560px){ td.hide,th.hide{display:none} }
</style>
<main>
  <h1>disco</h1>
  <p class="sub">Describe a business in a sentence. Get publishers, creatives, and a campaign config.</p>

  <form id="f">
    <textarea id="brief" placeholder="We sell premium dog food for senior dogs, targeting owners who care about joint health."></textarea>
    <button id="go">Draft campaign</button>
  </form>
  <div class="ex" id="ex"></div>

  <div id="out"></div>
</main>
<script>
const EXAMPLES = [
  "We sell premium dog food for senior dogs, targeting owners who care about joint health. Grain-free, vet-formulated, subscription-based.",
  "A sustainable activewear brand for women. Made from recycled ocean plastic.",
  "B2B SaaS for dental practices. We automate their patient recall workflow.",
  "idk just try it",
];
const $ = (s) => document.querySelector(s);
const esc = (s) => String(s ?? "").replace(/[&<>"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
const usd = (n) => "$" + Math.round(n).toLocaleString();
const num = (n) => Math.round(n).toLocaleString();

$("#ex").innerHTML = EXAMPLES.map((e,i) =>
  `<button type="button" data-i="${i}">${esc(e.slice(0,44))}${e.length>44?"…":""}</button>`).join("");
$("#ex").addEventListener("click", e => {
  const b = e.target.closest("button"); if (!b) return;
  $("#brief").value = EXAMPLES[b.dataset.i];
});

$("#f").addEventListener("submit", async (e) => {
  e.preventDefault();
  const brief = $("#brief").value.trim();
  if (!brief) return;
  $("#go").disabled = true;
  $("#out").innerHTML = `<p class="sub">Working…</p>`;
  try {
    const r = await fetch("/api/run", {
      method: "POST", headers: {"content-type": "application/json"},
      body: JSON.stringify({brief}),
    });
    const data = await r.json();
    if (!r.ok) throw new Error(data.error || r.statusText);
    render(data);
  } catch (err) {
    $("#out").innerHTML = `<p class="err">${esc(err.message)}</p>`;
  } finally {
    $("#go").disabled = false;
  }
});

const BANNER = {
  ready: "Campaign draft",
  needs_clarification: "Provisional — the brief was too vague to place confidently",
  no_recommendation: "No recommendation — this catalog cannot reach these buyers",
};

function render(c) {
  const out = [];
  out.push(`<div class="banner ${esc(c.status)}">${esc(BANNER[c.status] || c.status)}</div>`);

  if (c.clarifications?.length) {
    out.push(`<section><h2>What we need to know</h2><ul>` +
      c.clarifications.map(q => `<li>${esc(q)}</li>`).join("") + `</ul></section>`);
  }
  if (c.advertiser?.assumptions?.length) {
    out.push(`<section><h2>Assumptions</h2><ul class="why">` +
      c.advertiser.assumptions.map(a => `<li>${esc(a)}</li>`).join("") + `</ul></section>`);
  }

  if (c.budget?.allocation?.length) {
    out.push(`<section><h2>Budget — ${usd(c.budget.total_usd)} over ${c.flight.duration_days} days</h2>
      <table><tr><th>Publisher</th><th class="n">Share</th><th class="n">Spend</th>
      <th class="n hide">CPM</th><th class="n hide">Impressions</th></tr>` +
      c.budget.allocation.map(a => `<tr>
        <td><strong>${esc(a.publisher_name)}</strong><div class="why">${esc(a.rationale)}</div></td>
        <td class="n">${(a.share*100).toFixed(1)}%</td>
        <td class="n">${usd(a.amount_usd)}</td>
        <td class="n hide">$${a.est_cpm_usd.toFixed(2)}</td>
        <td class="n hide">${num(a.est_impressions)}</td></tr>`).join("") +
      `</table>
      <p class="why">Bid: ${esc(c.bid.strategy)} · floor $${c.bid.floor_usd.toFixed(2)} ·
       target $${c.bid.target_usd.toFixed(2)} · ceiling $${c.bid.ceiling_usd.toFixed(2)}</p>
      </section>`);
  }

  if (c.creatives?.length) {
    out.push(`<section><h2>Creative — ${c.creatives.length} variants</h2>` +
      c.creatives.map(cr => `<div class="card">
        <h3>${esc(cr.headline)}</h3><p>${esc(cr.body)}</p>
        <div class="why">${esc(cr.persona_id)} — ${esc(cr.rationale)}</div>
        ${(cr.messaging_levers||[]).map(l => `<span class="tag">${esc(l)}</span>`).join("")}
      </div>`).join("") + `</section>`);
  }

  const led = c.publisher_ledger || [];
  const by = v => led.filter(e => e.verdict === v);
  out.push(`<section><h2>Publisher ledger — ${by("recommended").length} recommended,
    ${by("considered").length} considered, ${by("excluded").length} excluded</h2>` +
    ["recommended","considered","excluded"].map(v => {
      const rows = by(v);
      if (!rows.length) return "";
      const body = `<table>` + rows.map(e => `<tr>
        <td><span class="v-${v}">${esc(e.publisher_name)}</span>
            ${e.hard_gate ? `<span class="tag">${esc(e.hard_gate)}</span>` : ""}
            <div class="why">${esc(e.reason)}</div></td>
        <td class="n">${e.score.toFixed(2)}</td></tr>`).join("") + `</table>`;
      return v === "excluded"
        ? `<details><summary>${rows.length} excluded — show reasons</summary>${body}</details>`
        : body;
    }).join("") + `</section>`);

  out.push(`<details><summary>Campaign config JSON</summary>
    <pre style="overflow-x:auto;font-size:12px">${esc(JSON.stringify(c, null, 2))}</pre></details>`);

  $("#out").innerHTML = out.join("");
}
</script>
```

- [ ] **Step 3: Write the failing test**

```go
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yashraj/disco/internal/pipeline"
)

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

func TestAPIRejectsEmptyBrief(t *testing.T) {
	h := New(pipeline.Options{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(`{"brief":"  "}`))
	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"error"`) {
		t.Error("an error response should carry an error field")
	}
}

func TestAPIRejectsGET(t *testing.T) {
	h := New(pipeline.Options{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/run", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}
```

- [ ] **Step 4: Run it to verify it fails**

Run: `go test ./internal/server/ -v`
Expected: FAIL — `undefined: New`

- [ ] **Step 5: Write `internal/server/server.go`**

```go
// Package server exposes the pipeline over HTTP for "disco serve".
package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/yashraj/disco/internal/pipeline"
	"github.com/yashraj/disco/web"
)

// New returns the handler: a single embedded page plus one JSON endpoint.
func New(o pipeline.Options) http.Handler {
	mux := http.NewServeMux()

	sub, err := fs.Sub(web.FS, ".")
	if err != nil {
		panic(err) // the embedded FS is compiled in; this cannot fail at runtime
	}
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	mux.HandleFunc("POST /api/run", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Brief string `json:"brief"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "could not read request: "+err.Error())
			return
		}
		brief := strings.TrimSpace(req.Brief)
		if brief == "" {
			writeErr(w, http.StatusBadRequest, "describe the business in a sentence first")
			return
		}

		ctx, cancel := contextWithTimeout(r, 5*time.Minute)
		defer cancel()

		campaign, err := pipeline.Run(ctx, brief, o)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("content-type", "application/json")
		if err := json.NewEncoder(w).Encode(campaign); err != nil {
			log.Printf("serve: writing response: %v", err)
		}
	})

	mux.HandleFunc("/api/run", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusMethodNotAllowed, "POST a JSON body with a brief field")
	})

	return mux
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Run serves until the process is stopped.
func Run(addr string, o pipeline.Options) error {
	fmt.Printf("disco listening on http://localhost%s\n", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           New(o),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.ListenAndServe()
}
```

Add this helper to the same file:

```go
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
```

and add `"context"` to the imports.

- [ ] **Step 6: Replace the `cmdServe` stub in `cmd/disco/main.go`**

```go
func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	common := registerCommon(fs)
	addr := fs.String("addr", ":8080", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}

	provider, cat, err := buildProvider(common)
	if err != nil {
		return err
	}
	return server.Run(*addr, pipeline.Options{
		Provider: provider, Catalog: cat, Params: common.params, Model: common.model,
	})
}
```

Add `"github.com/yashraj/disco/internal/server"` to the imports.

- [ ] **Step 7: Run the tests, then check it by hand**

```bash
go test ./internal/server/ -v
go build ./... && go run ./cmd/disco serve
```
Open http://localhost:8080. The page loads and the example buttons fill the box. Submitting before fixtures exist returns a clear error naming the missing fixture — that is correct until Task 16 records them.

- [ ] **Step 8: Commit**

```bash
git add internal/server web cmd/disco/main.go
git commit -m "feat: disco serve with an embedded single-page view

One binary, no npm, no build step. The page shows the exclusion ledger
behind a disclosure rather than hiding it entirely."
```

---

### Task 16: Eval harness

**Files:**
- Create: `internal/eval/eval.go`, `internal/eval/assertions.go`, `evals/briefs.txt`
- Modify: `cmd/disco/main.go` (replace the `cmdEval` stub)
- Test: `internal/eval/assertions_test.go`

**Interfaces:**
- Consumes: `pipeline.Run`, `pipeline.Options`, `model.Campaign`
- Produces:
  - `eval.Brief{N int; Text string}`, `eval.LoadBriefs(path string) ([]Brief, error)`
  - `eval.Result{Brief Brief; Campaign model.Campaign; Failures []string; Err error}`
  - `eval.Check(b Brief, c model.Campaign, cat *catalog.Catalog) []string`
  - `eval.Run(ctx context.Context, briefs []Brief, o pipeline.Options, cat *catalog.Catalog) []Result`
  - `eval.Report(w io.Writer, rs []Result) (passed, failed int)`

The assertions are structural and judgment invariants, not copy quality. Whether a headline is good is not measurable here; whether the system refused when it should have refused is binary and cheap. That distinction is the honest version of an eval at this scale, and the README says so.

- [ ] **Step 1: Write `evals/briefs.txt`**

Copy the 15 numbered briefs out of `data/example_advertisers.txt`, one per line, prefixed with the number and a pipe:

```
1|We sell premium dog food for senior dogs, targeting owners who care about joint health and longevity. Grain-free, vet-formulated, subscription-based.
2|A sustainable activewear brand for women. Made from recycled ocean plastic. Price point sits between Lululemon and Girlfriend Collective.
3|We make a non-alcoholic sparkling drink with adaptogens. It's for people who want to feel good without a hangover, kind of like a functional cocktail alternative.
4|Small-batch candles poured by hand in Vermont. Natural soy wax, no synthetic fragrances. Mostly bought as gifts.
5|We help people feel better.
6|Technical outerwear for serious backcountry skiers. Our shells are what patrollers wear. Starts at $650, goes up from there.
7|B2B SaaS for dental practices. We automate their patient recall workflow.
8|A new kind of thing for moms.
9|Refillable, concentrated cleaning products. Skip the single-use plastic bottles. Works as well as the big brands. We want to show up where people who already care about sustainability are checking out.
10|Custom-fit leather handbags, Italian-made, handcrafted in Florence. Minimum order ships in 6 weeks. Average price point $1,200.
11|We sell protein bars that don't taste like cardboard. That's basically the whole pitch.
12|A subscription box for new cat owners. First three months of their cat's life. Toys, food samples, a little booklet about what to expect.
13|Workout supplements: pre-workout, creatine, protein. We compete on price, not on marketing. Same formulations as the expensive brands for half the cost.
14|Bedding. Linen. Actually-breathable stuff made in Portugal. Our customers are mostly people who got tired of the Brooklinen/Parachute aesthetic and want something a little more grown-up.
15|idk just try it
```

- [ ] **Step 2: Write `internal/eval/assertions.go`**

```go
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
```

- [ ] **Step 3: Write `internal/eval/eval.go`**

```go
package eval

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
	"github.com/yashraj/disco/internal/pipeline"
)

// Brief is one numbered example advertiser description.
type Brief struct {
	N    int
	Text string
}

// Result is one brief's outcome.
type Result struct {
	Brief    Brief
	Campaign model.Campaign
	Failures []string
	Err      error
}

// LoadBriefs reads "N|text" lines, ignoring blanks and # comments.
func LoadBriefs(path string) ([]Brief, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("eval: %w", err)
	}
	defer f.Close()

	var out []Brief
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		t := strings.TrimSpace(sc.Text())
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		numStr, text, ok := strings.Cut(t, "|")
		if !ok {
			return nil, fmt.Errorf("eval: %s:%d: want N|text", path, line)
		}
		n, err := strconv.Atoi(strings.TrimSpace(numStr))
		if err != nil {
			return nil, fmt.Errorf("eval: %s:%d: %w", path, line, err)
		}
		out = append(out, Brief{N: n, Text: strings.TrimSpace(text)})
	}
	return out, sc.Err()
}

// Run executes every brief and checks it. Briefs run sequentially: the
// provider's rate limiter is shared, and a stable order keeps the report
// readable.
func Run(ctx context.Context, briefs []Brief, o pipeline.Options, cat *catalog.Catalog) []Result {
	out := make([]Result, 0, len(briefs))
	for _, b := range briefs {
		r := Result{Brief: b}
		r.Campaign, r.Err = pipeline.Run(ctx, b.Text, o)
		if r.Err == nil {
			r.Failures = Check(b, r.Campaign, cat)
		}
		out = append(out, r)
	}
	return out
}

// Report prints a pass/fail table and returns the counts.
func Report(w io.Writer, rs []Result) (passed, failed int) {
	fmt.Fprintf(w, "%-4s %-10s %-22s %s\n", "#", "RESULT", "STATUS", "BRIEF")
	fmt.Fprintln(w, strings.Repeat("─", 88))

	for _, r := range rs {
		brief := r.Brief.Text
		if len([]rune(brief)) > 44 {
			brief = string([]rune(brief)[:43]) + "…"
		}
		switch {
		case r.Err != nil:
			failed++
			fmt.Fprintf(w, "%-4d %-10s %-22s %s\n", r.Brief.N, "ERROR", "-", brief)
			fmt.Fprintf(w, "     %v\n", r.Err)
		case len(r.Failures) > 0:
			failed++
			fmt.Fprintf(w, "%-4d %-10s %-22s %s\n", r.Brief.N, "FAIL", r.Campaign.Status, brief)
			for _, f := range r.Failures {
				fmt.Fprintf(w, "     · %s\n", f)
			}
		default:
			passed++
			fmt.Fprintf(w, "%-4d %-10s %-22s %s\n", r.Brief.N, "pass", r.Campaign.Status, brief)
		}
	}

	fmt.Fprintln(w, strings.Repeat("─", 88))
	fmt.Fprintf(w, "%d passed, %d failed, %d total\n", passed, failed, len(rs))
	return passed, failed
}
```

- [ ] **Step 4: Write the failing test**

```go
package eval

import (
	"strings"
	"testing"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/model"
)

func cat(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load("../../data")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoadBriefsReadsFifteen(t *testing.T) {
	bs, err := LoadBriefs("../../evals/briefs.txt")
	if err != nil {
		t.Fatalf("LoadBriefs: %v", err)
	}
	if len(bs) != 15 {
		t.Fatalf("got %d briefs, want 15", len(bs))
	}
	if bs[6].N != 7 || !strings.Contains(bs[6].Text, "dental") {
		t.Errorf("brief 7 = %+v, want the dental SaaS brief", bs[6])
	}
}

func TestCheckFlagsUnknownPublisherInLedger(t *testing.T) {
	c := cat(t)
	camp := model.Campaign{Status: model.StatusNoRecommendation}
	for i := range c.Publishers {
		camp.PublisherLedger = append(camp.PublisherLedger, model.LedgerEntry{
			PublisherID: c.Publishers[i].ID, Verdict: "excluded", Reason: "r"})
	}
	camp.PublisherLedger[0].PublisherID = "pub_999"

	got := Check(Brief{N: 7}, camp, c)
	if !containsSubstr(got, "unknown publisher") {
		t.Errorf("failures = %v, want an unknown-publisher failure", got)
	}
}

func TestCheckFlagsNonConsumerBriefThatRecommends(t *testing.T) {
	c := cat(t)
	camp := model.Campaign{Status: model.StatusReady}
	for i := range c.Publishers {
		camp.PublisherLedger = append(camp.PublisherLedger, model.LedgerEntry{
			PublisherID: c.Publishers[i].ID, Verdict: "excluded", Reason: "r"})
	}
	camp.Budget.Allocation = []model.AllocationEntry{{PublisherID: "pub_007", Share: 1}}

	got := Check(Brief{N: 7}, camp, c)
	if !containsSubstr(got, "non-consumer") {
		t.Errorf("failures = %v, want a non-consumer status failure", got)
	}
	if !containsSubstr(got, "cannot serve") {
		t.Errorf("failures = %v, want a budget-allocated failure", got)
	}
}

func TestCheckFlagsUngroundedMessagingLever(t *testing.T) {
	c := cat(t)
	camp := model.Campaign{Status: model.StatusReady}
	for i := range c.Publishers {
		camp.PublisherLedger = append(camp.PublisherLedger, model.LedgerEntry{
			PublisherID: c.Publishers[i].ID, Verdict: "excluded", Reason: "r"})
	}
	camp.Creatives = []model.Creative{
		{PersonaID: "persona_004", Headline: "h", Body: "b",
			MessagingLevers: []string{"totally invented"}},
		{PersonaID: "persona_002", Headline: "h", Body: "b",
			MessagingLevers: []string{"time-saving"}},
		{PersonaID: "persona_001", Headline: "h", Body: "b",
			MessagingLevers: []string{"science-backed claims"}},
	}

	got := Check(Brief{N: 1}, camp, c)
	if !containsSubstr(got, "not in its persona record") {
		t.Errorf("failures = %v, want an ungrounded-lever failure", got)
	}
}

func containsSubstr(xs []string, want string) bool {
	for _, x := range xs {
		if strings.Contains(x, want) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run it to verify it fails, then passes**

Run: `go test ./internal/eval/ -v`
Expected: first FAIL with `undefined: LoadBriefs`, then PASS once Steps 2–3 are in place

- [ ] **Step 6: Replace the `cmdEval` stub in `cmd/disco/main.go`**

```go
func cmdEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	common := registerCommon(fs)
	briefsPath := fs.String("briefs", "evals/briefs.txt", "file of numbered example briefs")
	only := fs.Int("brief", 0, "run only this brief number")
	if err := fs.Parse(args); err != nil {
		return err
	}

	briefs, err := eval.LoadBriefs(*briefsPath)
	if err != nil {
		return err
	}
	if *only > 0 {
		var kept []eval.Brief
		for _, b := range briefs {
			if b.N == *only {
				kept = append(kept, b)
			}
		}
		if len(kept) == 0 {
			return fmt.Errorf("no brief numbered %d in %s", *only, *briefsPath)
		}
		briefs = kept
	}

	provider, cat, err := buildProvider(common)
	if err != nil {
		return err
	}

	results := eval.Run(context.Background(), briefs, pipeline.Options{
		Provider: provider, Catalog: cat, Params: common.params, Model: common.model,
	}, cat)

	_, failed := eval.Report(os.Stdout, results)
	if failed > 0 {
		return fmt.Errorf("%d of %d briefs failed", failed, len(results))
	}
	return nil
}
```

Add `"github.com/yashraj/disco/internal/eval"` to the imports.

- [ ] **Step 7: Record the fixtures against the live provider**

This is the only step that needs a key and quota. It costs roughly 120 calls, inside the free tier's 250/day on `gemini-2.5-flash`.

```bash
export GEMINI_API_KEY=...    # free, from aistudio.google.com
go run ./cmd/disco eval --provider gemini --record
```

Iterate on the prompt files until the report is all green. Rerun with `--provider fixture` to confirm the recorded set replays identically and for free.

- [ ] **Step 8: Verify the offline path**

```bash
go run ./cmd/disco eval                      # fixture provider, no key
go run ./cmd/disco run "We sell premium dog food for senior dogs, vet-formulated, subscription-based."
go run ./cmd/disco serve                     # click through the examples
```
Expected: all 15 pass offline; `run` prints a full campaign; the page works with no key set.

- [ ] **Step 9: Commit**

```bash
git add internal/eval evals cmd/disco/main.go
git commit -m "feat: eval harness over all 15 example briefs

Assertions are structural and judgment invariants, not copy quality: did
the system refuse what it could not serve, exclude for the stated reason,
and ground every messaging lever in the persona record. Committed fixtures
make the suite free and deterministic."
```

---

### Task 17: README

**Files:**
- Modify: `README.md` (replace the exercise brief — keep a copy at `docs/EXERCISE.md`)

The brief asks for one page and says to cut if it spills onto a second. Four sections are required: what it is and how to run it, next week's work, what was cut and why, and what is genuinely hard versus easy.

- [ ] **Step 1: Preserve the original exercise text**

```bash
mkdir -p docs && git mv README.md docs/EXERCISE.md
```

- [ ] **Step 2: Write the new `README.md`**

```markdown
# disco

An advertiser describes their business in a sentence. This drafts the campaign:
which publishers to run on and why, which ones to skip and why, 3–5 ad creatives
written for specific shopper personas, and a campaign config a downstream system
could act on.

## Run it

```bash
go run ./cmd/disco serve      # http://localhost:8080
go run ./cmd/disco run "We sell premium dog food for senior dogs, vet-formulated, subscription-based."
go run ./cmd/disco eval       # all 15 example briefs, with assertions
go test ./...
```

**No API key needed.** Committed fixtures in `evals/fixtures/` replay real model
responses, so everything above works offline. To run live against the model:

```bash
export GEMINI_API_KEY=...     # free, aistudio.google.com
go run ./cmd/disco run "..." --provider gemini
```

## How it works

Six stages. Four call a model; two are pure functions.

```
brief ─▶ profile ─▶ scoring ─▶ fit ─▶ personas ─▶ creative ─▶ campaign
          LLM        PURE       LLM     LLM         LLM        PURE
```

**Every number in the output comes from the pure stages.** Publisher scores,
CPMs, budget shares, impressions, and bid ranges are computed in
`internal/pipeline/{scoring,cpm,allocate,campaign}.go`. A model never returns a
figure that reaches the config. It supplies judgment and prose; arithmetic
supplies the money.

Three things the model is not trusted with, enforced in code rather than asked
for in a prompt: hard gates can't be overridden, publisher and persona IDs are
filtered against the catalog, and a publisher the model forgot is backfilled so
the ledger always covers all 20.

**The catalog has no CPM.** `EstimateCPM` derives one from income tier, category,
and AOV. That is a stand-in for a rate card, isolated in one function — swap it
and nothing else changes.

**Degraded modes.** A B2B brief (#7) returns `no_recommendation` and stops after
scoring rather than spending three more calls. A vague brief (#5, #8, #15)
returns `needs_clarification`: the inferred profile shown as a hypothesis, the
specific questions we'd need answered, and a provisional campaign clearly
labelled as such. Refusing "idk just try it" outright is as unhelpful as
pretending it was clear.

## What I'd do next week

1. **Measure creative quality.** Right now the eval asserts that copy is
   *grounded* — every messaging lever appears verbatim in its persona record —
   but not that it is *good*. A pairwise LLM-judge over held-out variants, with
   human spot-checks to validate the judge, is the first thing I'd build.
2. **Learn the scoring weights instead of asserting them.** The six weights are
   my judgment. With outcome data they'd be fit, and the whole pure layer is
   already shaped to accept that.
3. **Real inventory and pricing**, replacing `EstimateCPM` and the flat SOV cap
   with forecasts per placement.
4. **Let the user push back.** Edit the derived profile, force a publisher in or
   out, and re-run downstream stages only.

## What I cut, and why

**Image creative, auction simulation, multi-tenancy, auth, persistence.** All are
real in production and none would have told you anything about how I think.

**Vector search.** The catalog is 10KB. It fits in a prompt with room to spare.
Embedding 20 records would have been cargo-culted retrieval — the interesting
problem here is grounding, not recall.

**A frontier model.** This runs on Gemini 2.5 Flash's free tier. The copy is
weaker than Opus or Sonnet would write, and I'd rather say so than hide it. The
provider seam means regenerating the fixtures against a better model is a one-
file change.

## What's actually hard here

**Easy:** matching a pet brand to a pet publisher. Any approach gets it. Also
easy: the structured-output plumbing — schemas, validation, retries. Fiddly, not
hard.

**Hard, and where the engineering actually lives:**

*Knowing when to say no.* Three of the 15 briefs are deliberately low-signal and
one (dental SaaS) has no valid answer at all. A model asked to rank publishers
will always rank publishers. Making refusal a first-class outcome — gated in
code, upstream of the ranking, so it can't be argued out of — was the single
highest-value decision in this build.

*Exclusion reasoning.* Recommending is easy; explaining a non-obvious rejection
is not. Linden Park is excluded from an activewear brief because its audience
skews 50–70 against a 25–45 target. That's arithmetic, done before the model
sees anything, which is why it's reliable.

*The join between fit, price, and inventory.* At a $50k budget, Pawline is the
best-fitting publisher in the catalog and still drops from first to third,
because 37.8% of that budget would buy more impressions than it has to sell.
That conclusion needs a derived price multiplied against an inventory ceiling and
redistributed to a fixed point. A model will happily assert a budget split that
violates all three constraints, fluently. It's the least glamorous code here and
the part I'd defend hardest.

*Copy that isn't interchangeable.* Asking one call for five variants gets five
paraphrases of one idea. One call per persona, seeing only that persona, is what
makes them actually differ — and it's still the weakest output, because quality
here is a taste judgment I can't assert in a test.
```

- [ ] **Step 3: Check it is one page**

Render it. If it spills well past a screen and a half, cut from "What I'd do next
week" first — the last section is the one they said they care about.

- [ ] **Step 4: Final verification**

```bash
go build ./... && go vet ./... && go test ./...
go run ./cmd/disco eval
go run ./cmd/disco run "A sustainable activewear brand for women. Made from recycled ocean plastic."
```
Expected: build clean, vet silent, tests pass, 15/15 briefs pass, campaign renders.

- [ ] **Step 5: Commit**

```bash
git add README.md docs/EXERCISE.md
git commit -m "docs: README covering the build, cuts, and where the hard parts are"
```

---

## Self-Review

**Spec coverage.** Every section of the design doc maps to a task: objective and
scope → Tasks 12, 14–16 · architecture and repo layout → all · data contracts →
Tasks 1, 5 · stage 1 → 8 · stage 2 → 2 · stage 3 → 9 · stage 4 → 10 · stage 5 →
11 · stage 6 → 5 · derived CPM → 3 · budget allocation → 4 · config shape → 5 ·
degraded modes → 5, 12 · LLM integration → 6, 7 · eval → 16 · CLI surface → 14,
15 · testing → every task · risks → 17.

**Three fixes applied during review:**

1. `internal/model` is not in the spec's repo layout. The spec listed the data
   contracts without saying where they live; putting them in their own
   dependency-free package is what keeps `catalog`, `pipeline`, `render`, and
   `eval` from forming an import cycle. Added to the File Structure table.
2. Task 6 creates placeholder prompt files. `go:embed` fails to compile on an
   empty glob, so `prompts/` cannot be empty between Task 6 and Task 8. The
   placeholders are replaced stage by stage in Tasks 8–11.
3. `gateReason` is defined in Task 5 (`campaign.go`) and used in Tasks 9 and 12.
   Verified it is defined before first use in task order.

**Type consistency check.** `Deps`, `callStage`, and `fixtureKey` (Task 8) are
used unchanged in Tasks 9–11. `AllocParams`, `Candidate`, `Allocation` (Task 4)
are consumed in Task 5 under the same names. `llm.Request`, `Provider`,
`CacheKey`, `ShortHash` (Task 6) are consumed unchanged in Tasks 7–8.
`pipeline.Options` (Task 12) is consumed in Tasks 14–16. `loadCatalog`,
`dogFood`, `activewear`, `scoreFor` (Task 2 test) and `fixtureDeps` (Task 8
test) and `withBrief` (Task 9 test) are shared across `internal/pipeline` test
files — all in one package, so they resolve.

**Known ordering constraint.** Task 7's `TestShippedSchemasConvert` runs against
the Task 6 placeholders and only becomes meaningful after Task 11. It passes
throughout because `{"type":"object"}` converts cleanly.
