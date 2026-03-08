package strategy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const (
	anthropicMessagesURL = "https://api.anthropic.com/v1/messages"
	anthropicVersion     = "2023-06-01"
	deriveMaxTokens      = 2048
	deriveTimeout        = 60 * time.Second
)

const systemPrompt = `You are a web scraping CSS selector engine. You receive HTML snippets from a web page and output a JSON extraction strategy.

You will be shown either:
(A) Pre-detected data regions with sample item HTML, OR
(B) The full simplified page HTML

Your job: identify the repeating data items and write CSS selectors to extract each requested field.

Output ONLY this JSON (no markdown, no explanation):
{
  "site_pattern": "URL pattern (e.g. https://example.com/products/*)",
  "container_selector": "CSS selector for the section/container to scope extraction to (use when the page has multiple similar data tables/lists and you need to target a specific one; omit or set to empty string if there is only one data region)",
  "item_selector": "CSS selector matching each repeating item (within container if set)",
  "fields": [
    {
      "name": "field_name",
      "selector": "CSS selector RELATIVE TO each item",
      "attribute": "text | href | src | any HTML attribute",
      "transform": "optional: trim, parse_price, parse_date, parse_int, parse_float",
      "type": "string | int | float | bool | date",
      "fallbacks": ["alt selector 1"]
    }
  ],
  "pagination": null,
  "confidence": 0.0
}

RULES:
1. ONLY use tags and classes that appear in the provided HTML. Never invent IDs or selectors.
2. field selectors are RELATIVE to item_selector — they select within one item, not the whole page.
3. When shown candidate regions, pick the one whose content best matches the requested fields.
4. If the page has MULTIPLE similar data sections (e.g. several ranking tables), use container_selector to scope to the RIGHT section. Look at the region's context/heading and the user's query to decide which section.
5. Use 2-3 stable CSS classes per selector. Avoid long chains of utility classes.
6. Prefer: tag.class1.class2 over tag:nth-child(n). Use positional selectors only as fallbacks.
7. For "text" attribute: gets the element's text content. For links, use "href" to get the URL.
8. Set confidence honestly: 0.9+ if you clearly see the data, <0.5 if uncertain.
9. If a natural language query is given instead of field names, infer field names from the data.`

// DeriveRequest holds the inputs for strategy derivation.
type DeriveRequest struct {
	SimplifiedHTML   string
	URL              string
	FieldNames       []string
	FieldDescs       map[string]string // optional field descriptions
	Query            string            // natural language query (--query mode)
	Model            string
	APIKey           string
	CandidateRegions []CandidateRegion // pre-detected repeating patterns (optional)
	RawHTML          []byte            // raw HTML for validation-retry loop (optional)
}

// CandidateRegion is a detected repeating data region on the page.
type CandidateRegion struct {
	Selector       string
	ItemCount      int
	Context        string // nearby heading text
	SectionID      string // nearest ancestor element ID (e.g. "market-share")
	Sample         string // first ~2000 chars of region HTML
	ItemSelector   string // CSS selector for a single repeating item
	SingleItemHTML string // HTML of one single item (for the LLM to examine)
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicResponse struct {
	Content []anthropicContent `json:"content"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Derive calls the Anthropic API to derive an extraction strategy from HTML.
// If RawHTML is provided, it validates the strategy against the page and retries
// with feedback if selectors match 0 elements (up to 1 retry).
func Derive(ctx context.Context, req DeriveRequest) (*ExtractionStrategy, error) {
	messages := []anthropicMessage{{Role: "user", Content: buildDerivePrompt(req)}}

	strat, err := callAnthropic(ctx, req.Model, req.APIKey, messages)
	if err != nil {
		return nil, err
	}

	// If we have raw HTML, validate and retry once if selectors fail
	if len(req.RawHTML) > 0 {
		count, issues, _ := ValidateAgainstPage(strat, req.RawHTML)
		if count == 0 && len(issues) > 0 {
			retryMsg := buildRetryPrompt(strat, issues, req.RawHTML)
			messages = append(messages,
				anthropicMessage{Role: "assistant", Content: strat.toJSON()},
				anthropicMessage{Role: "user", Content: retryMsg},
			)
			retryStrat, err := callAnthropic(ctx, req.Model, req.APIKey, messages)
			if err != nil {
				return strat, nil // return original on retry failure
			}
			retryCount, _, _ := ValidateAgainstPage(retryStrat, req.RawHTML)
			if retryCount > count {
				return retryStrat, nil
			}
		}
	}

	return strat, nil
}

func callAnthropic(ctx context.Context, model, apiKey string, messages []anthropicMessage) (*ExtractionStrategy, error) {
	body := anthropicRequest{
		Model:     model,
		MaxTokens: deriveMaxTokens,
		System:    systemPrompt,
		Messages:  messages,
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpCtx, cancel := context.WithTimeout(ctx, deriveTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(httpCtx, http.MethodPost, anthropicMessagesURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("calling Anthropic API: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Anthropic API returned %d: %s", resp.StatusCode, string(respBytes))
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	if apiResp.Error != nil {
		return nil, fmt.Errorf("Anthropic API error: %s", apiResp.Error.Message)
	}

	if len(apiResp.Content) == 0 {
		return nil, fmt.Errorf("empty response from Anthropic API")
	}

	return parseStrategyFromResponse(apiResp.Content[0].Text)
}

// buildRetryPrompt constructs feedback for the LLM when its selectors didn't work.
func buildRetryPrompt(strat *ExtractionStrategy, issues []string, rawHTML []byte) string {
	var sb strings.Builder

	sb.WriteString("Your selectors did NOT work. Issues:\n")
	for _, issue := range issues {
		sb.WriteString("- ")
		sb.WriteString(issue)
		sb.WriteString("\n")
	}

	// Find actual repeating elements to show the LLM what exists
	sb.WriteString("\nThe page does NOT use the selectors you provided. ")
	sb.WriteString("Here is a sample of the actual HTML structure around the data region:\n\n```html\n")
	sample := extractDataRegionSample(rawHTML)
	sb.WriteString(sample)
	sb.WriteString("\n```\n\n")
	sb.WriteString("Look carefully at the ACTUAL tags and classes above. Derive new selectors that match the real HTML structure. Remember: many modern sites use divs with CSS grid/flexbox instead of <table> elements.")

	return sb.String()
}

// extractDataRegionSample finds a likely data region in the HTML and returns
// a representative sample (a few repeated items) for the LLM to examine.
func extractDataRegionSample(html []byte) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(html)))
	if err != nil {
		return "(could not parse HTML)"
	}

	// Strategy: find the container with the most repeated same-tag children
	var bestContainer *goquery.Selection
	bestCount := 0

	doc.Find("div, section, main, ul, ol, table").Each(func(_ int, s *goquery.Selection) {
		children := s.Children()
		if children.Length() < 3 {
			return
		}

		// Count children sharing the same tag
		tagCounts := make(map[string]int)
		children.Each(func(_ int, c *goquery.Selection) {
			if c.Get(0) != nil {
				tag := c.Get(0).Data
				cls, _ := c.Attr("class")
				// Use tag + first few class tokens as the pattern
				key := tag
				if cls != "" {
					fields := strings.Fields(cls)
					if len(fields) > 2 {
						fields = fields[:2]
					}
					key += "." + strings.Join(fields, ".")
				}
				tagCounts[key]++
			}
		})

		for _, count := range tagCounts {
			if count > bestCount && count >= 3 {
				bestCount = count
				bestContainer = s
			}
		}
	})

	if bestContainer == nil {
		// Fallback: return first 2000 chars of body
		body := doc.Find("body")
		if body.Length() > 0 {
			h, _ := body.Html()
			if len(h) > 2000 {
				h = h[:2000] + "\n..."
			}
			return h
		}
		return "(no data region found)"
	}

	// Return the container with just first 3 items for brevity
	h, _ := goquery.OuterHtml(bestContainer)
	if len(h) > 4000 {
		h = h[:4000] + "\n..."
	}
	return h
}

func buildDerivePrompt(req DeriveRequest) string {
	var sb strings.Builder

	sb.WriteString("URL: ")
	sb.WriteString(req.URL)
	sb.WriteString("\n\n")

	if req.Query != "" {
		sb.WriteString("Query: ")
		sb.WriteString(req.Query)
		sb.WriteString("\nInfer appropriate field names from this query and the page content.\n\n")
	}

	hasFields := len(req.FieldNames) > 0 && !(len(req.FieldNames) == 1 && req.FieldNames[0] == "data")
	if hasFields {
		sb.WriteString("Extract these fields: ")
		sb.WriteString(strings.Join(req.FieldNames, ", "))
		sb.WriteString("\n")
		for _, name := range req.FieldNames {
			if desc, ok := req.FieldDescs[name]; ok && desc != "" {
				sb.WriteString(fmt.Sprintf("  %s: %s\n", name, desc))
			}
		}
	}

	// Check if we have candidates with single item HTML
	hasSingleItem := false
	for _, r := range req.CandidateRegions {
		if r.SingleItemHTML != "" {
			hasSingleItem = true
			break
		}
	}

	if hasSingleItem {
		sb.WriteString("\nThe page has multiple data regions. Each region below shows ONE SAMPLE ITEM.\n")
		sb.WriteString("Pick the region whose data matches the requested fields, then derive selectors.\n")
		sb.WriteString("IMPORTANT: If multiple regions share the same item_selector, set container_selector to the region's parent to scope extraction to the correct section.\n\n")

		for i, r := range req.CandidateRegions {
			if r.SingleItemHTML == "" {
				continue
			}
			sb.WriteString(fmt.Sprintf("--- REGION %d (%d items on page)", i+1, r.ItemCount))
			if r.Context != "" {
				sb.WriteString(fmt.Sprintf(", section: %q", r.Context))
			}
			if r.SectionID != "" {
				sb.WriteString(fmt.Sprintf(", id: %q", r.SectionID))
			}
			sb.WriteString(" ---\n")
			if r.SectionID != "" {
				sb.WriteString(fmt.Sprintf("section_id: #%s\n", r.SectionID))
			}
			sb.WriteString(fmt.Sprintf("parent_selector: %s\n", r.Selector))
			sb.WriteString(fmt.Sprintf("item_selector: %s\n", r.ItemSelector))
			sb.WriteString(fmt.Sprintf("```html\n%s\n```\n\n", r.SingleItemHTML))
		}

		sb.WriteString("Choose the best region matching the query/fields. Use its item_selector.\n")
		sb.WriteString("If multiple regions use the same item_selector, set container_selector to scope to the correct section.\n")
		sb.WriteString("PREFER using #section_id as container_selector (e.g. \"#market-share\") — IDs are the most reliable selectors.\n")
		sb.WriteString("Write field selectors that work WITHIN the single item HTML shown.\n")
		sb.WriteString("ONLY use tags/classes visible in the HTML above.\n")
	} else {
		// Fallback: full simplified HTML
		sb.WriteString("\n```html\n")
		html := req.SimplifiedHTML
		if len(html) > 50000 {
			html = html[:50000] + "\n... (truncated)"
		}
		sb.WriteString(html)
		sb.WriteString("\n```\n")
	}

	return sb.String()
}

func extractJSON(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		lines := strings.SplitN(text, "\n", 2)
		if len(lines) == 2 {
			text = lines[1]
		}
		text = strings.TrimSuffix(text, "```")
		text = strings.TrimSpace(text)
	}

	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 || end < start {
		return text
	}
	return text[start : end+1]
}

func parseStrategyFromResponse(text string) (*ExtractionStrategy, error) {
	jsonStr := extractJSON(text)

	var s ExtractionStrategy
	if err := json.Unmarshal([]byte(jsonStr), &s); err != nil {
		return nil, fmt.Errorf("parsing strategy JSON: %w\nraw: %s", err, jsonStr)
	}

	if s.ItemSelector == "" {
		return nil, fmt.Errorf("strategy has empty item_selector")
	}
	if len(s.Fields) == 0 {
		return nil, fmt.Errorf("strategy has no field mappings")
	}

	return &s, nil
}
