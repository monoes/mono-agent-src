package n8nimport

import (
	"fmt"
	"strings"
)

func init() {
	Register("n8n-nodes-base.aggregate", mapAggregate)
	Register("n8n-nodes-base.sort", mapSort)
	Register("n8n-nodes-base.limit", mapLimit)
	Register("n8n-nodes-base.removeDuplicates", mapRemoveDuplicates)
	Register("n8n-nodes-base.itemLists", mapItemLists)
}

// commaList splits an n8n comma-separated field list.
func commaList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// jsCode is a core.code Mapping running body over all items with the
// shared helpers available.
func jsCode(body string) Mapping {
	return Mapping{Type: "core.code", Config: codeConfig(wrapCode(jsMergeHelpers + jsValueHelpers + body))}
}

func mapAggregate(c *MapContext) Mapping {
	c.Ignore("options.disableDotNotation", "options.includeBinaries")
	if c.Literal("aggregate", "aggregateIndividualFields") == "aggregateAllItemData" {
		dest := c.Literal("destinationFieldName", "data")
		include := c.Literal("include", "allFields")
		inc := commaList(c.Literal("fieldsToInclude", ""))
		exc := commaList(c.Literal("fieldsToExclude", ""))
		mode := map[string]string{"allFields": "all", "specifiedFields": "selected", "allFieldsExcept": "except"}[include]
		fields := inc
		if mode == "except" {
			fields = exc
		}
		return jsCode(fmt.Sprintf("var o = {};\no[%s] = items.map(function (it) { return %s; });\nreturn [o];\n",
			jsQuote(dest), jsBase(mode, fields, "it.json")))
	}
	type agg struct {
		Field  string `json:"field"`
		Output string `json:"output"`
	}
	list := []agg{}
	for _, f := range c.paramList("fieldsToAggregate.fieldToAggregate") {
		field := fmt.Sprint(f["fieldToAggregate"])
		out := field
		if r, _ := f["renameField"].(bool); r {
			out = fmt.Sprint(f["outputFieldName"])
		}
		list = append(list, agg{Field: field, Output: out})
	}
	opts := map[string]bool{
		"merge":   c.Bool("options.mergeLists", false),
		"unique":  c.Bool("options.keepOnlyUnique", false),
		"missing": c.Bool("options.keepMissing", false),
	}
	body := fmt.Sprintf(`var spec = %s, opt = %s, o = {};
spec.forEach(function (f) {
  var vals = [];
  items.forEach(function (it) {
    var v = __n8nGet(it.json, f.field);
    if (v === undefined || (v === null && !opt.missing)) { if (opt.missing) { vals.push(null); } return; }
    if (opt.merge && Array.isArray(v)) { vals = vals.concat(v); } else { vals.push(v); }
  });
  if (opt.unique) { var seen = {}; vals = vals.filter(function (v) { var k = JSON.stringify(v); if (seen[k]) { return false; } seen[k] = true; return true; }); }
  o[f.output] = vals;
});
return [o];
`, jsValue(list), jsValue(opts))
	return jsCode(body)
}

func mapSort(c *MapContext) Mapping {
	c.Ignore("options.disableDotNotation")
	switch c.Literal("type", "simple") {
	case "random":
		return jsCode("var a = items.map(function (it) { return it.json; });\nfor (var i = a.length - 1; i > 0; i--) { var j = Math.floor(Math.random() * (i + 1)); var t = a[i]; a[i] = a[j]; a[j] = t; }\nreturn a;\n")
	case "code":
		code := importCode(c, "code", fmt.Sprint(c.Raw("code")))
		return jsCode("var __cmp = function (a, b) {\n" + indentJS(code) + "};\nreturn items.map(function (it) { return it.json; }).sort(__cmp);\n")
	}
	type key struct {
		Field string `json:"field"`
		Desc  bool   `json:"desc"`
	}
	keys := []key{}
	for _, f := range c.paramList("sortFieldsUi.sortField") {
		keys = append(keys, key{Field: fmt.Sprint(f["fieldName"]), Desc: f["order"] == "descending"})
	}
	return jsCode(fmt.Sprintf(`var keys = %s;
function cmp(x, y) {
  if (x === y) { return 0; }
  if (x === undefined || x === null) { return 1; }
  if (y === undefined || y === null) { return -1; }
  if (typeof x === "number" && typeof y === "number") { return x - y; }
  if (typeof x === "object" || typeof y === "object") { x = JSON.stringify(x); y = JSON.stringify(y); }
  return String(x) < String(y) ? -1 : String(x) > String(y) ? 1 : 0;
}
return items.map(function (it) { return it.json; }).sort(function (a, b) {
  for (var i = 0; i < keys.length; i++) {
    var r = cmp(__n8nGet(a, keys[i].field), __n8nGet(b, keys[i].field));
    if (r !== 0) { return keys[i].desc ? -r : r; }
  }
  return 0;
});
`, jsValue(keys)))
}

func mapLimit(c *MapContext) Mapping {
	max := c.Float("maxItems", 1)
	if c.Literal("keep", "firstItems") == "lastItems" {
		return jsCode(fmt.Sprintf("var n = %s;\nreturn items.slice(Math.max(0, items.length - n)).map(function (it) { return it.json; });\n", jsValue(max)))
	}
	return Mapping{Type: "core.limit", Config: map[string]interface{}{"max_items": max}}
}

func mapRemoveDuplicates(c *MapContext) Mapping {
	if c.version(2) {
		switch op := c.Literal("operation", "removeDuplicateInputItems"); op {
		case "removeDuplicateInputItems":
		case "removeItemsSeenInPreviousExecutions", "removeItemsRepeatedWithinPreviousExecutions":
			return mapRemoveSeenBefore(c)
		case "clearDeduplicationHistory":
			return mapClearDedupeHistory(c)
		default:
			c.Ignore("logic", "dedupeValue", "incrementalDedupeValue", "dateDedupeValue", "mode", "options.scope", "options.historySize")
			return c.OperationUnsupported("", op)
		}
	}
	c.Ignore("options.disableDotNotation")
	compare := c.Literal("compare", "allFields")
	removeOther := c.Bool("options.removeOtherFields", false)
	var fields []string
	switch compare {
	case "allFields":
		if !removeOther {
			return Mapping{Type: "core.remove_duplicates", Config: map[string]interface{}{"keep": "first"}}
		}
	case "selectedFields":
		fields = commaList(c.Literal("fieldsToCompare", ""))
		if len(fields) == 1 && !removeOther {
			return Mapping{Type: "core.remove_duplicates", Config: map[string]interface{}{"field": fields[0], "keep": "first"}}
		}
	case "allFieldsExcept":
		fields = commaList(c.Literal("fieldsToExclude", ""))
	}
	return jsCode(fmt.Sprintf(`var mode = %s, fields = %s, removeOther = %s, seen = {}, out = [];
items.forEach(function (it) {
  var j = it.json, k;
  if (mode === "selectedFields") { k = fields.map(function (f) { return __n8nGet(j, f); }); }
  else { var c = Object.assign({}, j); if (mode === "allFieldsExcept") { fields.forEach(function (f) { delete c[f]; }); } k = Object.keys(c).sort().map(function (x) { return [x, c[x]]; }); }
  k = JSON.stringify(k);
  if (seen[k]) { return; }
  seen[k] = true;
  if (removeOther && mode === "selectedFields") { var o = {}; fields.forEach(function (f) { __n8nSet(o, f, __n8nGet(j, f)); }); out.push(o); } else { out.push(j); }
});
return out;
`, jsQuote(compare), jsValue(fields), jsValue(removeOther)))
}

// mapRemoveSeenBefore maps "Remove Items Processed in Previous Executions"
// onto core.remove_duplicates' persisted history. Only n8n's "value is
// new" logic has a counterpart; the high-water-mark logics (incremental
// number, later date) do not.
func mapRemoveSeenBefore(c *MapContext) Mapping {
	c.Ignore("compare", "fieldsToCompare", "fieldsToExclude", "options.disableDotNotation", "options.removeOtherFields")
	if logic := c.Literal("logic", "removeItemsWithAlreadySeenKeyValues"); logic != "removeItemsWithAlreadySeenKeyValues" {
		c.Ignore("dedupeValue", "incrementalDedupeValue", "dateDedupeValue", "options.scope", "options.historySize")
		return c.OperationUnsupported("", "removeItemsSeenInPreviousExecutions with logic "+logic)
	}
	c.Ignore("incrementalDedupeValue", "dateDedupeValue")
	cfg := map[string]interface{}{"operation": "removeItemsRepeatedWithinPreviousExecutions"}
	raw, _ := c.Raw("dedupeValue").(string)
	switch {
	case raw == "":
		c.Warn("dedupeValue", nil, "is empty: whole items are compared")
	case !strings.HasPrefix(raw, "="):
		c.Warn("dedupeValue", raw, "is plain text, the same value for every item in n8n; mono-agent reads it as a field path")
		cfg["dedupeValue"] = raw
	default:
		cfg["dedupeValue"] = c.Str("dedupeValue", "")
	}
	dedupeHistoryOptions(c, cfg)
	if c.Has("options.historySize") {
		cfg["historySize"] = c.Float("options.historySize", 10000)
	}
	return Mapping{Type: "core.remove_duplicates", Config: cfg}
}

// mapClearDedupeHistory maps "Clear Deduplication History" (n8n's only
// mode, cleanDatabase, forgets every stored value of the scope).
func mapClearDedupeHistory(c *MapContext) Mapping {
	c.Ignore("compare", "fieldsToCompare", "fieldsToExclude", "logic", "dedupeValue", "incrementalDedupeValue", "dateDedupeValue",
		"options.disableDotNotation", "options.removeOtherFields", "options.historySize")
	if mode := c.Literal("mode", "cleanDatabase"); mode != "cleanDatabase" {
		return c.OperationUnsupported("", "clearDeduplicationHistory with mode "+mode)
	}
	cfg := map[string]interface{}{"operation": "clearDeduplicationHistory"}
	dedupeHistoryOptions(c, cfg)
	return Mapping{Type: "core.remove_duplicates", Config: cfg}
}

// dedupeHistoryOptions copies the history scope ("node", n8n's default,
// or "workflow").
func dedupeHistoryOptions(c *MapContext, cfg map[string]interface{}) {
	if scope := c.Literal("options.scope", "node"); scope != "node" {
		cfg["scope"] = scope
	}
}

// mapItemLists dispatches the legacy Item Lists node to the standalone
// mappers its operations became.
func mapItemLists(c *MapContext) Mapping {
	c.Ignore("resource")
	switch op := c.Literal("operation", "splitOutItems"); op {
	case "aggregateItems", "concatenateItems":
		return mapAggregate(c)
	case "limit":
		return mapLimit(c)
	case "removeDuplicates":
		return mapRemoveDuplicates(c)
	case "sort":
		return mapSort(c)
	case "splitOutItems":
		return mapSplitOut(c)
	case "summarize":
		return mapSummarize(c)
	default:
		return c.OperationUnsupported("", op)
	}
}
