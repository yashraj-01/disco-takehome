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
	"github.com/yashraj/disco/internal/eval"
	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/pipeline"
	"github.com/yashraj/disco/internal/render"
	"github.com/yashraj/disco/internal/server"
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
