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
