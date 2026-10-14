package n8nimport

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// Payload shaping steps for webhook triggers whose n8n node rewrote the
// vendor's payload. They run as core.code after the trigger.webhook, whose
// item carries the request body under "body".

// shapeTypeform turns a Typeform form_response into answers keyed by
// question title (simplifyAnswers, default on) and, with onlyAnswers
// (default on), returns only those answers.
func shapeTypeform(c *MapContext) *ChainStep {
	simplify := c.Bool("simplifyAnswers", true)
	only := c.Bool("onlyAnswers", true)
	body := fmt.Sprintf(`var simplify = %s, onlyAnswers = %s;
function answerValue(a) {
  if (a.type === "choice") { return a.choice ? (a.choice.label !== undefined ? a.choice.label : a.choice.other) : null; }
  if (a.type === "choices") {
    var picked = (a.choices && a.choices.labels) || [];
    return a.choices && a.choices.other ? picked.concat([a.choices.other]) : picked;
  }
  return a[a.type];
}
return items.map(function (it) {
  var body = it.json.body !== undefined ? it.json.body : it.json;
  var resp = (body && body.form_response) || {};
  var answers = resp.answers || [];
  if (simplify) {
    var titles = {};
    ((resp.definition && resp.definition.fields) || []).forEach(function (f) { titles[f.id] = f.title; });
    var named = {};
    answers.forEach(function (a) {
      var f = a.field || {};
      named[titles[f.id] || f.ref || f.id] = answerValue(a);
    });
    answers = named;
  }
  if (onlyAnswers) { return simplify ? answers : { answers: answers }; }
  var out = Object.assign({}, body);
  out.form_response = Object.assign({}, resp, { answers: answers });
  return out;
});
`, jsValue(simplify), jsValue(only))
	return &ChainStep{Type: "core.code", Config: codeConfig(wrapCode(body))}
}

// shapeJotform decodes Jotform's rawRequest form field (a JSON string of
// q<N>_<name> answers) into answers keyed by the question's unique name;
// with onlyAnswers off the submission's ids come along.
func shapeJotform(c *MapContext) *ChainStep {
	only := c.Bool("onlyAnswers", true)
	if c.Bool("resolveData", true) {
		c.Note("answers are keyed by each question's unique name (n8n looked up the question labels through the Jotform API); app.jot_form form.getQuestions lists the labels")
	}
	body := fmt.Sprintf(`var onlyAnswers = %s;
return items.map(function (it) {
  var j = it.json.body !== undefined && it.json.body !== null && typeof it.json.body === "object" ? it.json.body : it.json;
  var raw = j.rawRequest, answers = {};
  if (typeof raw === "string") { try { raw = JSON.parse(raw); } catch (e) { raw = null; } }
  if (raw && typeof raw === "object") {
    Object.keys(raw).forEach(function (k) {
      var m = /^q\d+_(.+)$/.exec(k);
      if (m) { answers[m[1]] = raw[k]; }
    });
  }
  if (onlyAnswers) { return answers; }
  return { formID: j.formID, submissionID: j.submissionID, formTitle: j.formTitle, ip: j.ip, answers: answers, rawRequest: raw };
});
`, jsValue(only))
	return &ChainStep{Type: "core.code", Config: codeConfig(wrapCode(body))}
}

// shapeWufoo turns a Wufoo WebHook post (form fields plus the
// FieldStructure JSON) into answers keyed by field title: file fields give
// their URL, checkboxes the ticked choices, address/likert/name fields an
// object keyed by sub-field label. With onlyAnswers off the entry's
// metadata and parsed structures come along, as in n8n. Like n8n, posts
// whose HandshakeKey differs from the key set up for the webhook are
// dropped; the importer makes a new key for the user to enter in Wufoo.
func shapeWufoo(c *MapContext) *ChainStep {
	only := c.Bool("onlyAnswers", true)
	key := newHandshakeKey()
	c.Note("Wufoo: enter %s as the WebHook's Handshake Key; posts without that key are dropped, as n8n dropped posts without the key it registered", key)
	body := fmt.Sprintf(`var onlyAnswers = %s, handshakeKey = %s;
function parse(v) {
  if (typeof v !== "string") { return v || {}; }
  try { return JSON.parse(v); } catch (e) { return {}; }
}
var out = [];
items.forEach(function (it) {
  var b = it.json.body !== undefined && it.json.body !== null && typeof it.json.body === "object" ? it.json.body : it.json;
  if (b.HandshakeKey !== handshakeKey) { return; }
  var fields = parse(b.FieldStructure), entries = {};
  (fields.Fields || []).forEach(function (f) {
    var subs = f.SubFields || [];
    if (f.Type === "file") {
      entries[f.Title] = b[f.ID + "-url"];
    } else if (f.Type === "checkbox") {
      entries[f.Title] = subs.map(function (s) { return b[s.ID]; }).filter(function (v) { return v !== "" && v !== undefined; });
    } else if (f.Type === "address" || f.Type === "likert" || f.Type === "shortname") {
      var obj = {};
      subs.forEach(function (s) { obj[s.Label] = b[s.ID]; });
      entries[f.Title] = obj;
    } else {
      entries[f.Title] = b[f.ID];
    }
  });
  if (onlyAnswers) { out.push(entries); return; }
  out.push({ createdBy: b.CreatedBy, entryId: b.EntryId, dateCreated: b.DateCreated, formId: b.FormId,
    formStructure: parse(b.FormStructure), fieldStructure: fields, entries: entries });
});
return out;
`, jsValue(only), jsValue(key))
	return &ChainStep{Type: "core.code", Config: codeConfig(wrapCode(body))}
}

// newHandshakeKey makes the shared key a Wufoo webhook sends with each
// post (tests replace it).
var newHandshakeKey = func() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// shapeFormstack replaces each {value, …} field object of a Formstack
// submission by its value (Simplify, default on); off, the body passes
// unchanged and no step is needed.
func shapeFormstack(c *MapContext) *ChainStep {
	if !c.Bool("simple", true) {
		return nil
	}
	body := `return items.map(function (it) {
  var b = it.json.body !== undefined && it.json.body !== null && typeof it.json.body === "object" ? it.json.body : it.json;
  var out = {};
  Object.keys(b).forEach(function (k) {
    var v = b[k];
    out[k] = v !== null && typeof v === "object" && !Array.isArray(v) && Object.prototype.hasOwnProperty.call(v, "value") ? v.value : v;
  });
  return out;
});
`
	return &ChainStep{Type: "core.code", Config: codeConfig(wrapCode(body))}
}

// shapeFormIo returns the submission Form.io posts under "request": its
// data (Simplify, default on) or the whole submission; a list becomes one
// item per element.
func shapeFormIo(c *MapContext) *ChainStep {
	simple := c.Bool("simple", true)
	body := fmt.Sprintf(`var simple = %s, out = [];
items.forEach(function (it) {
  var b = it.json.body !== undefined && it.json.body !== null && typeof it.json.body === "object" ? it.json.body : it.json;
  var r = b.request || {};
  if (simple) { r = r.data || {}; }
  (Array.isArray(r) ? r : [r]).forEach(function (x) { out.push(x); });
});
return out;
`, jsValue(simple))
	return &ChainStep{Type: "core.code", Config: codeConfig(wrapCode(body))}
}
