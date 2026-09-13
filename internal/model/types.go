// Package model holds the data contracts passed between pipeline stages.
// It has no behavior and no dependencies, so every other package can import it.
package model

// AdvertiserProfile is stage 1 output. Confidence and IsConsumerDTC gate
// everything downstream.
type AdvertiserProfile struct {
	RawBrief         string   `json:"raw_brief"`
	PrimaryCategory  string   `json:"primary_category"`
	Subcategories    []string `json:"subcategories"`
	PriceTier        string   `json:"price_tier"` // budget | mid | premium | luxury
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
