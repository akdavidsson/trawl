![trawl](img/logo.png)

# trawl

Scrape structured data from any website using LLM-powered extraction.

trawl lets you define *what* you want semantically — not with CSS selectors — and it figures out *how* to extract it. When a site redesigns, trawl re-derives the extraction strategy automatically. The LLM is called **once per site structure**, not once per page. Steady-state scraping is pure Go at full speed with zero API cost.

---

## Install

```bash
go install github.com/akdavidsson/trawl@latest
```

Or build from source:

```bash
git clone https://github.com/akdavidsson/trawl
cd trawl
go build -o trawl .
```

---

## Quickstart

```bash
export ANTHROPIC_API_KEY=sk-ant-...

# Extract product data as JSON
trawl "https://books.toscrape.com" --fields "title, price, rating, in_stock"

# Output as CSV
trawl "https://books.toscrape.com" --fields "title, price" --format csv

# Preview the extraction plan without extracting
trawl "https://books.toscrape.com" --fields "title, price" --plan
```

---

## Usage

```
trawl [url] [flags]
```

### Examples

```bash
# Simple field extraction
trawl "https://example.com/products" --fields "name, price, rating, url" --format json

# Use a YAML schema for precise control
trawl "https://example.com/products" --schema products.yaml --format csv

# Natural language query — trawl infers field names from the data
trawl "https://example.com/products" --query "extract all product listings with names, prices, and stock status"

# Target a specific section on a page with multiple data tables
trawl "https://openrouter.ai/rankings" --query "Market Share" --fields "rank, name, tokens" --js

# Save to a file
trawl "https://example.com/products" --fields "name, price" --output products.json

# Streaming JSONL output, pipe to jq
trawl "https://news.example.com" --fields "headline, date, author" --format jsonl | jq '.headline'

# Re-use a previously derived strategy (no LLM call)
trawl "https://example.com/products" --strategy cached-strategy.json --format csv

# Verbose output to see the full pipeline
trawl "https://example.com/products" --fields "name, price" -v

# JS-rendered pages (React, Next.js, Vue, Svelte, etc.)
trawl "https://example.com/spa" --fields "name, value" --js

# Iframe-embedded apps (e.g. HuggingFace Spaces) — extra wait for content to load
trawl "https://huggingface.co/spaces/open-llm-leaderboard/open_llm_leaderboard" \
    --fields "rank, model, average" --js --wait 10s

# Custom headers and cookies
trawl "https://example.com/dashboard" --fields "metric, value" \
    --headers "Authorization: Bearer token123" \
    --cookie "session=abc123"
```

---

## Flags

### Input

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--fields` | `-f` | | Comma-separated field names to extract |
| `--query` | `-q` | | Natural language description of what to extract |
| `--schema` | `-s` | | Path to YAML schema file |

**`--query`** is especially useful for pages with multiple data sections. The query text is matched against section headings and HTML IDs to prioritize the right data region, and is passed to the LLM so it can select the most relevant section. When used together with `--fields`, the query guides section selection while the fields define the output structure.

### Output

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--format` | | `json` | Output format: `json`, `jsonl`, `csv`, `parquet` |
| `--output` | `-o` | stdout | Write output to file instead of stdout |

### Crawling

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--max-pages` | `-n` | `1` | Maximum pages to crawl |
| `--paginate` | | | Auto-detect and follow pagination |
| `--concurrency` | `-c` | `10` | Number of concurrent workers |
| `--delay` | | `1s` | Delay between requests to same domain |
| `--no-robots` | | | Ignore robots.txt (use responsibly) |
| `--js` | | | Enable headless browser for JS-rendered pages |
| `--wait` | | `0` | Extra time to wait after page load with `--js` (e.g. `5s`) |
| `--timeout` | | `30s` | Per-request timeout |
| `--headers` | | | Custom headers (`"Key: Value"` format) |
| `--cookie` | | | Cookie string to include |

### Strategy

| Flag | Default | Description |
|------|---------|-------------|
| `--strategy` | | Path to a cached extraction strategy JSON file |
| `--plan` | | Dry run: show the LLM-derived extraction plan, don't extract |
| `--no-cache` | | Don't cache or use cached strategies |
| `--no-heal` | | Disable self-healing (don't re-derive on failure) |

### LLM

| Flag | Default | Description |
|------|---------|-------------|
| `--model` | `claude-sonnet-4-6` | Anthropic model to use |
| `--no-llm` | | Disable LLM, use heuristic extraction only |

### General

| Flag | Short | Description |
|------|-------|-------------|
| `--verbose` | `-v` | Verbose output (show strategy derivation, health stats) |
| `--help` | `-h` | Help |

---

## How it works

```
URL ──► Fetch ──► Detect Data Regions ──► LLM Strategy Derivation ──► Extraction Strategy
                                                                              │
                                                                              ▼
         Output (JSON/CSV/JSONL) ◄────────────────────── Apply Strategy via CSS Selectors (Go)
                                                                  │
                                                           [Strategy fails?]
                                                                  │
                                                         Re-derive from new HTML
```

### The pipeline in detail

1. **Fetch** the target URL with configurable headers, cookies, and timeouts. With `--js`, uses a headless Chromium browser to render JavaScript. The browser automatically:
   - **Waits for DOM stability** — polls the page until element counts stop changing and skeleton loading placeholders (`.animate-pulse`, etc.) are resolved, ensuring React/Next.js SPAs finish rendering.
   - **Scrolls the page** to trigger intersection-observer lazy loading, so data sections further down the page are rendered.
   - **Clicks "Show more" / "Load more" buttons** to expand hidden data (up to 3 rounds).
   - **Captures iframe content** — sites like HuggingFace Spaces embed their app inside an iframe. trawl inspects all iframes, compares content richness, and uses the richest source.
2. **Detect candidate data regions** using heuristic analysis: find tables, lists, and repeated div/section patterns. Each region is scored by content richness (average item size) to distinguish real data from navigation, footers, and SVG charts. Section headings and HTML `id` attributes are captured for context.
3. **Check cache**: if a strategy exists for this URL pattern + structural fingerprint, skip the LLM entirely.
4. **Derive strategy** via Anthropic API: send focused single-item HTML snippets from the top candidate regions (not the full page), along with section context, query text, and field descriptions. The LLM returns CSS selectors, a `container_selector` to scope extraction to the correct page section, attribute mappings, transforms, and fallback selectors. If the selectors fail validation against the page, a retry with feedback is attempted automatically.
5. **Extract** data using pure Go + goquery: apply CSS selectors within the scoped container. Records where most fields are null (from mismatched sections) are automatically filtered out.
6. **Monitor health**: track what percentage of fields were populated. If it drops below 70%, trigger self-healing — re-derive the strategy and keep whichever produces better results.
7. **Output** results as JSON, JSONL, CSV, or Parquet.

The LLM is called **once** to figure out the selectors. Every subsequent page with the same structure uses the cached strategy — pure Go, no API calls, no token cost.

### Extraction strategy

The LLM returns a JSON strategy like this:

```json
{
  "site_pattern": "https://example.com/products/*",
  "container_selector": "#product-list",
  "item_selector": "div.product-card",
  "fields": [
    {
      "name": "name",
      "selector": "h2.product-title",
      "attribute": "text",
      "type": "string",
      "fallbacks": ["h3.title", ".product-name"]
    },
    {
      "name": "price",
      "selector": "span.price",
      "attribute": "text",
      "transform": "parse_price",
      "type": "float"
    }
  ],
  "pagination": {
    "type": "next_link",
    "selector": "a.next-page",
    "has_more": "a.next-page"
  },
  "confidence": 0.95,
  "fingerprint": "a8f3e2b1..."
}
```

- **`container_selector`** scopes extraction to a specific page section. This is critical for pages with multiple similar data tables (e.g. "Top Models" and "Market Share" on the same page both using `div.grid` items). The LLM uses HTML `id` attributes (e.g. `#market-share`) and section headings to pick the right container.
- **`item_selector`** matches each repeating data item within the container.
- Each **field** has a primary CSS selector (relative to the item), an attribute to read (`text`, `href`, `src`, or any HTML attribute), an optional transform (`parse_price`, `parse_date`, `trim`, `parse_int`, `parse_float`), and fallback selectors for resilience.

### Self-healing

```
Extract page
     │
     ├── All fields populated ──────────── continue
     │
     ├── Some fields empty (< 70%) ─────── re-derive strategy via LLM
     │                                       └── use new strategy if it improves success rate
     │
     └── Total failure (0 items matched) ── re-derive strategy via LLM
                                              └── resume with new strategy
```

When a site redesigns, the structural fingerprint changes, the cached strategy is bypassed, and trawl automatically derives a new one. No manual intervention needed.

### JavaScript and iframe support

Many modern sites render content with JavaScript or embed apps inside iframes. trawl handles both:

- **`--js`** launches a headless Chromium browser (auto-downloaded on first use via [rod](https://github.com/go-rod/rod)) to render the page before extraction.
- **DOM stability detection** — trawl polls the page until the DOM element count stabilizes and all skeleton loading placeholders (`.animate-pulse`, `[class*="skeleton"]`) are resolved. This ensures data-heavy SPAs built with React, Next.js, Vue, etc. are fully rendered.
- **Lazy loading support** — the browser scrolls through the entire page to trigger intersection-observer lazy loading, ensuring sections further down the page are rendered before capture.
- **Auto-expand** — buttons matching common patterns ("Show more", "Load more", "View all", "See all", "Expand") are automatically clicked to reveal hidden data. This repeats up to 3 rounds to handle cascading reveals.
- **`--wait`** adds extra wait time after all automatic detection for edge cases (e.g. `--wait 5s`).
- **Iframe detection** — sites like HuggingFace Spaces embed their actual app inside an iframe. trawl automatically inspects all iframes on the page, compares their content richness against the outer page, and uses the iframe content when it contains more extractable data. No special flags needed — just use `--js`.

```bash
# JS-rendered SPA
trawl "https://example.com/react-app" --fields "name, value" --js

# Iframe-embedded app with extra wait
trawl "https://huggingface.co/spaces/open-llm-leaderboard/open_llm_leaderboard" \
    --fields "rank, model, average" --js --wait 10s
```

If trawl detects that a page appears to be JavaScript-rendered but `--js` wasn't used, it will suggest adding it in the error message.

### Multi-section pages

Pages often contain multiple data tables or lists (e.g. "Top Models", "Market Share", "Top Apps" on a rankings page). trawl handles this through:

1. **Candidate region detection** — heuristically identifies all repeating data regions on the page, captures section headings and HTML `id` attributes for context.
2. **Query-based prioritization** — when `--query` is provided, regions whose section heading or `id` matches the query are prioritized (e.g. `--query "Market Share"` matches `id="market-share"`).
3. **Context deduplication** — at most 2 candidate regions per section heading are sent to the LLM, ensuring diverse page sections are represented.
4. **Container scoping** — the LLM sets `container_selector` (preferring `#section-id` selectors) to scope extraction to the correct section.
5. **Null filtering** — records where most fields are null (from adjacent sections with different HTML structure) are automatically dropped.

```bash
# Target the "Market Share" section specifically
trawl "https://openrouter.ai/rankings" --query "Market Share" --fields "rank, name, tokens" --js

# Target the main leaderboard
trawl "https://openrouter.ai/rankings" --query "LLM Leaderboard" --fields "rank, name, tokens" --js
```

### Preview the plan

Use `--plan` to see what trawl will do without extracting:

```
$ trawl "https://example.com/products" --fields "name, price" --plan

Strategy for https://example.com/products
  Container: #product-list
  Item selector: div.product-card
  Fields:
    name:                h2.product-title -> text (string)
    price:               span.price -> text -> parse_price (float)
  Pagination: a.next-page -> href (next_link)
  Confidence: 0.95
  Fingerprint: a8f3e2b1
  Items found: 24
```

---

## Schema files

For complex or recurring extractions, define a YAML schema:

```yaml
name: product_listing
url: "https://example.com/products/*"
fields:
  - name: product_name
    type: string
    description: "The product's display name"
  - name: price
    type: float
    description: "Price in local currency"
  - name: currency
    type: string
    description: "Currency code (USD, EUR, etc.)"
  - name: in_stock
    type: bool
    description: "Whether the item is available"
  - name: rating
    type: float
    nullable: true
    description: "Star rating out of 5"
```

Field descriptions are passed to the LLM to improve selector accuracy. Supported types: `string`, `int`, `float`, `bool`, `date`, `datetime`.

See the `examples/` directory for more schema files.

---

## Configuration

trawl reads configuration from the environment and an optional config file.

**Environment variable:**

```bash
export ANTHROPIC_API_KEY=sk-ant-...
```

**Config file** (`~/.trawl/config.yaml`):

```yaml
api_key: sk-ant-...
model: claude-sonnet-4-6
```

Environment variables take precedence over the config file. The `--model` flag takes precedence over both.

**Strategy cache** is stored in `~/.trawl/strategies/`. Strategies are keyed by URL pattern + page structure fingerprint. Use `--no-cache` to bypass.

---

## Output

- **stdout** — structured data only (JSON, JSONL, or CSV)
- **stderr** — warnings, verbose logs, and strategy derivation status

This makes trawl pipeline-friendly:

```bash
trawl "https://example.com/products" --fields "name, price" --format csv | csvkit | ...
trawl "https://example.com/products" --fields "name, price" --format jsonl | jq 'select(.price > 50)'
```

Type coercion is soft: if a value cannot be parsed to the target type, trawl emits a warning on stderr and falls back to the raw string (or `null` for nullable fields), rather than aborting.

---

## How trawl compares

| Tool | Approach | LLM? | Self-heals? | Speed |
|------|----------|------|-------------|-------|
| Scrapy | Hardcoded selectors | No | No | Fast |
| Playwright/Puppeteer | Hardcoded selectors | No | No | Medium |
| ScrapeGraphAI | Full LLM extraction | Every page | Inherent | Slow, expensive |
| Firecrawl | LLM page-by-page | Every page | Inherent | Slow, expensive |
| **trawl** | LLM strategy + Go extraction | Once per structure | Yes | Fast |

trawl uses the LLM for **intelligence** (figuring out the right selectors) and Go for **throughput** (applying them at scale). Competitors either skip the LLM (brittle) or use it on every page (slow and expensive).

---

## Requirements

- Go 1.24+
- `ANTHROPIC_API_KEY` (not required for `--plan` with `--strategy`)
