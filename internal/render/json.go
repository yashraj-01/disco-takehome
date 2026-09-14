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
