package crawl

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// ---------------------------------------------------------------------------
// CleanContent
// ---------------------------------------------------------------------------

// CleanContent extracts structured content from raw HTML.
func CleanContent(rawHTML string, pageURL string, opts CleanOptions) (PageContent, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(rawHTML))
	if err != nil {
		return PageContent{}, fmt.Errorf("parse html: %w", err)
	}

	base, _ := url.Parse(pageURL)

	pc := PageContent{URL: pageURL}

	// --- Metadata ---
	pc.Title = strings.TrimSpace(doc.Find("title").First().Text())
	if pc.Title == "" {
		pc.Title = strings.TrimSpace(doc.Find("h1").First().Text())
	}
	pc.Description = metaContent(doc, "description")
	pc.Author = metaContent(doc, "author")
	pc.PublishedAt = extractPublishedAt(doc)
	pc.Favicon = resolveURL(base, extractFavicon(doc))
	canonical := doc.Find("link[rel='canonical']").AttrOr("href", "")
	if canonical != "" {
		pc.URL = resolveURL(base, canonical)
	}

	// --- Extract links before removing elements ---
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		resolved := resolveURL(base, href)
		isExt := isExternal(base, resolved)
		pc.Links = append(pc.Links, Link{
			Text:       strings.TrimSpace(s.Text()),
			URL:        resolved,
			IsExternal: isExt,
		})
	})

	// --- Extract images before removing elements ---
	doc.Find("img").Each(func(_ int, s *goquery.Selection) {
		src, _ := s.Attr("src")
		alt, _ := s.Attr("alt")
		w, _ := strconv.Atoi(s.AttrOr("width", "0"))
		h, _ := strconv.Atoi(s.AttrOr("height", "0"))
		pc.Images = append(pc.Images, Image{
			Alt:    alt,
			Src:    resolveURL(base, src),
			Width:  w,
			Height: h,
		})
	})

	// --- Extract headings ---
	for level := 1; level <= 6; level++ {
		tag := fmt.Sprintf("h%d", level)
		lvl := level // capture
		doc.Find(tag).Each(func(_ int, s *goquery.Selection) {
			text := strings.TrimSpace(s.Text())
			if text != "" {
				pc.Headings = append(pc.Headings, Heading{Level: lvl, Text: text})
			}
		})
	}

	// --- Extract tables ---
	doc.Find("table").Each(func(_ int, table *goquery.Selection) {
		var rows []string
		table.Find("tr").Each(func(_ int, tr *goquery.Selection) {
			var cells []string
			tr.Find("td, th").Each(func(_ int, td *goquery.Selection) {
				cells = append(cells, strings.TrimSpace(td.Text()))
			})
			if len(cells) > 0 {
				rows = append(rows, strings.Join(cells, "\t"))
			}
		})
		// Flatten to 2D by splitting on tab.
		for _, row := range rows {
			pc.Tables = append(pc.Tables, strings.Split(row, "\t"))
		}
	})

	// --- Remove unwanted elements ---
	doc.Find("script, style, noscript, svg, iframe, link, meta").Remove()
	// Remove hidden elements.
	doc.Find("[style]").Each(func(_ int, s *goquery.Selection) {
		style, _ := s.Attr("style")
		lower := strings.ToLower(style)
		if strings.Contains(lower, "display:none") || strings.Contains(lower, "display: none") ||
			strings.Contains(lower, "visibility:hidden") || strings.Contains(lower, "visibility: hidden") {
			s.Remove()
		}
	})
	doc.Find("[aria-hidden='true']").Remove()

	if !opts.KeepNav {
		doc.Find("nav, header, footer").Remove()
	}

	// --- MainText ---
	bodyText := doc.Find("body").Text()
	pc.MainText = collapseWhitespace(bodyText)

	// --- Markdown + TokenCount ---
	pc.Markdown = ToMarkdown(rawHTML)
	pc.TokenCount = EstimateTokens(pc.Markdown)

	return pc, nil
}

// ---------------------------------------------------------------------------
// Metadata helpers
// ---------------------------------------------------------------------------

func metaContent(doc *goquery.Document, name string) string {
	val := doc.Find(fmt.Sprintf("meta[name='%s']", name)).AttrOr("content", "")
	if val == "" {
		val = doc.Find(fmt.Sprintf("meta[property='%s']", name)).AttrOr("content", "")
	}
	return strings.TrimSpace(val)
}

func extractPublishedAt(doc *goquery.Document) string {
	// Try common meta tags first.
	for _, prop := range []string{
		"article:published_time",
		"datePublished",
		"date",
		"DC.date.issued",
	} {
		val := doc.Find(fmt.Sprintf("meta[property='%s']", prop)).AttrOr("content", "")
		if val == "" {
			val = doc.Find(fmt.Sprintf("meta[name='%s']", prop)).AttrOr("content", "")
		}
		if val != "" {
			return strings.TrimSpace(val)
		}
	}
	// Fallback: <time> element.
	t := doc.Find("time").First()
	if dt, exists := t.Attr("datetime"); exists {
		return strings.TrimSpace(dt)
	}
	return strings.TrimSpace(t.Text())
}

func extractFavicon(doc *goquery.Document) string {
	fav := doc.Find("link[rel='icon']").AttrOr("href", "")
	if fav == "" {
		fav = doc.Find("link[rel='shortcut icon']").AttrOr("href", "")
	}
	if fav == "" {
		fav = "/favicon.ico"
	}
	return fav
}

// ---------------------------------------------------------------------------
// URL helpers
// ---------------------------------------------------------------------------

func resolveURL(base *url.URL, raw string) string {
	if base == nil || raw == "" {
		return raw
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return base.ResolveReference(ref).String()
}

func isExternal(base *url.URL, resolved string) bool {
	if base == nil {
		return false
	}
	u, err := url.Parse(resolved)
	if err != nil {
		return false
	}
	return u.Host != "" && u.Host != base.Host
}

// ---------------------------------------------------------------------------
// Text helpers
// ---------------------------------------------------------------------------

func collapseWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}
