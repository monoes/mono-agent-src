package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// WriteFile replaces the file through a private temporary file: no stray temp file stays, and the
// result is not readable by other users.
func TestWriteFileIsPrivateAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.xlsx")
	if err := WriteFile(path, "S", [][]any{{"a"}}); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, "S", [][]any{{"b"}}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm()&0o077 != 0 {
			t.Errorf("mode %v, err %v", st.Mode(), err)
		}
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("%d entries in the folder, want only the workbook", len(ents))
	}
	if got := readRows(t, path, "S"); !equalRows(got, [][]string{{"b"}}) {
		t.Errorf("rows %v", got)
	}
}

// TestRoundTrip is the writer's main contract: rows written come back out of
// the reader unchanged.
func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		sheet string
		rows  [][]any
		want  [][]string
	}{
		{
			name:  "plain text",
			sheet: "Sheet1",
			rows:  [][]any{{"name", "city"}, {"Ada", "London"}, {"Grace", "New York"}},
			want:  [][]string{{"name", "city"}, {"Ada", "London"}, {"Grace", "New York"}},
		},
		{
			name:  "empty cells inside a row",
			sheet: "Data",
			rows:  [][]any{{"a", "", "c"}, {"", "b", ""}},
			want:  [][]string{{"a", "", "c"}, {"", "b"}}, // trailing empties are dropped on read
		},
		{
			name:  "unicode",
			sheet: "Ünïcodé",
			rows:  [][]any{{"naïve", "日本語", "🚀", "Ωμέγα"}},
			want:  [][]string{{"naïve", "日本語", "🚀", "Ωμέγα"}},
		},
		{
			name:  "numbers keep their value, not their spelling",
			sheet: "Nums",
			rows:  [][]any{{1, -2, int64(3000000000), 1.5, 0.0, float32(2.5), uint(7)}},
			// A numeric zero is a value, not an empty cell.
			want: [][]string{{"1", "-2", "3000000000", "1.5", "0", "2.5", "7"}},
		},
		{
			name:  "text that looks numeric stays text",
			sheet: "Codes",
			rows:  [][]any{{"0012", "1.50", "+44 20", "1e5"}},
			want:  [][]string{{"0012", "1.50", "+44 20", "1e5"}},
		},
		{
			name:  "xml metacharacters",
			sheet: "Esc",
			rows:  [][]any{{`<tag>`, `a & b`, `"quoted"`, `it's`, "tab\there", "line\nbreak"}},
			want:  [][]string{{`<tag>`, `a & b`, `"quoted"`, `it's`, "tab\there", "line\nbreak"}},
		},
		{
			name:  "whitespace is preserved",
			sheet: "WS",
			rows:  [][]any{{"  leading", "trailing  ", "  both  ", " "}},
			want:  [][]string{{"  leading", "trailing  ", "  both  ", " "}},
		},
		{
			name:  "ragged rows",
			sheet: "Ragged",
			rows:  [][]any{{"a", "b", "c"}, {"d"}, {"e", "f"}},
			want:  [][]string{{"a", "b", "c"}, {"d"}, {"e", "f"}},
		},
		{
			name:  "nil is an empty cell",
			sheet: "Nils",
			rows:  [][]any{{nil, "x", nil}},
			want:  [][]string{{"", "x"}},
		},
		{
			name:  "non-string values are stringified",
			sheet: "Misc",
			rows:  [][]any{{true, false, []string{"a", "b"}}},
			want:  [][]string{{"true", "false", "[a b]"}},
		},
		{
			name:  "no rows",
			sheet: "Empty",
			rows:  nil,
			want:  nil,
		},
		{
			name:  "wide row",
			sheet: "Wide",
			rows:  [][]any{wideRow(120)},
			want:  [][]string{wantWideRow(120)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "out.xlsx")
			if err := WriteFile(path, tc.sheet, tc.rows); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			got := readRows(t, path, tc.sheet)
			if !equalRows(got, tc.want) {
				t.Errorf("round trip =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

func wideRow(n int) []any {
	row := make([]any, n)
	for i := range row {
		row[i] = fmt.Sprintf("c%d", i)
	}
	return row
}

func wantWideRow(n int) []string {
	row := make([]string, n)
	for i := range row {
		row[i] = fmt.Sprintf("c%d", i)
	}
	return row
}

// TestWrittenWorkbookIsWellFormed checks the container itself: the parts a
// spreadsheet application looks for are present, and every one of them parses
// as XML.
func TestWrittenWorkbookIsWellFormed(t *testing.T) {
	data, err := Marshal("Report", [][]any{{"h1", "h2"}, {"v1", 2}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a readable zip: %v", err)
	}

	seen := map[string]string{}
	for _, zf := range zr.File {
		rc, err := zf.Open()
		if err != nil {
			t.Fatalf("open %s: %v", zf.Name, err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", zf.Name, err)
		}
		seen[zf.Name] = string(body)

		d := xml.NewDecoder(bytes.NewReader(body))
		for {
			_, err := d.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s is not well-formed XML: %v", zf.Name, err)
			}
		}
	}

	for _, want := range []string{
		"[Content_Types].xml", "_rels/.rels", pathWorkbook,
		pathWorkbookRels, pathStyles, "xl/worksheets/sheet1.xml",
	} {
		if _, ok := seen[want]; !ok {
			t.Errorf("missing part %s", want)
		}
	}
	if !strings.Contains(seen[pathWorkbook], `name="Report"`) {
		t.Errorf("workbook.xml does not name the sheet: %s", seen[pathWorkbook])
	}
	// Text goes inline, so there is no shared-string table to index into.
	if _, ok := seen[pathSharedStrings]; ok {
		t.Error("a shared string table was written; inline strings were expected")
	}
	if !strings.Contains(seen["xl/worksheets/sheet1.xml"], `t="inlineStr"`) {
		t.Error("text was not written as an inline string")
	}
	// Numbers are numeric cells, with no type attribute.
	if !strings.Contains(seen["xl/worksheets/sheet1.xml"], `<c r="B2"><v>2</v></c>`) {
		t.Errorf("number not written as a numeric cell: %s", seen["xl/worksheets/sheet1.xml"])
	}
}

// TestWriterStripsCharactersXMLCannotCarry checks that a value with control
// characters or broken UTF-8 produces a file that still opens, rather than one
// Excel refuses.
func TestWriterStripsCharactersXMLCannotCarry(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"null byte", "a\x00b", "ab"},
		{"control characters", "a\x01\x02\x1fb", "ab"},
		{"vertical tab and form feed", "a\x0b\x0cb", "ab"},
		{"allowed whitespace survives", "a\tb\nc\rd", "a\tb\nc\rd"},
		{"invalid utf-8 is dropped", "a\xff\xfeb", "ab"},
		{"a lone surrogate byte sequence is dropped", "a\xed\xa0\x80b", "ab"},
		{"noncharacters at the end of the plane are dropped", "a￾b", "ab"},
		{"a real replacement character survives", "a�b", "a�b"},
		{"astral plane survives", "a\U0001F600b", "a\U0001F600b"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "out.xlsx")
			if err := WriteFile(path, "S", [][]any{{tc.in, "guard"}}); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			got := readRows(t, path, "S")
			if tc.want == "" {
				if len(got) != 1 || got[0][0] != "" {
					t.Fatalf("got %q, want an empty first cell", got)
				}
				return
			}
			if len(got) != 1 || got[0][0] != tc.want {
				t.Errorf("got %q, want first cell %q", got, tc.want)
			}
		})
	}
}

func TestSanitizeSheetName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Sheet1", "Sheet1"},
		{"", "Sheet1"},
		{"   ", "Sheet1"},
		{"Q1 Results", "Q1 Results"},
		{"a/b\\c?d*e[f]g:h", "a_b_c_d_e_f_g_h"},
		{strings.Repeat("x", 40), strings.Repeat("x", 31)},
		{"日本語のシート", "日本語のシート"},
		{strings.Repeat("日", 40), strings.Repeat("日", 31)}, // 31 characters, not bytes
		{"has\x00null", "hasnull"},
		{"'quoted'", "quoted"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := sanitizeSheetName(tc.in); got != tc.want {
				t.Errorf("sanitizeSheetName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSanitizedSheetNameIsWhatTheReaderFinds guards against writing a sheet
// under one name and looking it up under another.
func TestSanitizedSheetNameIsWhatTheReaderFinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.xlsx")
	if err := WriteFile(path, "bad/name", [][]any{{"x"}}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	f, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()
	if got := f.SheetNames(); len(got) != 1 || got[0] != "bad_name" {
		t.Fatalf("SheetNames() = %v, want [bad_name]", got)
	}
}

func TestColumnName(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{1, "A"}, {2, "B"}, {26, "Z"}, {27, "AA"}, {28, "AB"},
		{52, "AZ"}, {53, "BA"}, {702, "ZZ"}, {703, "AAA"}, {16384, "XFD"},
	}
	for _, tc := range tests {
		if got := columnName(tc.n); got != tc.want {
			t.Errorf("columnName(%d) = %q, want %q", tc.n, got, tc.want)
		}
		// columnName and columnOf must agree, since one writes references and
		// the other reads them.
		back, err := columnOf(tc.want + "1")
		if err != nil || back != tc.n {
			t.Errorf("columnOf(%q) = %d, %v; want %d", tc.want+"1", back, err, tc.n)
		}
	}
}

func TestWriterRefusesOversizedInput(t *testing.T) {
	t.Run("too many columns", func(t *testing.T) {
		row := make([]any, maxColumns+1)
		_, err := Marshal("S", [][]any{row})
		if err == nil || !strings.Contains(err.Error(), "column limit") {
			t.Fatalf("error = %v, want the column limit to be enforced", err)
		}
	})
}

// TestWriteFileReplacesAtomically checks that a failed write cannot leave a
// truncated file where a good one was.
func TestWriteFileReplacesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.xlsx")
	if err := WriteFile(path, "S", [][]any{{"first"}}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	row := make([]any, maxColumns+1)
	if err := WriteFile(path, "S", [][]any{row}); err == nil {
		t.Fatal("expected the oversized write to fail")
	}
	// The original content is still readable.
	got := readRows(t, path, "S")
	if len(got) != 1 || got[0][0] != "first" {
		t.Errorf("after a failed write, rows = %q, want the original [[first]]", got)
	}
}

// TestNonNumericFloatsBecomeText checks values a spreadsheet has no number for.
func TestNonNumericFloatsBecomeText(t *testing.T) {
	inf, nan := math.Inf(1), math.NaN()
	path := filepath.Join(t.TempDir(), "out.xlsx")
	if err := WriteFile(path, "S", [][]any{{inf, nan}}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got := readRows(t, path, "S")
	if len(got) != 1 || got[0][0] != "+Inf" || got[0][1] != "NaN" {
		t.Errorf("rows = %q, want [[+Inf NaN]]", got)
	}
}
