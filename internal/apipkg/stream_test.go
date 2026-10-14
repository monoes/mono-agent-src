package apipkg

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// routeServer answers by "METHOD /path" and records each request with
// its body.
type routedReq struct {
	method, path, query, auth, ctype string
	body                             []byte
}

func routeServer(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) func() []routedReq {
	t.Helper()
	var mu sync.Mutex
	var seen []routedReq
	pagedServer(t, func(_ int, r *http.Request, w http.ResponseWriter) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, routedReq{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), b})
		mu.Unlock()
		if h, ok := routes[r.Method+" "+r.URL.Path]; ok {
			h(w, r)
			return
		}
		w.Write([]byte(`{}`))
	})
	return func() []routedReq {
		mu.Lock()
		defer mu.Unlock()
		return append([]routedReq(nil), seen...)
	}
}

func sse(w http.ResponseWriter, lines ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, l := range lines {
		fmt.Fprint(w, l)
	}
}

func TestStreamPicksTheMatchingEvent(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"wait","title":"Wait","method":"GET","path":"/events",
	  "fields":[{"key":"id","label":"Id","type":"text"}],
	  "stream":{"until":"{{ res.id | eq:p.id }}","timeout_ms":5000},
	  "output":{"to":{"got":"{{ res.id }}","note":"{{ res.note }}"}}}`))
	reqs := routeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /v1/events": func(w http.ResponseWriter, r *http.Request) {
			sse(w, ": keep-alive\n\n", "event: status\nid: 1\ndata: {\"id\":\"a\"}\n\n",
				"data: {\"id\":\"b\",\n", "data: \"note\":\"two lines\"}\r\n\r\n", "data: {\"id\":\"c\"}\n\n")
		},
	})
	items, err := runPackageOp(p, map[string]any{"operation": "wait", "id": "b"}, cred, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].JSON["got"] != "b" || items[0].JSON["note"] != "two lines" {
		t.Fatalf("items = %+v", items)
	}
	if r := reqs(); len(r) != 1 || r[0].auth != "Bearer T" {
		t.Errorf("requests = %+v", r)
	}
}

func TestStreamFailures(t *testing.T) {
	op := func(extra string) *Package {
		return mustParse(t, pkgJSON(`{"resource":"","operation":"wait","title":"Wait","method":"GET","path":"/events",
		  "stream":{"until":"{{ res.ok }}","timeout_ms":300},"error_when":"{{ res.failed }}","error_message":"event says {{ res.failed }}"`+extra+`}`))
	}
	cases := []struct {
		name  string
		serve func(w http.ResponseWriter, r *http.Request)
		want  string
	}{
		{"closed", func(w http.ResponseWriter, r *http.Request) { sse(w, "data: {\"ok\":false}\n\n") },
			"closed the event stream before a matching event"},
		{"timeout", func(w http.ResponseWriter, r *http.Request) {
			sse(w, "data: {}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}, "no matching event within 300ms"},
		{"error event", func(w http.ResponseWriter, r *http.Request) {
			sse(w, "data: {\"ok\":true,\"failed\":\"disk full\"}\n\n")
		},
			"event says disk full"},
		{"status", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403); w.Write([]byte(`{"error":"no"}`)) },
			`HTTP 403: {"error":"no"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			routeServer(t, map[string]func(http.ResponseWriter, *http.Request){"GET /v1/events": c.serve})
			start := time.Now()
			_, err := runPackageOp(op(""), map[string]any{"operation": "wait"}, cred, nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if time.Since(start) > 3*time.Second {
				t.Errorf("took %s", time.Since(start))
			}
		})
	}
	bad := `{"$schema":"apipkg.v1","id":"demo","title":"Demo","base_url":"https://x.test","operations":[
	  {"resource":"","operation":"w","title":"W","method":"GET","path":"/e","stream":{"until":""},
	   "before":{"method":"GET","path":"/b","repeat":{"until":"{{ res.x | nope_filter }}"}}}]}`
	if _, err := Parse([]byte(bad), "bad"); err == nil || !strings.Contains(err.Error(), "stream") {
		t.Errorf("invalid stream accepted: %v", err)
	}
}

func TestRepeatStepAndUploadURL(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"up","title":"Up","method":"POST","path":"/files/{{ pres.0.id }}/done",
	  "fields":[{"key":"binaryPropertyName","label":"Bin","type":"text","default":"data"},{"key":"url","label":"URL","type":"text"}],
	  "binary":{"upload_field":"binaryPropertyName","upload_when":"false","upload_url":"{{ p.url }}"},
	  "before":[{"method":"POST","path":"/files","body":{"name":"{{ bin.file_name }}"}},
	    {"upload":true,"auth":false,"method":"PUT","path":"{{ pres.0.put }}","error_when":"false"},
	    {"method":"GET","path":"/files/{{ pres.0.id }}","repeat":{"until":"{{ res.status | eq:'ready' }}","interval_ms":10,"max_wait_ms":2000}}]}`))
	checks := 0
	reqs := routeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /src/report.pdf": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/pdf")
			w.Write([]byte("%PDF-bytes"))
		},
		"POST /v1/files": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"id":"f9","put":"https://upload.test/put/f9"}`))
		},
		"GET /v1/files/f9": func(w http.ResponseWriter, r *http.Request) {
			checks++
			if checks < 3 {
				w.Write([]byte(`{"status":"processing"}`))
				return
			}
			w.Write([]byte(`{"status":"ready"}`))
		},
	})
	if _, err := runPackageOp(p, map[string]any{"operation": "up", "url": "https://files.test/src/report.pdf"}, cred, nil); err != nil {
		t.Fatal(err)
	}
	got := reqs()
	var trail []string
	for _, r := range got {
		trail = append(trail, r.method+" "+r.path)
	}
	want := "GET /src/report.pdf,POST /v1/files,PUT /put/f9,GET /v1/files/f9,GET /v1/files/f9,GET /v1/files/f9,POST /v1/files/f9/done"
	if strings.Join(trail, ",") != want {
		t.Fatalf("requests:\n got %s\nwant %s", strings.Join(trail, ","), want)
	}
	if got[0].auth != "" || got[2].auth != "" {
		t.Errorf("the URL fetch and the presigned PUT must go without the credential: %q %q", got[0].auth, got[2].auth)
	}
	if string(got[2].body) != "%PDF-bytes" || got[2].ctype != "application/pdf" {
		t.Errorf("PUT body %q type %q", got[2].body, got[2].ctype)
	}
	if string(got[1].body) != `{"name":"report.pdf"}` {
		t.Errorf("slot request body %s", got[1].body)
	}

	checks = -1000 // never ready
	_, err := runPackageOp(p, map[string]any{"operation": "up", "url": "https://files.test/src/report.pdf"}, cred, nil)
	if err == nil || !strings.Contains(err.Error(), "repeat: not done after 2s") {
		t.Errorf("repeat timeout: %v", err)
	}
	_, err = runPackageOp(p, map[string]any{"operation": "up", "url": "ftp://files.test/x"}, cred, nil)
	if err == nil || !strings.Contains(err.Error(), "not an http(s) URL") {
		t.Errorf("bad url: %v", err)
	}
}

func airtopPkg(t *testing.T) *Package {
	t.Helper()
	p, ok := ByID("airtop")
	if !ok {
		t.Fatal("airtop package missing from the catalog")
	}
	return p
}

var airtopCred = map[string]any{"api_key": "K"}

func TestAirtopFileUploadFromBinary(t *testing.T) {
	reqs := routeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/files": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"data":{"id":"f1","uploadUrl":"https://storage.test/up/f1?sig=x"}}`))
		},
		"GET /api/v1/files/f1": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"data":{"status":"available"}}`)) },
		"GET /api/v1/sessions/s1/events": func(w http.ResponseWriter, r *http.Request) {
			sse(w, "data: {\"event\":\"file_upload_status\",\"fileId\":\"f1\",\"status\":\"uploading\"}\n\n",
				"data: {\"event\":\"file_upload_status\",\"fileId\":\"f1\",\"status\":\"available\"}\n\n")
		},
	})
	in := map[string]any{"sessionId": "s1", "windowId": "w1", "doc": "aGVsbG8="} // base64 "hello"
	items, err := runPackageOp(airtopPkg(t), map[string]any{"resource": "file", "operation": "upload", "source": "binary",
		"binaryPropertyName": "doc", "fileName": "hello.txt", "triggerFileInputParameter": false}, airtopCred, in)
	if err != nil {
		t.Fatal(err)
	}
	var trail []string
	for _, r := range reqs() {
		trail = append(trail, r.method+" "+r.path)
	}
	want := "POST /api/v1/files,PUT /up/f1,GET /api/v1/files/f1,POST /api/v1/files/f1/push,GET /api/v1/sessions/s1/events"
	if strings.Join(trail, ",") != want {
		t.Fatalf("requests:\n got %s\nwant %s", strings.Join(trail, ","), want)
	}
	put := reqs()[1]
	if string(put.body) != "hello" || put.auth != "" || put.ctype != "application/octet-stream" || put.query != "sig=x" {
		t.Errorf("PUT = %+v", put)
	}
	data, _ := items[0].JSON["data"].(map[string]any)
	if items[0].JSON["sessionId"] != "s1" || items[0].JSON["windowId"] != "w1" || data["fileId"] != "f1" {
		t.Errorf("item = %+v", items[0].JSON)
	}
}

func TestAirtopUploadFailedEvent(t *testing.T) {
	routeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/sessions/s1/events": func(w http.ResponseWriter, r *http.Request) {
			sse(w, "data: {\"event\":\"file_upload_status\",\"fileId\":\"f1\",\"status\":\"upload_failed\",\"eventData\":{\"error\":\"virus found\"}}\n\n")
		},
	})
	_, err := runPackageOp(airtopPkg(t), map[string]any{"resource": "file", "operation": "load", "sessionId": "s1", "windowId": "w1", "fileId": "f1"}, airtopCred, nil)
	if err == nil || !strings.Contains(err.Error(), "virus found") {
		t.Fatalf("err = %v", err)
	}
}

func TestAirtopAgentRunWithoutAwait(t *testing.T) {
	reqs := routeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v2/agents/ag1": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"webhookId":"wh1"}`)) },
		"POST /api/hooks/agents/ag1/webhooks/wh1": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"invocationId":"inv1"}`))
		},
		"GET /api/hooks/agents/ag1/invocations/inv1/result": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"status":"Failed","error":"blocked by captcha"}`))
		},
	})
	items, err := runPackageOp(airtopPkg(t), map[string]any{"resource": "agent", "operation": "run", "agentId": "ag1",
		"awaitExecution": false, "profileName": "prof"}, airtopCred, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs()) != 2 || reqs()[1].query != "profileId=prof" || string(reqs()[1].body) != `{"configVars":{}}` {
		t.Errorf("requests = %+v", reqs())
	}
	if items[0].JSON["invocationId"] != "inv1" || items[0].JSON["status"] != nil {
		t.Errorf("item = %+v", items[0].JSON)
	}
	_, err = runPackageOp(airtopPkg(t), map[string]any{"resource": "agent", "operation": "run", "agentId": "ag1"}, airtopCred, nil)
	if err == nil || !strings.Contains(err.Error(), "blocked by captcha. Agent Invocation ID: inv1") {
		t.Errorf("agent error: %v", err)
	}
}
