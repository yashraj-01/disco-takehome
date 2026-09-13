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
