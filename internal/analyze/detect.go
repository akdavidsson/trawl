package analyze

import (
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// CandidateRegion represents a part of the page that likely contains
// repeating data (tables, lists, grids of similar elements).
type CandidateRegion struct {
	HTML           string // simplified HTML of the region
	Tag            string // root element tag (table, ul, div, etc.)
	Selector       string // CSS selector to find this region
	ItemCount      int    // number of repeated items found
	HasHeaders     bool   // whether the region has obvious headers (th, thead)
	Context        string // nearby heading or caption text
	SectionID      string // nearest ancestor element ID (e.g. "market-share", "leaderboard")
	ItemTag        string // tag of the repeating child element (e.g. "div", "tr", "li")
	ItemClass      string // class of the repeating child (first few tokens)
	SingleItemHTML string // outer HTML of one single item
}

// DetectCandidateRegions scans an HTML document for regions that likely
// contain structured, repeating data. Returns regions sorted by likelihood.
func DetectCandidateRegions(html []byte) ([]CandidateRegion, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(html)))
	if err != nil {
		return nil, err
	}

	var regions []CandidateRegion

	// 1. Find all <table> elements (highest confidence)
	doc.Find("table").Each(func(i int, s *goquery.Selection) {
		rows := s.Find("tbody tr")
		if rows.Length() == 0 {
			rows = s.Find("tr")
		}
		if rows.Length() < 2 {
			return
		}
		region := CandidateRegion{
			Tag:        "table",
			Selector:   buildSelector(s, i),
			ItemCount:  rows.Length(),
			HasHeaders: s.Find("thead, th").Length() > 0,
			Context:    findContext(s),
			SectionID:  findSectionID(s),
			ItemTag:    "tr",
		}
		h, _ := goquery.OuterHtml(s)
		region.HTML = h
		if firstRow := rows.First(); firstRow.Length() > 0 {
			region.SingleItemHTML, _ = goquery.OuterHtml(firstRow)
		}
		regions = append(regions, region)
	})

	// 2. Find repeated list items
	doc.Find("ul, ol").Each(func(i int, s *goquery.Selection) {
		lis := s.Children().Filter("li")
		if lis.Length() < 3 {
			return
		}
		region := CandidateRegion{
			Tag:       s.Get(0).Data,
			Selector:  buildSelector(s, i),
			ItemCount: lis.Length(),
			Context:   findContext(s),
			SectionID: findSectionID(s),
			ItemTag:   "li",
		}
		h, _ := goquery.OuterHtml(s)
		region.HTML = h
		if first := lis.First(); first.Length() > 0 {
			region.SingleItemHTML, _ = goquery.OuterHtml(first)
		}
		regions = append(regions, region)
	})

	// 3. Find divs/sections with repeated child patterns.
	// Uses the MOST COMMON child pattern (not just the first child),
	// which handles containers with header rows or mixed content.
	doc.Find("div, section, main, article").Each(func(i int, s *goquery.Selection) {
		children := s.Children()
		if children.Length() < 3 {
			return
		}

		// Count all child patterns and find the most common one
		patternCounts := make(map[string]int)
		patternFirst := make(map[string]*goquery.Selection)
		children.Each(func(_ int, c *goquery.Selection) {
			p := childPattern(c)
			if p == "" {
				return
			}
			patternCounts[p]++
			if patternFirst[p] == nil {
				patternFirst[p] = c
			}
		})

		// Find the most repeated pattern
		bestPattern := ""
		bestCount := 0
		for p, count := range patternCounts {
			if count > bestCount {
				bestCount = count
				bestPattern = p
			}
		}

		if bestCount < 3 {
			return
		}

		firstMatch := patternFirst[bestPattern]

		// Extract item tag and class info
		itemTag := ""
		itemClass := ""
		if firstMatch != nil && firstMatch.Get(0) != nil {
			itemTag = firstMatch.Get(0).Data
			if cls, ok := firstMatch.Attr("class"); ok {
				itemClass = cls
			}
		}

		region := CandidateRegion{
			Tag:       "div",
			Selector:  buildSelector(s, i),
			ItemCount: bestCount,
			Context:   findContext(s),
			SectionID: findSectionID(s),
			ItemTag:   itemTag,
			ItemClass: itemClass,
		}
		h, _ := goquery.OuterHtml(s)
		region.HTML = h
		if firstMatch != nil {
			region.SingleItemHTML, _ = goquery.OuterHtml(firstMatch)
		}
		regions = append(regions, region)
	})

	// 4. Deep scan: find repeated elements by class fingerprint anywhere in the DOM.
	// This catches React/Tailwind sites where items share a class pattern but are
	// nested deeply or don't share an immediate common parent container.
	seen := make(map[string]bool) // track selectors we already found
	for _, r := range regions {
		seen[r.Selector] = true
	}
	classGroups := make(map[string][]*goquery.Selection) // class fingerprint -> elements
	doc.Find("*").Each(func(_ int, s *goquery.Selection) {
		cls, ok := s.Attr("class")
		if !ok || cls == "" {
			return
		}
		// Use tag + first 3 class tokens as fingerprint
		tag := s.Get(0).Data
		tokens := strings.Fields(cls)
		if len(tokens) > 3 {
			tokens = tokens[:3]
		}
		key := tag + "." + strings.Join(tokens, ".")
		classGroups[key] = append(classGroups[key], s)
	})
	for key, elems := range classGroups {
		if len(elems) < 3 {
			continue
		}
		// Check average content size — skip tiny elements (nav links, icons)
		totalLen := 0
		for _, e := range elems {
			h, _ := goquery.OuterHtml(e)
			totalLen += len(h)
		}
		avgLen := totalLen / len(elems)
		if avgLen < 100 {
			continue // too small to be data items
		}

		// Find the common parent of these elements
		parent := elems[0].Parent()
		if parent.Length() == 0 {
			continue
		}
		parentSel := buildSelector(parent, 0)
		if seen[parentSel] {
			continue
		}
		seen[parentSel] = true

		// Extract class from the key
		parts := strings.SplitN(key, ".", 2)
		itemTag := parts[0]
		itemClass := ""
		if len(parts) > 1 {
			itemClass = strings.ReplaceAll(parts[1], ".", " ")
		}

		firstHTML, _ := goquery.OuterHtml(elems[0])
		parentHTML, _ := goquery.OuterHtml(parent)

		regions = append(regions, CandidateRegion{
			Tag:            "div",
			Selector:       parentSel,
			ItemCount:      len(elems),
			Context:        findContext(parent),
			SectionID:      findSectionID(parent),
			ItemTag:        itemTag,
			ItemClass:      itemClass,
			SingleItemHTML: firstHTML,
			HTML:           parentHTML,
		})
	}

	// Sort by relevance: prefer content-rich regions over navigation lists.
	sortRegions(regions)

	return regions, nil
}

// sortRegions sorts candidate regions by relevance:
// prefer more items and content-rich regions over navigation lists.
func sortRegions(regions []CandidateRegion) {
	for i := 1; i < len(regions); i++ {
		for j := i; j > 0 && regionScore(regions[j]) > regionScore(regions[j-1]); j-- {
			regions[j], regions[j-1] = regions[j-1], regions[j]
		}
	}
}

func regionScore(r CandidateRegion) int {
	itemSize := len(r.SingleItemHTML)
	if itemSize == 0 {
		itemSize = len(r.HTML) / max(r.ItemCount, 1)
	}

	// Sweet spot: data items are typically 200-5000 chars.
	// Too small = nav links. Too large = wrapper containers.
	score := 0
	switch {
	case itemSize < 100:
		score = -2000 // icons, tiny links
	case itemSize < 300:
		score = itemSize // small nav links
	case itemSize >= 300 && itemSize <= 5000:
		score = itemSize + 2000 // ideal data item range
	case itemSize <= 20000:
		score = 5000 - (itemSize-5000)/10 // diminishing returns
	default:
		score = -1000 // huge items = container wrappers
	}

	// Boost tables (highest confidence data containers)
	if r.Tag == "table" {
		score += 5000
	}
	if r.HasHeaders {
		score += 2000
	}
	// More items is a mild boost (capped to prevent SVG/chart regions from dominating)
	itemBonus := r.ItemCount * 10
	if itemBonus > 500 {
		itemBonus = 500
	}
	score += itemBonus

	// Penalize regions inside nav/footer elements
	sel := strings.ToLower(r.Selector)
	if strings.Contains(sel, "nav") || strings.Contains(sel, "footer") ||
		strings.Contains(sel, "header") || strings.Contains(sel, "menu") {
		score -= 3000
	}

	// Penalize SVG/chart regions
	if r.ItemTag == "g" || r.ItemTag == "svg" || r.ItemTag == "path" ||
		strings.Contains(sel, "recharts") || strings.Contains(sel, "chart") {
		score -= 5000
	}

	return score
}

// buildSelector creates a CSS selector for a goquery selection.
func buildSelector(s *goquery.Selection, index int) string {
	node := s.Get(0)
	sel := node.Data

	if id, ok := s.Attr("id"); ok && id != "" {
		return sel + "#" + id
	}
	if cls, ok := s.Attr("class"); ok && cls != "" {
		classes := strings.Fields(cls)
		if len(classes) > 0 {
			return sel + "." + strings.Join(classes, ".")
		}
	}
	// Fallback: use nth-of-type
	return fmt.Sprintf("%s:nth-of-type(%d)", sel, index+1)
}

// childPattern returns a string representing the tag+class of a selection.
func childPattern(s *goquery.Selection) string {
	if s.Length() == 0 {
		return ""
	}
	node := s.Get(0)
	pattern := node.Data
	if cls, ok := s.Attr("class"); ok {
		pattern += "." + cls
	}
	return pattern
}

// findContext looks for a nearby heading or caption that describes a region.
// Searches preceding siblings, then walks up the DOM to find headings in ancestor containers.
func findContext(s *goquery.Selection) string {
	// Check for caption inside
	if cap := s.Find("caption").First(); cap.Length() > 0 {
		return strings.TrimSpace(cap.Text())
	}

	// Check preceding sibling headings
	prev := s.Prev()
	for i := 0; i < 3 && prev.Length() > 0; i++ {
		tag := prev.Get(0).Data
		if isHeading(tag) {
			return strings.TrimSpace(prev.Text())
		}
		// Check if preceding sibling contains a heading
		if h := prev.Find("h1, h2, h3, h4, h5, h6").Last(); h.Length() > 0 {
			return strings.TrimSpace(h.Text())
		}
		prev = prev.Prev()
	}

	// Check parent for aria-label
	if label, ok := s.Parent().Attr("aria-label"); ok {
		return label
	}

	// Walk up the DOM tree looking for headings in ancestor containers
	ancestor := s.Parent()
	for depth := 0; depth < 5 && ancestor.Length() > 0; depth++ {
		if h := ancestor.Find("h1, h2, h3, h4, h5, h6").First(); h.Length() > 0 {
			text := strings.TrimSpace(h.Text())
			if text != "" && len(text) < 100 {
				return text
			}
		}
		ancestor = ancestor.Parent()
	}

	return ""
}

func isHeading(tag string) bool {
	return tag == "h1" || tag == "h2" || tag == "h3" || tag == "h4" || tag == "h5" || tag == "h6"
}

// findSectionID walks up the DOM tree to find the nearest ancestor with an id attribute.
// Returns the id value (e.g. "market-share", "leaderboard") or empty string.
func findSectionID(s *goquery.Selection) string {
	el := s
	for depth := 0; depth < 10 && el.Length() > 0; depth++ {
		if id, ok := el.Attr("id"); ok && id != "" {
			return id
		}
		el = el.Parent()
	}
	return ""
}
