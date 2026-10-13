package vector

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"
)

// Loaders accepted by vector.load_document.
const (
	LoaderAuto     = "auto"
	LoaderText     = "text"
	LoaderMarkdown = "markdown"
	LoaderHTML     = "html"
	LoaderCSV      = "csv"
	LoaderJSON     = "json"
	LoaderDocx     = "docx"
	LoaderPDF      = "pdf"
)

// detectLoader picks a loader from the file name, MIME type, then content.
func detectLoader(name, mime string, data []byte) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return LoaderPDF
	case ".docx":
		return LoaderDocx
	case ".csv", ".tsv":
		return LoaderCSV
	case ".html", ".htm", ".xhtml":
		return LoaderHTML
	case ".md", ".markdown":
		return LoaderMarkdown
	case ".json":
		return LoaderJSON
	case ".txt", ".text", ".log":
		return LoaderText
	}
	m := strings.ToLower(mime)
	switch {
	case strings.Contains(m, "pdf"):
		return LoaderPDF
	case strings.Contains(m, "wordprocessingml"):
		return LoaderDocx
	case strings.Contains(m, "csv"):
		return LoaderCSV
	case strings.Contains(m, "html"):
		return LoaderHTML
	case strings.Contains(m, "markdown"):
		return LoaderMarkdown
	case strings.Contains(m, "json"):
		return LoaderJSON
	}
	switch {
	case bytes.HasPrefix(data, []byte("%PDF")):
		return LoaderPDF
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return LoaderDocx
	}
	head := strings.ToLower(string(data[:min(len(data), 512)]))
	if strings.Contains(head, "<html") || strings.Contains(head, "<!doctype html") {
		return LoaderHTML
	}
	return LoaderText
}

// htmlText returns the visible text of an HTML document: script, style and
// similar elements are dropped, block elements become line breaks.
func htmlText(data []byte) string {
	z := html.NewTokenizer(bytes.NewReader(data))
	var sb strings.Builder
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return collapseBlankLines(sb.String())
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "noscript", "template", "head":
				skip++
			case "br", "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "section", "article", "header", "footer", "pre", "blockquote", "table":
				sb.WriteByte('\n')
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "noscript", "template", "head":
				if skip > 0 {
					skip--
				}
			case "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "section", "article", "pre", "blockquote", "table":
				sb.WriteByte('\n')
			case "td", "th":
				sb.WriteByte('\t')
			}
		case html.TextToken:
			if skip == 0 {
				sb.WriteString(strings.Join(strings.Fields(string(z.Text())), " "))
				sb.WriteByte(' ')
			}
		}
	}
}

func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// docxText reads the paragraphs of word/document.xml.
func docxText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("not a .docx (zip) file: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		return wordXMLText(io.LimitReader(rc, maxResponseBody))
	}
	return "", fmt.Errorf(".docx has no word/document.xml")
}

func wordXMLText(r io.Reader) (string, error) {
	dec := xml.NewDecoder(r)
	var sb strings.Builder
	inText := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return strings.TrimSpace(sb.String()), nil
		}
		if err != nil {
			return "", fmt.Errorf("reading document.xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				sb.WriteByte('\t')
			case "br", "cr":
				sb.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				sb.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				sb.Write(t)
			}
		}
	}
}

// csvRows renders each data row as "header: value" lines (one document per
// row). With column set, only that column's value is used.
func csvRows(data []byte, sep rune, column string) ([]string, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = sep
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parsing CSV: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	header := records[0]
	col := -1
	if column != "" {
		for i, h := range header {
			if strings.TrimSpace(h) == column {
				col = i
			}
		}
		if col < 0 {
			return nil, fmt.Errorf("CSV has no column %q (columns: %s)", column, strings.Join(header, ", "))
		}
	}
	var rows []string
	for _, rec := range records[1:] {
		if col >= 0 {
			if col < len(rec) {
				rows = append(rows, rec[col])
			} else {
				rows = append(rows, "")
			}
			continue
		}
		lines := make([]string, 0, len(rec))
		for i, v := range rec {
			h := fmt.Sprintf("column_%d", i+1)
			if i < len(header) {
				h = strings.TrimSpace(header[i])
			}
			lines = append(lines, h+": "+v)
		}
		rows = append(rows, strings.Join(lines, "\n"))
	}
	return rows, nil
}
