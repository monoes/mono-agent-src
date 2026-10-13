package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/fsconfine"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Sources accepted by vector.load_document.
const (
	SourceJSON   = "json"
	SourceBinary = "binary"
	SourceFile   = "file"
)

// maxDocumentBytes caps a file or binary read by the loader.
const maxDocumentBytes = 64 << 20

// LoadDocumentNode implements vector.load_document: it turns item fields,
// item binaries or files on disk into documents ({pageContent, metadata}),
// optionally splitting them into chunks in the same step.
type LoadDocumentNode struct{}

func (n *LoadDocumentNode) Type() string { return "vector.load_document" }

// PerItemConfigFields keeps "text" unresolved by the engine so a template
// such as {{ $json.body }} is evaluated against each item, not just the
// first one.
func (n *LoadDocumentNode) PerItemConfigFields() []string { return []string{"text"} }

type loadConfig struct {
	source     string
	loader     string
	text       string
	fields     []string
	binaryKey  string
	filePath   string
	pathField  string
	csvSep     rune
	csvColumn  string
	splitPages bool
	metaFields []string
	split      *splitConfig
}

func parseLoadConfig(cfg map[string]interface{}) (loadConfig, error) {
	c := loadConfig{
		source:     strings.ToLower(strOr(cfg, SourceJSON, "source")),
		loader:     strings.ToLower(strOr(cfg, LoaderAuto, "loader")),
		text:       str(cfg, "text"),
		fields:     stringList(cfg, "input_field"),
		binaryKey:  strOr(cfg, "data", "binary_key"),
		filePath:   str(cfg, "file_path"),
		pathField:  strOr(cfg, "file_path", "file_path_field"),
		csvSep:     ',',
		csvColumn:  str(cfg, "csv_column"),
		splitPages: boolField(cfg, "split_pages", true),
		metaFields: stringList(cfg, "metadata_fields"),
	}
	if s := unescapeSeparator(str(cfg, "csv_separator")); s != "" {
		r, size := utf8.DecodeRuneInString(s)
		if size != len(s) {
			return c, fmt.Errorf("csv_separator must be a single character, got %q", s)
		}
		c.csvSep = r
	}
	switch c.source {
	case SourceJSON, SourceBinary, SourceFile:
	default:
		return c, fmt.Errorf("source %q is not supported (want json, binary or file)", c.source)
	}
	switch c.loader {
	case LoaderAuto, LoaderText, LoaderMarkdown, LoaderHTML, LoaderCSV, LoaderJSON, LoaderDocx, LoaderPDF:
	default:
		return c, fmt.Errorf("loader %q is not supported (want auto, text, markdown, html, csv, json, docx or pdf)", c.loader)
	}
	if boolField(cfg, "split", false) {
		sc, err := parseSplitConfig(cfg)
		if err != nil {
			return c, err
		}
		c.split = &sc
	}
	return c, nil
}

// rawDoc is one document before metadata merging and splitting.
type rawDoc struct {
	text string
	meta map[string]interface{}
}

func (n *LoadDocumentNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	c, err := parseLoadConfig(config)
	if err != nil {
		return nil, fmt.Errorf("vector.load_document: %w", err)
	}
	items := input.Items
	if len(items) == 0 && (c.source == SourceFile && c.filePath != "" || c.text != "") {
		items = []workflow.Item{workflow.NewItem(map[string]interface{}{})}
	}
	var out []workflow.Item
	for i, item := range items {
		ic := c
		if ic.text, err = perItemString(c.text, item, input); err != nil {
			return nil, fmt.Errorf("vector.load_document: item %d: text: %w", i, err)
		}
		docs, err := ic.load(ctx, item)
		if err != nil {
			return nil, fmt.Errorf("vector.load_document: item %d: %w", i, err)
		}
		base := documentMetadata(item.JSON, c.metaFields)
		for _, d := range docs {
			meta := copyMap(base)
			for k, v := range d.meta {
				meta[k] = v
			}
			chunks := []string{d.text}
			if c.split != nil {
				chunks = c.split.split(d.text)
			}
			for ci, chunk := range chunks {
				m := meta
				if c.split != nil {
					m = copyMap(meta)
					m["chunk_index"] = ci
					m["chunk_count"] = len(chunks)
				}
				out = append(out, workflow.NewItem(map[string]interface{}{"pageContent": chunk, "metadata": m}))
			}
		}
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

func (c loadConfig) load(ctx context.Context, item workflow.Item) ([]rawDoc, error) {
	switch c.source {
	case SourceBinary:
		data, ok := item.Binary[c.binaryKey]
		if !ok {
			keys := make([]string, 0, len(item.Binary))
			for k := range item.Binary {
				keys = append(keys, k)
			}
			return nil, fmt.Errorf("item has no binary %q (binary keys: %s)", c.binaryKey, strings.Join(keys, ", "))
		}
		if len(data) > maxDocumentBytes {
			return nil, fmt.Errorf("binary %q is %d bytes, over the %d-byte limit", c.binaryKey, len(data), maxDocumentBytes)
		}
		name := str(item.JSON, "fileName")
		if name == "" {
			name = str(item.JSON, "file_name")
		}
		return c.parse(data, name, strOr(item.JSON, "", "mimeType", "mime_type"), map[string]interface{}{"source": "binary:" + c.binaryKey})
	case SourceFile:
		p := c.filePath
		if p == "" {
			p = str(item.JSON, c.pathField)
		}
		if p == "" {
			return nil, fmt.Errorf("no file: set file_path or pass items with a %q field", c.pathField)
		}
		confined, err := fsconfine.Path(ctx, p)
		if err != nil {
			return nil, err
		}
		data, err := readCapped(confined)
		if err != nil {
			return nil, err
		}
		return c.parse(data, confined, "", map[string]interface{}{"source": confined, "file_name": filepath.Base(confined)})
	}
	// SourceJSON
	if c.text != "" {
		return []rawDoc{{text: c.text, meta: map[string]interface{}{"source": "json"}}}, nil
	}
	text, err := itemText(item.JSON, c.fields)
	if err != nil {
		return nil, err
	}
	if c.loader == LoaderHTML {
		text = htmlText([]byte(text))
	}
	return []rawDoc{{text: text, meta: map[string]interface{}{"source": "json"}}}, nil
}

func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDocumentBytes {
		return nil, fmt.Errorf("%s is over the %d-byte limit", path, maxDocumentBytes)
	}
	return data, nil
}

// parse turns raw bytes into documents with the configured (or detected)
// loader.
func (c loadConfig) parse(data []byte, name, mime string, meta map[string]interface{}) ([]rawDoc, error) {
	loader := c.loader
	if loader == LoaderAuto {
		loader = detectLoader(name, mime, data)
	}
	with := func(extra map[string]interface{}) map[string]interface{} {
		m := copyMap(meta)
		m["loader"] = loader
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	switch loader {
	case LoaderPDF:
		pages := extractPDFPages(data)
		if len(pages) == 0 {
			return nil, fmt.Errorf("no extractable text in PDF (scanned or encrypted PDFs and CID fonts are not supported)")
		}
		if !c.splitPages {
			return []rawDoc{{text: strings.Join(pages, "\n\n"), meta: with(map[string]interface{}{"pages": len(pages)})}}, nil
		}
		docs := make([]rawDoc, 0, len(pages))
		for i, p := range pages {
			docs = append(docs, rawDoc{text: p, meta: with(map[string]interface{}{"page": i + 1, "pages": len(pages)})})
		}
		return docs, nil
	case LoaderDocx:
		t, err := docxText(data)
		if err != nil {
			return nil, err
		}
		return []rawDoc{{text: t, meta: with(nil)}}, nil
	case LoaderCSV:
		rows, err := csvRows(data, c.csvSep, c.csvColumn)
		if err != nil {
			return nil, err
		}
		docs := make([]rawDoc, 0, len(rows))
		for i, r := range rows {
			docs = append(docs, rawDoc{text: r, meta: with(map[string]interface{}{"row": i + 1})})
		}
		return docs, nil
	case LoaderHTML:
		return []rawDoc{{text: htmlText(data), meta: with(nil)}}, nil
	case LoaderJSON:
		var v interface{}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("parsing JSON: %w", err)
		}
		pretty, _ := json.MarshalIndent(v, "", "  ")
		return []rawDoc{{text: string(pretty), meta: with(nil)}}, nil
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("content is not UTF-8 text; set loader to pdf, docx, csv or html if it is one of those")
	}
	return []rawDoc{{text: string(data), meta: with(nil)}}, nil
}
