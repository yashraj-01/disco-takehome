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
