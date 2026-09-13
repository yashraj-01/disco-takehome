package model

import "strings"

// NormalizeLever canonicalizes a messaging lever (or any other free-text
// claim) for case- and whitespace-insensitive comparison. It exists so that
// the production filter (internal/pipeline/creative.go's groundedIn) and the
// eval assertion (internal/eval/assertions.go) apply exactly one rule for
// what counts as "the same lever" — a lever with leading or trailing
// whitespace must be treated identically by both, or it can pass the
// production filter and still fail the eval as a self-inflicted false
// failure.
func NormalizeLever(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
