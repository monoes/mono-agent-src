package apipkg

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// An n8n v1 Twitter DM with additionalFields.attachment uploads the named
// binary first: an image in one media/upload call, other media by
// INIT/APPEND/FINALIZE; the DM carries the returned media id.
func TestTwitterDMAttachmentUpload(t *testing.T) {
	p := catalogPkg(t, "twitter")
	data := []byte("PNGDATA")
	for _, c := range []struct {
		mime     string
		commands string
		mediaID  string
	}{
		{"image/png", "media_data", "IMG1"},
		{"video/mp4", "INIT APPEND FINALIZE", "VID1"},
	} {
		var uploads []url.Values
		reqs := recServer(t, func(r seenReq) string {
			if strings.HasSuffix(r.Path, "/media/upload.json") {
				form, _ := url.ParseQuery(r.Body)
				uploads = append(uploads, form)
				if form.Get("command") == "" {
					return `{"media_id_string":"IMG1"}`
				}
				return `{"media_id_string":"VID1"}`
			}
			return `{"data":{"dm_event_id":"9"}}`
		})
		item := workflow.Item{
			JSON:   map[string]any{"binary": map[string]any{"file": map[string]any{"mimeType": c.mime, "fileName": "f.bin"}}},
			Binary: map[string][]byte{"file": data},
		}
		cfg := map[string]any{"resource": "directMessage", "operation": "create", "user": "12", "text": "hi",
			"additionalFields": map[string]any{"attachment": "file"}, "credential": map[string]any{"access_token": "t"}}
		if _, err := runItems(t, p, cfg, item); err != nil {
			t.Fatalf("%s: %v", c.mime, err)
		}
		var cmds []string
		for _, f := range uploads {
			if f.Get("command") == "" {
				if f.Get("media_data") != base64.StdEncoding.EncodeToString(data) {
					t.Errorf("image upload %v", f)
				}
				cmds = append(cmds, "media_data")
				continue
			}
			cmds = append(cmds, f.Get("command"))
			switch f.Get("command") {
			case "INIT":
				if f.Get("total_bytes") != "7" || f.Get("media_type") != c.mime {
					t.Errorf("INIT %v", f)
				}
			case "APPEND":
				if f.Get("media_id") != "VID1" || f.Get("segment_index") != "0" || f.Get("media_data") != base64.StdEncoding.EncodeToString(data) {
					t.Errorf("APPEND %v", f)
				}
			}
		}
		if strings.Join(cmds, " ") != c.commands {
			t.Errorf("%s uploads %v", c.mime, cmds)
		}
		dm := (*reqs)[len(*reqs)-1]
		if dm.Path != "/2/dm_conversations/with/12/messages" || dm.Body != `{"attachments":[{"media_id":"`+c.mediaID+`"}],"text":"hi"}` {
			t.Errorf("%s DM %s %s", c.mime, dm.Path, dm.Body)
		}
	}
	// No attachment: no upload, no attachments key.
	reqs := recServer(t, func(seenReq) string { return `{"data":{}}` })
	cfg := map[string]any{"resource": "directMessage", "operation": "create", "user": "12", "text": "hi", "credential": map[string]any{"access_token": "t"}}
	if _, err := runItems(t, p, cfg); err != nil || len(*reqs) != 1 || (*reqs)[0].Body != `{"text":"hi"}` {
		t.Errorf("plain DM: %v %v", err, *reqs)
	}
}

// Dropcontact: a poll reply that is not ready yet (success false, no
// error) keeps waiting; a reported error fails, as does success false on
// the submit or on Fetch Request.
func TestDropcontactPendingVersusError(t *testing.T) {
	p := catalogPkg(t, "dropcontact")
	op, _ := p.Op("contact", "enrich")
	fast := *op
	pl := *op.Poll
	pl.IntervalMs = 1
	fast.Poll = &pl
	p.Operations[0] = fast // the enrich op is listed first
	polls := 0
	recServer(t, func(r seenReq) string {
		if r.Method == "POST" {
			return `{"request_id":"r1","success":true}`
		}
		if polls++; polls < 3 {
			return `{"error":false,"success":false,"reason":"Request not ready yet"}`
		}
		return `{"error":false,"success":true,"data":[{"email":[{"email":"a@b.co"}]}]}`
	})
	cfg := map[string]any{"resource": "contact", "operation": "enrich", "email": "a@b.co", "credential": map[string]any{"api_key": "k"}}
	items, err := runItems(t, p, cfg)
	if err != nil || polls != 3 || len(items) != 1 {
		t.Fatalf("pending then done: %v, %d polls, %v", err, polls, items)
	}
	recServer(t, func(r seenReq) string {
		if r.Method == "POST" {
			return `{"request_id":"r1","success":true}`
		}
		return `{"error":true,"success":false,"reason":"Your request is invalid"}`
	})
	if _, err := runItems(t, p, cfg); err == nil || !strings.Contains(err.Error(), "Your request is invalid") {
		t.Errorf("poll error: %v", err)
	}
	recServer(t, func(seenReq) string { return `{"success":false,"reason":"No credits"}` })
	if _, err := runItems(t, p, cfg); err == nil || !strings.Contains(err.Error(), "No credits") {
		t.Errorf("submit error: %v", err)
	}
	cfg = map[string]any{"resource": "contact", "operation": "fetchRequest", "requestId": "r1", "credential": map[string]any{"api_key": "k"}}
	if _, err := runItems(t, p, cfg); err == nil || !strings.Contains(err.Error(), "No credits") {
		t.Errorf("fetch error: %v", err)
	}
}

// Notion's simplified property keys fold non-ASCII letters for nodes
// imported from n8n before version 3 (keyCase ascii via the package's
// "<3" version default) and keep them otherwise.
func TestNotionKeyCase(t *testing.T) {
	p := catalogPkg(t, "notion")
	if p.N8nVersionDefaults["<3"]["keyCase"] != "ascii" {
		t.Fatalf("version defaults %v", p.N8nVersionDefaults)
	}
	recServer(t, func(seenReq) string {
		return `{"results":[{"id":"p1","object":"page","url":"u","properties":{"Prénom":{"type":"rich_text","rich_text":[{"plain_text":"Ada"}]}}}],"has_more":false}`
	})
	for keyCase, want := range map[string]string{"": "property_prénom", "snake": "property_prénom", "ascii": "property_pr_nom"} {
		cfg := map[string]any{"resource": "page", "operation": "search", "returnAll": true, "simple": true,
			"credential": map[string]any{"access_token": "t"}}
		if keyCase != "" {
			cfg["keyCase"] = keyCase
		}
		items, err := runItems(t, p, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].JSON[want] != "Ada" {
			t.Errorf("keyCase %q: %v", keyCase, items)
		}
	}
}

// n8n v1 tweet attachments are binary property names: each is uploaded
// (an image in one call, other media by INIT/APPEND/FINALIZE in 5 MiB
// segments) and the
// tweet carries every media id in order. As media IDs (v2) they pass as is.
func TestTwitterTweetAttachmentUploads(t *testing.T) {
	p := catalogPkg(t, "twitter")
	n := 0
	var forms []url.Values
	reqs := recServer(t, func(r seenReq) string {
		if strings.HasSuffix(r.Path, "/media/upload.json") {
			f, _ := url.ParseQuery(r.Body)
			forms = append(forms, f)
			if f.Get("command") == "" {
				n++
				return `{"media_id_string":"IMG` + string(rune('0'+n)) + `"}`
			}
			return `{"media_id_string":"VID"}`
		}
		return `{"data":{"id":"9"}}`
	})
	vid := []byte(strings.Repeat("v", 5242880) + "end")
	item := workflow.Item{
		JSON: map[string]any{"binary": map[string]any{
			"pic":  map[string]any{"mimeType": "image/png", "fileName": "a.png"},
			"vid":  map[string]any{"mimeType": "video/mp4", "fileName": "b.mp4"},
			"pic2": map[string]any{"mimeType": "image/jpeg", "fileName": "c.jpg"}}},
		Binary: map[string][]byte{"pic": []byte("p1"), "vid": vid, "pic2": []byte("p2")},
	}
	cfg := map[string]any{"resource": "tweet", "operation": "create", "text": "hi", "attachmentsAre": "binaryProperties",
		"additionalFields": map[string]any{"attachments": "pic, vid,pic2"}, "credential": map[string]any{"access_token": "t"}}
	if _, err := runItems(t, p, cfg, item); err != nil {
		t.Fatal(err)
	}
	var cmds []string
	for _, f := range forms {
		c := f.Get("command")
		if c == "" {
			c = "image"
		}
		if c == "APPEND" {
			seg := f.Get("segment_index")
			want := vid[:5242880]
			if seg == "1" {
				want = vid[5242880:]
			}
			if f.Get("media_id") != "VID" || f.Get("name") != "b.mp4" || f.Get("media_data") != base64.StdEncoding.EncodeToString(want) {
				t.Errorf("APPEND segment %s: media_id %q name %q, %d base64 bytes", seg, f.Get("media_id"), f.Get("name"), len(f.Get("media_data")))
			}
			c += seg
		}
		if c == "INIT" && f.Get("total_bytes") != "5242883" {
			t.Errorf("INIT %v", f)
		}
		cmds = append(cmds, c)
	}
	if strings.Join(cmds, " ") != "image image INIT APPEND0 APPEND1 FINALIZE" {
		t.Errorf("uploads %v", cmds)
	}
	tw := (*reqs)[len(*reqs)-1]
	if tw.Path != "/2/tweets" || tw.Body != `{"media":{"media_ids":["IMG1","VID","IMG2"]},"text":"hi"}` {
		t.Errorf("tweet %s %s", tw.Path, tw.Body)
	}

	reqs = recServer(t, func(seenReq) string { return `{"data":{"id":"9"}}` })
	cfg = map[string]any{"resource": "tweet", "operation": "create", "text": "hi",
		"additionalFields": map[string]any{"attachments": "55"}, "credential": map[string]any{"access_token": "t"}}
	if _, err := runItems(t, p, cfg); err != nil || len(*reqs) != 1 || (*reqs)[0].Body != `{"media":{"media_ids":["55"]},"text":"hi"}` {
		t.Errorf("media id tweet: %v %v", err, *reqs)
	}
}
