package crawl

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/akdavidsson/trawl/internal/analyze"
	"github.com/akdavidsson/trawl/internal/config"
	"github.com/akdavidsson/trawl/internal/extract"
	"github.com/akdavidsson/trawl/internal/fetch"
	"github.com/akdavidsson/trawl/internal/strategy"
)

// Options configures a crawl run.
type Options struct {
	URL        string
	FetchOpts  fetch.Options
	Strategy   *strategy.ExtractionStrategy // pre-loaded strategy (nil = derive)
	FieldNames []string
	FieldDescs map[string]string
	Query      string // natural language query (--query mode)
	Provider   string
	Model      string
	APIKey     string
	MaxPages   int
	NoCache    bool
	NoHeal     bool
	Verbose    bool
}

// Result holds the output of a full crawl run.
type Result struct {
	Strategy *strategy.ExtractionStrategy
	Extract  *extract.Result
	Pages    int
}

// Run executes the full crawl pipeline: fetch, analyze, derive/load strategy, extract.
func Run(ctx context.Context, opts Options) (*Result, error) {
	// 1. Fetch the page
	if opts.Verbose {
		if opts.FetchOpts.JS {
			fmt.Fprintf(os.Stderr, "[fetch] %s (headless browser)\n", opts.URL)
		} else {
			fmt.Fprintf(os.Stderr, "[fetch] %s\n", opts.URL)
		}
	}
	var html []byte
	var err error
	if opts.FetchOpts.JS {
		html, err = fetch.FetchWithBrowser(opts.URL, opts.FetchOpts)
	} else {
		html, err = fetch.Fetch(opts.URL, opts.FetchOpts)
	}
	if err != nil {
		return nil, fmt.Errorf("fetching page: %w", err)
	}

	// 2. Get or derive strategy
	strat, err := resolveStrategy(ctx, opts, html)
	if err != nil {
		return nil, err
	}

	// 3. Apply strategy to extract data
	if opts.Verbose {
		if strat.ContainerSelector != "" {
			fmt.Fprintf(os.Stderr, "[extract] applying strategy (container: %s, item_selector: %s)\n", strat.ContainerSelector, strat.ItemSelector)
		} else {
			fmt.Fprintf(os.Stderr, "[extract] applying strategy (item_selector: %s)\n", strat.ItemSelector)
		}
		for _, f := range strat.Fields {
			fmt.Fprintf(os.Stderr, "[extract]   field %q: selector=%q attr=%q\n", f.Name, f.Selector, f.Attribute)
		}
	}
	result, err := extract.Apply(strat, html)
	if err != nil {
		// Self-healing: if extraction fails completely, try re-deriving
		if !opts.NoHeal {
			if opts.Verbose {
				fmt.Fprintf(os.Stderr, "[heal] extraction failed, re-deriving strategy...\n")
			}
			strat, err = deriveNewStrategy(ctx, opts, html)
			if err != nil {
				return nil, fmt.Errorf("re-derivation failed: %w", err)
			}
			result, err = extract.Apply(strat, html)
			if err != nil {
				if !opts.FetchOpts.JS && looksJSRendered(html) {
					return nil, fmt.Errorf("%w\n\n  This page appears to be JavaScript-rendered. Try adding --js to use a headless browser", err)
				}
				return nil, fmt.Errorf("extraction after re-derivation: %w", err)
			}
		} else {
			return nil, fmt.Errorf("extraction: %w", err)
		}
	}

	// 4. Check health and self-heal if needed
	if !opts.NoHeal && result != nil {
		health := extract.ComputeHealth(result)
		if opts.Verbose {
			fmt.Fprintf(os.Stderr, "[health] %d records, %.0f%% fields populated\n",
				health.TotalRecords, health.SuccessRate())
		}
		if health.NeedsReInference(70) && health.TotalRecords > 0 {
			if opts.Verbose {
				fmt.Fprintf(os.Stderr, "[heal] low success rate (%.0f%%), re-deriving strategy...\n", health.SuccessRate())
			}
			newStrat, err := deriveNewStrategy(ctx, opts, html)
			if err == nil {
				newResult, err := extract.Apply(newStrat, html)
				if err == nil {
					newHealth := extract.ComputeHealth(newResult)
					if newHealth.SuccessRate() > health.SuccessRate() {
						strat = newStrat
						result = newResult
						if opts.Verbose {
							fmt.Fprintf(os.Stderr, "[heal] improved to %.0f%% success rate\n", newHealth.SuccessRate())
						}
					}
				}
			}
		}
	}

	return &Result{
		Strategy: strat,
		Extract:  result,
		Pages:    1,
	}, nil
}

func resolveStrategy(ctx context.Context, opts Options, html []byte) (*strategy.ExtractionStrategy, error) {
	// If a strategy was pre-loaded (--strategy flag), use it
	if opts.Strategy != nil {
		if opts.Verbose {
			fmt.Fprintf(os.Stderr, "[strategy] using provided strategy\n")
		}
		return opts.Strategy, nil
	}

	// Compute fingerprint for cache lookup
	fingerprint, err := analyze.Fingerprint(html)
	if err != nil {
		return nil, fmt.Errorf("fingerprinting page: %w", err)
	}

	// Try cache
	if !opts.NoCache {
		cached, err := strategy.LoadCached(opts.URL, fingerprint)
		if err != nil {
			if opts.Verbose {
				fmt.Fprintf(os.Stderr, "[strategy] cache error: %v\n", err)
			}
		} else if cached != nil {
			if opts.Verbose {
				fmt.Fprintf(os.Stderr, "[strategy] using cached strategy (fingerprint match)\n")
			}
			return cached, nil
		}
		if opts.Verbose {
			fmt.Fprintf(os.Stderr, "[strategy] no cached strategy found\n")
		}
	}

	// Derive new strategy
	strat, err := deriveNewStrategy(ctx, opts, html)
	if err != nil {
		return nil, err
	}
	strat.Fingerprint = fingerprint

	// Cache it
	if !opts.NoCache {
		if err := strategy.SaveCache(strat); err != nil {
			if opts.Verbose {
				fmt.Fprintf(os.Stderr, "[strategy] cache write error: %v\n", err)
			}
		} else if opts.Verbose {
			fmt.Fprintf(os.Stderr, "[strategy] cached strategy\n")
		}
	}

	return strat, nil
}

func deriveNewStrategy(ctx context.Context, opts Options, html []byte) (*strategy.ExtractionStrategy, error) {
	// Validate API config
	cfg := &config.Config{APIKey: opts.APIKey, Model: opts.Model}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Detect candidate data regions — gives the LLM focused item HTML
	candidates := detectCandidates(html, opts.Query, opts.Verbose)

	// Only simplify the full page if we have no candidates (fallback)
	simplified := ""
	if len(candidates) == 0 {
		var err error
		simplified, err = analyze.SimplifyHTML(html)
		if err != nil {
			return nil, fmt.Errorf("simplifying HTML: %w", err)
		}
	}

	if opts.Verbose {
		fmt.Fprintf(os.Stderr, "[strategy] deriving via LLM (%s)...\n", opts.Model)
	}

	strat, err := strategy.Derive(ctx, strategy.DeriveRequest{
		SimplifiedHTML:   simplified,
		URL:              opts.URL,
		FieldNames:       opts.FieldNames,
		FieldDescs:       opts.FieldDescs,
		Query:            opts.Query,
		Provider:         opts.Provider,
		Model:            opts.Model,
		APIKey:           opts.APIKey,
		CandidateRegions: candidates,
		RawHTML:          html,
	})
	if err != nil {
		return nil, fmt.Errorf("strategy derivation: %w", err)
	}

	if opts.Verbose {
		fmt.Fprintf(os.Stderr, "[strategy] derived (confidence: %.2f)\n", strat.Confidence)
	}

	return strat, nil
}

func detectCandidates(html []byte, query string, verbose bool) []strategy.CandidateRegion {
	regions, err := analyze.DetectCandidateRegions(html)
	if err != nil || len(regions) == 0 {
		return nil
	}

	if verbose {
		fmt.Fprintf(os.Stderr, "[analyze] detected %d candidate data region(s)\n", len(regions))
	}

	// If a query is provided, re-sort regions to prioritize those whose
	// context/heading matches the query. This ensures the LLM sees the
	// relevant section even if it ranks lower by size.
	if query != "" {
		queryLower := strings.ToLower(query)
		queryWords := strings.Fields(queryLower)
		reorderByQueryRelevance(regions, queryWords)
	}

	// Convert to strategy.CandidateRegion with samples.
	// Deduplicate by context so the LLM sees diverse page sections
	// (not 5 variants of the same "Top Models" table).
	seenContext := make(map[string]int) // context -> count of regions with that context
	var candidates []strategy.CandidateRegion
	for _, r := range regions {
		singleItem := r.SingleItemHTML
		if len(singleItem) > 2000 {
			singleItem = singleItem[:2000] + "\n..."
		}
		if singleItem == "" {
			continue
		}

		// Build item CSS selector
		itemSel := r.ItemTag
		if r.ItemClass != "" {
			classes := strings.Fields(r.ItemClass)
			if len(classes) > 3 {
				classes = classes[:3]
			}
			itemSel += "." + strings.Join(classes, ".")
		}

		// Limit duplicate contexts: allow at most 2 regions per section heading.
		// This ensures we see diverse sections (Top Models, Market Share, Top Apps)
		// instead of 5 variants of the same section.
		ctxKey := strings.ToLower(r.Context)
		if ctxKey == "" {
			ctxKey = "_no_context_"
		}
		if seenContext[ctxKey] >= 2 {
			continue
		}
		seenContext[ctxKey]++

		if verbose {
			snippet := singleItem
			if len(snippet) > 200 {
				snippet = snippet[:200] + "..."
			}
			fmt.Fprintf(os.Stderr, "[analyze]   region %d: selector=%q items=%d itemSel=%q context=%q\n", len(candidates)+1, r.Selector, r.ItemCount, itemSel, r.Context)
			fmt.Fprintf(os.Stderr, "[analyze]     singleItem: %s\n", snippet)
		}

		sample := r.HTML
		if len(sample) > 2000 {
			sample = sample[:2000] + "\n..."
		}

		candidates = append(candidates, strategy.CandidateRegion{
			Selector:       r.Selector,
			ItemCount:      r.ItemCount,
			Context:        r.Context,
			SectionID:      r.SectionID,
			Sample:         sample,
			ItemSelector:   itemSel,
			SingleItemHTML: singleItem,
		})
		if len(candidates) >= 7 {
			break
		}
	}
	return candidates
}

// reorderByQueryRelevance moves regions with query-matching context to the front,
// preserving their relative order. Non-matching regions follow in original order.
func reorderByQueryRelevance(regions []analyze.CandidateRegion, queryWords []string) {
	var matching, rest []analyze.CandidateRegion
	for _, r := range regions {
		ctxLower := strings.ToLower(r.Context)
		idLower := strings.ToLower(r.SectionID)
		// Also check ID with hyphens replaced (e.g. "market-share" matches "market share")
		idWords := strings.ReplaceAll(idLower, "-", " ")
		htmlLower := strings.ToLower(r.SingleItemHTML)
		matched := false
		for _, w := range queryWords {
			if strings.Contains(ctxLower, w) || strings.Contains(idLower, w) ||
				strings.Contains(idWords, w) || strings.Contains(htmlLower, w) {
				matched = true
				break
			}
		}
		if matched {
			matching = append(matching, r)
		} else {
			rest = append(rest, r)
		}
	}
	copy(regions, append(matching, rest...))
}

// looksJSRendered checks if the HTML looks like a client-side SPA
// where content is loaded dynamically by JavaScript.
func looksJSRendered(data []byte) bool {
	limit := len(data)
	if limit > 16384 {
		limit = 16384
	}
	s := strings.ToLower(string(data[:limit]))

	markers := []string{
		`id="__next"`,
		`__next_data__`,
		`data-reactroot`,
		`window.__react`,
		`id="app"`,
		`id="root"`,
		`ng-app`,
		`data-v-`,
		`gradio`,
		`svelte`,
		`__svelte`,
		`__nuxt`,
	}

	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}
