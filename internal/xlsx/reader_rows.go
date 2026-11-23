package xlsx

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Rows returns every row of the named sheet as a slice of cell values.
//
// The shape matches what excelize's File.GetRows produced, because workflows
// already depend on it:
//
//   - Each row runs up to its own last non-empty cell, so rows are ragged;
//     they are not padded out to the width of the sheet.
//   - A cell reference may be missing, and then the cell is "" — a row that
//     declares only C3 comes back as ["", "", <C3>].
//   - A row may be missing entirely, and then an empty row holds its place so
//     that row N of the sheet stays at index N-1.
//   - Trailing empty rows are dropped.
//
// An unknown sheet name is an error.
func (f *File) Rows(sheet string) ([][]string, error) {
	part := ""
	found := false
	for _, s := range f.sheets {
		if s.name == sheet {
			part, found = s.part, true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("sheet %s does not exist", sheet)
	}
	if part == "" {
		return nil, fmt.Errorf("xlsx: sheet %s has no worksheet part", sheet)
	}
	rc, ok, err := f.open(part)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("xlsx: sheet %s: missing part %s", sheet, part)
	}
	defer rc.Close()

	if err := f.styles(); err != nil {
		return nil, err
	}

	d := newDecoder(rc)
	var out [][]string
	rowNum := 0
	cells := 0
	inSheetData := false

	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xlsx: sheet %s: %w", sheet, err)
		}
		switch se := tok.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "sheetData":
				inSheetData = true
			case "row":
				if !inSheetData {
					continue
				}
				rowNum++
				if r, ok := attrInt(se.Attr, "r"); ok && r > rowNum {
					// Trust r only to skip forward. A row numbered at or
					// before one already emitted would otherwise let a file
					// rewrite earlier output or drive the gap fill negative;
					// such a row is appended in document order instead.
					rowNum = r
				}
				if rowNum > maxRows {
					return nil, fmt.Errorf("xlsx: sheet %s: row %d exceeds the %d row limit", sheet, rowNum, maxRows)
				}
				row, err := f.readRow(d, &se, &cells)
				if err != nil {
					return nil, fmt.Errorf("xlsx: sheet %s: row %d: %w", sheet, rowNum, err)
				}
				if len(row) == 0 {
					continue
				}
				for len(out) < rowNum-1 {
					out = append(out, nil)
				}
				out = append(out, row)
			}
		case xml.EndElement:
			if se.Name.Local == "sheetData" {
				// Nothing after the cells is of interest, and walking it
				// would mean decoding the rest of the part for nothing.
				return out, nil
			}
		}
	}
	return out, nil
}

// rawCell is a <c> record. Every attribute is kept as a string so that a
// malformed value is a value we reject, not a decode error for the whole file.
type rawCell struct {
	R  string    `xml:"r,attr"`
	S  string    `xml:"s,attr"`
	T  string    `xml:"t,attr"`
	V  *string   `xml:"v"`
	IS *richText `xml:"is"`
	F  *struct {
		Text string `xml:",chardata"`
	} `xml:"f"`
}

// readRow decodes the cells of one <row>, returning them positioned by column
// and truncated at the last non-empty cell.
func (f *File) readRow(d *xml.Decoder, start *xml.StartElement, cells *int) ([]string, error) {
	var row []string
	col := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return row, nil
		}
		if err != nil {
			return nil, err
		}
		switch se := tok.(type) {
		case xml.StartElement:
			if se.Name.Local != "c" {
				// Skip whole subtrees such as <extLst> rather than walking
				// into them, so nothing inside can be mistaken for a cell or
				// for the end of the row.
				if err := d.Skip(); err != nil {
					return nil, err
				}
				continue
			}
			col++
			var c rawCell
			if err := d.DecodeElement(&c, &se); err != nil {
				return nil, err
			}
			if c.R != "" {
				n, err := columnOf(c.R)
				if err != nil {
					return nil, err
				}
				col = n
			}
			if col > maxColumns {
				return nil, fmt.Errorf("column %d exceeds the %d column limit", col, maxColumns)
			}
			val, err := f.cellValue(&c)
			if err != nil {
				return nil, err
			}
			// An empty value contributes nothing unless the cell carries a
			// formula, which is how excelize decided a cell existed; that in
			// turn is what makes rows ragged rather than sheet-wide.
			if val == "" && c.F == nil {
				continue
			}
			// Count the padding as well as the value, so a sheet that places
			// one cell far to the right on every row is bounded too. A cell
			// left of one already seen adds no padding.
			pad := 0
			if col-1 > len(row) {
				pad = col - 1 - len(row)
			}
			*cells += pad + 1
			if *cells > maxCells {
				return nil, fmt.Errorf("sheet exceeds the %d cell limit", maxCells)
			}
			if col-1 < len(row) { // an out-of-order or repeated reference: place it, do not shift the rest
				if val != "" {
					row[col-1] = val
				}
				continue
			}
			for len(row) < col-1 {
				row = append(row, "")
			}
			row = append(row, val)
		case xml.EndElement:
			if se.Name.Local == start.Name.Local {
				return row, nil
			}
		}
	}
}

// columnOf extracts the 1-based column index from a cell reference like "AB12".
func columnOf(ref string) (int, error) {
	col := 0
	for i := 0; i < len(ref); i++ {
		ch := ref[i]
		switch {
		case ch >= 'A' && ch <= 'Z':
			col = col*26 + int(ch-'A') + 1
		case ch >= 'a' && ch <= 'z':
			col = col*26 + int(ch-'a') + 1
		case ch >= '0' && ch <= '9':
			if col == 0 {
				return 0, fmt.Errorf("invalid cell reference %q", ref)
			}
			return col, nil
		default:
			return 0, fmt.Errorf("invalid cell reference %q", ref)
		}
		if col > maxColumns {
			return 0, fmt.Errorf("invalid cell reference %q: column out of range", ref)
		}
	}
	if col == 0 {
		return 0, fmt.Errorf("invalid cell reference %q", ref)
	}
	return col, nil
}

// cellValue renders one cell to the string a workflow sees.
func (f *File) cellValue(c *rawCell) (string, error) {
	v := ""
	if c.V != nil {
		v = *c.V
	}
	switch c.T {
	case "b":
		// Booleans render as Excel displays them, not as the stored 0/1.
		if strings.TrimSpace(v) == "1" {
			return "TRUE", nil
		}
		return "FALSE", nil

	case "s":
		if v == "" {
			return f.formatted(c.S, "", false), nil
		}
		idx, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return "", fmt.Errorf("invalid shared string index %q", v)
		}
		sst, err := f.sharedStrings()
		if err != nil {
			return "", err
		}
		// The whole reason this package exists: excelize indexed here without
		// a lower-bound check, so a negative index panicked (GO-2026-6452).
		if idx < 0 || idx >= len(sst) {
			return "", fmt.Errorf("invalid shared string index %d", idx)
		}
		return f.formatted(c.S, sst[idx], false), nil

	case "str":
		// A formula's cached string result is shown as stored, never
		// number-formatted.
		return v, nil

	case "inlineStr":
		if c.IS != nil {
			return f.formatted(c.S, c.IS.String(), false), nil
		}
		return f.formatted(c.S, v, false), nil

	default:
		// Numbers, dates and error values all live here.
		return f.formatted(c.S, v, true), nil
	}
}

// formatted applies the cell's number format. numeric says whether the value
// should first be normalised as a number — true for numeric cells, false for
// text, which is never reinterpreted as a number even under a date format.
//
// Every failure to resolve a style or a format code falls back to the raw
// value rather than erroring: an unreadable format is a display detail, and a
// file with a broken style table still has readable data.
func (f *File) formatted(sAttr, v string, numeric bool) string {
	if numeric {
		v = normalizeNumber(v)
	}
	if sAttr == "" || sAttr == "0" {
		return v
	}
	s, err := strconv.Atoi(strings.TrimSpace(sAttr))
	if err != nil || s < 0 || s >= len(f.cellXfs) {
		return v
	}
	numFmtID := f.cellXfs[s]
	code, ok := f.numFmts[numFmtID]
	if !ok {
		code, ok = builtInNumFmt[numFmtID]
		if !ok {
			return v
		}
	}
	if out, ok := formatDateTime(v, code, f.date1904); ok {
		return out
	}
	return v
}

// normalizeNumber renders a stored number the way Excel displays it under the
// General format: trailing zeros dropped ("1.50" -> "1.5", "3.0" -> "3"), and
// anything beyond 15 significant digits shown in scientific notation, which is
// the precision Excel itself keeps. Values that are not numbers — including
// ones padded with spaces — pass through untouched.
func normalizeNumber(v string) string {
	if v == "" || strings.Contains(v, "_") {
		return v
	}
	fl, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return v
	}
	plain := strconv.FormatFloat(fl, 'f', -1, 64)
	if len(plain)-strings.Count(plain, ".") > 15 {
		return strconv.FormatFloat(fl, 'G', 15, 64)
	}
	return plain
}
