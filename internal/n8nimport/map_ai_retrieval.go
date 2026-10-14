package n8nimport

import (
	"fmt"
	"strings"

	vectornodes "github.com/monoes/mono-agent/internal/nodes/vector"
)

// n8n's Question and Answer Chain retrieves documents for each question
// through an ai_retriever sub-node tree and answers from them. Mono runs
// the same steps as a chain of nodes:
//
//	core.set (question) → retrieval steps → core.code (context)
//	  → agent.ask (answer) → core.code (n8n's output shape)
//
// Every retrieval step takes "query items" carrying __n8n_query and emits
// document items {pageContent, metadata, source_item}: source_item is the
// index of the query item the document was found for (-1 for all of
// them). Wrapping retrievers re-read their query items by node name.
func init() { Register(lc+"chainRetrievalQa", mapRetrievalQA) }

const (
	qaQuery   = "__n8n_query"
	qaContext = "__n8n_context"
	qaAnswer  = "__n8n_answer"
	// qaNoText is what the compressor answers for an irrelevant document.
	qaNoText = "NO_RELEVANT_TEXT"
)

const qaDefaultSystem = "Answer the user's question from the context below. If the context does not hold the answer, say that you do not know rather than guessing.\n\nContext:\n{context}"

// qaChain collects the mono steps of one chain.
type qaChain struct {
	c     *MapContext
	steps []ChainStep
	used  map[string]bool
}

// add appends a step named "<n8n name> (<label>)" and returns that name.
func (q *qaChain) add(typ, label string, cfg map[string]interface{}) string {
	suffix := " (" + label + ")"
	for i := 2; q.used[suffix]; i++ {
		suffix = fmt.Sprintf(" (%s %d)", label, i)
	}
	q.used[suffix] = true
	q.steps = append(q.steps, ChainStep{Type: typ, Suffix: suffix, Config: cfg})
	return q.c.NodeName + suffix
}

// rollback drops the steps added after the first n.
func (q *qaChain) rollback(n int) {
	for _, s := range q.steps[n:] {
		delete(q.used, s.Suffix)
	}
	q.steps = q.steps[:n]
}

func (q *qaChain) mapping() Mapping {
	first := q.steps[0]
	last := len(q.steps) - 1
	q.steps[last].Suffix = ""
	return Mapping{Type: first.Type, Config: first.Config, EntrySuffix: first.Suffix, Chain: q.steps[1:]}
}

// model sets runtime and model from the ai_languageModel sub-nodes in subs.
func (q *qaChain) model(subs []SubNode, cfg map[string]interface{}) {
	var models []SubNode
	for _, s := range subs {
		if s.Kind == "ai_languageModel" {
			models = append(models, s)
		}
	}
	saved := q.c.SubNodes
	q.c.SubNodes = models
	applyModel(q.c, cfg)
	q.c.SubNodes = saved
}

// sub is a MapContext over a sub-node's own parameters; absorb moves its
// report entries onto the chain node.
func (q *qaChain) sub(s SubNode) *MapContext {
	return &MapContext{NodeName: s.Name, Type: s.Type, TypeVersion: s.TypeVersion, Params: s.Parameters,
		Credentials: s.Credentials, tr: q.c.tr, used: map[string]bool{}}
}

func (q *qaChain) absorb(sc *MapContext) {
	sc.reportUnused()
	for _, is := range sc.issues {
		if is.Param != "" {
			is.Param = sc.NodeName + "." + is.Param
		}
		q.c.issues = append(q.c.issues, is)
	}
	for _, e := range sc.exprs {
		e.Param = sc.NodeName + "." + e.Param
		q.c.exprs = append(q.c.exprs, e)
	}
	q.c.creds = append(q.c.creds, sc.creds...)
}

func firstKind(subs []SubNode, kind string) *SubNode {
	for i := range subs {
		if subs[i].Kind == kind {
			return &subs[i]
		}
	}
	return nil
}

func mapRetrievalQA(c *MapContext) Mapping {
	c.Ignore("notice", "promptType", "options.batching")
	q := &qaChain{c: c, used: map[string]bool{}}
	question := q.add("core.set", "question", map[string]interface{}{
		"assignments":   []interface{}{map[string]interface{}{"field": qaQuery, "value": retrievalQuery(c), "type": "string"}},
		"include_input": false,
	})
	var models []SubNode
	var retriever *SubNode
	for i, s := range c.SubNodes {
		switch s.Kind {
		case "ai_languageModel":
			models = append(models, s)
		case "ai_retriever":
			if retriever == nil {
				retriever = &c.SubNodes[i]
			}
		default:
			c.Warn(s.Kind, s.Name, "sub-node %s is not supported", shortType(s.Type))
		}
	}
	ctxField := ""
	switch {
	case retriever == nil:
		c.Warn("ai_retriever", nil, "no retriever is attached: the question is answered without retrieved documents")
	case q.retrieve(*retriever, question):
		q.add("core.code", "context", codeConfig(wrapCode(qaGroupJS(question))))
		ctxField = "{{ $json." + qaContext + " }}"
		c.Note("when the retriever finds no document for any of the questions, the steps after it do not run (n8n answers from an empty context)")
	}
	cfg := map[string]interface{}{"prompt": "{{ $json." + qaQuery + " }}", "system_prompt": qaSystemPrompt(c, ctxField),
		"output_key": qaAnswer}
	if c.Has("options.batching.batchSize") {
		if n, ok := c.Num("options.batching.batchSize", 0).(float64); ok && n >= 1 {
			cfg["concurrency"] = n
		}
	}
	q.model(models, cfg)
	q.add("agent.ask", "answer", cfg)
	shape := "{ response: it.json." + qaAnswer + " }"
	if c.TypeVersion < 1.5 {
		shape = "{ response: { text: it.json." + qaAnswer + " } }"
	}
	q.add("core.code", "output", codeConfig(wrapCode("return items.map(function (it) { return "+shape+"; });\n")))
	return q.mapping()
}

// retrievalQuery is the question: "query" up to typeVersion 1.2, then
// promptType/text.
func retrievalQuery(c *MapContext) string {
	if c.TypeVersion <= 1.2 {
		def := "={{ $json.chatInput }}"
		switch {
		case c.TypeVersion < 1.1:
			def = "={{ $json.input }}"
		case c.TypeVersion < 1.2:
			def = "={{ $json.chat_input }}"
		}
		return c.Str("query", def)
	}
	c.Ignore("query")
	switch c.Literal("promptType", "auto") {
	case "define":
		return c.Str("text", "")
	case "guardrails":
		return c.Str("text", "={{ $json.guardrailsInput }}")
	}
	return c.Str("text", "={{ $json.chatInput }}")
}

// qaSystemPrompt fills the template's {context} and {input} ({question}
// before typeVersion 1.5) with the step fields.
func qaSystemPrompt(c *MapContext, context string) string {
	tmpl := c.Str("options.systemPromptTemplate", qaDefaultSystem)
	if c.TypeVersion < 1.5 {
		tmpl = strings.ReplaceAll(tmpl, "{question}", "{input}")
	}
	tmpl = strings.ReplaceAll(tmpl, "{context}", context)
	return strings.ReplaceAll(tmpl, "{input}", "{{ $json."+qaQuery+" }}")
}

// retrieve adds the steps of retriever s, fed by the items of queryStep
// (the step just added). It reports false, adding nothing, when s cannot
// retrieve.
func (q *qaChain) retrieve(s SubNode, queryStep string) bool {
	mark := len(q.steps)
	var ok bool
	switch shortType(s.Type) {
	case "retrieverVectorStore":
		ok = q.vectorRetriever(s)
	case "retrieverMultiQuery":
		ok = q.multiQuery(s, queryStep)
	case "retrieverContextualCompression":
		ok = q.compression(s, queryStep)
	case "retrieverWorkflow":
		ok = q.workflowRetriever(s, queryStep)
	default:
		q.c.Warn("ai_retriever", s.Name, "retriever %s is not supported: the question is answered without retrieved documents", shortType(s.Type))
	}
	if !ok {
		q.rollback(mark)
	}
	return ok
}

func (q *qaChain) noDocuments(s SubNode, why string) bool {
	q.c.Warn("ai_retriever", s.Name, "retriever %q (%s) %s: the question is answered without retrieved documents", s.Name, shortType(s.Type), why)
	return false
}

// vectorRetriever: the ai_vectorStore child becomes a vector.store search
// (internal/nodes/vector folds the store and its embedding).
func (q *qaChain) vectorRetriever(s SubNode) bool {
	c := q.c
	store := firstKind(s.Children, "ai_vectorStore")
	if store == nil {
		return q.noDocuments(s, "has no vector store attached")
	}
	res, err := vectornodes.CollapseN8nSubNodes(s.Type, withoutEmpty(s.Parameters), c.vectorSubs(s.Children))
	if err != nil {
		return q.noDocuments(s, err.Error())
	}
	for _, w := range res.Warnings {
		c.Note("%s", w)
	}
	if len(res.Before) > 0 {
		c.Note("the document loader attached to %q is not used: the retriever only searches", store.Name)
	}
	cfg := c.translateConfig(res.Node.Config)
	for _, k := range []string{"query", "tool_name", "tool_description"} {
		delete(cfg, k)
	}
	cfg["mode"] = "search"
	cfg["query_field"] = qaQuery
	if platform, _ := vectorCredential(store.Credentials); platform != "" {
		cfg["credential_id"] = platform // recorded by vectorSubs
	}
	q.add("vector.store", "retrieve", cfg)
	return true
}

// multiQuery: an agent writes alternative questions, each one is
// retrieved, and the documents are merged back per question.
func (q *qaChain) multiQuery(s SubNode, queryStep string) bool {
	sc := q.sub(s)
	n := sc.Float("options.queryCount", 3)
	q.absorb(sc)
	inner := firstKind(s.Children, "ai_retriever")
	if inner == nil {
		return q.noDocuments(s, "has no retriever attached")
	}
	cfg := map[string]interface{}{
		"prompt": fmt.Sprintf("Write %d different versions of the question below, each worded differently, so that a similarity search "+
			"over a document store finds more of the relevant documents. Do not answer it.\n\nQuestion: {{ $json.%s }}", int(n), qaQuery),
		"output_key":    "__n8n_queries",
		"output_schema": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
	}
	q.model(s.Children, cfg)
	q.add("agent.ask", "alternative queries", cfg)
	fan := q.add("core.code", "queries", codeConfig(wrapCode(qaFanOutJS)))
	if !q.retrieve(*inner, fan) {
		return false
	}
	q.add("core.code", "merge queries", codeConfig(wrapCode(qaMergeJS(fan))))
	q.c.Note("%q also searches with the original question, next to the %d generated ones", s.Name, int(n))
	return true
}

// compression: after the inner retriever, an agent keeps only the parts of
// each document that bear on its question; empty documents are dropped.
func (q *qaChain) compression(s SubNode, queryStep string) bool {
	q.absorb(q.sub(s))
	inner := firstKind(s.Children, "ai_retriever")
	if inner == nil {
		return q.noDocuments(s, "has no retriever attached")
	}
	if !q.retrieve(*inner, queryStep) {
		return false
	}
	q.add("core.code", "compress input", codeConfig(wrapCode(qaAttachQueryJS(queryStep))))
	cfg := map[string]interface{}{
		"prompt": "Here are a question and a document. Copy out, word for word, only the parts of the document that help answer the question, " +
			"and add nothing of your own. If no part of it helps, reply with exactly " + qaNoText + ".\n\nQuestion: {{ $json." + qaQuery +
			" }}\n\nDocument:\n{{ $json.pageContent }}",
		"output_key": "__n8n_extract",
	}
	q.model(s.Children, cfg)
	q.add("agent.ask", "compress", cfg)
	q.add("core.code", "compressed", codeConfig(wrapCode(qaCompressedJS)))
	return true
}

// workflowRetriever runs the saved sub-workflow once per question with
// {query} and turns its output items into documents.
func (q *qaChain) workflowRetriever(s SubNode, queryStep string) bool {
	sc := q.sub(s)
	sc.Ignore("executeNotice")
	if src := sc.Literal("source", "database"); src != "database" {
		sc.Ignore("workflowJson", "fields")
		q.absorb(sc)
		return q.noDocuments(s, "runs a workflow given as JSON; core.execute_workflow runs saved workflows only, so import that workflow and pick it")
	}
	assign := []interface{}{map[string]interface{}{"field": "query", "value": "{{ $json." + qaQuery + " }}", "type": "string"}}
	types := map[string]string{"stringValue": "string", "numberValue": "number", "booleanValue": "bool",
		"arrayValue": "json", "objectValue": "json"}
	for i, f := range sc.paramList("fields.values") {
		typ, _ := f["type"].(string)
		if typ == "" {
			typ = "stringValue"
		}
		name, _ := f["name"].(string)
		if name == "" || types[typ] == "" {
			continue
		}
		v := sc.Translate(fmt.Sprintf("fields.values[%d]", i), f[typ])
		assign = append(assign, map[string]interface{}{"field": name, "value": v, "type": types[typ]})
	}
	m := mapExecuteWorkflow(sc)
	q.absorb(sc)
	if m.Unmapped || m.Type != "core.execute_workflow" {
		return q.noDocuments(s, "cannot be run")
	}
	m.Config["mode"] = "each"
	q.add("core.set", "retriever input", map[string]interface{}{"assignments": assign, "include_input": false})
	q.add("core.execute_workflow", "retriever workflow", m.Config)
	q.add("core.code", "retrieved", codeConfig(wrapCode(qaWorkflowDocsJS(queryStep, fmt.Sprint(m.Config["workflowId"])))))
	q.c.Warn("ai_retriever", s.Name, "with several questions in one run, each is given the documents of every sub-workflow run: core.execute_workflow does not say which item a result came from")
	return true
}

// ── generated JavaScript ────────────────────────────────────────────────────

// qaGroupJS builds one item per question with the joined page contents of
// its documents.
func qaGroupJS(questionStep string) string {
	return "var qs = globalThis.$(" + jsQuote(questionStep) + ").all();\n" +
		"var docs = items.map(function (it) { return it.json; }).filter(function (d) { return !d.__n8n_drop; });\n" +
		"return qs.map(function (q, i) {\n" +
		"  var ctx = docs.filter(function (d) { return d.source_item === i || d.source_item === -1; })\n" +
		"    .map(function (d) { return d.pageContent == null ? \"\" : String(d.pageContent); }).join(\"\\n\\n\");\n" +
		"  return { " + qaQuery + ": q." + qaQuery + ", " + qaContext + ": ctx };\n" +
		"});\n"
}

// qaFanOutJS emits one query item per question and generated variant.
const qaFanOutJS = `var out = [];
items.forEach(function (it, i) {
  var seen = {};
  var list = [it.json.__n8n_query].concat(Array.isArray(it.json.__n8n_queries) ? it.json.__n8n_queries : []);
  list.forEach(function (q) {
    q = q == null ? "" : String(q).trim();
    if (q && !seen[q]) { seen[q] = true; out.push({ __n8n_query: q, __n8n_parent: i }); }
  });
});
return out;
`

// qaMergeJS points each document back at its question and drops
// duplicates per question.
func qaMergeJS(fanStep string) string {
	return "var fan = globalThis.$(" + jsQuote(fanStep) + ").all();\n" + `var seen = {};
var out = [];
items.forEach(function (it) {
  var d = it.json;
  var src = d.source_item;
  src = (typeof src === "number" && src >= 0 && fan[src]) ? fan[src].__n8n_parent : -1;
  var key = src + "\u0000" + d.pageContent;
  if (!d.__n8n_drop) {
    if (seen[key]) { return; }
    seen[key] = true;
  }
  var o = {};
  Object.keys(d).forEach(function (k) { o[k] = d[k]; });
  o.source_item = src;
  out.push(o);
});
return out;
`
}

// qaAttachQueryJS copies each document's question onto it.
func qaAttachQueryJS(queryStep string) string {
	return "var qs = globalThis.$(" + jsQuote(queryStep) + ").all();\n" + `var all = qs.map(function (x) { return x.__n8n_query; }).join("\n");
return items.map(function (it) {
  var d = it.json;
  var o = {};
  Object.keys(d).forEach(function (k) { o[k] = d[k]; });
  var src = d.source_item;
  o.__n8n_query = (typeof src === "number" && src >= 0 && qs[src]) ? qs[src].__n8n_query : all;
  return o;
});
`
}

// qaCompressedJS keeps the extracted text, marking empty documents dropped.
const qaCompressedJS = `return items.map(function (it) {
  var d = it.json;
  var o = {};
  Object.keys(d).forEach(function (k) {
    if (k !== "__n8n_extract" && k.indexOf("_agent_") !== 0) { o[k] = d[k]; }
  });
  var t = d.__n8n_extract == null ? "" : String(d.__n8n_extract).trim();
  if (!t || t.indexOf("` + qaNoText + `") >= 0) { o.__n8n_drop = true; } else { o.pageContent = t; }
  return o;
});
`

// qaWorkflowDocsJS turns sub-workflow output items into documents.
func qaWorkflowDocsJS(queryStep, workflowID string) string {
	return "var n = globalThis.$(" + jsQuote(queryStep) + ").all().length;\n" +
		"var wf = " + jsQuote(workflowID) + ";\n" + `return items.map(function (it, i) {
  var j = it.json;
  return {
    pageContent: typeof j.pageContent === "string" ? j.pageContent : JSON.stringify(j),
    metadata: { source: "workflow", workflowId: wf, itemIndex: i },
    source_item: n === 1 ? 0 : -1
  };
});
`
}
