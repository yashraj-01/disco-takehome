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
