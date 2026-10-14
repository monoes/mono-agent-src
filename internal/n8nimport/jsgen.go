package n8nimport

import (
	"encoding/json"
	"strings"
)

// jsQuote returns s as a JavaScript string literal.
func jsQuote(s string) string {
	b, _ := json.Marshal(s)
	// JSON string literals are valid JavaScript except for U+2028/U+2029,
	// which encoding/json already escapes.
	return string(b)
}

// jsValue returns v as a JavaScript literal. Nil lists become [] so
// generated code can iterate them.
func jsValue(v interface{}) string {
	switch x := v.(type) {
	case []string:
		if x == nil {
			return "[]"
		}
	case [][2]string:
		if x == nil {
			return "[]"
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// codePrelude gives generated and imported JavaScript the n8n runtime
// names mono's core.code lacks: items/$input with n8n's {json} wrapping,
// a Luxon-like $now/$today/DateTime subset, console, and $('Node') and
// $node over mono's own $('Node') (first() and item are the node's first
// item: mono keeps no item pairing). It runs inside the function wrapper,
// so names are local; mono's own $input and $ are read through globalThis
// because the local declarations shadow them.
const codePrelude = `var __rawItems = globalThis.$input.all();
var items = __rawItems.map(function (j) { return { json: j, binary: {} }; });
var $input = {
  all: function () { return items; },
  first: function () { return items[0]; },
  last: function () { return items[items.length - 1]; },
  item: items[0],
  params: {}
};
var $json = items.length ? items[0].json : {};
var $runIndex = 0;
var $itemIndex = 0;
var console = { log: function () {}, info: function () {}, warn: function () {}, error: function () {}, debug: function () {} };
var $ = function (name) {
  var all = globalThis.$(name).all().map(function (j) { return { json: j, binary: {} }; });
  return { all: function () { return all; }, first: function () { return all[0]; }, last: function () { return all[all.length - 1]; }, item: all[0] };
};
var $node = typeof Proxy === "function" ? new Proxy({}, { get: function (_, name) { var f = $(String(name)).first(); return { json: f ? f.json : {} }; } }) : {};
function __n8nPad(n, w) { n = String(n); while (n.length < w) { n = "0" + n; } return n; }
function __n8nDate(d) {
  var units = { millisecond: 1, milliseconds: 1, second: 1e3, seconds: 1e3, minute: 6e4, minutes: 6e4, hour: 36e5, hours: 36e5, day: 864e5, days: 864e5, week: 6048e5, weeks: 6048e5 };
  function shift(obj, sign) {
    var nd = new Date(d.getTime());
    Object.keys(obj).forEach(function (k) {
      if (k === "month" || k === "months") { nd.setUTCMonth(nd.getUTCMonth() + sign * obj[k]); }
      else if (k === "year" || k === "years") { nd.setUTCFullYear(nd.getUTCFullYear() + sign * obj[k]); }
      else if (units[k]) { nd = new Date(nd.getTime() + sign * obj[k] * units[k]); }
    });
    return __n8nDate(nd);
  }
  return {
    year: d.getUTCFullYear(), month: d.getUTCMonth() + 1, day: d.getUTCDate(), hour: d.getUTCHours(), minute: d.getUTCMinutes(), second: d.getUTCSeconds(), weekday: d.getUTCDay() || 7,
    toISO: function () { return d.toISOString(); },
    toISODate: function () { return d.toISOString().slice(0, 10); },
    toString: function () { return d.toISOString(); },
    toJSON: function () { return d.toISOString(); },
    toJSDate: function () { return d; },
    toMillis: function () { return d.getTime(); },
    toSeconds: function () { return d.getTime() / 1000; },
    toUnixInteger: function () { return Math.floor(d.getTime() / 1000); },
    valueOf: function () { return d.getTime(); },
    plus: function (o) { return typeof o === "number" ? __n8nDate(new Date(d.getTime() + o)) : shift(o, 1); },
    minus: function (o) { return typeof o === "number" ? __n8nDate(new Date(d.getTime() - o)) : shift(o, -1); },
    startOf: function (u) {
      var nd = new Date(d.getTime());
      if (u === "year") { nd.setUTCMonth(0, 1); }
      if (u === "year" || u === "month") { nd.setUTCDate(u === "month" ? 1 : nd.getUTCDate()); }
      nd.setUTCHours(u === "hour" ? nd.getUTCHours() : 0, 0, 0, 0);
      return __n8nDate(nd);
    },
    toFormat: function (f) {
      var map = { yyyy: d.getUTCFullYear(), yy: String(d.getUTCFullYear()).slice(2), MM: __n8nPad(d.getUTCMonth() + 1, 2), M: d.getUTCMonth() + 1, dd: __n8nPad(d.getUTCDate(), 2), d: d.getUTCDate(), HH: __n8nPad(d.getUTCHours(), 2), H: d.getUTCHours(), mm: __n8nPad(d.getUTCMinutes(), 2), ss: __n8nPad(d.getUTCSeconds(), 2), SSS: __n8nPad(d.getUTCMilliseconds(), 3) };
      return f.replace(/'([^']*)'|yyyy|yy|MM|M|dd|d|HH|H|mm|ss|SSS/g, function (m, lit) { return lit !== undefined ? lit : String(map[m]); });
    }
  };
}
var DateTime = {
  now: function () { return __n8nDate(new Date()); },
  local: function () { return __n8nDate(new Date()); },
  utc: function () { return __n8nDate(new Date()); },
  fromISO: function (s) { return __n8nDate(new Date(s)); },
  fromJSDate: function (d) { return __n8nDate(d); },
  fromMillis: function (ms) { return __n8nDate(new Date(ms)); },
  fromSeconds: function (s) { return __n8nDate(new Date(s * 1000)); }
};
var $now = DateTime.now();
var $today = $now.startOf("day");
function __n8nOut(r) {
  if (r === undefined || r === null) { return []; }
  var list = Array.isArray(r) ? r : [r];
  return list.map(function (x) { return (x && typeof x === "object" && x.json && typeof x.json === "object") ? x.json : x; });
}
`

// wrapCode builds a core.code program: the prelude, then body (JavaScript
// whose result is the node output) inside a function so a top-level
// "return" works as in n8n.
func wrapCode(body string) string {
	return "(async function () {\n" + indentJS(codePrelude) + "  return __n8nOut(await (async function () {\n" + indentJS(indentJS(body)) + "  })());\n})();\n"
}

// wrapPerItem builds a program running body once per item with $json,
// $input.item and item bound to that item (n8n's "run once for each item").
func wrapPerItem(body string) string {
	loop := "var __out = [];\nvar __base = $input;\nfor (var __i = 0; __i < items.length; __i++) {\n" +
		"  await (async function (item, __idx) {\n    var $json = item.json;\n" +
		"    var $input = { item: item, all: __base.all, first: __base.first, last: __base.last, params: {} };\n" +
		"    $itemIndex = __idx;\n    var __r = await (async function () {\n" + indentJS(indentJS(indentJS(body))) + "    })();\n" +
		"    if (__r === undefined || __r === null) { return; }\n" +
		"    __out = __out.concat(__n8nOut(__r));\n  })(items[__i], __i);\n}\nreturn __out;\n"
	return wrapCode(loop)
}

func indentJS(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = "  " + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// codeConfig is the core.code config for a generated program. "{{" is
// escaped because mono resolves templates in every config string.
func codeConfig(program string) map[string]interface{} {
	return map[string]interface{}{"code": escapeTemplateText(program)}
}
