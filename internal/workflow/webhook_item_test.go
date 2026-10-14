package workflow

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// itemHook serves one webhook without a runner and returns what it fired.
func itemHook(t *testing.T, reg *WebhookRegistration) (*WebhookServer, *[]Item) {
	t.Helper()
	s := NewWebhookServer(":0", zerolog.Nop())
	var fired []Item
	if reg.Path == "" {
		reg.Path = "hook"
	}
	if reg.Method == "" {
		reg.Method = "ANY"
	}
	reg.TriggerFn = func(items []Item) { fired = append(fired, items...) }
	if err := s.Register(reg); err != nil {
		t.Fatal(err)
	}
	return s, &fired
}

func send(s *WebhookServer, method, target, contentType string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func firedJSON(t *testing.T, rec *httptest.ResponseRecorder, fired *[]Item) map[string]interface{} {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if len(*fired) != 1 {
		t.Fatalf("fired %d items, want 1", len(*fired))
	}
	return (*fired)[0].JSON
}

// A JSON object body keeps its fields at the top level (as before) and the
// item also carries body, query, headers, method and path.
func TestWebhookItem_JSONObjectShape(t *testing.T) {
	s, fired := itemHook(t, &WebhookRegistration{})
	rec := send(s, http.MethodPost, "/webhook/hook?a=1&b=x&b=y", "application/json", []byte(`{"name":"ada","n":2}`))
	got := firedJSON(t, rec, fired)

	if got["name"] != "ada" || got["n"] != float64(2) {
		t.Errorf("top-level body fields = %v, want name and n", got)
	}
	body, _ := got["body"].(map[string]interface{})
	if body["name"] != "ada" {
		t.Errorf("body = %v, want the full body", got["body"])
	}
	wantQuery := map[string]interface{}{"a": "1", "b": []interface{}{"x", "y"}}
	if !reflect.DeepEqual(got["query"], wantQuery) {
		t.Errorf("query = %v, want %v", got["query"], wantQuery)
	}
	headers, _ := got["headers"].(map[string]interface{})
	if headers["content-type"] != "application/json" {
		t.Errorf("headers = %v, want lower-cased content-type", headers)
	}
	if got["method"] != "POST" || got["path"] != "hook" {
		t.Errorf("method/path = %v/%v", got["method"], got["path"])
	}
}

// A body field named like a request key wins at the top level; "body"
// always holds the whole body.
func TestWebhookItem_BodyFieldCollisions(t *testing.T) {
	s, fired := itemHook(t, &WebhookRegistration{})
	rec := send(s, http.MethodPost, "/webhook/hook?q=1", "", []byte(`{"query":"mine","method":"m","body":"inner"}`))
	got := firedJSON(t, rec, fired)

	if got["query"] != "mine" || got["method"] != "m" {
		t.Errorf("collisions: query=%v method=%v, want the body's values", got["query"], got["method"])
	}
	body, _ := got["body"].(map[string]interface{})
	if body["body"] != "inner" || body["query"] != "mine" {
		t.Errorf("body = %v, want the whole body", got["body"])
	}
}

// A GET gets its query string (it used to get an empty item).
func TestWebhookItem_GETQuery(t *testing.T) {
	s, fired := itemHook(t, &WebhookRegistration{Method: "GET"})
	rec := send(s, http.MethodGet, "/webhook/hook?token=abc", "", nil)
	got := firedJSON(t, rec, fired)
	if q, _ := got["query"].(map[string]interface{}); q["token"] != "abc" {
		t.Errorf("query = %v, want token=abc", got["query"])
	}
	if _, top := got["token"]; top {
		t.Error("query fields must not be copied to the top level")
	}
}

func TestWebhookItem_FormURLEncoded(t *testing.T) {
	s, fired := itemHook(t, &WebhookRegistration{})
	rec := send(s, http.MethodPost, "/webhook/hook", "application/x-www-form-urlencoded", []byte("a=1&tag=x&tag=y"))
	got := firedJSON(t, rec, fired)
	if got["a"] != "1" {
		t.Errorf("form field a = %v, want 1 at the top level", got["a"])
	}
	body, _ := got["body"].(map[string]interface{})
	if !reflect.DeepEqual(body["tag"], []interface{}{"x", "y"}) {
		t.Errorf("body.tag = %v, want [x y]", body["tag"])
	}
}

func TestWebhookItem_TextBody(t *testing.T) {
	s, fired := itemHook(t, &WebhookRegistration{})
	rec := send(s, http.MethodPost, "/webhook/hook", "text/plain; charset=utf-8", []byte("hello there"))
	got := firedJSON(t, rec, fired)
	if got["body"] != "hello there" {
		t.Errorf("body = %v, want the text", got["body"])
	}
}

func TestWebhookItem_JSONArray(t *testing.T) {
	s, fired := itemHook(t, &WebhookRegistration{})
	rec := send(s, http.MethodPost, "/webhook/hook", "application/json", []byte(`[{"id":1},{"id":2}]`))
	got := firedJSON(t, rec, fired)
	list, ok := got["body"].([]interface{})
	if !ok || len(list) != 2 {
		t.Errorf("body = %#v, want the list", got["body"])
	}
}

func TestWebhookItem_InvalidJSONStill400(t *testing.T) {
	s, fired := itemHook(t, &WebhookRegistration{})
	rec := send(s, http.MethodPost, "/webhook/hook", "application/json", []byte(`{nope`))
	if rec.Code != http.StatusBadRequest || len(*fired) != 0 {
		t.Fatalf("status = %d fired = %d, want 400 and nothing fired", rec.Code, len(*fired))
	}
}

func TestWebhookItem_MultipartFieldsAndFiles(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("title", "report")
	fw, _ := mw.CreateFormFile("doc", "r.txt")
	_, _ = fw.Write([]byte("file-bytes"))
	_ = mw.Close()

	s, fired := itemHook(t, &WebhookRegistration{})
	rec := send(s, http.MethodPost, "/webhook/hook", mw.FormDataContentType(), buf.Bytes())
	got := firedJSON(t, rec, fired)

	if got["title"] != "report" {
		t.Errorf("title = %v, want report", got["title"])
	}
	bin, _ := got["binary"].(map[string]interface{})
	doc, _ := bin["doc"].(map[string]interface{})
	if doc["file_name"] != "r.txt" || doc["file_size"] != len("file-bytes") {
		t.Errorf("binary.doc = %v", doc)
	}
	if doc["data"] != base64.StdEncoding.EncodeToString([]byte("file-bytes")) {
		t.Errorf("binary.doc.data = %v, want base64 of the file", doc["data"])
	}
	if string((*fired)[0].Binary["doc"]) != "file-bytes" {
		t.Errorf("Item.Binary[doc] = %q", (*fired)[0].Binary["doc"])
	}
}

// The size cap covers multipart files too, and max_body_bytes raises it.
func TestWebhookItem_BodyCap(t *testing.T) {
	big := []byte(`{"x":"` + strings.Repeat("a", 2<<20) + `"}`)
	s, _ := itemHook(t, &WebhookRegistration{})
	if rec := send(s, http.MethodPost, "/webhook/hook", "application/json", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("default cap: status = %d, want 413", rec.Code)
	}
	s, fired := itemHook(t, &WebhookRegistration{MaxBodyBytes: 4 << 20})
	rec := send(s, http.MethodPost, "/webhook/hook", "application/json", big)
	firedJSON(t, rec, fired)
}

// Credential headers stay out of the item (trigger data is stored as is).
func TestWebhookItem_CredentialHeadersLeftOut(t *testing.T) {
	s, fired := itemHook(t, &WebhookRegistration{AuthHeader: "X-Webhook-Secret", AuthToken: "s3cr3t"})
	req := httptest.NewRequest(http.MethodPost, "/webhook/hook", strings.NewReader(`{}`))
	req.Header.Set("X-Webhook-Secret", "s3cr3t")
	req.Header.Set("X-Other", "kept")
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("Cookie", "sid=1")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	got := firedJSON(t, rec, fired)
	headers, _ := got["headers"].(map[string]interface{})
	for _, h := range []string{"x-webhook-secret", "authorization", "cookie"} {
		if _, leaked := headers[h]; leaked {
			t.Errorf("credential header %s was put on the item", h)
		}
	}
	if headers["x-other"] != "kept" {
		t.Errorf("headers = %v, want x-other kept", headers)
	}
}
