package apipkg

import (
	"crypto/md5"  //nolint:gosec // test vector
	"crypto/sha1" //nolint:gosec // test vector
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSnakeASCIIHashModWeekday(t *testing.T) {
	sum := func(b []byte) (string, string, string) {
		m, s1, s2 := md5.Sum(b), sha1.Sum(b), sha256.Sum256(b) //nolint:gosec // test vector
		return hex.EncodeToString(m[:]), hex.EncodeToString(s1[:]), hex.EncodeToString(s2[:])
	}
	m, s1, s2 := sum([]byte("hi"))
	sc := Scope{P: map[string]any{"name": "Prénom", "dec": "Prénom", "txt": "hi", "b64": "aGk=", "d": "2026-10-13T08:00:00Z", "neg": -1}}
	cases := map[string]any{
		"{{ p.name | snake }}":                "prénom",
		"{{ p.name | snake:'ascii' }}":        "pr_nom",
		"{{ p.dec | snake:'ascii' }}":         "pre_nom",
		"{{ p.txt | md5 }}":                   m,
		"{{ p.txt | sha1 }}":                  s1,
		"{{ p.txt | sha256 }}":                s2,
		"{{ p.b64 | sha256:'base64' }}":       s2,
		"{{ p.none | sha256 }}":               nil,
		"{{ p.d | weekday }}":                 int64(2), // a Tuesday
		"{{ p.neg | mod:7 }}":                 int64(6),
		"{{ p.d | weekday | add:5 | mod:7 }}": int64(0),
		// GA4 lastCalendarWeek: Monday and Sunday of the week before.
		"{{ (p.d | date:'YYYY-MM-DD' | epoch) | sub:((p.d | weekday | add:6) | mul:86400) | date:'YYYY-MM-DD' }}": "2026-10-05",
		"{{ (p.d | date:'YYYY-MM-DD' | epoch) | sub:(p.d | weekday | mul:86400) | date:'YYYY-MM-DD' }}":           "2026-10-11",
	}
	for tpl, want := range cases {
		got, err := renderString(tpl, sc)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v (%v), want %#v", tpl, got, err, want)
		}
	}
	if _, err := renderString("{{ p.txt | sha256:'base64' }}", sc); err == nil {
		t.Error("sha256:'base64' on non-base64 text must fail")
	}
	if _, err := renderString("{{ p.neg | mod:0 }}", sc); err == nil {
		t.Error("mod:0 must fail")
	}
}

func TestDotnetDates(t *testing.T) {
	in := map[string]any{
		"CreatedOn": "/Date(1586833770780)/",
		"Zoned":     "/Date(1586833770780+1300)/",
		"Name":      "Date(1)",
		"Lines":     []any{map[string]any{"DueDate": "/Date(0)/", "Qty": 2.0}},
	}
	got := dotnetDates(in)
	want := map[string]any{
		"CreatedOn": "2020-04-14T03:09:30.780Z",
		"Zoned":     "2020-04-14T03:09:30.780Z",
		"Name":      "Date(1)",
		"Lines":     []any{map[string]any{"DueDate": "1970-01-01T00:00:00.000Z", "Qty": 2.0}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dotnet_date = %#v", got)
	}
	if in["CreatedOn"] != "/Date(1586833770780)/" {
		t.Error("dotnet_date must not change its input")
	}
}

func TestErrorWhen(t *testing.T) {
	p := mustParse(t, `{"$schema":"apipkg.v1","id":"rpc","title":"R","base_url":"https://r.test",
	  "error_when":"{{ res.error }}","error_message":"{{ res.error.data.message | default:res.error.message }}",
	  "credentials":[{"platform":"rpc","methods":["none"],"inject":{}}],
	  "operations":[{"resource":"","operation":"call","title":"C","method":"POST","path":"/jsonrpc","body":{"x":1}},
	   {"resource":"","operation":"slack","title":"S","method":"POST","path":"/chat","body":{"x":1},
	    "error_when":"{{ res.ok | eq:'false' }}","error_message":"{{ res.error }}"}]}`)
	reply := `{"error":{"message":"Odoo Server Error","data":{"message":"Access denied"}}}`
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) { w.Write([]byte(reply)) })
	_, err := runPackageOp(p, map[string]any{"operation": "call"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "the API reported an error: Access denied") {
		t.Fatalf("package error_when: %v", err)
	}
	reply = `{"result":[1]}`
	if items, err := runPackageOp(p, map[string]any{"operation": "call"}, nil, nil); err != nil || len(items) != 1 {
		t.Fatalf("ok response: %v %v", items, err)
	}
	// The op's rule replaces the package's: {"error": …} with ok:true passes.
	reply = `{"ok":true,"error":"ignored"}`
	if _, err := runPackageOp(p, map[string]any{"operation": "slack"}, nil, nil); err != nil {
		t.Fatalf("op rule must replace the package rule: %v", err)
	}
	reply = `{"ok":false,"error":"channel_not_found"}`
	if _, err := runPackageOp(p, map[string]any{"operation": "slack"}, nil, nil); err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("op error_when: %v", err)
	}
	if _, err := Parse([]byte(`{"$schema":"apipkg.v1","id":"x","title":"X","base_url":"https://x","error_when":"{{ nope.x }}",
	  "operations":[{"resource":"","operation":"a","title":"A","method":"GET","path":"/"}]}`), "t"); err == nil {
		t.Error("a bad error_when template must fail validation")
	}
}

func TestOutputDownloadPerElement(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"list","title":"L","method":"GET","path":"/rows",
	  "fields":[{"key":"download","label":"D","type":"boolean"},{"key":"fields","label":"F","type":"text"},{"key":"bin","label":"B","type":"text","default":"data"}],
	  "output":{"path":"rows","to":{"id":"{{ el.id }}"},
	    "download":{"when":"{{ p.download }}","auth":false,"method":"GET","path":"{{ el.url }}",
	      "files":{"$map":"{{ p.fields | split }}","$to":{"$map":"{{ up | get:el | fromjson }}","$to":{"url":"{{ el.signedUrl | default:el.url }}","prop":"{{ up }}_{{ idx }}","title":"{{ el.title }}"}}},
	      "property":"{{ el.prop }}","file_name":"{{ el.title }}"}}}`))
	var auth []string
	seen := pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		auth = append(auth, r.Header.Get("Authorization"))
		if r.URL.Path == "/v1/rows" {
			w.Write([]byte(`{"rows":[
			  {"id":1,"Docs":[{"url":"https://files.test/a.pdf","title":"a.pdf"},{"signedUrl":"https://files.test/b","url":"x","title":"b.png"}]},
			  {"id":2,"Docs":"[{\"url\":\"https://files.test/c.txt\",\"title\":\"c.txt\"}]","Pics":[]}]}`))
			return
		}
		if r.Header.Get("Accept") == "application/json" {
			t.Errorf("a download must not ask for JSON")
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("file:" + r.URL.Path))
	})
	items, err := runPackageOp(p, map[string]any{"operation": "list", "download": true, "fields": "Docs,Pics"}, cred, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 4 || len(items) != 2 {
		t.Fatalf("want 1 list + 3 downloads and 2 items, got %d requests, %d items", len(*seen), len(items))
	}
	if auth[0] != "Bearer T" || auth[1] != "" {
		t.Errorf("auth: list %q, download %q (auth:false)", auth[0], auth[1])
	}
	if got := string(items[0].Binary["Docs_1"]); got != "file:/b" || string(items[0].Binary["Docs_0"]) != "file:/a.pdf" {
		t.Errorf("item 0 binary = %v", items[0].Binary)
	}
	if got := string(items[1].Binary["Docs_0"]); got != "file:/c.txt" {
		t.Errorf("item 1 binary = %v", items[1].Binary)
	}
	meta, _ := items[0].JSON["binary"].(map[string]any)
	if m, _ := meta["Docs_0"].(map[string]any); m["fileName"] != "a.pdf" || m["mimeType"] != "application/octet-stream" || m["fileSize"] != len("file:/a.pdf") {
		t.Errorf("binary meta = %#v", meta)
	}
	if items[0].JSON["id"] != 1.0 || items[0].JSON[elemBinaryKey] != nil {
		t.Errorf("item json = %#v", items[0].JSON)
	}
	*seen = nil
	items, err = runPackageOp(p, map[string]any{"operation": "list", "fields": "Docs"}, cred, nil)
	if err != nil || len(*seen) != 1 || items[0].Binary != nil {
		t.Errorf("download off: %d requests, %v, %v", len(*seen), items, err)
	}
}

func TestCortexCaseArtifactFile(t *testing.T) {
	p := catalogPkg(t, "cortex")
	m := newMockServer(t, map[string]any{"id": "job1"}, 200)
	_, err := runPackageOp(p, map[string]any{"resource": "responder", "operation": "execute", "responder": "R1::Mailer",
		"entityType": "case_artifact", "parameters": map[string]any{"values": map[string]any{
			"dataType": "file", "binaryPropertyName": "doc", "message": "m"}}},
		map[string]any{"host": "https://cortex.test", "access_token": "k"},
		map[string]any{"binary": map[string]any{"doc": map[string]any{"data": "aGk=", "fileName": "a.txt", "mimeType": "text/plain"}}})
	if err != nil {
		t.Fatal(err)
	}
	reqs := m.requests()
	if len(reqs) != 1 {
		t.Fatalf("want 1 request, got %d", len(reqs))
	}
	var body map[string]any
	if err := json.Unmarshal(reqs[0].body, &body); err != nil {
		t.Fatal(err)
	}
	m5, s1, s2 := md5.Sum([]byte("hi")), sha1.Sum([]byte("hi")), sha256.Sum256([]byte("hi")) //nolint:gosec // test vector
	want := map[string]any{"_type": "case_artifact", "dataType": "file", "message": "m", "attachment": map[string]any{
		"name": "a.txt", "contentType": "text/plain", "size": 2.0, "id": hex.EncodeToString(s2[:]),
		"hashes": []any{hex.EncodeToString(s2[:]), hex.EncodeToString(s1[:]), hex.EncodeToString(m5[:])}}}
	if !reflect.DeepEqual(body["data"], want) {
		t.Errorf("data = %#v\nwant %#v", body["data"], want)
	}
}

func TestGA4LastCalendarWeek(t *testing.T) {
	p := catalogPkg(t, "google_analytics")
	m := newMockServer(t, map[string]any{}, 200)
	if _, err := runPackageOp(p, map[string]any{"resource": "report", "operation": "get", "propertyId": "123",
		"dateRange": "lastCalendarWeek", "metricsGA4": map[string]any{"metricValues": []any{map[string]any{"listName": "totalUsers"}}}},
		map[string]any{"access_token": "t"}, nil); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(m.requests()[0].body, &body); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	back := int(now.Weekday()+6) % 7 // days since this Monday
	mon := time.Date(now.Year(), now.Month(), now.Day()-back-7, 0, 0, 0, 0, time.UTC)
	want := []any{map[string]any{"startDate": mon.Format("2006-01-02"), "endDate": mon.AddDate(0, 0, 6).Format("2006-01-02")}}
	if !reflect.DeepEqual(body["dateRanges"], want) {
		t.Errorf("dateRanges = %v, want %v", body["dateRanges"], want)
	}
}

func TestOdooJSONRPCErrorFails(t *testing.T) {
	p := catalogPkg(t, "odoo")
	cases, err := loadCases("odoo")
	if err != nil {
		t.Fatal(err)
	}
	c := cases["opportunity.get"] // username/password: JSON-RPC
	rpcErr := map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": 200, "message": "Odoo Server Error",
		"data": map[string]any{"name": "odoo.exceptions.AccessDenied", "message": "Access Denied"}}}
	// The login answers fine; the call reports the error.
	m := newMockServer(t, nil, 200, map[string]any{"jsonrpc": "2.0", "result": 2}, rpcErr)
	params := map[string]any{"resource": "opportunity", "operation": "get"}
	for k, v := range c.Params {
		params[k] = v
	}
	_, err = runPackageOp(p, params, c.Credential, c.Input)
	if err == nil || !strings.Contains(err.Error(), "the API reported an error: Access Denied") || len(m.requests()) != 2 {
		t.Fatalf("an HTTP 200 JSON-RPC error must fail the item, got %v after %d requests", err, len(m.requests()))
	}
}

func TestUnleashedDotnetDates(t *testing.T) {
	p := catalogPkg(t, "unleashed_software")
	newMockServer(t, map[string]any{"ProductCode": "P1", "LastModifiedOn": "/Date(1586833770780)/"}, 200)
	items, err := runPackageOp(p, map[string]any{"resource": "stockOnHand", "operation": "get", "productId": "P1"},
		map[string]any{"api_id": "i", "api_key": "k"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].JSON["LastModifiedOn"] != "2020-04-14T03:09:30.780Z" || items[0].JSON["ProductCode"] != "P1" {
		t.Errorf("items = %v", items)
	}
}

func TestNocoDBAttachmentDownload(t *testing.T) {
	p := catalogPkg(t, "noco_db")
	newMockServer(t, nil, 200,
		map[string]any{"id": 5.0, "fields": map[string]any{"Docs": `[{"url":"https://f.test/a.pdf","title":"a.pdf","mimetype":"application/pdf"}]`}})
	items, err := runPackageOp(p, map[string]any{"resource": "row", "operation": "get", "projectId": "p1", "table": "t1", "id": "5",
		"downloadAttachments": true, "downloadFieldNames": "Docs"}, map[string]any{"host": "https://app.nocodb.com", "api_token": "T"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := items[0].JSON["binary"].(map[string]any)
	if len(items) != 1 || string(items[0].Binary["Docs_0"]) != "{}" || meta["Docs_0"].(map[string]any)["fileName"] != "a.pdf" {
		t.Errorf("items = %+v", items)
	}
}

func TestNotionDownloadFiles(t *testing.T) {
	p := catalogPkg(t, "notion")
	cases, err := loadCases("notion")
	if err != nil {
		t.Fatal(err)
	}
	c := cases["databasePage.get"]
	m := newMockServer(t, c.Response, 200)
	params := map[string]any{"resource": "databasePage", "operation": "get"}
	for k, v := range c.Params {
		params[k] = v
	}
	items, err := runPackageOp(p, params, c.Credential, nil)
	if err != nil {
		t.Fatal(err)
	}
	reqs := m.requests()
	if len(reqs) != 2 || reqs[1].path != "/x.pdf" || reqs[1].query.Get("X-Amz-Signature") != "s" || reqs[1].header.Get("Authorization") != "" {
		t.Fatalf("download request: %+v", reqs)
	}
	if len(items) != 1 || items[0].Binary["Files & media_0"] == nil || items[0].JSON["property_files_media"] == nil {
		t.Errorf("items = %+v", items)
	}
}

func TestSlidesThumbnailDownloadWithoutToken(t *testing.T) {
	p := catalogPkg(t, "google_slides")
	m := newMockServer(t, map[string]any{"contentUrl": "https://lh3.googleusercontent.test/thumb1"}, 200)
	items, err := runPackageOp(p, map[string]any{"resource": "page", "operation": "getThumbnail", "presentationId": "P",
		"pageObjectId": "p1", "download": true}, map[string]any{"access_token": "tok"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reqs := m.requests()
	if len(reqs) != 2 || reqs[0].header.Get("Authorization") != "Bearer tok" || reqs[1].path != "/thumb1" || reqs[1].header.Get("Authorization") != "" {
		t.Fatalf("requests: %+v", reqs)
	}
	meta, _ := items[0].JSON["binary"].(map[string]any)
	if items[0].Binary["data"] == nil || meta["data"].(map[string]any)["fileName"] != "p1.png" || items[0].JSON["contentUrl"] == nil {
		t.Errorf("item = %+v", items[0])
	}
}
