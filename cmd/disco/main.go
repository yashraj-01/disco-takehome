// Command disco turns an advertiser's one-line brief into a draft ad campaign:
// ranked publishers with reasons, persona-tuned creatives, and a campaign config.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yashraj/disco/internal/catalog"
	"github.com/yashraj/disco/internal/eval"
	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/measure"
	"github.com/yashraj/disco/internal/pipeline"
	"github.com/yashraj/disco/internal/render"
	"github.com/yashraj/disco/internal/server"
)

const usage = `disco — draft an ad campaign from a one-line brief

  disco run "<brief>"   generate a campaign
  disco serve           browse results at http://localhost:8080
  disco eval            run every example brief and check the invariants
  disco measure         run every example brief and measure reason quality

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
	case "measure":
		err = cmdMeasure(os.Args[2:])
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
	provider   string
	model      string
	dataDir    string
	fixtureDir string
	cacheDir   string
	rpm        int
	record     bool
	noCache    bool
	params     pipeline.AllocParams
}

// registerCommon adds the flags shared by every subcommand to fs. Allocation
// defaults come from pipeline.DefaultAllocParams() rather than being
// hardcoded here a second time, so the flag defaults and the pipeline's own
// defaults can never drift apart.
func registerCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{params: pipeline.DefaultAllocParams()}
	fs.StringVar(&c.provider, "provider", "auto",
		"which LLM backend to use: \"auto\" replays a recorded brief and calls the live "+
			"model for anything new when GEMINI_API_KEY is set, \"fixture\" replays only "+
			"(no key or network needed), \"gemini\" always calls the live API")
	fs.StringVar(&c.model, "model", "gemini-3.5-flash-lite", "model id to request when --provider=gemini (the one the committed fixtures were recorded against)")
	fs.StringVar(&c.dataDir, "data", "data", "directory holding publishers.json and shopper_personas.json")
	fs.StringVar(&c.fixtureDir, "fixtures", "evals/fixtures", "directory of recorded provider responses used by --provider=fixture, and written to by --provider=gemini --record")
	fs.StringVar(&c.cacheDir, "cache", ".cache", "directory used to cache live --provider=gemini responses on disk so repeat runs of the same brief cost nothing")
	fs.IntVar(&c.rpm, "rpm", 10, "maximum --provider=gemini requests per minute (conservative default for the Gemini free tier)")
	fs.BoolVar(&c.record, "record", false, "with --provider=gemini, also write each live response to --fixtures so it can be replayed later with --provider=fixture")
	fs.BoolVar(&c.noCache, "no-cache", false, "with --provider=gemini, bypass the on-disk response cache")

	fs.Float64Var(&c.params.TotalUSD, "budget", c.params.TotalUSD,
		"total campaign budget in USD to split across publishers; 0 (the default) "+
			"sizes the campaign to what the recommended publishers can actually deliver")
	fs.IntVar(&c.params.Days, "days", c.params.Days, "flight length in days")
	fs.Float64Var(&c.params.Gamma, "gamma", c.params.Gamma,
		"how strongly budget concentrates on the best-fit publishers; 1.0 splits budget in direct proportion to fit, higher values concentrate more of it on the top publishers")
	fs.Float64Var(&c.params.MaxShare, "max-share", c.params.MaxShare,
		"maximum fraction of the total budget any single publisher may receive")
	fs.Float64Var(&c.params.SOVCap, "sov-cap", c.params.SOVCap,
		"maximum share of a publisher's monthly impressions to buy")
	fs.Float64Var(&c.params.MinShare, "min-share", c.params.MinShare,
		"minimum budget share worth giving a publisher; a publisher whose computed share falls below this is dropped and its budget redistributed")
	return c
}

// buildProvider returns the configured provider and the loaded catalog.
func buildProvider(c *commonFlags) (llm.Provider, *catalog.Catalog, error) {
	cat, err := catalog.Load(c.dataDir)
	if err != nil {
		return nil, nil, err
	}

	newGemini := func() (llm.Provider, error) {
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
		return llm.NewGemini(opts)
	}

	switch c.provider {
	case "auto":
		// Replay what is recorded; call the model for anything new, but only if
		// a key is actually configured. Without one the chain still answers the
		// recorded briefs and explains itself on a miss, so the offline demo
		// works exactly as before.
		fixture := llm.NewFixture(c.fixtureDir)
		if os.Getenv("GEMINI_API_KEY") == "" {
			return llm.NewChain(fixture, nil), cat, nil
		}
		// Anything drafted live is recorded, so the same brief is free next time.
		saved := c.record
		c.record = true
		live, err := newGemini()
		c.record = saved
		if err != nil {
			return llm.NewChain(fixture, nil), cat, nil
		}
		return llm.NewChain(fixture, live), cat, nil
	case "fixture":
		return llm.NewChain(llm.NewFixture(c.fixtureDir), nil), cat, nil
	case "gemini":
		p, err := newGemini()
		return p, cat, err
	default:
		return nil, nil, fmt.Errorf("unknown provider %q: want auto, fixture or gemini", c.provider)
	}
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: disco run [flags] \"<brief>\"\n\ngenerate a draft ad campaign from a one-line advertiser brief\n\nflags:\n")
		fs.PrintDefaults()
	}
	common := registerCommon(fs)
	asJSON := fs.Bool("json", false, "emit the campaign config as JSON instead of a human-readable summary")
	if err := fs.Parse(args); err != nil {
		return err
	}

	brief := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if brief == "" {
		return fmt.Errorf(`no brief given; try: disco run "We sell premium dog food for senior dogs, targeting owners who care about joint health and longevity. Grain-free, vet-formulated, subscription-based."`)
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

// cmdServe starts the browsable results server: one embedded page plus the
// JSON endpoint it calls, backed by the same pipeline as "disco run".
func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: disco serve [flags]\n\nbrowse campaign drafts at http://localhost:8080\n\nflags:\n")
		fs.PrintDefaults()
	}
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

// cmdEval runs every example brief through the pipeline and checks the
// invariants that can actually be asserted (Task 16).
func cmdEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: disco eval [flags]\n\nrun every example brief and check the invariants\n\nflags:\n")
		fs.PrintDefaults()
	}
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

// cmdMeasure runs every example brief through the pipeline and computes
// quality metrics that eval's binary invariants do not capture — measurement
// rather than a gate. It writes the report as JSON, prints it as text, and
// (unless --save-baseline is set) diffs it against the last saved baseline
// when one exists.
func cmdMeasure(args []string) error {
	fs := flag.NewFlagSet("measure", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: disco measure [flags]\n\nrun every example brief and measure reason quality\n\nflags:\n")
		fs.PrintDefaults()
	}
	common := registerCommon(fs)
	briefsPath := fs.String("briefs", "evals/briefs.txt", "file of numbered example briefs")
	outPath := fs.String("out", "evals/measurements/latest.json", "path to write this run's measurement report as JSON")
	baselinePath := fs.String("baseline", "evals/measurements/baseline.json", "path to the committed baseline report to diff against")
	saveBaseline := fs.Bool("save-baseline", false, "write this run's report to --baseline instead of --out, replacing the committed baseline")
	if err := fs.Parse(args); err != nil {
		return err
	}

	briefs, err := eval.LoadBriefs(*briefsPath)
	if err != nil {
		return err
	}

	provider, cat, err := buildProvider(common)
	if err != nil {
		return err
	}

	opts := pipeline.Options{Provider: provider, Catalog: cat, Params: common.params, Model: common.model}
	campaigns := make([]measure.Campaign, 0, len(briefs))
	for _, b := range briefs {
		c, err := pipeline.Run(context.Background(), b.Text, opts)
		if err != nil {
			return fmt.Errorf("brief %d: %w", b.N, err)
		}
		campaigns = append(campaigns, measure.Campaign{BriefN: b.N, Campaign: c})
	}

	report := measure.Report{
		GeneratedAt: time.Now().UTC(),
		Provider:    provider.Name(),
		Model:       common.model,
		Metrics: map[string]measure.Metric{
			"reason_consistency": measure.ReasonConsistency(campaigns, cat),
		},
	}

	dest := *outPath
	if *saveBaseline {
		dest = *baselinePath
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("measure: %w", err)
	}
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("measure: %w", err)
	}
	if err := report.WriteJSON(f); err != nil {
		f.Close()
		return fmt.Errorf("measure: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("measure: %w", err)
	}
	fmt.Fprintf(os.Stdout, "wrote %s\n", dest)

	if err := report.WriteText(os.Stdout); err != nil {
		return err
	}

	if !*saveBaseline {
		if baseline, err := loadMeasurementReport(*baselinePath); err == nil {
			fmt.Fprintln(os.Stdout, "\n--- diff vs baseline ---")
			fmt.Fprint(os.Stdout, measure.Diff(report, baseline))
		}
	}
	return nil
}

// loadMeasurementReport reads a previously written measure.Report from
// path. A missing or unreadable baseline is not an error the caller should
// surface: it just means there is nothing yet to diff against.
func loadMeasurementReport(path string) (measure.Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return measure.Report{}, err
	}
	defer f.Close()
	var r measure.Report
	if err := json.NewDecoder(f).Decode(&r); err != nil {
		return measure.Report{}, err
	}
	return r, nil
}
