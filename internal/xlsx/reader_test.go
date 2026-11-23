package xlsx

import (
	"fmt"
	"strings"
	"testing"
)

func TestRowsCellTypes(t *testing.T) {
	tests := []struct {
		name string
		book book
		want [][]string
	}{
		{
			name: "shared strings",
			book: book{
				sharedStrings: []string{
					`<si><t>alpha</t></si>`,
					`<si><t>beta</t></si>`,
					`<si><r><t>rich </t></r><r><t>text</t></r></si>`,
					`<si><t xml:space="preserve">  padded  </t></si>`,
				},
				sheets: []bookSheet{{name: "S", rows: `` +
					`<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>` +
					`<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2" t="s"><v>3</v></c></row>`}},
			},
			want: [][]string{{"alpha", "beta"}, {"rich text", "  padded  "}},
		},
		{
			name: "inline strings",
			book: book{sheets: []bookSheet{{name: "S", rows: `` +
				`<row r="1"><c r="A1" t="inlineStr"><is><t>one</t></is></c>` +
				`<c r="B1" t="inlineStr"><is><r><t>tw</t></r><r><t>o</t></r></is></c></row>`}}},
			want: [][]string{{"one", "two"}},
		},
		{
			name: "booleans",
			book: book{sheets: []bookSheet{{name: "S", rows: `` +
				`<row r="1"><c r="A1" t="b"><v>1</v></c><c r="B1" t="b"><v>0</v></c>` +
				`<c r="C1" t="b"><v>TRUE</v></c></row>`}}},
			// Anything that is not the stored 1 is false, including the word.
			want: [][]string{{"TRUE", "FALSE", "FALSE"}},
		},
		{
			name: "formula string results",
			book: book{sheets: []bookSheet{{name: "S", rows: `` +
				`<row r="1"><c r="A1" t="str"><f>CONCAT(B1,C1)</f><v>joined</v></c>` +
				// A str cell is never reinterpreted as a number.
				`<c r="B1" t="str"><v>0012</v></c></row>`}}},
			want: [][]string{{"joined", "0012"}},
		},
		{
			name: "empty cell with a formula holds its place",
			book: book{sheets: []bookSheet{{name: "S", rows: `` +
				`<row r="1"><c r="A1" t="str"><f>IF(1,"","")</f><v></v></c>` +
				`<c r="B1"><v>2</v></c></row>`}}},
			want: [][]string{{"", "2"}},
		},
		{
			name: "numbers lose trailing zeros",
			book: book{sheets: []bookSheet{{name: "S", rows: `` +
				`<row r="1"><c r="A1"><v>1.50</v></c><c r="B1"><v>3.0</v></c>` +
				`<c r="C1"><v>0000012</v></c><c r="D1"><v>1e3</v></c>` +
				`<c r="E1"><v>-2.500</v></c></row>` +
				// Beyond 15 significant digits Excel shows scientific notation.
				`<row r="2"><c r="A2"><v>1234567890123456789</v></c>` +
				// Spaces make it text, not a number.
				`<c r="B2"><v>  42  </v></c>` +
				`<c r="C2"><v>#DIV/0!</v></c></row>`}}},
			want: [][]string{
				{"1.5", "3", "12", "1000", "-2.5"},
				{"1.23456789012346E+18", "  42  ", "#DIV/0!"},
			},
		},
		{
			name: "unicode survives",
			book: book{sheets: []bookSheet{{name: "S", rows: `` +
				`<row r="1"><c r="A1" t="inlineStr"><is><t>naïve · 日本語 · 🚀</t></is></c></row>`}}},
			want: [][]string{{"naïve · 日本語 · 🚀"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := readRows(t, tc.book.write(t), "S")
			if !equalRows(got, tc.want) {
				t.Errorf("rows =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// TestRowsShape pins the ragged, gap-preserving shape that workflows consume:
// rows stop at their own last non-empty cell, missing cells become "", missing
// rows are held open, and trailing empty rows are dropped.
func TestRowsShape(t *testing.T) {
	tests := []struct {
		name string
		rows string
		want [][]string
	}{
		{
			name: "a cell left of the cursor is placed, not appended",
			rows: `<row r="1"><c r="B1"><v>2</v></c><c r="A1"><v>1</v></c><c r="C1"><v>3</v></c></row>`,
			want: [][]string{{"1", "2", "3"}},
		},
		{
			name: "sparse cells become empty strings",
			rows: `<row r="1"><c r="C1"><v>3</v></c></row>`,
			want: [][]string{{"", "", "3"}},
		},
		{
			name: "rows are ragged, not padded to sheet width",
			rows: `<row r="1"><c r="A1"><v>1</v></c><c r="D1"><v>4</v></c></row>` +
				`<row r="2"><c r="A2"><v>1</v></c></row>`,
			want: [][]string{{"1", "", "", "4"}, {"1"}},
		},
		{
			name: "trailing empty cells are dropped",
			rows: `<row r="1"><c r="A1"><v>1</v></c><c r="B1"/><c r="C1"><v></v></c></row>`,
			want: [][]string{{"1"}},
		},
		{
			name: "missing rows hold their place",
			rows: `<row r="1"><c r="A1"><v>1</v></c></row>` +
				`<row r="4"><c r="A4"><v>4</v></c></row>`,
			want: [][]string{{"1"}, nil, nil, {"4"}},
		},
		{
			name: "empty rows in the middle are kept",
			rows: `<row r="1"><c r="A1"><v>1</v></c></row>` +
				`<row r="2"/>` +
				`<row r="3"><c r="A3"><v>3</v></c></row>`,
			want: [][]string{{"1"}, nil, {"3"}},
		},
		{
			name: "trailing empty rows are dropped",
			rows: `<row r="1"><c r="A1"><v>1</v></c></row><row r="2"/><row r="9"/>`,
			want: [][]string{{"1"}},
		},
		{
			name: "cells without a reference follow document order",
			rows: `<row r="1"><c><v>a</v></c><c><v>b</v></c><c r="E1"><v>e</v></c></row>`,
			want: [][]string{{"a", "b", "", "", "e"}},
		},
		{
			name: "a sheet with no rows reads as no rows",
			rows: ``,
			want: nil,
		},
		{
			name: "non-cell elements inside a row are skipped",
			rows: `<row r="1"><c r="A1"><v>1</v></c>` +
				`<extLst><ext uri="x"><thing r="A1"><v>junk</v></thing></ext></extLst>` +
				`<c r="B1"><v>2</v></c></row>`,
			want: [][]string{{"1", "2"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := book{sheets: []bookSheet{{name: "S", rows: tc.rows}}}
			got := readRows(t, b.write(t), "S")
			if !equalRows(got, tc.want) {
				t.Errorf("rows =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// TestSheetResolution covers the part of the format that is easiest to get
// wrong by assuming: the sheet a name refers to is the one the relationships
// table points at, not sheet1.xml and not the first file in the container.
func TestSheetResolution(t *testing.T) {
	b := book{sheets: []bookSheet{
		{name: "Summary", part: "xl/worksheets/sheet7.xml", rows: `<row r="1"><c r="A1"><v>7</v></c></row>`},
		{name: "Raw Data", part: "xl/worksheets/sheet2.xml", rows: `<row r="1"><c r="A1"><v>2</v></c></row>`},
		{name: "Notes", part: "xl/worksheets/sheet1.xml", rows: `<row r="1"><c r="A1"><v>1</v></c></row>`},
	}}
	path := b.write(t)

	f, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()

	if got, want := f.SheetNames(), []string{"Summary", "Raw Data", "Notes"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("SheetNames() = %v, want %v", got, want)
	}
	for name, want := range map[string]string{"Summary": "7", "Raw Data": "2", "Notes": "1"} {
		rows, err := f.Rows(name)
		if err != nil {
			t.Fatalf("Rows(%q): %v", name, err)
		}
		if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != want {
			t.Errorf("Rows(%q) = %q, want [[%q]]", name, rows, want)
		}
	}

	// An unknown name is an error that says which name.
	_, err = f.Rows("Nope")
	if err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Errorf("Rows(unknown) error = %v, want one naming the sheet", err)
	}
}

// TestMalformedInputReturnsErrors is the reason this package replaced
// excelize: a malformed file must produce an error, never a panic that takes
// the daemon and every workflow in it down.
func TestMalformedInputReturnsErrors(t *testing.T) {
	sst := []string{`<si><t>only</t></si>`}

	tests := []struct {
		name    string
		book    book
		wantErr string
	}{
		{
			// GO-2026-6452: excelize indexed the shared-string table without a
			// lower-bound check, so this input panicked.
			name: "negative shared string index",
			book: book{sharedStrings: sst, sheets: []bookSheet{{name: "S",
				rows: `<row r="1"><c r="A1" t="s"><v>-1</v></c></row>`}}},
			wantErr: "invalid shared string index -1",
		},
		{
			name: "shared string index past the end",
			book: book{sharedStrings: sst, sheets: []bookSheet{{name: "S",
				rows: `<row r="1"><c r="A1" t="s"><v>9999</v></c></row>`}}},
			wantErr: "invalid shared string index 9999",
		},
		{
			name: "shared string index into a missing table",
			book: book{sheets: []bookSheet{{name: "S",
				rows: `<row r="1"><c r="A1" t="s"><v>0</v></c></row>`}}},
			wantErr: "invalid shared string index 0",
		},
		{
			name: "shared string index is not a number",
			book: book{sharedStrings: sst, sheets: []bookSheet{{name: "S",
				rows: `<row r="1"><c r="A1" t="s"><v>../../etc/passwd</v></c></row>`}}},
			wantErr: "invalid shared string index",
		},
		{
			name:    "sheet xml is not well formed",
			book:    book{sheets: []bookSheet{{name: "S", rows: `<row r="1"><c r="A1"><v>1</v>`}}},
			wantErr: "sheet S",
		},
		{
			name: "cell reference is nonsense",
			book: book{sheets: []bookSheet{{name: "S",
				rows: `<row r="1"><c r="!!"><v>1</v></c></row>`}}},
			wantErr: "invalid cell reference",
		},
		{
			name: "cell reference is past the last column",
			book: book{sheets: []bookSheet{{name: "S",
				rows: `<row r="1"><c r="ZZZZ1"><v>1</v></c></row>`}}},
			wantErr: "invalid cell reference",
		},
		{
			name:    "worksheet part is missing",
			book:    book{sheets: []bookSheet{{name: "S", rows: ``}}, omit: []string{"xl/worksheets/sheet1.xml"}},
			wantErr: "missing part",
		},
		{
			name: "shared strings xml is not well formed",
			book: book{
				sheets: []bookSheet{{name: "S", rows: `<row r="1"><c r="A1" t="s"><v>0</v></c></row>`}},
				extra:  map[string]string{pathSharedStrings: `<sst><si><t>a</t>`},
			},
			wantErr: "sharedStrings",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := rowsErr(t, tc.book.write(t), "S")
			if err == nil {
				t.Fatalf("got no error, want one containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestMalformedContainerReturnsErrors covers damage to the zip and to the
// workbook-level parts, where the failure has to come out of OpenFile.
func TestMalformedContainerReturnsErrors(t *testing.T) {
	t.Run("corrupt zip", func(t *testing.T) {
		path := writeTemp(t, []byte("PK\x03\x04 this is not a zip file at all"))
		if err := rowsErr(t, path, "S"); err == nil {
			t.Fatal("got no error for a corrupt container")
		}
	})

	t.Run("zip with no workbook part", func(t *testing.T) {
		path := writeZip(t, map[string]string{"hello.txt": "not a spreadsheet"})
		err := rowsErr(t, path, "S")
		if err == nil || !strings.Contains(err.Error(), "xl/workbook.xml") {
			t.Fatalf("error = %v, want one naming the missing workbook part", err)
		}
	})

	t.Run("workbook xml is not well formed", func(t *testing.T) {
		b := book{sheets: []bookSheet{{name: "S", rows: ``}},
			extra: map[string]string{pathWorkbook: `<workbook><sheets>`}}
		err := rowsErr(t, b.write(t), "S")
		if err == nil || !strings.Contains(err.Error(), "workbook.xml") {
			t.Fatalf("error = %v, want a workbook parse error", err)
		}
	})

	t.Run("relationships xml is not well formed", func(t *testing.T) {
		b := book{sheets: []bookSheet{{name: "S", rows: ``}},
			extra: map[string]string{pathWorkbookRels: `<Relationships><Relationship`}}
		err := rowsErr(t, b.write(t), "S")
		if err == nil || !strings.Contains(err.Error(), "workbook.xml.rels") {
			t.Fatalf("error = %v, want a rels parse error", err)
		}
	})

	t.Run("sheet with no relationship", func(t *testing.T) {
		b := book{sheets: []bookSheet{{name: "S", rows: ``}},
			extra: map[string]string{pathWorkbookRels: xmlHeader +
				`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`}}
		err := rowsErr(t, b.write(t), "S")
		if err == nil || !strings.Contains(err.Error(), "no worksheet part") {
			t.Fatalf("error = %v, want an unresolved sheet error", err)
		}
	})

	t.Run("undefined entity is not expanded", func(t *testing.T) {
		b := book{sheets: []bookSheet{{name: "S",
			rows: `<row r="1"><c r="A1" t="inlineStr"><is><t>&boom;</t></is></c></row>`}}}
		if err := rowsErr(t, b.write(t), "S"); err == nil {
			t.Fatal("got no error for an undefined entity")
		}
	})

	t.Run("relationship target escaping the package is contained", func(t *testing.T) {
		// A traversing target must not reach outside the container; it simply
		// resolves to a part that is not there.
		b := book{sheets: []bookSheet{{name: "S", rows: ``}},
			extra: map[string]string{pathWorkbookRels: xmlHeader +
				`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
				`<Relationship Id="rId1" Type="` + relTypeWorksheet + `" Target="../../../../etc/passwd"/>` +
				`</Relationships>`}}
		err := rowsErr(t, b.write(t), "S")
		if err == nil || !strings.Contains(err.Error(), "missing part") {
			t.Fatalf("error = %v, want a missing part error", err)
		}
	})
}

// TestBrokenStylesFallBackToRawValues checks that damage to the style tables
// costs formatting, not data: a cell whose style cannot be resolved still
// yields its stored value.
func TestBrokenStylesFallBackToRawValues(t *testing.T) {
	tests := []struct {
		name    string
		cellXfs []string
		numFmts []string
		cell    string
		want    string
	}{
		{
			name:    "style index past the end of cellXfs",
			cellXfs: []string{`<xf numFmtId="0"/>`},
			cell:    `<c r="A1" s="99"><v>45000</v></c>`,
			want:    "45000",
		},
		{
			name:    "negative style index",
			cellXfs: []string{`<xf numFmtId="0"/>`, `<xf numFmtId="14"/>`},
			cell:    `<c r="A1" s="-3"><v>45000</v></c>`,
			want:    "45000",
		},
		{
			name:    "style index is not a number",
			cellXfs: []string{`<xf numFmtId="0"/>`, `<xf numFmtId="14"/>`},
			cell:    `<c r="A1" s="abc"><v>45000</v></c>`,
			want:    "45000",
		},
		{
			name:    "numFmtId that is not defined anywhere",
			cellXfs: []string{`<xf numFmtId="0"/>`, `<xf numFmtId="200"/>`},
			cell:    `<c r="A1" s="1"><v>45000</v></c>`,
			want:    "45000",
		},
		{
			name:    "xf with no numFmtId at all",
			cellXfs: []string{`<xf numFmtId="0"/>`, `<xf fontId="2"/>`},
			cell:    `<c r="A1" s="1"><v>45000</v></c>`,
			want:    "45000",
		},
		{
			name:    "custom numFmt with an unrenderable code",
			cellXfs: []string{`<xf numFmtId="0"/>`, `<xf numFmtId="164"/>`},
			numFmts: []string{`<numFmt numFmtId="164" formatCode="#,##0.00"/>`},
			want:    "45000",
			cell:    `<c r="A1" s="1"><v>45000</v></c>`,
		},
		{
			name:    "custom numFmt wins over the built-in with the same id",
			cellXfs: []string{`<xf numFmtId="0"/>`, `<xf numFmtId="14"/>`},
			numFmts: []string{`<numFmt numFmtId="14" formatCode="yyyy-mm-dd"/>`},
			cell:    `<c r="A1" s="1"><v>45000</v></c>`,
			want:    "2023-03-15",
		},
		{
			name:    "formatCode16 wins over formatCode",
			cellXfs: []string{`<xf numFmtId="0"/>`, `<xf numFmtId="164"/>`},
			numFmts: []string{`<numFmt numFmtId="164" formatCode="mm-dd-yy" formatCode16="yyyy-mm-dd"/>`},
			cell:    `<c r="A1" s="1"><v>45000</v></c>`,
			want:    "2023-03-15",
		},
		{
			name:    "cellStyleXfs entries are not what s indexes",
			cellXfs: []string{`<xf numFmtId="0"/>`, `<xf numFmtId="14"/>`},
			cell:    `<c r="A1" s="1"><v>45000</v></c>`,
			want:    "03-15-23",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := book{
				cellXfs: tc.cellXfs,
				numFmts: tc.numFmts,
				sheets:  []bookSheet{{name: "S", rows: `<row r="1">` + tc.cell + `</row>`}},
			}
			got := readRows(t, b.write(t), "S")
			if len(got) != 1 || len(got[0]) != 1 || got[0][0] != tc.want {
				t.Errorf("rows = %q, want [[%q]]", got, tc.want)
			}
		})
	}
}

// TestZipBombIsRefused checks the decompression budget: a small container that
// inflates past the cap is an error, not an out-of-memory kill.
func TestZipBombIsRefused(t *testing.T) {
	// The budget is lowered rather than fed half a gigabyte of fixture: the
	// behaviour under test is the refusal, not the size of the number.
	restore := decompressionBudget
	decompressionBudget = 64 << 10
	t.Cleanup(func() { decompressionBudget = restore })

	// A highly compressible sheet far larger than the budget allows.
	var sb strings.Builder
	sb.WriteString(xmlHeader)
	sb.WriteString(`<worksheet xmlns="` + sheetNS + `"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>`)
	sb.WriteString(strings.Repeat("A", int(decompressionBudget)+(1<<20)))
	sb.WriteString(`</t></is></c></row></sheetData></worksheet>`)

	b := book{sheets: []bookSheet{{name: "S", rows: ``}}}
	parts := b.parts()
	parts["xl/worksheets/sheet1.xml"] = sb.String()

	err := rowsErr(t, writeZip(t, parts), "S")
	if err == nil || !strings.Contains(err.Error(), "decompressed size exceeds") {
		t.Fatalf("error = %v, want the decompression budget to be refused", err)
	}
}

// TestRowLimitIsEnforced checks that a row number far out in the grid cannot
// drive unbounded gap filling.
func TestRowLimitIsEnforced(t *testing.T) {
	b := book{sheets: []bookSheet{{name: "S",
		rows: fmt.Sprintf(`<row r="%d"><c r="A%d"><v>1</v></c></row>`, maxRows+1, maxRows+1)}}}
	err := rowsErr(t, b.write(t), "S")
	if err == nil || !strings.Contains(err.Error(), "row limit") {
		t.Fatalf("error = %v, want the row limit to be enforced", err)
	}
}

func TestDuplicateZipEntryKeepsTheFirst(t *testing.T) {
	// Two entries under one name is how a file smuggles a second definition
	// of a part past a reader that keeps the last one.
	b := book{sheets: []bookSheet{{name: "S", rows: `<row r="1"><c r="A1"><v>1</v></c></row>`}}}
	parts := b.parts()
	path := writeZipWithDuplicate(t, parts, "xl/worksheets/sheet1.xml",
		xmlHeader+`<worksheet xmlns="`+sheetNS+`"><sheetData><row r="1"><c r="A1"><v>999</v></c></row></sheetData></worksheet>`)

	got := readRows(t, path, "S")
	if len(got) != 1 || got[0][0] != "1" {
		t.Errorf("rows = %q, want the first entry [[1]]", got)
	}
}
