package apipkg

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// aiServer answers each request with fn(path) → (content type, body) and
// records method, path and body.
func aiServer(t *testing.T, fn func(r *http.Request, body []byte) (string, string)) *[]seenReq {
	t.Helper()
	var reqs []seenReq
	pagedServer(t, func(_ int, r *http.Request, w http.ResponseWriter) {
		b, _ := io.ReadAll(r.Body)
		reqs = append(reqs, seenReq{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			Type: r.Header.Get("Content-Type"), Body: string(b), Header: r.Header.Clone()})
		ct, body := fn(r, b)
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		fmt.Fprint(w, body)
	})
	return &reqs
}

// multipartParts decodes a multipart body into name → value, file parts
// as "file:<name>:<type>:<content>"; repeated names are joined by "|".
func multipartParts(t *testing.T, r seenReq) map[string]string {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Type)
	if err != nil {
		t.Fatalf("content type %q: %v", r.Type, err)
	}
	out := map[string]string{}
	mr := multipart.NewReader(strings.NewReader(r.Body), params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(part)
		v := string(b)
		if part.FileName() != "" {
			v = "file:" + part.FileName() + ":" + part.Header.Get("Content-Type") + ":" + v
		}
		if prev, ok := out[part.FormName()]; ok {
			v = prev + "|" + v
		}
		out[part.FormName()] = v
	}
	return out
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func binItem(props map[string][2]string) workflow.Item {
	meta := map[string]any{}
	bin := map[string][]byte{}
	for prop, v := range props {
		meta[prop] = map[string]any{"fileName": v[0], "mimeType": v[1]}
		bin[prop] = []byte("BYTES-" + prop)
	}
	return workflow.Item{JSON: map[string]any{"binary": meta}, Binary: bin}
}

var oaCred = map[string]any{"api_key": "sk"}

// Generated images (base64 in the JSON) become one item per image with
// the bytes in the chosen binary property; DALL·E URL answers stay JSON.
func TestOpenAIImageGenerateBinaryAndURLs(t *testing.T) {
	p := catalogPkg(t, "openai_api")
	reqs := aiServer(t, func(*http.Request, []byte) (string, string) {
		return "", `{"data":[{"b64_json":"` + b64("IMG-A") + `","revised_prompt":"a red cat"},{"b64_json":"` + b64("IMG-B") + `"}]}`
	})
	cfg := map[string]any{"resource": "image", "operation": "generate", "prompt": "a cat", "credential": oaCred,
		"options": map[string]any{"binaryPropertyOutput": "pic"}}
	items, err := runItems(t, p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || string(items[0].Binary["pic"]) != "IMG-A" || string(items[1].Binary["pic"]) != "IMG-B" {
		t.Fatalf("items %+v", items)
	}
	j := items[0].JSON
	if j["revised_prompt"] != "a red cat" || j["b64_json"] != nil || j["file_name"] != "data.png" || j["mime_type"] != "image/png" || j["binary_property"] != "pic" {
		t.Errorf("item json %v", j)
	}
	if want := `{"model":"gpt-image-1-mini","prompt":"a cat"}`; (*reqs)[0].Body != want {
		t.Errorf("body %s, want %s", (*reqs)[0].Body, want)
	}

	// A node from n8n before 2.2 (legacy model, dall-e-3 by default) asking for URLs.
	reqs = aiServer(t, func(*http.Request, []byte) (string, string) {
		return "", `{"data":[{"url":"https://img/1.png"}]}`
	})
	cfg = map[string]any{"resource": "image", "operation": "generate", "prompt": "a cat", "credential": oaCred,
		"legacyImageModel": true, "options": map[string]any{"returnImageUrls": true, "dalleQuality": "hd", "style": "natural"}}
	items, err = runItems(t, p, cfg)
	if err != nil || len(items) != 1 || items[0].JSON["url"] != "https://img/1.png" || len(items[0].Binary) != 0 {
		t.Fatalf("url items %v %+v", err, items)
	}
	if want := `{"model":"dall-e-3","prompt":"a cat","quality":"hd","response_format":"url","style":"natural"}`; (*reqs)[0].Body != want {
		t.Errorf("legacy body %s, want %s", (*reqs)[0].Body, want)
	}
}

// Image edit uploads every image as image[] for GPT Image (plus the mask)
// and one "image" part for DALL·E 2, whose default URL answer stays JSON.
func TestOpenAIImageEditUploads(t *testing.T) {
	p := catalogPkg(t, "openai_api")
	reqs := aiServer(t, func(*http.Request, []byte) (string, string) {
		return "", `{"data":[{"b64_json":"` + b64("OUT") + `"}]}`
	})
	item := binItem(map[string][2]string{"a": {"a.png", "image/png"}, "b": {"b.jpg", "image/jpeg"}, "m": {"m.png", "image/png"}})
	cfg := map[string]any{"resource": "image", "operation": "edit", "prompt": "hat", "credential": oaCred, "outputFormat": "jpeg",
		"images":  map[string]any{"values": []any{map[string]any{"binaryPropertyName": "a"}, map[string]any{"binaryPropertyName": "b"}}},
		"options": map[string]any{"imageMask": "m"}}
	items, err := runItems(t, p, cfg, item)
	if err != nil {
		t.Fatal(err)
	}
	parts := multipartParts(t, (*reqs)[0])
	if parts["image[]"] != "file:a.png:image/png:BYTES-a|file:b.jpg:image/jpeg:BYTES-b" || parts["mask"] != "file:m.png:image/png:BYTES-m" {
		t.Errorf("file parts %v", parts)
	}
	if parts["model"] != "gpt-image-1" || parts["output_format"] != "jpeg" || parts["output_compression"] != "100" || parts["response_format"] != "" {
		t.Errorf("fields %v", parts)
	}
	if len(items) != 1 || string(items[0].Binary["data"]) != "OUT" || items[0].JSON["file_name"] != "data.jpeg" || items[0].JSON["mime_type"] != "image/jpeg" {
		t.Errorf("edit output %+v", items)
	}

	reqs = aiServer(t, func(*http.Request, []byte) (string, string) { return "", `{"data":[{"url":"https://img/e.png"}]}` })
	cfg = map[string]any{"resource": "image", "operation": "edit", "prompt": "hat", "credential": oaCred,
		"modelId": "dall-e-2", "binaryPropertyName": "a", "size": "512x512"}
	items, err = runItems(t, p, cfg, item)
	if err != nil || len(items) != 1 || items[0].JSON["url"] != "https://img/e.png" {
		t.Fatalf("dall-e edit %v %+v", err, items)
	}
	parts = multipartParts(t, (*reqs)[0])
	if parts["image"] != "file:a.png:image/png:BYTES-a" || parts["image[]"] != "" || parts["response_format"] != "url" || parts["quality"] != "" {
		t.Errorf("dall-e parts %v", parts)
	}
}

// Speech comes back as a file in the chosen property, named after the
// requested format, typed by the response.
func TestOpenAIAudioGenerateDownload(t *testing.T) {
	p := catalogPkg(t, "openai_api")
	aiServer(t, func(*http.Request, []byte) (string, string) { return "audio/flac", "FLACBYTES" })
	cfg := map[string]any{"resource": "audio", "operation": "generate", "input": "hi", "credential": oaCred,
		"options": map[string]any{"response_format": "flac", "binaryPropertyOutput": "voice"}}
	items, err := runItems(t, p, cfg)
	if err != nil || len(items) != 1 {
		t.Fatalf("%v %+v", err, items)
	}
	j := items[0].JSON
	if string(items[0].Binary["voice"]) != "FLACBYTES" || j["file_name"] != "audio.flac" || j["mime_type"] != "audio/flac" || j["binary_property"] != "voice" {
		t.Errorf("speech item %v", j)
	}
}

// Transcription sends the item's audio file; temperature 0 is left out
// as n8n does.
func TestOpenAITranscribeUpload(t *testing.T) {
	p := catalogPkg(t, "openai_api")
	reqs := aiServer(t, func(*http.Request, []byte) (string, string) { return "", `{"text":"hello"}` })
	cfg := map[string]any{"resource": "audio", "operation": "transcribe", "credential": oaCred, "options": map[string]any{"temperature": 0}}
	items, err := runItems(t, p, cfg, binItem(map[string][2]string{"data": {"rec.wav", "audio/wav"}}))
	if err != nil || len(items) != 1 || items[0].JSON["text"] != "hello" {
		t.Fatalf("%v %+v", err, items)
	}
	parts := multipartParts(t, (*reqs)[0])
	if parts["file"] != "file:rec.wav:audio/wav:BYTES-data" || parts["model"] != "whisper-1" || len(parts) != 2 {
		t.Errorf("parts %v", parts)
	}
	_, err = runItems(t, p, cfg, workflow.Item{JSON: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), `no binary property "data"`) {
		t.Errorf("missing binary: %v", err)
	}
}

// Moderation: simplified output keeps only flagged (false included);
// nodes from n8n before 2.1 use the text-moderation models.
func TestOpenAIClassify(t *testing.T) {
	p := catalogPkg(t, "openai_api")
	reqs := aiServer(t, func(*http.Request, []byte) (string, string) {
		return "", `{"results":[{"flagged":false,"categories":{"violence":false}}]}`
	})
	cfg := map[string]any{"resource": "text", "operation": "classify", "input": "x", "simplify": true, "credential": oaCred,
		"legacyModeration": true, "options": map[string]any{"useStableModel": true}}
	items, err := runItems(t, p, cfg)
	if err != nil || len(items) != 1 || jsonOf(items[0].JSON) != `{"flagged":false}` {
		t.Fatalf("%v %+v", err, items)
	}
	if !strings.Contains((*reqs)[0].Body, `"model":"text-moderation-stable"`) {
		t.Errorf("body %s", (*reqs)[0].Body)
	}
	cfg = map[string]any{"resource": "text", "operation": "classify", "input": "x", "credential": oaCred}
	items, err = runItems(t, p, cfg)
	if err != nil || len(items) != 1 || items[0].JSON["categories"] == nil {
		t.Fatalf("full result %v %+v", err, items)
	}
}

// Image analysis sends the item's images as data URLs; Simplify returns
// the response's output entries.
func TestOpenAIImageAnalyzeBinary(t *testing.T) {
	p := catalogPkg(t, "openai_api")
	reqs := aiServer(t, func(*http.Request, []byte) (string, string) {
		return "", `{"id":"r","output":[{"type":"reasoning"},{"type":"message","content":[{"type":"output_text","text":"cats"}]}]}`
	})
	item := binItem(map[string][2]string{"a": {"a.png", "image/png"}, "b": {"b.webp", "image/webp"}})
	cfg := map[string]any{"resource": "image", "operation": "analyze", "modelId": "gpt-4.1", "inputType": "base64",
		"binaryPropertyName": "a,b", "credential": oaCred}
	items, err := runItems(t, p, cfg, item)
	if err != nil || len(items) != 2 || items[1].JSON["type"] != "message" {
		t.Fatalf("%v %+v", err, items)
	}
	body := (*reqs)[0].Body
	for _, want := range []string{`"image_url":"data:image/png;base64,` + b64("BYTES-a") + `"`, `"image_url":"data:image/webp;base64,` + b64("BYTES-b") + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s lacks %s", body, want)
		}
	}
}

// A Sora job is polled until it completes, then its content is
// downloaded onto the item; a failed job fails the item.
func TestOpenAIVideoGeneratePollAndDownload(t *testing.T) {
	p := catalogPkg(t, "openai_api")
	for i := range p.Operations {
		if p.Operations[i].Key() == "video.generate" {
			pl := *p.Operations[i].Poll
			pl.IntervalMs = 1
			p.Operations[i].Poll = &pl
		}
	}
	polls := 0
	reqs := aiServer(t, func(r *http.Request, _ []byte) (string, string) {
		switch {
		case r.Method == "POST":
			return "", `{"id":"video_1","status":"queued","error":null}`
		case strings.HasSuffix(r.URL.Path, "/content"):
			return "video/mp4", "MP4BYTES"
		}
		if polls++; polls < 2 {
			return "", `{"id":"video_1","status":"in_progress"}`
		}
		return "", `{"id":"video_1","status":"completed","seconds":"4"}`
	})
	cfg := map[string]any{"resource": "video", "operation": "generate", "modelId": "sora-2", "credential": oaCred,
		"options": map[string]any{"fileName": "clip.mp4"}}
	items, err := runItems(t, p, cfg)
	if err != nil || len(items) != 1 {
		t.Fatalf("%v %+v", err, items)
	}
	if string(items[0].Binary["data"]) != "MP4BYTES" || items[0].JSON["status"] != "completed" {
		t.Errorf("video item %+v", items[0])
	}
	meta, _ := items[0].JSON["binary"].(map[string]any)["data"].(map[string]any)
	if meta["fileName"] != "clip.mp4" || meta["mimeType"] != "video/mp4" {
		t.Errorf("binary meta %v", items[0].JSON["binary"])
	}
	var paths []string
	for _, r := range *reqs {
		paths = append(paths, r.Method+" "+r.Path)
	}
	if got := strings.Join(paths, ", "); got != "POST /v1/videos, GET /v1/videos/video_1, GET /v1/videos/video_1, GET /v1/videos/video_1/content" {
		t.Errorf("requests %s", got)
	}
	parts := multipartParts(t, (*reqs)[0])
	if parts["input_reference"] != "" || parts["seconds"] != "4" || parts["size"] != "1280x720" {
		t.Errorf("create parts %v", parts)
	}

	aiServer(t, func(r *http.Request, _ []byte) (string, string) {
		if r.Method == "POST" {
			return "", `{"id":"video_2","status":"queued"}`
		}
		return "", `{"id":"video_2","status":"failed","error":{"code":"moderation","message":"blocked by moderation"}}`
	})
	if _, err := runItems(t, p, cfg); err == nil || !strings.Contains(err.Error(), "blocked by moderation") {
		t.Errorf("failed job: %v", err)
	}
}

var antCred = map[string]any{"api_key": "sk-ant"}

// Anthropic upload from a URL fetches the file without the API key, then
// uploads it; file items carry their API url.
func TestAnthropicUploadFromURL(t *testing.T) {
	p := catalogPkg(t, "anthropic_api")
	reqs := aiServer(t, func(r *http.Request, _ []byte) (string, string) {
		if r.URL.Path == "/docs/r.pdf" {
			return "application/pdf", "PDFBYTES"
		}
		return "", `{"id":"file_7","filename":"file"}`
	})
	cfg := map[string]any{"resource": "file", "operation": "upload", "fileUrl": "https://files.example.com/docs/r.pdf", "credential": antCred}
	items, err := runItems(t, p, cfg)
	if err != nil || len(items) != 1 {
		t.Fatalf("%v %+v", err, items)
	}
	if (*reqs)[0].Header.Get("x-api-key") != "" {
		t.Error("the URL download carried the API key")
	}
	up := (*reqs)[1]
	if up.Path != "/v1/files" || up.Header.Get("anthropic-beta") != "files-api-2025-04-14" {
		t.Errorf("upload %s %v", up.Path, up.Header)
	}
	if parts := multipartParts(t, up); parts["file"] != "file:file:application/pdf:PDFBYTES" {
		t.Errorf("parts %v", parts)
	}
	if items[0].JSON["url"] != "https://api.anthropic.com/v1/files/file_7" || items[0].JSON["filename"] != "file" {
		t.Errorf("item %v", items[0].JSON)
	}
}

// Analyze: binary images go as base64 blocks typed by the binary's MIME
// type; Simplify keeps only the content blocks.
func TestAnthropicImageAnalyzeBinary(t *testing.T) {
	p := catalogPkg(t, "anthropic_api")
	reqs := aiServer(t, func(*http.Request, []byte) (string, string) {
		return "", `{"id":"msg","model":"m","content":[{"type":"text","text":"a cat"}],"usage":{}}`
	})
	cfg := map[string]any{"resource": "image", "operation": "analyze", "modelId": "claude-sonnet-4-5", "inputType": "binary", "credential": antCred}
	items, err := runItems(t, p, cfg, binItem(map[string][2]string{"data": {"c.jpg", "image/jpeg"}}))
	if err != nil || len(items) != 1 || jsonOf(items[0].JSON) != `{"content":[{"text":"a cat","type":"text"}]}` {
		t.Fatalf("%v %+v", err, items)
	}
	want := `{"max_tokens":1024,"messages":[{"content":[{"source":{"data":"` + b64("BYTES-data") +
		`","media_type":"image/jpeg","type":"base64"},"type":"image"},{"text":"What's in this image?","type":"text"}],"role":"user"}],"model":"claude-sonnet-4-5"}`
	if (*reqs)[0].Body != want {
		t.Errorf("body %s\nwant %s", (*reqs)[0].Body, want)
	}
	cfg["simplify"] = false
	if items, _ = runItems(t, p, cfg, binItem(map[string][2]string{"data": {"c.jpg", "image/jpeg"}})); len(items) != 1 || items[0].JSON["id"] != "msg" {
		t.Errorf("full response %+v", items)
	}
}
