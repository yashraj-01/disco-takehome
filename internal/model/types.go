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
	// ExceedsMaxShare is true when this publisher's final Share exceeds the
	// allocator's concentration cap (AllocParams.MaxShare). The cap needs at
	// least ceil(1/MaxShare) survivors to be satisfiable at all, so with only
	// one or two recommended publishers it is legitimately infeasible and
	// yields rather than stranding budget or dropping a fitting publisher.
	// Copied straight through from pipeline.Allocation.
	ExceedsMaxShare bool `json:"exceeds_max_share,omitempty"`
}

// Budget is the total and its split.
type Budget struct {
	TotalUSD    float64           `json:"total_usd"`
	DailyCapUSD float64           `json:"daily_cap_usd"`
	Allocation  []AllocationEntry `json:"allocation"`
	// UnallocatedUSD is TotalUSD minus the sum of every allocation's
	// AmountUSD, clamped to zero for float noise (anything under one cent).
	// It is nonzero when the recommended publishers' combined deliverable
	// inventory is below the budget: each is capped at what it can physically
	// deliver in the flight, the remainder cannot be spent on this set, and
	// this field is the honest signal of that shortfall.
	UnallocatedUSD float64 `json:"unallocated_usd,omitempty"`
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
