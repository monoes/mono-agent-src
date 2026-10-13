package vector

import (
	"bytes"
	"compress/zlib"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// A deliberately small, dependency-free PDF text extractor. It handles the
// common case — text drawn with Tj/TJ/'/" in Flate-compressed or plain
// content streams using simple (single-byte) fonts — and returns one string
// per page.
//
// Known limits, by design: no ToUnicode CMap decoding (text in
// composite/CID fonts, typical of some CJK and subset-embedded PDFs, comes
// out garbled or empty), no decryption, no page dictionaries inside
// compressed object streams (then all text-bearing streams are returned in
// file order, one entry each), no OCR of scanned pages. Text order follows
// the content stream, not the visual layout.

var (
	pdfObjRe      = regexp.MustCompile(`(?s)(\d+)\s+(\d+)\s+obj\b(.*?)\bendobj`)
	pdfStreamRe   = regexp.MustCompile(`(?s)^(.*?)stream\r?\n(.*)\r?\n?endstream`)
	pdfPageTypeRe = regexp.MustCompile(`/Type\s*/Page\b`)
	pdfContentsRe = regexp.MustCompile(`/Contents\s*(\[[^\]]*\]|\d+\s+\d+\s+R)`)
	pdfRefRe      = regexp.MustCompile(`(\d+)\s+\d+\s+R`)
)

type pdfObj struct {
	dict   string
	stream []byte // decoded; nil when the object has no (decodable) stream
}

// extractPDFPages returns the text of each page.
func extractPDFPages(data []byte) []string {
	objs := map[string]pdfObj{}
	var order []string
	for _, m := range pdfObjRe.FindAllSubmatch(data, -1) {
		num := string(m[1])
		body := m[3]
		o := pdfObj{dict: string(body)}
		if sm := pdfStreamRe.FindSubmatch(body); sm != nil {
			o.dict = string(sm[1])
			o.stream = decodePDFStream(o.dict, sm[2])
		}
		objs[num] = o
		order = append(order, num)
	}
	var pages []string
	for _, num := range order {
		o := objs[num]
		if o.stream != nil || !pdfPageTypeRe.MatchString(o.dict) {
			continue
		}
		cm := pdfContentsRe.FindStringSubmatch(o.dict)
		if cm == nil {
			pages = append(pages, "")
			continue
		}
		var sb strings.Builder
		for _, ref := range pdfRefRe.FindAllStringSubmatch(cm[1], -1) {
			if c, ok := objs[ref[1]]; ok && c.stream != nil {
				sb.WriteString(pdfContentText(c.stream))
			}
		}
		pages = append(pages, strings.TrimSpace(sb.String()))
	}
	if len(pages) > 0 {
		return pages
	}
	// No page dictionaries found: fall back to every stream with text.
	for _, num := range order {
		if s := objs[num].stream; s != nil && bytes.Contains(s, []byte("BT")) {
			if t := strings.TrimSpace(pdfContentText(s)); t != "" {
				pages = append(pages, t)
			}
		}
	}
	return pages
}

func decodePDFStream(dict string, raw []byte) []byte {
	if !strings.Contains(dict, "/Filter") {
		return raw
	}
	if !strings.Contains(dict, "/FlateDecode") || strings.Count(dict, "Decode") > 1 {
		return nil // other or chained filters are not supported
	}
	r, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, maxResponseBody))
	if err != nil && len(out) == 0 {
		return nil
	}
	return out
}

// pdfContentText interprets the text operators of one content stream.
func pdfContentText(s []byte) string {
	var sb strings.Builder
	var operands []interface{} // string | float64 | []interface{}
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '(':
			str, n := pdfLiteral(s[i:])
			operands = append(operands, str)
			i += n
		case c == '<' && i+1 < len(s) && s[i+1] != '<':
			end := bytes.IndexByte(s[i:], '>')
			if end < 0 {
				return sb.String()
			}
			operands = append(operands, pdfHex(s[i+1:i+end]))
			i += end + 1
		case c == '[':
			arr, n := pdfArray(s[i:])
			operands = append(operands, arr)
			i += n
		case c == '%':
			for i < len(s) && s[i] != '\n' && s[i] != '\r' {
				i++
			}
		case isPDFSpace(c) || c == ']' || c == '<' || c == '>':
			i++
		default:
			j := i
			for j < len(s) && !isPDFSpace(s[j]) && !strings.ContainsRune("()<>[]/%", rune(s[j])) {
				j++
			}
			if j == i { // a name like /F1
				j = i + 1
				for j < len(s) && !isPDFSpace(s[j]) && !strings.ContainsRune("()<>[]/%", rune(s[j])) {
					j++
				}
				operands = append(operands, nil)
				i = j
				continue
			}
			tok := string(s[i:j])
			i = j
			if f, err := strconv.ParseFloat(tok, 64); err == nil {
				operands = append(operands, f)
				continue
			}
			pdfApplyOp(&sb, tok, operands)
			operands = operands[:0]
		}
	}
	return sb.String()
}

func pdfApplyOp(sb *strings.Builder, op string, ops []interface{}) {
	newline := func() {
		if sb.Len() > 0 && !strings.HasSuffix(sb.String(), "\n") {
			sb.WriteByte('\n')
		}
	}
	switch op {
	case "Tj":
		if len(ops) > 0 {
			if t, ok := ops[len(ops)-1].(string); ok {
				sb.WriteString(t)
			}
		}
	case "'", "\"":
		newline()
		if len(ops) > 0 {
			if t, ok := ops[len(ops)-1].(string); ok {
				sb.WriteString(t)
			}
		}
	case "TJ":
		if len(ops) == 0 {
			return
		}
		arr, _ := ops[len(ops)-1].([]interface{})
		for _, e := range arr {
			switch v := e.(type) {
			case string:
				sb.WriteString(v)
			case float64:
				if v < -200 { // a large negative kern is a word gap
					sb.WriteByte(' ')
				}
			}
		}
	case "T*", "ET":
		newline()
	case "Td", "TD":
		if len(ops) >= 2 {
			if ty, ok := ops[len(ops)-1].(float64); ok && ty != 0 {
				newline()
			} else if tx, ok := ops[len(ops)-2].(float64); ok && tx > 0 && !strings.HasSuffix(sb.String(), " ") && sb.Len() > 0 {
				sb.WriteByte(' ')
			}
		}
	}
}

func isPDFSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0
}

// pdfLiteral decodes a (…) string starting at s[0]; it returns the text and
// the number of bytes consumed.
func pdfLiteral(s []byte) (string, int) {
	var out []byte
	depth := 0
	i := 0
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '(':
			depth++
			if depth == 1 {
				continue
			}
		case c == ')':
			depth--
			if depth == 0 {
				return pdfDecodeText(out), i + 1
			}
		case c == '\\' && i+1 < len(s):
			i++
			switch e := s[i]; e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b', 'f':
			case '\r', '\n':
			default:
				if e >= '0' && e <= '7' {
					j := i
					for j < len(s) && j < i+3 && s[j] >= '0' && s[j] <= '7' {
						j++
					}
					v, _ := strconv.ParseUint(string(s[i:j]), 8, 8)
					out = append(out, byte(v))
					i = j - 1
				} else {
					out = append(out, e)
				}
			}
			continue
		}
		out = append(out, c)
	}
	return pdfDecodeText(out), i
}

func pdfHex(h []byte) string {
	var clean []byte
	for _, c := range h {
		if !isPDFSpace(c) {
			clean = append(clean, c)
		}
	}
	if len(clean)%2 == 1 {
		clean = append(clean, '0')
	}
	out := make([]byte, len(clean)/2)
	for i := range out {
		v, _ := strconv.ParseUint(string(clean[2*i:2*i+2]), 16, 8)
		out[i] = byte(v)
	}
	return pdfDecodeText(out)
}

// pdfArray parses a [...] operand of strings and numbers.
func pdfArray(s []byte) ([]interface{}, int) {
	var arr []interface{}
	i := 1
	for i < len(s) {
		switch c := s[i]; {
		case c == ']':
			return arr, i + 1
		case c == '(':
			t, n := pdfLiteral(s[i:])
			arr = append(arr, t)
			i += n
		case c == '<':
			end := bytes.IndexByte(s[i:], '>')
			if end < 0 {
				return arr, len(s)
			}
			arr = append(arr, pdfHex(s[i+1:i+end]))
			i += end + 1
		case isPDFSpace(c):
			i++
		default:
			j := i
			for j < len(s) && !isPDFSpace(s[j]) && s[j] != ']' && s[j] != '(' && s[j] != '<' {
				j++
			}
			if f, err := strconv.ParseFloat(string(s[i:j]), 64); err == nil {
				arr = append(arr, f)
			}
			if j == i {
				j++
			}
			i = j
		}
	}
	return arr, i
}

// pdfDecodeText reads UTF-16BE (with BOM) or single-byte text.
func pdfDecodeText(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF {
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		}
		return string(utf16.Decode(u))
	}
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c) // Latin-1 ≈ PDFDocEncoding for printable text
	}
	return string(r)
}
