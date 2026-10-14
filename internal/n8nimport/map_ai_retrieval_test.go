package n8nimport_test

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"

	"github.com/monoes/mono-agent/internal/n8nimport"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/workflow"
)

// qaNode is one n8n node of a test workflow.
func qaNode(name, typ string, version float64, params map[string]interface{}) map[string]interface{} {
	if params == nil {
		params = map[string]interface{}{}
	}
	return map[string]interface{}{"id": name, "name": name, "type": typ, "typeVersion": version,
		"position": []int{0, 0}, "parameters": params}
}

// qaWorkflow builds an n8n export: main runs Start → QA → Done, plus
// sub-node links given as child → [kind, parent].
func qaWorkflow(t *testing.T, qa map[string]interface{}, subs []map[string]interface{}, links [][3]string) []byte {
	t.Helper()
	nodes := []interface{}{qaNode("Start", "n8n-nodes-base.manualTrigger", 1, nil), qa,
		qaNode("Done", "n8n-nodes-base.noOp", 1, nil)}
	for _, s := range subs {
		nodes = append(nodes, s)
	}
	conns := map[string]interface{}{
		"Start": map[string]interface{}{"main": []interface{}{[]interface{}{map[string]interface{}{"node": "QA", "type": "main", "index": 0}}}},
		"QA":    map[string]interface{}{"main": []interface{}{[]interface{}{map[string]interface{}{"node": "Done", "type": "main", "index": 0}}}},
	}
	for _, l := range links {
		conns[l[0]] = map[string]interface{}{l[1]: []interface{}{[]interface{}{map[string]interface{}{"node": l[2], "type": l[1], "index": 0}}}}
	}
	raw, err := json.Marshal(map[string]interface{}{"name": "qa", "nodes": nodes, "connections": conns})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func convertQA(t *testing.T, raw []byte) (*workflow.WorkflowFile, *n8nimport.Report) {
	t.Helper()
	wf, rep, err := n8nimport.Convert(raw)
	if err != nil {
		t.Fatal(err)
	}
	return wf, rep
}

// qaChainNodes follows the chain from the node after Start to QA.
func qaChainNodes(t *testing.T, wf *workflow.WorkflowFile) []workflow.WorkflowFileNode {
	t.Helper()
	byID := map[string]workflow.WorkflowFileNode{}
	next := map[string]string{}
	for _, n := range wf.Nodes {
		byID[n.ID] = n
	}
	for _, e := range wf.Connections {
		next[e.Source] = e.Target
	}
	var out []workflow.WorkflowFileNode
	id := next["Start"]
	for id != "" && byID[id].Name != "Done" {
		out = append(out, byID[id])
		id = next[id]
		if len(out) > 20 {
			t.Fatal("chain does not reach Done")
		}
	}
	if len(out) == 0 || out[len(out)-1].Name != "QA" {
		t.Fatalf("chain does not end in the node named QA: %+v", out)
	}
	return out
}

func chainTypes(nodes []workflow.WorkflowFileNode) string {
	var ts []string
	for _, n := range nodes {
		ts = append(ts, n.Type)
	}
	return strings.Join(ts, " → ")
}

func qaStep(t *testing.T, nodes []workflow.WorkflowFileNode, name string) workflow.WorkflowFileNode {
	t.Helper()
	for _, n := range nodes {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("no node %q", name)
	return workflow.WorkflowFileNode{}
}

// fakeEmbeddings serves an OpenAI-compatible /embeddings endpoint with
// bag-of-words vectors, so similar wording ranks first.
func fakeEmbeddings(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var data []map[string]interface{}
		for i, text := range body.Input {
			data = append(data, map[string]interface{}{"index": i, "embedding": bagOfWords(text)})
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func bagOfWords(text string) []float64 {
	v := make([]float64, 64)
	v[0] = 0.01
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if len(w) < 4 {
			continue
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(w))
		v[h.Sum32()%64]++
	}
	return v
}

// insertDocs fills a memory vector store through vector.store insert.
func insertDocs(t *testing.T, key, embedURL string, docs ...string) {
	t.Helper()
	var items []workflow.Item
	for _, d := range docs {
		items = append(items, workflow.NewItem(map[string]interface{}{"pageContent": d}))
	}
	factory, _ := noderegistry.Build(nil).Get("vector.store")
	_, err := factory().Execute(context.Background(), workflow.NodeInput{Items: items}, map[string]interface{}{
		"provider": "memory", "mode": "insert", "memory_key": key, "clear": true,
		"embedding": map[string]interface{}{"provider": "openai_compatible", "base_url": embedURL}})
	if err != nil {
		t.Fatal(err)
	}
}

// qaFakes stand in for the nodes that need an agent or the engine.
type qaFakes struct {
	ask  func(outputKey, prompt, system string, item map[string]interface{}) interface{}
	exec func(items []map[string]interface{}) []map[string]interface{}
}

// runQAChain runs the chain nodes in order the way the engine does
// (outputs by name, a node with no input items is skipped) and returns
// the last node's items.
func runQAChain(t *testing.T, nodes []workflow.WorkflowFileNode, in []map[string]interface{}, f qaFakes) []map[string]interface{} {
	t.Helper()
	reg := noderegistry.Build(nil)
	outputs := map[string][]workflow.Item{}
	var items []workflow.Item
	for _, it := range in {
		items = append(items, workflow.NewItem(it))
	}
	expr := workflow.NewExpressionEngine()
	for _, n := range nodes {
		if len(items) == 0 {
			t.Fatalf("node %q gets no items: the engine would skip it", n.Name)
		}
		var out []workflow.Item
		switch n.Type {
		case "agent.ask":
			for _, it := range items {
				eval := func(k string) string {
					s, _ := n.Config[k].(string)
					r, err := expr.EvaluateString(s, workflow.ExpressionContext{JSON: it.JSON})
					if err != nil {
						t.Fatalf("%s: %s: %v", n.Name, k, err)
					}
					return r
				}
				j := map[string]interface{}{}
				for k, v := range it.JSON {
					j[k] = v
				}
				key := n.Config["output_key"].(string)
				j[key] = f.ask(key, eval("prompt"), eval("system_prompt"), it.JSON)
				out = append(out, workflow.NewItem(j))
			}
		case "core.execute_workflow":
			var js []map[string]interface{}
			for _, it := range items {
				js = append(js, it.JSON)
			}
			for _, j := range f.exec(js) {
				out = append(out, workflow.NewItem(j))
			}
		default:
			factory, ok := reg.Get(n.Type)
			if !ok {
				t.Fatalf("type %s not registered", n.Type)
			}
			cfg := map[string]interface{}{}
			b, _ := json.Marshal(n.Config)
			_ = json.Unmarshal(b, &cfg)
			res, err := factory().Execute(context.Background(), workflow.NodeInput{Items: items, NodeOutputs: outputs}, cfg)
			if err != nil {
				t.Fatalf("%s (%s): %v", n.Name, n.Type, err)
			}
			for _, o := range res {
				out = append(out, o.Items...)
			}
		}
		outputs[n.Name] = out
		items = out
	}
	var res []map[string]interface{}
	for _, it := range items {
		res = append(res, it.JSON)
	}
	return res
}

const lcp = "@n8n/n8n-nodes-langchain."

var qaDocs = []string{
	"Paris is the capital of France and its largest city.",
	"Bananas are yellow fruit rich in potassium.",
	"Mount Everest is the highest mountain on Earth.",
}

func vectorRetrieverSubs(embedURL, key string) ([]map[string]interface{}, [][3]string) {
	return []map[string]interface{}{
		qaNode("Retriever", lcp+"retrieverVectorStore", 1, map[string]interface{}{"topK": 1}),
		qaNode("Store", lcp+"vectorStoreInMemory", 1.1, map[string]interface{}{"memoryKey": key}),
		qaNode("Embeddings", lcp+"embeddingsOpenAi", 1.2, map[string]interface{}{"model": "text-embedding-3-small",
			"options": map[string]interface{}{"baseURL": embedURL}}),
	}, [][3]string{
		{"Store", "ai_vectorStore", "Retriever"},
		{"Embeddings", "ai_embedding", "Store"},
	}
}

// answerWithSystem makes the answer the system prompt the agent got.
func answerWithSystem(outputKey, prompt, system string, item map[string]interface{}) interface{} {
	return "Q=" + prompt + "\n" + system
}

func TestRetrievalQAVectorStore(t *testing.T) {
	srv := fakeEmbeddings(t)
	insertDocs(t, "qa-vs", srv.URL, qaDocs...)
	subs, links := vectorRetrieverSubs(srv.URL, "qa-vs")
	subs = append(subs, qaNode("Model", lcp+"lmChatOpenAi", 1.2, map[string]interface{}{
		"model": map[string]interface{}{"__rl": true, "mode": "list", "value": "gpt-4o-mini"}}))
	links = append(links, [3]string{"Retriever", "ai_retriever", "QA"}, [3]string{"Model", "ai_languageModel", "QA"})
	qa := qaNode("QA", lcp+"chainRetrievalQa", 1.7, map[string]interface{}{
		"options": map[string]interface{}{"systemPromptTemplate": "Use this: {context}"}})
	wf, rep := convertQA(t, qaWorkflow(t, qa, subs, links))
	nodes := qaChainNodes(t, wf)
	if got, want := chainTypes(nodes), "core.set → vector.store → core.code → agent.ask → core.code"; got != want {
		t.Fatalf("chain %s, want %s", got, want)
	}
	store := qaStep(t, nodes, "QA (retrieve)").Config
	emb, _ := store["embedding"].(map[string]interface{})
	if store["provider"] != "memory" || store["mode"] != "search" || store["memory_key"] != "qa-vs" ||
		fmt.Sprint(store["top_k"]) != "1" || store["query_field"] != "__n8n_query" ||
		emb["provider"] != "openai_compatible" || emb["base_url"] != srv.URL {
		t.Fatalf("vector.store config %v", store)
	}
	ask := qaStep(t, nodes, "QA (answer)").Config
	if ask["runtime"] != "codex" || ask["model"] != "gpt-4o-mini" || ask["system_prompt"] != "Use this: {{ $json.__n8n_context }}" {
		t.Fatalf("agent.ask config %v", ask)
	}
	for _, nr := range rep.Nodes {
		if nr.Name == "QA" && nr.Status != n8nimport.StatusFull {
			t.Errorf("status %s: %+v", nr.Status, nr.Issues)
		}
	}
	got := runQAChain(t, nodes, []map[string]interface{}{
		{"chatInput": "What is the capital city of France?"},
		{"chatInput": "Which mountain is the highest on Earth?"},
	}, qaFakes{ask: answerWithSystem})
	expectJSON(t, "answers", got, `[
		{"response":"Q=What is the capital city of France?\nUse this: `+qaDocs[0]+`"},
		{"response":"Q=Which mountain is the highest on Earth?\nUse this: `+qaDocs[2]+`"}]`)
}

func TestRetrievalQAMultiQuery(t *testing.T) {
	srv := fakeEmbeddings(t)
	insertDocs(t, "qa-mq", srv.URL, qaDocs...)
	subs, links := vectorRetrieverSubs(srv.URL, "qa-mq")
	subs = append(subs,
		qaNode("Multi", lcp+"retrieverMultiQuery", 1, map[string]interface{}{"options": map[string]interface{}{"queryCount": 2}}),
		qaNode("Claude", lcp+"lmChatAnthropic", 1.3, map[string]interface{}{"model": "claude-haiku-4-5"}))
	links = append(links, [3]string{"Retriever", "ai_retriever", "Multi"}, [3]string{"Claude", "ai_languageModel", "Multi"},
		[3]string{"Multi", "ai_retriever", "QA"})
	qa := qaNode("QA", lcp+"chainRetrievalQa", 1.2, map[string]interface{}{})
	wf, _ := convertQA(t, qaWorkflow(t, qa, subs, links))
	nodes := qaChainNodes(t, wf)
	want := "core.set → agent.ask → core.code → vector.store → core.code → core.code → agent.ask → core.code"
	if got := chainTypes(nodes); got != want {
		t.Fatalf("chain %s, want %s", got, want)
	}
	alt := qaStep(t, nodes, "QA (alternative queries)").Config
	if alt["runtime"] != "claude" || alt["model"] != "claude-haiku-4-5" || alt["output_schema"] == nil ||
		!strings.Contains(alt["prompt"].(string), "Write 2 different versions") {
		t.Fatalf("multi-query agent.ask %v", alt)
	}
	if q := qaStep(t, nodes, "QA (question)").Config["assignments"]; !strings.Contains(fmt.Sprint(q), "$json.chatInput") {
		t.Fatalf("v1.2 question %v", q)
	}
	got := runQAChain(t, nodes, []map[string]interface{}{{"chatInput": "capital of France"}}, qaFakes{
		ask: func(key, prompt, system string, item map[string]interface{}) interface{} {
			if key == "__n8n_queries" {
				// One variant finds the same document again, one another.
				return []interface{}{"France capital city", "highest mountain"}
			}
			return answerWithSystem(key, prompt, system, item)
		}})
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	resp := got[0]["response"].(map[string]interface{})["text"].(string) // before 1.5: {response: {text}}
	if strings.Count(resp, qaDocs[0]) != 1 || !strings.Contains(resp, qaDocs[2]) || strings.Contains(resp, qaDocs[1]) {
		t.Fatalf("context not merged and deduplicated:\n%s", resp)
	}
}

func TestRetrievalQAWorkflowRetriever(t *testing.T) {
	subs := []map[string]interface{}{qaNode("WF", lcp+"retrieverWorkflow", 1.1, map[string]interface{}{
		"workflowId": map[string]interface{}{"__rl": true, "mode": "list", "value": "abc", "cachedResultName": "KB search"},
		"fields": map[string]interface{}{"values": []interface{}{
			map[string]interface{}{"name": "lang", "stringValue": "en"}}},
	})}
	qa := qaNode("QA", lcp+"chainRetrievalQa", 1.4, map[string]interface{}{"promptType": "define", "text": "={{ $json.q }}"})
	wf, rep := convertQA(t, qaWorkflow(t, qa, subs, [][3]string{{"WF", "ai_retriever", "QA"}}))
	nodes := qaChainNodes(t, wf)
	want := "core.set → core.set → core.execute_workflow → core.code → core.code → agent.ask → core.code"
	if got := chainTypes(nodes); got != want {
		t.Fatalf("chain %s, want %s", got, want)
	}
	ex := qaStep(t, nodes, "QA (retriever workflow)").Config
	if ex["workflowId"] != "KB search" || ex["mode"] != "each" {
		t.Fatalf("execute_workflow %v", ex)
	}
	in := qaStep(t, nodes, "QA (retriever input)").Config
	expectJSON(t, "retriever input", in, `{"include_input":false,"assignments":[
		{"field":"query","value":"{{ $json.__n8n_query }}","type":"string"},
		{"field":"lang","value":"en","type":"string"}]}`)
	warned := false
	for _, nr := range rep.Nodes {
		for _, is := range nr.Issues {
			warned = warned || (nr.Name == "QA" && is.Severity == n8nimport.SevWarning && strings.Contains(is.Message, "several questions"))
		}
	}
	if !warned {
		t.Error("no warning about document attribution")
	}
	var sent []map[string]interface{}
	got := runQAChain(t, nodes, []map[string]interface{}{{"q": "refund policy?"}}, qaFakes{
		ask: answerWithSystem,
		exec: func(items []map[string]interface{}) []map[string]interface{} {
			sent = items
			return []map[string]interface{}{{"title": "Refunds", "days": 30.0}, {"pageContent": "Refunds take 5 days."}}
		}})
	expectJSON(t, "sub-workflow input", sent, `[{"query":"refund policy?","lang":"en"}]`)
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	text := got[0]["response"].(map[string]interface{})["text"].(string)
	if !strings.HasPrefix(text, "Q=refund policy?\n") || !strings.Contains(text, `"title":"Refunds"`) || !strings.Contains(text, `"days":30`) ||
		!strings.Contains(text, "Refunds take 5 days.") {
		t.Fatalf("answer %q", text)
	}
}

func TestRetrievalQACompression(t *testing.T) {
	srv := fakeEmbeddings(t)
	insertDocs(t, "qa-cc", srv.URL, qaDocs...)
	subs, links := vectorRetrieverSubs(srv.URL, "qa-cc")
	subs[0]["parameters"] = map[string]interface{}{"topK": 3}
	subs = append(subs, qaNode("Compress", lcp+"retrieverContextualCompression", 1, nil),
		qaNode("Mini", lcp+"lmChatOpenAi", 1.2, map[string]interface{}{"model": "gpt-4.1-mini"}))
	links = append(links, [3]string{"Retriever", "ai_retriever", "Compress"}, [3]string{"Mini", "ai_languageModel", "Compress"},
		[3]string{"Compress", "ai_retriever", "QA"})
	wf, _ := convertQA(t, qaWorkflow(t, qaNode("QA", lcp+"chainRetrievalQa", 1.7, nil), subs, links))
	nodes := qaChainNodes(t, wf)
	want := "core.set → vector.store → core.code → agent.ask → core.code → core.code → agent.ask → core.code"
	if got := chainTypes(nodes); got != want {
		t.Fatalf("chain %s, want %s", got, want)
	}
	cmp := qaStep(t, nodes, "QA (compress)").Config
	if cmp["runtime"] != "codex" || cmp["model"] != "gpt-4.1-mini" {
		t.Fatalf("compressor %v", cmp)
	}
	got := runQAChain(t, nodes, []map[string]interface{}{{"chatInput": "Tell me about France"}}, qaFakes{
		ask: func(key, prompt, system string, item map[string]interface{}) interface{} {
			if key == "__n8n_extract" {
				if !strings.Contains(prompt, "Tell me about France") {
					t.Errorf("compress prompt lacks the question: %q", prompt)
				}
				if strings.Contains(prompt, "Paris") {
					return "Paris is the capital of France"
				}
				return "NO_RELEVANT_TEXT"
			}
			return answerWithSystem(key, prompt, system, item)
		}})
	resp := got[0]["response"].(string)
	if !strings.Contains(resp, "Context:\nParis is the capital of France\n") && !strings.HasSuffix(resp, "Context:\nParis is the capital of France") {
		t.Fatalf("compressed context wrong:\n%s", resp)
	}
}

func TestRetrievalQANoRetriever(t *testing.T) {
	wf, rep := convertQA(t, qaWorkflow(t, qaNode("QA", lcp+"chainRetrievalQa", 1, nil), nil, nil))
	nodes := qaChainNodes(t, wf)
	if got := chainTypes(nodes); got != "core.set → agent.ask → core.code" {
		t.Fatalf("chain %s", got)
	}
	if q := fmt.Sprint(nodes[0].Config["assignments"]); !strings.Contains(q, "$json.input") {
		t.Fatalf("v1 question %s", q)
	}
	for _, nr := range rep.Nodes {
		if nr.Name == "QA" && (nr.Status != n8nimport.StatusPartial || !strings.Contains(fmt.Sprint(nr.Issues), "no retriever")) {
			t.Fatalf("report %+v", nr)
		}
	}
	got := runQAChain(t, nodes, []map[string]interface{}{{"input": "hi"}}, qaFakes{ask: answerWithSystem})
	if text := got[0]["response"].(map[string]interface{})["text"].(string); !strings.HasPrefix(text, "Q=hi\n") {
		t.Fatalf("answer %q", text)
	}
}
