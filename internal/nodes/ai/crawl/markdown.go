package crawl

import (
	"math"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// ---------------------------------------------------------------------------
// ToMarkdown
// ---------------------------------------------------------------------------

// ToMarkdown converts raw HTML to a markdown string.
func ToMarkdown(rawHTML string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(rawHTML))
	if err != nil {
		return ""
	}
	// Remove noise elements for markdown conversion.
	doc.Find("script, style, noscript, svg, iframe").Remove()

	var buf strings.Builder
	doc.Find("body").Each(func(_ int, body *goquery.Selection) {
		body.Contents().Each(func(_ int, s *goquery.Selection) {
			convertNode(&buf, s)
		})
	})

	result := buf.String()
	// Collapse 3+ newlines into 2.
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(result)
}

func convertNode(buf *strings.Builder, s *goquery.Selection) {
	if len(s.Nodes) == 0 {
		return
	}

	node := s.Nodes[0]

	// Text node.
	if node.Type == 1 { // html.TextNode
		text := strings.TrimSpace(node.Data)
		if text != "" {
			buf.WriteString(text)
		}
		return
	}

	// Element node.
	if node.Type != 3 { // not html.ElementNode
		// Recurse into children for other node types.
		s.Contents().Each(func(_ int, child *goquery.Selection) {
			convertNode(buf, child)
		})
		return
	}

	tag := strings.ToLower(node.Data)

	switch tag {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level, _ := strconv.Atoi(tag[1:])
		prefix := strings.Repeat("#", level)
		text := strings.TrimSpace(s.Text())
		if text != "" {
			buf.WriteString("\n\n")
			buf.WriteString(prefix)
			buf.WriteString(" ")
			buf.WriteString(text)
			buf.WriteString("\n\n")
		}

	case "p", "div", "section", "article":
		buf.WriteString("\n\n")
		s.Contents().Each(func(_ int, child *goquery.Selection) {
			convertNode(buf, child)
		})
		buf.WriteString("\n\n")

	case "a":
		href, _ := s.Attr("href")
		text := strings.TrimSpace(s.Text())
		if text != "" {
			buf.WriteString("[")
			buf.WriteString(text)
			buf.WriteString("](")
			buf.WriteString(href)
			buf.WriteString(")")
		}

	case "img":
		alt, _ := s.Attr("alt")
		src, _ := s.Attr("src")
		buf.WriteString("![")
		buf.WriteString(alt)
		buf.WriteString("](")
		buf.WriteString(src)
		buf.WriteString(")")

	case "strong", "b":
		text := strings.TrimSpace(s.Text())
		if text != "" {
			buf.WriteString("**")
			buf.WriteString(text)
			buf.WriteString("**")
		}

	case "em", "i":
		text := strings.TrimSpace(s.Text())
		if text != "" {
			buf.WriteString("*")
			buf.WriteString(text)
			buf.WriteString("*")
		}

	case "br":
		buf.WriteString("\n")

	case "hr":
		buf.WriteString("\n\n---\n\n")

	case "ul":
		buf.WriteString("\n")
		s.Find("> li").Each(func(_ int, li *goquery.Selection) {
			buf.WriteString("- ")
			li.Contents().Each(func(_ int, child *goquery.Selection) {
				convertNode(buf, child)
			})
			buf.WriteString("\n")
		})
		buf.WriteString("\n")

	case "ol":
		buf.WriteString("\n")
		s.Find("> li").Each(func(i int, li *goquery.Selection) {
			buf.WriteString(strconv.Itoa(i + 1))
			buf.WriteString(". ")
			li.Contents().Each(func(_ int, child *goquery.Selection) {
				convertNode(buf, child)
			})
			buf.WriteString("\n")
		})
		buf.WriteString("\n")

	case "blockquote":
		text := strings.TrimSpace(s.Text())
		if text != "" {
			for _, line := range strings.Split(text, "\n") {
				buf.WriteString("> ")
				buf.WriteString(strings.TrimSpace(line))
				buf.WriteString("\n")
			}
		}

	case "pre":
		code := s.Find("code")
		var text string
		if code.Length() > 0 {
			text = code.Text()
		} else {
			text = s.Text()
		}
		buf.WriteString("\n\n```\n")
		buf.WriteString(text)
		buf.WriteString("\n```\n\n")

	case "code":
		// Only inline code; <pre><code> is handled by "pre".
		parent := s.Parent()
		if parent.Length() > 0 && goquery.NodeName(parent) == "pre" {
			buf.WriteString(s.Text())
			return
		}
		buf.WriteString("`")
		buf.WriteString(s.Text())
		buf.WriteString("`")

	case "table":
		convertTable(buf, s)

	case "li":
		// Handled by ul/ol parents. If standalone, just output text.
		s.Contents().Each(func(_ int, child *goquery.Selection) {
			convertNode(buf, child)
		})

	default:
		// Generic: recurse into children.
		s.Contents().Each(func(_ int, child *goquery.Selection) {
			convertNode(buf, child)
		})
	}
}

func convertTable(buf *strings.Builder, table *goquery.Selection) {
	var rows [][]string
	table.Find("tr").Each(func(_ int, tr *goquery.Selection) {
		var row []string
		tr.Find("td, th").Each(func(_ int, td *goquery.Selection) {
			row = append(row, strings.TrimSpace(td.Text()))
		})
		if len(row) > 0 {
			rows = append(rows, row)
		}
	})
	if len(rows) == 0 {
		return
	}

	// Determine max columns.
	maxCols := 0
	for _, row := range rows {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}

	buf.WriteString("\n\n")
	// Header row.
	writeTableRow(buf, rows[0], maxCols)
	// Separator.
	buf.WriteString("|")
	for i := 0; i < maxCols; i++ {
		buf.WriteString(" --- |")
	}
	buf.WriteString("\n")
	// Data rows.
	for _, row := range rows[1:] {
		writeTableRow(buf, row, maxCols)
	}
	buf.WriteString("\n")
}

func writeTableRow(buf *strings.Builder, row []string, maxCols int) {
	buf.WriteString("|")
	for i := 0; i < maxCols; i++ {
		buf.WriteString(" ")
		if i < len(row) {
			buf.WriteString(row[i])
		}
		buf.WriteString(" |")
	}
	buf.WriteString("\n")
}

// ---------------------------------------------------------------------------
// Token helpers
// ---------------------------------------------------------------------------

// EstimateTokens estimates the number of tokens in text (~0.75 tokens per word).
func EstimateTokens(text string) int {
	words := len(strings.Fields(text))
	return int(math.Ceil(float64(words) * 0.75))
}

// TruncateToTokens truncates markdown to approximately maxTokens by word count.
func TruncateToTokens(markdown string, maxTokens int) string {
	// tokens ~ words * 0.75, so max words ~ maxTokens / 0.75
	maxWords := int(math.Ceil(float64(maxTokens) / 0.75))
	words := strings.Fields(markdown)
	if len(words) <= maxWords {
		return markdown
	}
	return strings.Join(words[:maxWords], " ")
}
