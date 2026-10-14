package apipkg

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func catalogPkg(t *testing.T, id string) *Package {
	t.Helper()
	b, err := catalogFS.ReadFile("catalog/" + id + ".json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(b, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var graphCred = map[string]any{"access_token": "t"}

func TestExcelLookupReturnAllMatches(t *testing.T) {
	for _, id := range []string{"microsoft_excel", "microsoft_excel_share_point"} {
		p := catalogPkg(t, id)
		pagedServer(t, func(n int, _ *http.Request, w http.ResponseWriter) {
			if n%2 == 1 { // the columns request
				fmt.Fprint(w, `{"value":[{"name":"name","index":0},{"name":"team","index":1}]}`)
				return
			}
			fmt.Fprint(w, `{"value":[{"values":[["ada","x"]]},{"values":[["bob","y"]]},{"values":[["cy","x"]]}]}`)
		})
		params := map[string]any{"resource": "table", "operation": "lookup", "workbook": "W", "worksheet": "S", "table": "T",
			"site": "s", "library": "l", "lookupColumn": "team", "lookupValue": "x"}
		items, err := runPackageOp(p, params, graphCred, nil)
		if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0].JSON, map[string]any{"name": "ada", "team": "x"}) {
			t.Fatalf("%s first match: %v, %v", id, items, err)
		}
		params["options"] = map[string]any{"returnAllMatches": true}
		items, err = runPackageOp(p, params, graphCred, nil)
		if err != nil || len(items) != 2 || items[1].JSON["name"] != "cy" {
			t.Fatalf("%s returnAllMatches: %v, %v", id, items, err)
		}
	}
}

func TestExcelRawAppendManyRows(t *testing.T) {
	for _, id := range []string{"microsoft_excel", "microsoft_excel_share_point"} {
		p := catalogPkg(t, id)
		var path string
		var body map[string]any
		pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
			if n == 1 {
				fmt.Fprint(w, `{"address":"Sheet1!B1:D3","values":[["a","b","c"],["1","2","3"],["4","5","6"]]}`)
				return
			}
			path, _ = url.PathUnescape(r.URL.EscapedPath())
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			fmt.Fprint(w, `{"values":[]}`)
		})
		params := map[string]any{"resource": "worksheet", "operation": "append", "workbook": "W", "worksheet": "Sheet1",
			"site": "s", "library": "l", "dataMode": "raw", "data": `[["x","y"],["z","w"],["u","v"]]`,
			"options": map[string]any{"rawData": true}}
		if _, err := runPackageOp(p, params, graphCred, nil); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if !strings.HasSuffix(path, "/range(address='B4:C6')") {
			t.Errorf("%s raw append range: %s", id, path)
		}
		want := []any{[]any{"x", "y"}, []any{"z", "w"}, []any{"u", "v"}}
		if !reflect.DeepEqual(body["values"], want) {
			t.Errorf("%s raw append body: %v", id, body)
		}
	}
}

func TestMicrosoftScopeGroupsInCatalog(t *testing.T) {
	cases := map[string]map[string]string{ // package → op → effective scope group
		"microsoft_outlook":        {"message.send": "mail", "event.create": "calendar", "calendar.getAll": "calendar", "contact.create": "contacts"},
		"microsoft_to_do":          {"task.create": "tasks", "list.getAll": "tasks"},
		"microsoft_entra":          {"user.create": "directory", "group.getAll": "directory"},
		"microsoft_graph_security": {"secureScore.getAll": "security"},
		"microsoft_teams":          {"chat.create": "teams", "channel.create": "teams_admin", "channelMessage.getAll": "teams_admin", "task.create": "tasks", "onlineMeeting.create": "teams"},
	}
	for id, ops := range cases {
		p := catalogPkg(t, id)
		var umbrella *Credential
		for i := range p.Credentials {
			if p.Credentials[i].ReusePlatform == "microsoft" {
				umbrella = &p.Credentials[i]
			}
		}
		for key, want := range ops {
			op, ok := opByKey(p, key)
			if !ok {
				t.Fatalf("%s has no %s", id, key)
			}
			got := op.ScopeGroup
			if got == "" && umbrella != nil {
				got = umbrella.ScopeGroup
			}
			if got != want {
				t.Errorf("%s %s scope group = %q, want %q", id, key, got, want)
			}
		}
	}
}

func TestUploadSessionChunks(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"upload","title":"Upload","method":"PUT","path":"/items/{{ p.name }}/content",
	  "fields":[{"key":"name","label":"Name","type":"text"},{"key":"binaryPropertyName","label":"Bin","type":"text","default":"data"}],
	  "binary":{"upload_field":"binaryPropertyName","upload_session":{"min_size":4,"chunk_size":3,
	    "create":{"method":"POST","path":"/items/{{ p.name }}/createUploadSession","body":{"item":{"name":"{{ p.name }}"}}}}}}`))
	type seenReq struct{ method, path, auth, rng, body string }
	var reqs []seenReq
	var uploadBase string
	pagedServer(t, func(_ int, r *http.Request, w http.ResponseWriter) {
		b, _ := io.ReadAll(r.Body)
		reqs = append(reqs, seenReq{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Range"), string(b)})
		switch {
		case strings.HasSuffix(r.URL.Path, "/createUploadSession"):
			fmt.Fprintf(w, `{"uploadUrl":%q}`, uploadBase+"/up/session-1?sig=x")
		case strings.HasSuffix(r.URL.Path, "/content"):
			fmt.Fprint(w, `{"id":"small"}`)
		default:
			if strings.HasSuffix(r.Header.Get("Content-Range"), "9/10") {
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, `{"id":"F1","size":10}`)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"nextExpectedRanges":["x-"]}`)
		}
	})
	uploadBase = "https://upload.demo.test"

	in := map[string]any{"data": "MDEyMzQ1Njc4OQ=="} // "0123456789"
	items, err := runPackageOp(p, map[string]any{"operation": "upload", "name": "f.bin"}, cred, in)
	if err != nil || len(items) != 1 || items[0].JSON["id"] != "F1" {
		t.Fatalf("session upload: %v, %v", items, err)
	}
	want := []seenReq{
		{"POST", "/v1/items/f.bin/createUploadSession", "Bearer T", "", `{"item":{"name":"f.bin"}}`},
		{"PUT", "/up/session-1", "", "bytes 0-2/10", "012"},
		{"PUT", "/up/session-1", "", "bytes 3-5/10", "345"},
		{"PUT", "/up/session-1", "", "bytes 6-8/10", "678"},
		{"PUT", "/up/session-1", "", "bytes 9-9/10", "9"},
	}
	if !reflect.DeepEqual(reqs, want) {
		t.Errorf("requests:\n got %+v\nwant %+v", reqs, want)
	}

	// At or below min_size: one plain request, as before.
	reqs = nil
	items, err = runPackageOp(p, map[string]any{"operation": "upload", "name": "s.bin"}, cred, map[string]any{"data": "YWJj"})
	if err != nil || len(reqs) != 1 || reqs[0].path != "/v1/items/s.bin/content" || reqs[0].body != "abc" || items[0].JSON["id"] != "small" {
		t.Errorf("small upload: %+v, %v, %v", reqs, items, err)
	}
}

func TestOneDriveLargeUploadUsesSession(t *testing.T) {
	p := catalogPkg(t, "microsoft_one_drive")
	var paths []string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if n == 1 {
			fmt.Fprint(w, `{"uploadUrl":"https://up.example.test/s"}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":"big"}`)
	})
	big := strings.Repeat("a", defaultSessionMinSize+1)
	it := map[string]any{"data": base64.StdEncoding.EncodeToString([]byte(big))}
	items, err := runPackageOp(p, map[string]any{"resource": "file", "operation": "upload", "parentId": "P", "fileName": "big.bin",
		"binaryData": true}, graphCred, it)
	if err != nil || len(items) != 1 || items[0].JSON["id"] != "big" {
		t.Fatalf("large upload: %v, %v", items, err)
	}
	if len(paths) != 2 || paths[0] != "POST /v1.0/me/drive/items/P:/big.bin:/createUploadSession" || paths[1] != "PUT /s" {
		t.Errorf("requests: %v", paths)
	}
}

// Teams online meetings accept n8n's url-mode locator: the join URL is
// looked up first (quotes doubled for OData), then the meeting is used by id.
func TestTeamsMeetingByJoinURL(t *testing.T) {
	p := catalogPkg(t, "microsoft_teams")
	join := "https://teams.microsoft.com/l/meetup-join/19%3ameeting_x'y"
	for _, op := range []string{"get", "deleteMeeting", "update"} {
		seen := pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
			if n == 1 {
				fmt.Fprint(w, `{"value":[{"id":"MSo1"}]}`)
				return
			}
			fmt.Fprint(w, `{"id":"MSo1"}`)
		})
		params := map[string]any{"resource": "onlineMeeting", "operation": op,
			"meetingId":    map[string]any{"__rl": true, "mode": "url", "value": join},
			"updateFields": map[string]any{"subject": "s"}}
		if _, err := runPackageOp(p, params, graphCred, nil); err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		reqs := *seen
		if len(reqs) != 2 || reqs[0].URL.Path != "/v1.0/me/onlineMeetings" ||
			reqs[0].URL.Query().Get("$filter") != "JoinWebUrl eq '"+strings.ReplaceAll(join, "'", "''")+"'" ||
			reqs[1].URL.Path != "/v1.0/me/onlineMeetings/MSo1" {
			var got []string
			for _, r := range reqs {
				got = append(got, r.URL.String())
			}
			t.Errorf("%s: requests %v", op, got)
		}
	}
}

// Excel worksheet.update with updateAll rewrites the used range with every
// matching row changed and returns only those rows.
func TestExcelUpdateAll(t *testing.T) {
	for _, id := range []string{"microsoft_excel", "microsoft_excel_share_point"} {
		p := catalogPkg(t, id)
		var sent map[string]any
		var patched string
		pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `{"address":"Sheet1!A1:C4","values":[["name","team","n"],["ada","x",1],["bob","y",2],["cy","x",3]]}`)
				return
			}
			patched = r.URL.EscapedPath()
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &sent)
			fmt.Fprint(w, `{"values":[["name","team","n"],["ada","x","9"],["bob","y",2],["cy","x","9"]]}`)
		})
		params := map[string]any{"resource": "worksheet", "operation": "update", "workbook": "W", "worksheet": "S",
			"site": "s", "library": "l", "dataMode": "define", "columnToMatchOn": "team", "valueToMatchOn": "x",
			"fieldsUi": map[string]any{"values": []any{map[string]any{"column": "n", "fieldValue": "9"}}},
			"options":  map[string]any{"updateAll": true}}
		items, err := runPackageOp(p, params, graphCred, nil)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		want := []any{[]any{"name", "team", "n"}, []any{nil, nil, "9"}, []any{"bob", "y", 2.0}, []any{nil, nil, "9"}}
		if !reflect.DeepEqual(sent["values"], want) {
			t.Errorf("%s values %v", id, sent["values"])
		}
		if !strings.HasSuffix(patched, "/range(address='A1:C4')") {
			t.Errorf("%s path %s", id, patched)
		}
		if len(items) != 2 || items[0].JSON["name"] != "ada" || items[1].JSON["name"] != "cy" || items[1].JSON["n"] != "9" {
			t.Errorf("%s items %v", id, items)
		}
		params["dataMode"], params["options"] = "autoMap", map[string]any{"updateAll": true}
		delete(params, "fieldsUi")
		if _, err := runPackageOp(p, params, graphCred, map[string]any{"team": "x", "n": 7}); err != nil {
			t.Fatalf("%s autoMap: %v", id, err)
		}
		want = []any{[]any{"name", "team", "n"}, []any{nil, "x", 7.0}, []any{"bob", "y", 2.0}, []any{nil, "x", 7.0}}
		if !reflect.DeepEqual(sent["values"], want) {
			t.Errorf("%s autoMap values %v", id, sent["values"])
		}
	}
}
