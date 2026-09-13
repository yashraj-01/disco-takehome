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
	"github.com/yashraj/disco/internal/llm"
	"github.com/yashraj/disco/internal/pipeline"
	"github.com/yashraj/disco/internal/render"
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
	fs.StringVar(&c.provider, "provider", "fixture",
		"which LLM backend to use: \"fixture\" replays recorded responses (no API key or network needed) or \"gemini\" calls the live Gemini API")
	fs.StringVar(&c.model, "model", "gemini-2.5-flash", "model id to request when --provider=gemini")
	fs.StringVar(&c.dataDir, "data", "data", "directory holding publishers.json and shopper_personas.json")
	fs.StringVar(&c.fixtureDir, "fixtures", "evals/fixtures", "directory of recorded provider responses used by --provider=fixture, and written to by --provider=gemini --record")
	fs.StringVar(&c.cacheDir, "cache", ".cache", "directory used to cache live --provider=gemini responses on disk so repeat runs of the same brief cost nothing")
	fs.IntVar(&c.rpm, "rpm", 10, "maximum --provider=gemini requests per minute (the Gemini free tier allows 10 on gemini-2.5-flash)")
	fs.BoolVar(&c.record, "record", false, "with --provider=gemini, also write each live response to --fixtures so it can be replayed later with --provider=fixture")
	fs.BoolVar(&c.noCache, "no-cache", false, "with --provider=gemini, bypass the on-disk response cache")

	fs.Float64Var(&c.params.TotalUSD, "budget", c.params.TotalUSD, "total campaign budget in USD to split across publishers")
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

	switch c.provider {
	case "fixture":
		return llm.NewFixture(c.fixtureDir), cat, nil
	case "gemini":
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
		p, err := llm.NewGemini(opts)
		return p, cat, err
	default:
		return nil, nil, fmt.Errorf("unknown provider %q: want fixture or gemini", c.provider)
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
		return fmt.Errorf(`no brief given; try: disco run "We sell premium dog food for senior dogs"`)
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

// cmdServe will start the browsable results server (Task 15). It is stubbed
// here so the disco binary has a stable three-command surface from Task 14
// onward; internal/server does not exist yet and must not be imported until
// Task 15 adds it.
func cmdServe(args []string) error { return fmt.Errorf("serve: not implemented yet") }

// cmdEval will run every example brief and check the invariants (Task 16). It
// is stubbed here for the same reason as cmdServe; internal/eval does not
// exist yet and must not be imported until Task 16 adds it.
func cmdEval(args []string) error { return fmt.Errorf("eval: not implemented yet") }
