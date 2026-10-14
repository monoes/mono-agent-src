package apipkg

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// Outlook message.get with downloadAttachments lists the message's
// attachments, downloads each onto attachment_<n>, and getMimeContent adds
// the .eml source, as n8n does.
func TestOutlookMessageAttachmentsAndMime(t *testing.T) {
	p := catalogPkg(t, "microsoft_outlook")
	seen := pagedServer(t, func(_ int, r *http.Request, w http.ResponseWriter) {
		switch r.URL.Path {
		case "/v1.0/me/messages/M1":
			fmt.Fprint(w, `{"id":"M1","subject":"s","hasAttachments":true}`)
		case "/v1.0/me/messages/M1/attachments":
			fmt.Fprint(w, `{"value":[{"id":"A1","name":"a.txt","contentType":"text/plain"},{"id":"A2","name":"b.pdf","contentType":"application/pdf"}]}`)
		case "/v1.0/me/messages/M1/attachments/A1/$value":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "hello")
		case "/v1.0/me/messages/M1/attachments/A2/$value":
			fmt.Fprint(w, "%PDF")
		case "/v1.0/me/messages/M1/$value":
			w.Header().Set("Content-Type", "message/rfc822")
			fmt.Fprint(w, "Subject: s\r\n\r\nbody")
		default:
			http.Error(w, r.URL.Path, http.StatusNotFound)
		}
	})
	params := map[string]any{"resource": "message", "operation": "get", "messageId": "M1",
		"options": map[string]any{"downloadAttachments": true, "attachmentsPrefix": "file_",
			"getMimeContent": map[string]any{"values": map[string]any{"binaryPropertyName": "mime"}}}}
	items, err := runPackageOp(p, params, graphCred, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items %v", items)
	}
	it := items[0]
	if string(it.Binary["file_0"]) != "hello" || string(it.Binary["file_1"]) != "%PDF" || !strings.HasPrefix(string(it.Binary["mime"]), "Subject") {
		t.Fatalf("binary %v", it.Binary)
	}
	meta, _ := it.JSON["binary"].(map[string]any)
	if m, _ := meta["file_1"].(map[string]any); m["fileName"] != "b.pdf" || m["mimeType"] != "application/pdf" {
		t.Errorf("file_1 meta %v", meta["file_1"])
	}
	if m, _ := meta["mime"].(map[string]any); m["fileName"] != "M1.eml" || m["mimeType"] != "message/rfc822" {
		t.Errorf("mime meta %v", meta["mime"])
	}
	if it.JSON["subject"] != "s" || it.JSON["attachments"] != nil {
		t.Errorf("simple output %v", it.JSON)
	}
	if q := (*seen)[1].URL.Query().Get("$select"); q != "id,name,contentType" {
		t.Errorf("attachment list $select = %q", q)
	}

	// Off by default: one request, no files.
	*seen = nil
	delete(params, "options")
	if items, err = runPackageOp(p, params, graphCred, nil); err != nil || len(*seen) != 1 || len(items[0].Binary) != 0 {
		t.Fatalf("no options: %d requests, %v", len(*seen), err)
	}
}

// folder.getAll with includeChildFolders adds every folder below the
// listed ones, asking only folders that have children.
func TestOutlookFolderIncludeChildFolders(t *testing.T) {
	p := catalogPkg(t, "microsoft_outlook")
	seen := pagedServer(t, func(_ int, r *http.Request, w http.ResponseWriter) {
		switch r.URL.Path {
		case "/v1.0/me/mailFolders":
			fmt.Fprint(w, `{"value":[{"id":"inbox","childFolderCount":1},{"id":"sent","childFolderCount":0}]}`)
		case "/v1.0/me/mailFolders/inbox/childFolders":
			if r.URL.Query().Get("skip") == "" {
				fmt.Fprint(w, `{"value":[{"id":"a","childFolderCount":1}],"@odata.nextLink":"https://graph.microsoft.com/v1.0/me/mailFolders/inbox/childFolders?skip=1"}`)
				return
			}
			fmt.Fprint(w, `{"value":[{"id":"b","childFolderCount":0}]}`)
		case "/v1.0/me/mailFolders/a/childFolders":
			fmt.Fprint(w, `{"value":[{"id":"a1"}]}`)
		default:
			http.Error(w, r.URL.Path, http.StatusNotFound)
		}
	})
	items, err := runPackageOp(p, map[string]any{"resource": "folder", "operation": "getAll", "returnAll": true,
		"options": map[string]any{"includeChildFolders": true}}, graphCred, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []any
	for _, it := range items {
		ids = append(ids, it.JSON["id"])
	}
	if want := []any{"inbox", "sent", "a", "b", "a1"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids %v, want %v", ids, want)
	}
	if len(*seen) != 4 {
		t.Errorf("%d requests, want 4 (sent has no children)", len(*seen))
	}
}
