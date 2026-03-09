package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/akdavidsson/trawl/internal/analyze"
	"github.com/akdavidsson/trawl/internal/config"
	"github.com/akdavidsson/trawl/internal/crawl"
	"github.com/akdavidsson/trawl/internal/extract"
	"github.com/akdavidsson/trawl/internal/fetch"
	"github.com/akdavidsson/trawl/internal/output"
	"github.com/akdavidsson/trawl/internal/schema"
	"github.com/akdavidsson/trawl/internal/strategy"
)

var (
	// Input
	flagFields string
	flagQuery  string
	flagSchema string

	// Output
	flagFormat string
	flagOutput string

	// Crawling
	flagMaxPages    int
	flagPaginate    bool
	flagConcurrency int
	flagDelay       time.Duration
	flagNoRobots    bool
	flagJS          bool
	flagWait        time.Duration
	flagTimeout     time.Duration
	flagHeaders     []string
	flagCookie      string

	// Strategy
	flagStrategy string
	flagPlan     bool
	flagNoCache  bool
	flagNoHeal   bool

	// LLM
	flagModel string
	flagNoLLM bool

	// General
	flagVerbose bool
)

var rootCmd = &cobra.Command{
	Use:   "trawl [url]",
	Short: "Scrape structured data from any website using LLM-powered extraction",
	Long: `trawl scrapes structured data from websites using LLM-powered extraction.
You define what you want semantically, not with CSS selectors, and it
self-heals when sites change.

The LLM is called once per site structure, not once per page.
Steady-state scraping is pure Go at full speed with zero API cost.`,
	Args:          cobra.ExactArgs(1),
	RunE:          run,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	// Input
	rootCmd.Flags().StringVarP(&flagFields, "fields", "f", "", "comma-separated field names to extract")
	rootCmd.Flags().StringVarP(&flagQuery, "query", "q", "", "natural language description of what to extract")
	rootCmd.Flags().StringVarP(&flagSchema, "schema", "s", "", "path to YAML schema file")

	// Output
	rootCmd.Flags().StringVar(&flagFormat, "format", "json", "output format: json, jsonl, csv, parquet")
	rootCmd.Flags().StringVarP(&flagOutput, "output", "o", "", "output file path (default: stdout)")

	// Crawling
	rootCmd.Flags().IntVarP(&flagMaxPages, "max-pages", "n", 1, "maximum pages to crawl")
	rootCmd.Flags().BoolVar(&flagPaginate, "paginate", false, "auto-detect and follow pagination")
	rootCmd.Flags().IntVarP(&flagConcurrency, "concurrency", "c", 10, "number of concurrent workers")
	rootCmd.Flags().DurationVar(&flagDelay, "delay", 1*time.Second, "delay between requests to same domain")
	rootCmd.Flags().BoolVar(&flagNoRobots, "no-robots", false, "ignore robots.txt")
	rootCmd.Flags().BoolVar(&flagJS, "js", false, "enable headless browser for JS-rendered pages")
	rootCmd.Flags().DurationVar(&flagWait, "wait", 0, "extra time to wait after page load with --js (e.g. 5s)")
	rootCmd.Flags().DurationVar(&flagTimeout, "timeout", 30*time.Second, "per-request timeout")
	rootCmd.Flags().StringSliceVar(&flagHeaders, "headers", nil, `custom headers ("Key: Value" format)`)
	rootCmd.Flags().StringVar(&flagCookie, "cookie", "", "cookie string to include")

	// Strategy
	rootCmd.Flags().StringVar(&flagStrategy, "strategy", "", "path to a cached extraction strategy JSON file")
	rootCmd.Flags().BoolVar(&flagPlan, "plan", false, "dry run: show the extraction plan, don't extract")
	rootCmd.Flags().BoolVar(&flagNoCache, "no-cache", false, "don't cache or use cached strategies")
	rootCmd.Flags().BoolVar(&flagNoHeal, "no-heal", false, "disable self-healing")

	// LLM
	rootCmd.Flags().StringVar(&flagModel, "model", "", "LLM model (default depends on which API key is set)")
	rootCmd.Flags().BoolVar(&flagNoLLM, "no-llm", false, "disable LLM, use heuristic extraction only")

	// General
	rootCmd.Flags().BoolVarP(&flagVerbose, "verbose", "v", false, "verbose output")
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func run(cmd *cobra.Command, args []string) error {
	targetURL := args[0]
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		targetURL = "https://" + targetURL
	}

	// 1. Load config
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Verbose = flagVerbose
	if flagModel != "" {
		cfg.Model = flagModel
	}

	// 2. Parse schema/fields
	userSchema, err := parseUserSchema()
	if err != nil {
		return err
	}

	// 3. Build fetch options
	fetchOpts := fetch.Options{
		Timeout:   flagTimeout,
		Cookie:    flagCookie,
		Headers:   parseHeaders(flagHeaders),
		JS:        flagJS,
		WaitExtra: flagWait,
	}

	// 4. Load pre-existing strategy if provided
	var strat *strategy.ExtractionStrategy
	if flagStrategy != "" {
		strat, err = strategy.LoadFromFile(flagStrategy)
		if err != nil {
			return fmt.Errorf("loading strategy: %w", err)
		}
		if flagVerbose {
			fmt.Fprintf(os.Stderr, "[strategy] loaded from %s\n", flagStrategy)
		}
	}

	// 5. If --plan, derive and display strategy, then exit
	if flagPlan {
		return runPlan(cmd.Context(), targetURL, cfg, userSchema, fetchOpts)
	}

	// 6. Build field descriptions for the LLM
	fieldDescs := make(map[string]string)
	for _, f := range userSchema.Fields {
		if f.Description != "" {
			fieldDescs[f.Name] = f.Description
		}
	}

	// 7. Run the crawl pipeline
	result, err := crawl.Run(cmd.Context(), crawl.Options{
		URL:        targetURL,
		FetchOpts:  fetchOpts,
		Strategy:   strat,
		FieldNames: userSchema.FieldNames(),
		FieldDescs: fieldDescs,
		Query:      flagQuery,
		Provider:   cfg.Provider,
		Model:      cfg.Model,
		APIKey:     cfg.APIKey,
		MaxPages:   flagMaxPages,
		NoCache:    flagNoCache,
		NoHeal:     flagNoHeal,
		Verbose:    flagVerbose,
	})
	if err != nil {
		return err
	}

	// 8. Print warnings
	for _, w := range result.Extract.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	if flagVerbose {
		health := extract.ComputeHealth(result.Extract)
		fmt.Fprintf(os.Stderr, "[output] %d records, health: %.0f%%\n",
			health.TotalRecords, health.SuccessRate())
	}

	// 9. Write output
	return writeOutput(result.Extract, flagFormat, flagOutput)
}

func parseUserSchema() (*schema.Schema, error) {
	switch {
	case flagSchema != "":
		return schema.ParseYAML(flagSchema)
	case flagFields != "":
		return schema.ParseFields(flagFields)
	case flagQuery != "":
		// For --query mode, create a minimal schema; the LLM will figure out fields
		return &schema.Schema{
			Fields: []schema.Field{{Name: "data", Type: schema.TypeString}},
		}, nil
	default:
		return nil, fmt.Errorf("specify fields with --fields, --schema, or --query")
	}
}

func parseHeaders(raw []string) map[string]string {
	headers := make(map[string]string)
	for _, h := range raw {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) == 2 {
			headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return headers
}

func runPlan(ctx context.Context, url string, cfg *config.Config, userSchema *schema.Schema, fetchOpts fetch.Options) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	// Fetch sample page
	if cfg.Verbose {
		fmt.Fprintf(os.Stderr, "[fetch] %s\n", url)
	}
	var html []byte
	var fetchErr error
	if fetchOpts.JS {
		html, fetchErr = fetch.FetchWithBrowser(url, fetchOpts)
	} else {
		html, fetchErr = fetch.Fetch(url, fetchOpts)
	}
	if fetchErr != nil {
		return fmt.Errorf("fetching page: %w", fetchErr)
	}

	// Compute fingerprint
	fingerprint, err := analyze.Fingerprint(html)
	if err != nil {
		return fmt.Errorf("fingerprinting: %w", err)
	}

	// Build field descriptions
	fieldDescs := make(map[string]string)
	for _, f := range userSchema.Fields {
		if f.Description != "" {
			fieldDescs[f.Name] = f.Description
		}
	}

	// Detect candidate regions
	candidates := buildCandidates(html)

	// Only simplify full HTML if we have no candidates
	simplified := ""
	if len(candidates) == 0 {
		simplified, err = analyze.SimplifyHTML(html)
		if err != nil {
			return fmt.Errorf("simplifying HTML: %w", err)
		}
	}

	// Derive strategy
	fmt.Fprintf(os.Stderr, "[strategy] deriving via LLM (%s)...\n", cfg.Model)
	strat, err := strategy.Derive(ctx, strategy.DeriveRequest{
		SimplifiedHTML:   simplified,
		URL:              url,
		FieldNames:       userSchema.FieldNames(),
		FieldDescs:       fieldDescs,
		Query:            flagQuery,
		Provider:         cfg.Provider,
		Model:            cfg.Model,
		APIKey:           cfg.APIKey,
		CandidateRegions: candidates,
		RawHTML:          html,
	})
	if err != nil {
		return fmt.Errorf("strategy derivation: %w", err)
	}
	strat.Fingerprint = fingerprint

	// Validate against the page
	count, issues, _ := strategy.ValidateAgainstPage(strat, html)

	// Display the plan
	fmt.Printf("Strategy for %s\n", url)
	if strat.ContainerSelector != "" {
		fmt.Printf("  Container: %s\n", strat.ContainerSelector)
	}
	fmt.Printf("  Item selector: %s\n", strat.ItemSelector)
	fmt.Printf("  Fields:\n")
	for _, f := range strat.Fields {
		attr := f.Attribute
		if f.Transform != "" {
			attr += " -> " + f.Transform
		}
		fmt.Printf("    %-20s %s -> %s (%s)\n", f.Name+":", f.Selector, attr, f.Type)
	}
	if strat.Pagination != nil && strat.Pagination.Type != "none" {
		fmt.Printf("  Pagination: %s -> %s (%s)\n", strat.Pagination.Selector, strat.Pagination.Type, strat.Pagination.Type)
	} else {
		fmt.Printf("  Pagination: none detected\n")
	}
	fmt.Printf("  Confidence: %.2f\n", strat.Confidence)
	fmt.Printf("  Fingerprint: %s\n", fingerprint)
	fmt.Printf("  Items found: %d\n", count)
	if len(issues) > 0 {
		fmt.Printf("  Issues:\n")
		for _, issue := range issues {
			fmt.Printf("    - %s\n", issue)
		}
	}

	// Also output the strategy as JSON for --strategy reuse
	stratJSON, _ := json.MarshalIndent(strat, "", "  ")
	fmt.Printf("\nStrategy JSON (save with --strategy):\n%s\n", string(stratJSON))

	return nil
}

func writeOutput(result *extract.Result, format, outputPath string) error {
	w := os.Stdout
	if outputPath != "" {
		f, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("creating output file: %w", err)
		}
		defer f.Close()
		w = f
	}

	switch strings.ToLower(format) {
	case "json":
		return output.WriteJSON(w, result)
	case "jsonl":
		return output.WriteJSONL(w, result)
	case "csv":
		return output.WriteCSV(w, result)
	case "parquet":
		return output.WriteParquet(w, result)
	default:
		return fmt.Errorf("unknown format %q; use json, jsonl, csv, or parquet", format)
	}
}

func buildCandidates(html []byte) []strategy.CandidateRegion {
	regions, _ := analyze.DetectCandidateRegions(html)
	var candidates []strategy.CandidateRegion
	for _, r := range regions {
		singleItem := r.SingleItemHTML
		if len(singleItem) > 2000 {
			singleItem = singleItem[:2000] + "\n..."
		}
		itemSel := r.ItemTag
		if r.ItemClass != "" {
			classes := strings.Fields(r.ItemClass)
			if len(classes) > 3 {
				classes = classes[:3]
			}
			itemSel += "." + strings.Join(classes, ".")
		}
		if singleItem == "" {
			continue
		}
		candidates = append(candidates, strategy.CandidateRegion{
			Selector:       r.Selector,
			ItemCount:      r.ItemCount,
			Context:        r.Context,
			ItemSelector:   itemSel,
			SingleItemHTML: singleItem,
		})
		if len(candidates) >= 5 {
			break
		}
	}
	return candidates
}
