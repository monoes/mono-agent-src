package apipkg

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

var knownFilters = map[string]bool{
	"json": true, "csv": true, "int": true, "float": true, "bool": true,
	"string": true, "lower": true, "upper": true, "default": true, "rl": true,
	"then": true, "eq": true, "ne": true, "kv": true, "omit": true,
	"fromjson": true, "epoch_ms": true, "epoch": true, "split": true, "date": true,
	"prefix": true, "suffix": true, "base64": true, "base64url": true, "row": true,
	"gid": true, "find": true, "find_row": true,
	"add": true, "sub": true, "colnum": true, "colname": true, "get": true, "sheetgid": true, "entries": true, "path": true,
	"lines": true, "escape_html": true, "join": true, "mul": true, "numeric": true,
	"firestore_value": true, "firestore_fields": true, "pick": true,
	"replace": true, "re": true, "zip": true, "pluck": true, "hex": true, "values": true, "snake": true, "matches": true, "in": true, "flatten": true, "unflatten": true,
	"len": true, "urlencode": true,
	"md5": true, "sha1": true, "sha256": true, "mod": true, "weekday": true, "dotnet_date": true,
	"wrap": true, "mime_parts": true, "sealbox": true,
	"unique": true, "set": true, "not": true, "bind": true, "base64_chunks": true,
	"map": true,
}

// applyFilter runs one pipe filter. Filters other than default and then pass
// nil through, so an unset param stays unset (and is omitted).
func applyFilter(name string, v, arg any) (any, error) {
	switch name {
	case "default":
		if isEmpty(v) {
			return arg, nil
		}
		return v, nil
	case "then":
		if unset(v) {
			return nil, nil
		}
		return arg, nil
	case "eq":
		if stringify(v) == stringify(arg) {
			return true, nil
		}
		return nil, nil
	case "ne":
		if stringify(v) != stringify(arg) {
			return true, nil
		}
		return nil, nil
	case "set":
		return setKey(v, arg) // nil is {}: the object is built from nothing
	case "not":
		if unset(v) {
			return true, nil
		}
		return nil, nil
	case "rl":
		// n8n resource locator: {"__rl": true, "mode": "id", "value": "x"}.
		// A "url" locator holding a Google Docs/Sheets/Drive/Slides URL
		// yields the file id, which is what the APIs take.
		if m, ok := v.(map[string]any); ok {
			if val, ok := m["value"]; ok {
				if m["mode"] == "url" {
					return googleFileID(val), nil
				}
				return val, nil
			}
		}
		return v, nil
	}
	if v == nil {
		return nil, nil
	}
	switch name {
	case "len":
		return lengthOf(v), nil
	case "map":
		return mapThrough(v, stringify(arg)), nil
	case "urlencode":
		return urlEncode(v), nil
	case "json":
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("json: %w", err)
		}
		return string(b), nil
	case "csv":
		return toCSV(v), nil
	case "int":
		return toInt(v)
	case "float":
		return toFloat(v)
	case "bool":
		return toBool(v)
	case "string":
		return stringify(v), nil
	case "kv":
		return toKV(v, arg)
	case "fromjson":
		return fromJSON(v)
	case "split":
		return splitString(v, arg)
	case "prefix", "suffix", "escape_html":
		// Lists are handled element-wise (lines | escape_html | prefix:'<p>').
		if l, ok := v.([]any); ok {
			out := make([]any, 0, len(l))
			for _, el := range l {
				r, err := applyFilter(name, el, arg)
				if err != nil {
					return nil, err
				}
				if !isEmpty(r) {
					out = append(out, r)
				}
			}
			return out, nil
		}
		if isEmpty(v) {
			return nil, nil
		}
		switch name {
		case "prefix":
			return stringify(arg) + stringify(v), nil
		case "suffix":
			return stringify(v) + stringify(arg), nil
		}
		return html.EscapeString(stringify(v)), nil
	case "firestore_value":
		return firestoreValue(v), nil
	case "firestore_fields":
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("firestore_fields: value is %T, not an object", v)
		}
		var cols []string
		if a := stringify(arg); a != "" {
			for _, c := range strings.Split(a, ",") {
				if c = strings.TrimSpace(c); c != "" {
					cols = append(cols, c)
				}
			}
		}
		return firestoreFields(obj, cols), nil
	case "matches":
		re, err := cachedRegexp(stringify(arg))
		if err != nil {
			return nil, fmt.Errorf("matches: %w", err)
		}
		if re.MatchString(stringify(v)) {
			return true, nil
		}
		return nil, nil
	case "in":
		var set []any
		if l, ok := arg.([]any); ok {
			set = l
		} else {
			for _, x := range strings.Split(stringify(arg), ",") {
				set = append(set, strings.TrimSpace(x))
			}
		}
		for _, x := range set {
			if stringify(x) == stringify(v) {
				return true, nil
			}
		}
		return nil, nil
	case "unflatten":
		m, ok := v.(map[string]any)
		if !ok {
			return v, nil
		}
		out := map[string]any{}
		for _, k := range sortedKeys(m) {
			setPath(out, k, m[k])
		}
		return out, nil
	case "flatten":
		// Lists flatten at every depth; objects of lists only at the top
		// (Rocket.Chat {update, remove}); objects inside lists stay elements.
		var out []any
		var add func(x any, top bool)
		add = func(x any, top bool) {
			switch t := x.(type) {
			case []any:
				for _, e := range t {
					add(e, false)
				}
			case map[string]any:
				if !top {
					out = append(out, t)
					return
				}
				for _, k := range sortedKeys(t) {
					add(t[k], false)
				}
			default:
				if x != nil {
					out = append(out, x)
				}
			}
		}
		add(v, true)
		return out, nil
	case "snake":
		str := stringify(v)
		if stringify(arg) == "ascii" {
			str = foldNonASCII(str)
		}
		return snakeCase(str), nil
	case "values":
		m, ok := v.(map[string]any)
		if !ok {
			return v, nil
		}
		return objectValues(m), nil
	case "hex":
		h := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(stringify(v)), "#"), "0x")
		if h == "" {
			return nil, nil
		}
		n, err := strconv.ParseInt(h, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("hex: %q is not hex", stringify(v))
		}
		return n, nil
	case "pluck":
		l, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("pluck: value is %T, not a list", v)
		}
		path := strings.Split(stringify(arg), ".")
		var out []any
		for _, el := range l {
			if x := lookupPath(el, path); !isEmpty(x) {
				out = append(out, x)
			}
		}
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	case "zip":
		a := arg.([]any)
		hk, vk := "", ""
		if len(a) > 1 {
			hk = stringify(a[1])
		}
		if len(a) > 2 {
			vk = stringify(a[2])
		}
		return zipLists(a[0], v, hk, vk), nil
	case "replace":
		a := arg.([]any)
		return strings.ReplaceAll(stringify(v), stringify(a[0]), stringify(a[1])), nil
	case "re":
		re, err := cachedRegexp(stringify(arg))
		if err != nil {
			return nil, fmt.Errorf("re: %w", err)
		}
		s := stringify(v)
		m := re.FindStringSubmatch(s)
		switch {
		case m == nil:
			return v, nil
		case len(m) > 1:
			return m[1], nil
		}
		return m[0], nil
	case "lines":
		var out []any
		for _, ln := range strings.Split(strings.ReplaceAll(stringify(v), "\r\n", "\n"), "\n") {
			if t := strings.TrimSpace(ln); t != "" {
				out = append(out, t)
			}
		}
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	case "join":
		l, ok := v.([]any)
		if !ok {
			return stringify(v), nil
		}
		parts := make([]string, len(l))
		for i, el := range l {
			parts[i] = stringify(el)
		}
		return strings.Join(parts, stringify(arg)), nil
	case "numeric":
		if s, ok := v.(string); ok {
			if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
				if f == math.Trunc(f) {
					return int64(f), nil
				}
				return f, nil
			}
		}
		return v, nil
	case "base64":
		return base64.StdEncoding.EncodeToString([]byte(stringify(v))), nil
	case "base64url":
		return base64.RawURLEncoding.EncodeToString([]byte(stringify(v))), nil
	case "row":
		args := arg.([]any)
		nullMissing := len(args) > 1 && (args[1] == nil || stringify(args[1]) == "null")
		return toRow(v, args[0], nullMissing)
	case "path":
		// Escape each segment but keep the slashes of resource names
		// (spaces/AAA/messages/BBB) in a path template.
		// path:'query' keeps a "?query" suffix as is (a server-relative
		// link with its own query, e.g. Confluence's downloadLink).
		p, query := stringify(v), ""
		if stringify(arg) == "query" {
			if i := strings.Index(p, "?"); i >= 0 {
				p, query = p[:i], p[i:]
			}
		}
		segs := strings.Split(p, "/")
		for i, sg := range segs {
			segs[i] = url.PathEscape(sg)
		}
		return pathValue(strings.Join(segs, "/") + query), nil
	case "entries":
		return toEntries(v, arg)
	case "sheetgid":
		return sheetGID(v), nil
	case "gid":
		return googleFileID(v), nil
	case "add", "sub", "mul":
		a, err := toFloat(v)
		if err != nil || a == nil {
			return nil, err
		}
		b, err := toFloat(arg)
		if err != nil || b == nil {
			return nil, fmt.Errorf("%s: argument %v is not a number", name, arg)
		}
		x, y := a.(float64), b.(float64)
		var r float64
		switch name {
		case "sub":
			r = x - y
		case "mul":
			r = x * y
		default:
			r = x + y
		}
		if r == math.Trunc(r) {
			return int64(r), nil
		}
		return r, nil
	case "get":
		return lookupPath(v, strings.Split(stringify(arg), ".")), nil
	case "colnum":
		return columnNumber(stringify(v))
	case "colname":
		n, err := toInt(v)
		if err != nil || n == nil {
			return nil, err
		}
		return columnName(n.(int64))
	case "find":
		a := arg.([]any)
		return findIn(v, stringify(a[0]), a[1]), nil
	case "find_row":
		a := arg.([]any)
		return findRow(v, stringify(a[0]), a[1]), nil
	case "date":
		return formatDate(v, arg)
	case "epoch_ms":
		return toEpoch(v, true)
	case "epoch":
		return toEpoch(v, false)
	case "omit":
		return omitKeys(v, arg)
	case "pick":
		return pickKeys(v, arg)
	case "md5", "sha1", "sha256":
		return hashHex(name, v, arg)
	case "mod":
		return modulo(v, arg)
	case "sealbox":
		return sealBox(v, arg)
	case "weekday":
		return isoWeekday(v)
	case "dotnet_date":
		return dotnetDates(v), nil
	case "wrap":
		return wrapLines(v, arg)
	case "mime_parts":
		return mimeParts(v, arg)
	case "unique":
		return uniqueList(v, arg)
	case "bind":
		return bindParams(v, arg)
	case "base64_chunks":
		return base64Chunks(v, arg)
	case "lower":
		return strings.ToLower(stringify(v)), nil
	case "upper":
		return strings.ToUpper(stringify(v)), nil
	}
	return nil, fmt.Errorf("unknown filter %q", name)
}

func toCSV(v any) any {
	switch t := v.(type) {
	case []any:
		parts := make([]string, 0, len(t))
		for _, el := range t {
			parts = append(parts, stringify(el))
		}
		return strings.Join(parts, ",")
	case []string:
		return strings.Join(t, ",")
	}
	return stringify(v)
}

func toInt(v any) (any, error) {
	switch t := v.(type) {
	case float64:
		return int64(math.Trunc(t)), nil
	case int:
		return int64(t), nil
	case int64:
		return t, nil
	case bool:
		if t {
			return int64(1), nil
		}
		return int64(0), nil
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return nil, nil
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("int: %q is not a number", t)
		}
		return int64(math.Trunc(f)), nil
	}
	return nil, fmt.Errorf("int: cannot convert %T", v)
}

func toFloat(v any) (any, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case int:
		return float64(t), nil
	case int64:
		return float64(t), nil
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return nil, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("float: %q is not a number", t)
		}
		return f, nil
	}
	return nil, fmt.Errorf("float: cannot convert %T", v)
}

func toBool(v any) (any, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	case float64:
		return t != 0, nil
	case int:
		return t != 0, nil
	case int64:
		return t != 0, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "":
			return nil, nil
		case "true", "1", "yes", "on":
			return true, nil
		case "false", "0", "no", "off":
			return false, nil
		}
		return nil, fmt.Errorf("bool: %q is not a boolean", t)
	}
	return nil, fmt.Errorf("bool: cannot convert %T", v)
}

// toKV turns a list of {<key>: k, <value>: v} rows (n8n's fixedCollection
// key/value lists) into the object {k: v}. arg is "keyField,valueField".
// A single-key object wrapping the list (e.g. {"fieldValues": [...]}) is
// unwrapped first.
func toKV(v, arg any) (any, error) {
	names := strings.SplitN(stringify(arg), ",", 3)
	if len(names) < 2 || strings.TrimSpace(names[0]) == "" || strings.TrimSpace(names[1]) == "" {
		return nil, fmt.Errorf(`kv: argument must be "keyField,valueField" or "keyField,valueField,keyPrefix", got %q`, stringify(arg))
	}
	kf, vf := strings.TrimSpace(names[0]), strings.TrimSpace(names[1])
	prefix := ""
	if len(names) == 3 {
		prefix = strings.TrimSpace(names[2])
	}
	if m, ok := v.(map[string]any); ok && len(m) == 1 {
		for _, inner := range m {
			v = inner
		}
	}
	rows, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("kv: value is %T, not a list of rows", v)
	}
	out := map[string]any{}
	for i, r := range rows {
		row, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("kv: row %d is %T, not an object", i, r)
		}
		k := stringify(row[kf])
		if k == "" {
			continue
		}
		out[prefix+k] = row[vf]
	}
	return out, nil
}

// omitKeys returns a copy of the object v without the keys named in arg
// (a comma-separated string or a list).
func omitKeys(v, arg any) (any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("omit: value is %T, not an object", v)
	}
	drop := map[string]bool{}
	switch a := arg.(type) {
	case []any:
		for _, x := range a {
			drop[strings.TrimSpace(stringify(x))] = true
		}
	default:
		for _, x := range strings.Split(stringify(a), ",") {
			drop[strings.TrimSpace(x)] = true
		}
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		if !drop[k] {
			out[k] = val
		}
	}
	return out, nil
}

// fromJSON parses a JSON string (n8n's jsonParameters / *Json params);
// non-strings pass through, an empty string is nil.
func fromJSON(v any) (any, error) {
	str, ok := v.(string)
	if !ok {
		return v, nil
	}
	if strings.TrimSpace(str) == "" {
		return nil, nil
	}
	var out any
	if err := json.Unmarshal([]byte(str), &out); err != nil {
		return nil, fmt.Errorf("fromjson: %w", err)
	}
	return out, nil
}

var epochLayouts = []string{
	time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000", "2006-01-02T15:04:05",
	"2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02",
}

// toEpoch turns a date (RFC 3339, n8n's ISO forms, a date, or a number
// that is already an epoch) into Unix milliseconds or seconds.
func toEpoch(v any, ms bool) (any, error) {
	if f, ok := v.(float64); ok {
		return int64(f), nil
	}
	str := strings.TrimSpace(stringify(v))
	if str == "" {
		return nil, nil
	}
	if n, err := strconv.ParseInt(str, 10, 64); err == nil {
		return n, nil
	}
	for _, l := range epochLayouts {
		if t, err := time.Parse(l, str); err == nil {
			if ms {
				return t.UnixMilli(), nil
			}
			return t.Unix(), nil
		}
	}
	return nil, fmt.Errorf("epoch: %q is not a date", str)
}

// splitString turns "a, b,c" into ["a","b","c"] (sep defaults to ",");
// empty parts are dropped and lists pass through.
func splitString(v, arg any) (any, error) {
	if l, ok := v.([]any); ok {
		return l, nil
	}
	sep := stringify(arg)
	if sep == "" {
		sep = ","
	}
	var out []any
	for _, part := range strings.Split(stringify(v), sep) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// dateTokens maps the tokens package authors write (moment/Luxon style,
// longest first) to Go layout pieces.
var dateTokens = []struct{ tok, layout string }{
	{"YYYY", "2006"}, {"yyyy", "2006"}, {"YY", "06"}, {"yy", "06"},
	{"MM", "01"}, {"DD", "02"}, {"dd", "02"}, {"HH", "15"}, {"hh", "03"},
	{"mm", "04"}, {"ss", "05"}, {"SSS", "000"}, {"ZZ", "-07:00"}, {"Z", "Z07:00"}, {"A", "PM"},
}

// formatDate parses a date (the epoch filter's formats, or epoch ms) and
// formats it with tokens like 'YYYY-MM-DD' or 'YYYY-MM-DDTHH:mm:ssZ' in UTC.
func formatDate(v, arg any) (any, error) {
	pattern := stringify(arg)
	if pattern == "" {
		return nil, fmt.Errorf(`date: needs a format like 'YYYY-MM-DD'`)
	}
	var t time.Time
	switch x := v.(type) {
	case float64:
		t = epochTime(int64(x))
	case int64:
		t = epochTime(x)
	case int:
		t = epochTime(int64(x))
	default:
		str := strings.TrimSpace(stringify(v))
		if str == "" {
			return nil, nil
		}
		ok := false
		for _, l := range epochLayouts {
			if pt, err := time.Parse(l, str); err == nil {
				t, ok = pt, true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("date: %q is not a date", str)
		}
	}
	var b strings.Builder
	for i := 0; i < len(pattern); {
		matched := false
		for _, dt := range dateTokens {
			if strings.HasPrefix(pattern[i:], dt.tok) {
				b.WriteString(t.UTC().Format(dt.layout))
				i += len(dt.tok)
				matched = true
				break
			}
		}
		if !matched {
			b.WriteByte(pattern[i])
			i++
		}
	}
	return b.String(), nil
}

// toRow orders an object's values by a header list (e.g. a sheet's first
// row): {"b":2,"a":1} with ["a","b","c"] → [1,2,""]. arg may also be a
// list of rows, whose first row is used.
func toRow(v, arg any, nullMissing bool) (any, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("row: value is %T, not an object", v)
	}
	header, ok := arg.([]any)
	if ok && len(header) > 0 {
		if first, isRow := header[0].([]any); isRow {
			header = first
		}
	}
	if len(header) == 0 {
		return nil, fmt.Errorf("row: the header is empty (a sheet without a header row?)")
	}
	out := make([]any, len(header))
	for i, h := range header {
		if val, ok := obj[stringify(h)]; ok && val != nil {
			out[i] = val
		} else if nullMissing {
			out[i] = nil // the Sheets API leaves a null cell unchanged
		} else {
			out[i] = ""
		}
	}
	return out, nil
}

// rowsToObjects turns [[h1,h2],[v1,v2],…] into [{h1:v1,h2:v2},…]; short
// rows leave keys out, extra cells are dropped.
func rowsToObjects(v any) any {
	rows, ok := v.([]any)
	if !ok || len(rows) == 0 {
		return v
	}
	header, ok := rows[0].([]any)
	if !ok {
		return v
	}
	out := make([]any, 0, len(rows)-1)
	for _, r := range rows[1:] {
		cells, _ := r.([]any)
		obj := map[string]any{}
		for i, h := range header {
			if i < len(cells) {
				obj[stringify(h)] = cells[i]
			}
		}
		out = append(out, obj)
	}
	return out
}

var googleIDPatterns = []*regexp.Regexp{
	regexp.MustCompile(`/(?:d|folders|file/d|presentation/d|spreadsheets/d|document/d|forms/d)/([A-Za-z0-9_-]{10,})`),
	regexp.MustCompile(`[?&]id=([A-Za-z0-9_-]{10,})`),
}

// googleFileID returns the file id from a Google Docs/Sheets/Slides/Drive
// URL (…/d/<id>/edit, …/folders/<id>, …?id=<id>); anything else (a bare
// id, a non-Google value) passes through unchanged.
func googleFileID(v any) any {
	s, ok := v.(string)
	if !ok || !strings.Contains(s, "google.com") {
		return v
	}
	for _, re := range googleIDPatterns {
		if m := re.FindStringSubmatch(s); m != nil {
			return m[1]
		}
	}
	return v
}

// findIn returns the first element of the list v whose value at the dot
// path equals want (compared as text), or nil.
func findIn(v any, path string, want any) any {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	w := stringify(want)
	for _, el := range list {
		if stringify(lookupPath(el, strings.Split(path, "."))) == w {
			return el
		}
	}
	return nil
}

// findRow searches value rows whose first row is the header: it returns the
// 1-based row number (the sheet row when the rows start at row 1) of the
// first data row whose cell in column equals want, or nil. When want is an
// object (e.g. the input item), its value for the column is used.
func findRow(v any, column string, want any) any {
	rows, ok := v.([]any)
	if !ok || len(rows) < 2 {
		return nil
	}
	header, ok := rows[0].([]any)
	if !ok {
		return nil
	}
	col := -1
	for i, h := range header {
		if stringify(h) == column {
			col = i
			break
		}
	}
	if col < 0 {
		return nil
	}
	if m, ok := want.(map[string]any); ok {
		want = m[column]
	}
	w := stringify(want)
	if w == "" {
		return nil
	}
	for i, r := range rows[1:] {
		cells, _ := r.([]any)
		if col < len(cells) && stringify(cells[col]) == w {
			return int64(i + 2)
		}
	}
	return nil
}

// columnNumber turns a spreadsheet column letter into its 1-based number
// ("A" → 1, "AA" → 27).
func columnNumber(s string) (any, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return nil, nil
	}
	n := int64(0)
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return nil, fmt.Errorf("colnum: %q is not a column letter", s)
		}
		n = n*26 + int64(c-'A'+1)
	}
	return n, nil
}

// columnName turns a 1-based column number into its letter (28 → "AB").
func columnName(n int64) (any, error) {
	if n < 1 {
		return nil, fmt.Errorf("colname: %d is not a column number", n)
	}
	var b []byte
	for n > 0 {
		n--
		b = append([]byte{byte('A' + n%26)}, b...)
		n /= 26
	}
	return string(b), nil
}

var gidRe = regexp.MustCompile(`gid=(\d+)`)

// sheetGID turns an n8n sheet locator into the numeric sheet id text:
// "gid=0" (list mode, first sheet), ".../edit#gid=123" (url mode) and
// "123" (id mode / expression) all become the digits. A locator object
// is read at its value. Anything else passes through.
func sheetGID(v any) any {
	if m, ok := v.(map[string]any); ok {
		if val, ok := m["value"]; ok {
			v = val
		}
	}
	s, ok := v.(string)
	if !ok {
		if f, isNum := v.(float64); isNum {
			return strconv.FormatInt(int64(f), 10)
		}
		return v
	}
	if m := gidRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return strings.TrimSpace(s)
}

// toEntries turns an object into [{key: k, value: v}, …] sorted by key;
// arg "keyName,valueName" renames the two keys (Coda: 'column,value').
func toEntries(v, arg any) (any, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("entries: value is %T, not an object", v)
	}
	kn, vn := "key", "value"
	if a := stringify(arg); a != "" {
		parts := strings.SplitN(a, ",", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return nil, fmt.Errorf(`entries: argument must be "keyName,valueName", got %q`, a)
		}
		kn, vn = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]any{kn: k, vn: obj[k]})
	}
	return out, nil
}

// pathValue is text already escaped for a URL path (the path filter).
type pathValue string

// pickKeys returns a copy of the object v with only the keys named in arg
// (a comma-separated string or a list); listed keys v lacks are skipped.
func pickKeys(v, arg any) (any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("pick: value is %T, not an object", v)
	}
	var keys []string
	switch a := arg.(type) {
	case []any:
		for _, x := range a {
			keys = append(keys, strings.TrimSpace(stringify(x)))
		}
	default:
		keys = strings.Split(stringify(a), ",")
	}
	out := map[string]any{}
	for _, k := range keys {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		if val, ok := m[k]; ok {
			out[k] = val
		}
	}
	return out, nil
}

var reCache sync.Map // pattern → *regexp.Regexp

func cachedRegexp(p string) (*regexp.Regexp, error) {
	if v, ok := reCache.Load(p); ok {
		return v.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, err
	}
	reCache.Store(p, re)
	return re, nil
}

// zipLists pairs a header list with a value list into an object: key i is
// headers[i] (or headers[i][headerKey]), value i is values[i] (or
// values[i][valueKey]). GA4 rows: {{ el.dimensionValues | zip:res.dimensionHeaders,'name','value' }}.
func zipLists(headers, values any, headerKey, valueKey string) any {
	hs, _ := headers.([]any)
	vs, _ := values.([]any)
	out := map[string]any{}
	for i, h := range hs {
		if i >= len(vs) {
			break
		}
		k := h
		if headerKey != "" {
			k = lookupPath(h, strings.Split(headerKey, "."))
		}
		v := vs[i]
		if valueKey != "" {
			v = lookupPath(v, strings.Split(valueKey, "."))
		}
		if ks := stringify(k); ks != "" {
			out[ks] = v
		}
	}
	return out
}

// epochTime reads a Unix time in seconds or, from 1e11 up (after 1973 in
// ms), milliseconds.
func epochTime(n int64) time.Time {
	if n >= 1e11 || n <= -1e11 {
		return time.UnixMilli(n)
	}
	return time.Unix(n, 0)
}

// truthy is how *_when, until and $when conditions read a rendered value:
// empty, false, 0 and "false"/"0"/"no"/"off" are false; any other
// non-empty value (an id, a mode name) is true.
func truthy(v any) bool {
	if isEmpty(v) {
		return false
	}
	if b, err := toBool(v); err == nil && b != nil {
		return b == true
	}
	return true
}

// snakeCase is change-case's snakeCase (v5, as n8n uses it): words split
// where a lower-case letter or digit meets an upper-case one, before the
// last capital of an acronym followed by a lower-case letter, and on
// anything that is not a letter or ASCII digit; lower-cased and joined by
// "_" ("Due Date", "DueDate", "due-date" → "due_date"; "Q1 Goals" →
// "q1_goals"; letters and digits are not split apart).
func snakeCase(s string) string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	isDigit := func(r rune) bool { return r >= '0' && r <= '9' }
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case unicode.IsLetter(r) || isDigit(r):
			if len(cur) > 0 {
				prev := cur[len(cur)-1]
				lowerToUpper := unicode.IsUpper(r) && (unicode.IsLower(prev) || isDigit(prev))
				acronymEnd := unicode.IsUpper(r) && unicode.IsUpper(prev) && i+1 < len(rs) && unicode.IsLower(rs[i+1])
				if lowerToUpper || acronymEnd {
					flush()
				}
			}
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return strings.Join(words, "_")
}

// foldNonASCII turns every character outside printable ASCII into a space,
// so snake gives the older ASCII-only keys ("Prénom" → "pr_nom"; n8n
// Notion before node version 3).
func foldNonASCII(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return ' '
		}
		return r
	}, s)
}

// unset is what then treats as "no value": empty, false and the text
// "false" (n8n boolean toggles arrive as either). 0 is a value.
func unset(v any) bool {
	return isEmpty(v) || v == false || stringify(v) == "false"
}
