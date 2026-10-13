package vector

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func loadRun(t *testing.T, cfg map[string]interface{}, items ...workflow.Item) []workflow.Item {
	t.Helper()
	out, err := (&LoadDocumentNode{}).Execute(context.Background(), workflow.NodeInput{Items: items}, cfg)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return out[0].Items
}

func docText(it workflow.Item) string { return it.JSON["pageContent"].(string) }
func docMeta(it workflow.Item) map[string]interface{} {
	return it.JSON["metadata"].(map[string]interface{})
}

func TestLoadJSONFieldWithInlineSplit(t *testing.T) {
	items := loadRun(t, map[string]interface{}{
		"input_field": "body", "metadata_fields": "id", "split": true,
		"splitter": "character", "separator": " ", "chunk_size": float64(7),
	}, workflow.NewItem(map[string]interface{}{"id": "a1", "body": "aaa bbb ccc"}))
	if len(items) != 2 || docText(items[0]) != "aaa bbb" || docText(items[1]) != "ccc" {
		t.Fatalf("items %v", items)
	}
	m := docMeta(items[1])
	if m["id"] != "a1" || m["source"] != "json" || m["chunk_index"] != 1 {
		t.Fatalf("meta %v", m)
	}
}

func TestLoadBinaryHTMLAndCSV(t *testing.T) {
	html := `<html><head><title>x</title><style>p{}</style></head><body><h1>Title</h1><p>Hello <b>there</b></p><script>bad()</script></body></html>`
	items := loadRun(t, map[string]interface{}{"source": "binary"},
		workflow.Item{JSON: map[string]interface{}{"fileName": "page.html"}, Binary: map[string][]byte{"data": []byte(html)}})
	if len(items) != 1 || docText(items[0]) != "Title\n\nHello there" {
		t.Fatalf("html text %q", docText(items[0]))
	}
	if m := docMeta(items[0]); m["loader"] != "html" || m["source"] != "binary:data" {
		t.Fatalf("meta %v", m)
	}

	csv := "name;city\nAda;London\nAlan;Wilmslow\n"
	items = loadRun(t, map[string]interface{}{"source": "binary", "binary_key": "file", "csv_separator": ";"},
		workflow.Item{JSON: map[string]interface{}{"mimeType": "text/csv"}, Binary: map[string][]byte{"file": []byte(csv)}})
	if len(items) != 2 || docText(items[1]) != "name: Alan\ncity: Wilmslow" || docMeta(items[1])["row"] != 2 {
		t.Fatalf("csv rows %v", items)
	}
	items = loadRun(t, map[string]interface{}{"source": "binary", "binary_key": "file", "csv_separator": ";", "csv_column": "city", "loader": "csv"},
		workflow.Item{JSON: map[string]interface{}{}, Binary: map[string][]byte{"file": []byte(csv)}})
	if len(items) != 2 || docText(items[0]) != "London" {
		t.Fatalf("csv column %v", items)
	}
}

func makeDocx(t *testing.T) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("word/document.xml")
	fmt.Fprint(w, `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`+
		`<w:p><w:r><w:t>First</w:t></w:r><w:r><w:tab/><w:t xml:space="preserve">para</w:t></w:r></w:p>`+
		`<w:p><w:r><w:t>Second</w:t><w:br/><w:t>line</w:t></w:r></w:p></w:body></w:document>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// makePDF builds a two-page PDF; page 1's content stream is Flate-compressed.
func makePDF(t *testing.T) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write([]byte("BT /F1 12 Tf 72 712 Td (Hello \\(PDF\\)) Tj 0 -14 Td [(Wor) -50 (ld) -400 (again)] TJ ET"))
	zw.Close()
	page2 := "BT /F1 12 Tf 72 712 Td <FEFF00500061006700650020003200> Tj ET"
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	b.WriteString("1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n")
	b.WriteString("2 0 obj << /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >> endobj\n")
	b.WriteString("3 0 obj << /Type /Page /Parent 2 0 R /Contents 4 0 R >> endobj\n")
	fmt.Fprintf(&b, "4 0 obj << /Length %d /Filter /FlateDecode >>\nstream\n", z.Len())
	b.Write(z.Bytes())
	b.WriteString("\nendstream\nendobj\n")
	b.WriteString("5 0 obj << /Type /Page /Parent 2 0 R /Contents [6 0 R] >> endobj\n")
	fmt.Fprintf(&b, "6 0 obj << /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(page2), page2)
	b.WriteString("trailer << /Root 1 0 R >>\n%%EOF\n")
	return b.Bytes()
}

func TestLoadFileDocxAndPDF(t *testing.T) {
	dir := t.TempDir()
	docx := filepath.Join(dir, "a.docx")
	pdf := filepath.Join(dir, "b.pdf")
	os.WriteFile(docx, makeDocx(t), 0o600)
	os.WriteFile(pdf, makePDF(t), 0o600)

	items := loadRun(t, map[string]interface{}{"source": "file"}, workflow.NewItem(map[string]interface{}{"file_path": docx}))
	if len(items) != 1 || docText(items[0]) != "First\tpara\nSecond\nline" {
		t.Fatalf("docx %q", docText(items[0]))
	}
	if m := docMeta(items[0]); m["file_name"] != "a.docx" || m["loader"] != "docx" {
		t.Fatalf("docx meta %v", m)
	}

	items = loadRun(t, map[string]interface{}{"source": "file", "file_path": pdf})
	if len(items) != 2 {
		t.Fatalf("want 2 pages, got %d: %v", len(items), items)
	}
	if docText(items[0]) != "Hello (PDF)\nWorld again" || docText(items[1]) != "Page 2" {
		t.Fatalf("pages %q / %q", docText(items[0]), docText(items[1]))
	}
	if m := docMeta(items[1]); m["page"] != 2 || m["pages"] != 2 {
		t.Fatalf("pdf meta %v", m)
	}
	items = loadRun(t, map[string]interface{}{"source": "file", "file_path": pdf, "split_pages": false})
	if len(items) != 1 || docText(items[0]) != "Hello (PDF)\nWorld again\n\nPage 2" {
		t.Fatalf("joined %q", docText(items[0]))
	}
}

func TestLoadErrors(t *testing.T) {
	for _, c := range []struct {
		cfg  map[string]interface{}
		item workflow.Item
		want string
	}{
		{map[string]interface{}{"source": "s3"}, workflow.NewItem(nil), `source "s3" is not supported`},
		{map[string]interface{}{"loader": "epub"}, workflow.NewItem(nil), `loader "epub" is not supported`},
		{map[string]interface{}{"source": "binary"}, workflow.Item{JSON: map[string]interface{}{}, Binary: map[string][]byte{"file": nil}}, `item has no binary "data" (binary keys: file)`},
		{map[string]interface{}{"source": "file"}, workflow.NewItem(map[string]interface{}{}), "no file: set file_path"},
		{map[string]interface{}{"source": "binary", "loader": "text"}, workflow.Item{JSON: map[string]interface{}{}, Binary: map[string][]byte{"data": {0xff, 0xfe, 0x00}}}, "not UTF-8 text"},
		{map[string]interface{}{"csv_separator": ";;"}, workflow.NewItem(nil), "csv_separator must be a single character"},
	} {
		_, err := (&LoadDocumentNode{}).Execute(context.Background(), workflow.NodeInput{Items: []workflow.Item{c.item}}, c.cfg)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err %v, want %q", c.cfg, err, c.want)
		}
	}
}

func TestDetectLoader(t *testing.T) {
	cases := map[string]string{
		detectLoader("x.MD", "", nil):                      LoaderMarkdown,
		detectLoader("", "application/pdf", nil):           LoaderPDF,
		detectLoader("", "", []byte("%PDF-1.7")):           LoaderPDF,
		detectLoader("", "", []byte("PK\x03\x04...")):      LoaderDocx,
		detectLoader("", "", []byte("<!DOCTYPE html><p>")): LoaderHTML,
		detectLoader("", "", []byte("plain")):              LoaderText,
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %s want %s", got, want)
		}
	}
	if !reflect.DeepEqual(extractPDFPages([]byte("not a pdf")), []string(nil)) {
		t.Fatal("garbage should yield no pages")
	}
}

func TestLoadTextTemplateIsPerItem(t *testing.T) {
	if got := (&LoadDocumentNode{}).PerItemConfigFields(); !reflect.DeepEqual(got, []string{"text"}) {
		t.Fatalf("per-item fields %v", got)
	}
	items := loadRun(t, map[string]interface{}{"text": "Q: {{ $json.q }}"},
		workflow.NewItem(map[string]interface{}{"q": "one"}), workflow.NewItem(map[string]interface{}{"q": "two"}))
	if len(items) != 2 || docText(items[0]) != "Q: one" || docText(items[1]) != "Q: two" {
		t.Fatalf("items %v", items)
	}
}
