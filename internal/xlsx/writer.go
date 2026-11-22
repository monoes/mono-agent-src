package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// WriteFile writes rows to a new single-sheet workbook at path, replacing any
// file already there.
//
// Values are written as numeric cells when they are Go numbers and as text
// otherwise, with nil written as an empty cell. Text goes into the cell inline
// rather than into a shared-string table: there is no table to index, so there
// is no index for a reader to get wrong.
//
// The workbook is built in memory and written in one call, so a failure part
// way through leaves the previous file intact rather than a truncated one.
func WriteFile(path, sheetName string, rows [][]any) error {
	buf, err := Marshal(sheetName, rows)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".xlsx-*") // 0600
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Marshal builds the bytes of a single-sheet workbook.
func Marshal(sheetName string, rows [][]any) ([]byte, error) {
	if len(rows) > maxRows {
		return nil, fmt.Errorf("xlsx: %d rows exceeds the %d row limit", len(rows), maxRows)
	}
	sheetName = sanitizeSheetName(sheetName)

	sheet, err := sheetXML(rows)
	if err != nil {
		return nil, err
	}

	parts := []struct{ name, body string }{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", rootRelsXML},
		{pathWorkbook, workbookXML(sheetName)},
		{pathWorkbookRels, workbookRelsXML},
		{pathStyles, stylesXML},
		{"xl/worksheets/sheet1.xml", sheet},
	}

	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, p := range parts {
		w, err := zw.Create(p.name)
		if err != nil {
			return nil, fmt.Errorf("xlsx: write %s: %w", p.name, err)
		}
		if _, err := w.Write([]byte(p.body)); err != nil {
			return nil, fmt.Errorf("xlsx: write %s: %w", p.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("xlsx: finish container: %w", err)
	}
	return out.Bytes(), nil
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

const contentTypesXML = xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
	`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
	`<Default Extension="xml" ContentType="application/xml"/>` +
	`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
	`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
	`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
	`</Types>`

const rootRelsXML = xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
	`</Relationships>`

const workbookRelsXML = xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="` + relTypeWorksheet + `" Target="worksheets/sheet1.xml"/>` +
	`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
	`</Relationships>`

// stylesXML is the smallest style table Excel accepts. Nothing here is used —
// every cell written is style 0 — but Excel warns about a workbook whose
// styles part is absent, so it is cheaper to include than to explain.
const stylesXML = xmlHeader + `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
	`<fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts>` +
	`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
	`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
	`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
	`<cellXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/></cellXfs>` +
	`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
	`</styleSheet>`

func workbookXML(sheetName string) string {
	return xmlHeader + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"` +
		` xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		`<sheets><sheet name="` + escape(sheetName) + `" sheetId="1" r:id="rId1"/></sheets>` +
		`</workbook>`
}

func sheetXML(rows [][]any) (string, error) {
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for r, row := range rows {
		if len(row) > maxColumns {
			return "", fmt.Errorf("xlsx: row %d has %d columns, exceeding the %d column limit", r+1, len(row), maxColumns)
		}
		fmt.Fprintf(&b, `<row r="%d">`, r+1)
		for c, v := range row {
			writeCell(&b, columnName(c+1), r+1, v)
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String(), nil
}

// writeCell emits one <c>. An empty value emits nothing at all, which is how
// a sparse row is expressed and how the reader gives back "" for it.
func writeCell(b *strings.Builder, col string, row int, v any) {
	if v == nil {
		return
	}
	if num, ok := numericValue(v); ok {
		fmt.Fprintf(b, `<c r="%s%d"><v>%s</v></c>`, col, row, num)
		return
	}
	s := stripInvalidXML(stringValue(v))
	if s == "" {
		return
	}
	space := ""
	if s != strings.TrimSpace(s) {
		// Without xml:space the reader is free to drop the whitespace.
		space = ` xml:space="preserve"`
	}
	fmt.Fprintf(b, `<c r="%s%d" t="inlineStr"><is><t%s>%s</t></is></c>`, col, row, space, escape(s))
}

// numericValue reports whether v is a Go number, and renders it for a numeric
// cell. NaN and infinity have no numeric representation in a spreadsheet, so
// they fall through to being written as text.
func numericValue(v any) (string, bool) {
	switch n := v.(type) {
	case int:
		return strconv.FormatInt(int64(n), 10), true
	case int8:
		return strconv.FormatInt(int64(n), 10), true
	case int16:
		return strconv.FormatInt(int64(n), 10), true
	case int32:
		return strconv.FormatInt(int64(n), 10), true
	case int64:
		return strconv.FormatInt(n, 10), true
	case uint:
		return strconv.FormatUint(uint64(n), 10), true
	case uint8:
		return strconv.FormatUint(uint64(n), 10), true
	case uint16:
		return strconv.FormatUint(uint64(n), 10), true
	case uint32:
		return strconv.FormatUint(uint64(n), 10), true
	case uint64:
		return strconv.FormatUint(n, 10), true
	case float32:
		return floatValue(float64(n), 32)
	case float64:
		return floatValue(n, 64)
	}
	return "", false
}

func floatValue(f float64, bits int) (string, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", false
	}
	return strconv.FormatFloat(f, 'f', -1, bits), true
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// columnName converts a 1-based column index to its letters: 1 is A, 27 is AA.
func columnName(n int) string {
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		n--
		i--
		buf[i] = byte('A' + n%26)
		n /= 26
	}
	return string(buf[i:])
}

// stripInvalidXML removes the code points XML 1.0 has no way to represent,
// along with any invalid UTF-8. Emitting them produces a file Excel refuses to
// open, which is a worse outcome than a value missing a control character.
func stripInvalidXML(s string) string {
	ok := true
	for _, r := range s {
		if !validXMLRune(r) {
			ok = false
			break
		}
	}
	if ok && utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range s {
		if r == utf8.RuneError {
			// Distinguish a real U+FFFD from a decode failure.
			if _, size := utf8.DecodeRuneInString(s[i:]); size == 1 {
				continue
			}
		}
		if validXMLRune(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func validXMLRune(r rune) bool {
	switch {
	case r == 0x09 || r == 0x0A || r == 0x0D:
		return true
	case r >= 0x20 && r <= 0xD7FF:
		return true
	case r >= 0xE000 && r <= 0xFFFD:
		return true
	case r >= 0x10000 && r <= 0x10FFFF:
		return true
	}
	return false
}

func escape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// sanitizeSheetName makes a name Excel will accept: non-empty, at most 31
// characters, and free of the characters reserved for cell references. A name
// is corrected rather than rejected so that writing a sheet never fails over a
// cosmetic detail of a config value.
func sanitizeSheetName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case ':', '\\', '/', '?', '*', '[', ']':
			return '_'
		}
		if !validXMLRune(r) {
			return -1
		}
		return r
	}, name)
	name = strings.Trim(name, "'")
	if r := []rune(name); len(r) > 31 {
		name = string(r[:31])
	}
	if strings.TrimSpace(name) == "" {
		return "Sheet1"
	}
	return name
}
