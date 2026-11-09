package action

import (
	"context"
	"encoding/base64"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/fsconfine"
)

func TestPageScript(t *testing.T) {
	page := &extPage{eval: func(js string) (interface{}, error) {
		if !strings.Contains(js, "(function(args){\nreturn args.n * 2;\n})({\"n\":21})") {
			t.Fatalf("script not wrapped as expected: %s", js)
		}
		return jsOut(map[string]interface{}{"doubled": 42}), nil
	}}
	ae := newExtExecutor(t, page)
	wantFail(t, runExt(t, ae, StepDef{ID: "p", Type: "page_script", Script: "x.js"}), "needs an automation package")

	ae.SetPackage(&extPkg{id: "site", scripts: map[string]string{"double.js": "return args.n * 2;", "blank.js": "  "}})
	ae.SetVariable("num", 21)
	res := runExt(t, ae, StepDef{ID: "p", Type: "page_script", Script: "double.js", Inputs: map[string]interface{}{"n": "{{num}}"}, VariableName: "out"})
	wantOK(t, res)
	if m, _ := getVar(ae, "out").(map[string]interface{}); m["doubled"] != 42.0 {
		t.Fatalf("out = %v", getVar(ae, "out"))
	}
	wantFail(t, runExt(t, ae, StepDef{ID: "p", Type: "page_script", Script: "nope.js"}), "not found")
	wantFail(t, runExt(t, ae, StepDef{ID: "p", Type: "page_script", Script: "blank.js"}), "is empty")
	wantFail(t, runExt(t, ae, StepDef{ID: "p", Type: "page_script"}), "no script named")
}

func TestPageScriptEvalErrorAndTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	page := &extPage{eval: func(js string) (interface{}, error) {
		if strings.Contains(js, "slow") {
			<-block
		}
		return nil, context.DeadlineExceeded
	}}
	ae := newExtExecutor(t, page)
	ae.SetPackage(&extPkg{id: "site", scripts: map[string]string{"fail.js": "throw 1", "slow.js": "// slow"}})
	wantFail(t, runExt(t, ae, StepDef{ID: "p", Type: "page_script", Script: "fail.js"}), "deadline exceeded")
	start := time.Now()
	wantFail(t, runExt(t, ae, StepDef{ID: "p", Type: "page_script", Script: "slow.js", Timeout: 0.2}), "timed out")
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout not honoured")
	}
}

func TestEvalJSOnPlainPage(t *testing.T) {
	// No page → error; a non-string driver value passes through untouched.
	ae := newExtExecutor(t, nil)
	if _, err := ae.evalJS(context.Background(), "1", time.Second); err == nil {
		t.Fatal("want error without a page")
	}
	ae = newExtExecutor(t, &extPage{eval: func(string) (interface{}, error) { return 7.0, nil }})
	if v, err := ae.evalJS(context.Background(), "7", time.Second); err != nil || v != 7.0 {
		t.Fatalf("v=%v err=%v", v, err)
	}
	ae = newExtExecutor(t, &extPage{eval: func(string) (interface{}, error) { return "{broken", nil }})
	if _, err := ae.evalJS(context.Background(), "x", time.Second); err == nil {
		t.Fatal("want error for a non-JSON string result")
	}
}

func fetchPage(t *testing.T, finalURL string) *extPage {
	return &extPage{url: "https://api.site.test/home", eval: func(js string) (interface{}, error) {
		if !strings.Contains(js, `credentials":"include"`) {
			t.Fatalf("fetch must include credentials: %s", js)
		}
		return jsOut(map[string]interface{}{"status": 200, "headers": map[string]interface{}{"content-type": "application/json"},
			"body": map[string]interface{}{"ok": true}, "url": finalURL}), nil
	}}
}

func TestHTTPFetchInPage(t *testing.T) {
	page := fetchPage(t, "https://api.site.test/v1/me")
	ae := newExtExecutor(t, page)
	ae.SetPackage(&extPkg{id: "site", domains: []string{"*.site.test"}})
	res := runExt(t, ae, StepDef{ID: "f", Type: "http_fetch_in_page", URL: "/v1/me", Method: "post", VariableName: "resp",
		Inputs: map[string]interface{}{"headers": map[string]interface{}{"X-Test": "1"}, "body": map[string]interface{}{"q": "x"}}})
	wantOK(t, res)
	js := page.lastEval()
	for _, want := range []string{`"https://api.site.test/v1/me"`, `"method":"POST"`, `"Content-Type":"application/json"`, `"X-Test":"1"`, `\"q\":\"x\"`} {
		if !strings.Contains(js, want) {
			t.Errorf("fetch script lacks %s:\n%s", want, js)
		}
	}
	resp := getVar(ae, "resp").(map[string]interface{})
	if resp["status"] != 200.0 || resp["body"].(map[string]interface{})["ok"] != true {
		t.Fatalf("resp = %v", resp)
	}
	if _, leaked := resp["url"]; leaked {
		t.Fatal("internal url field leaked into the result")
	}
}

func TestHTTPFetchInPageDomainChecks(t *testing.T) {
	ae := newExtExecutor(t, fetchPage(t, "https://evil.test/x"))
	ae.SetPackage(&extPkg{id: "site", domains: []string{"*.site.test"}})
	wantFail(t, runExt(t, ae, StepDef{ID: "f", Type: "http_fetch_in_page", URL: "https://evil.test/data"}), "off_domain")
	wantFail(t, runExt(t, ae, StepDef{ID: "f", Type: "http_fetch_in_page", URL: "https://api.site.test/r"}), "redirected")
	wantFail(t, runExt(t, ae, StepDef{ID: "f", Type: "http_fetch_in_page", URL: "ftp://site.test/x"}), "not an absolute http(s) URL")
	wantFail(t, runExt(t, ae, StepDef{ID: "f", Type: "http_fetch_in_page"}), "no url")

	// No package: unrestricted.
	ae = newExtExecutor(t, fetchPage(t, "https://anywhere.test/"))
	wantOK(t, runExt(t, ae, StepDef{ID: "f", Type: "http_fetch_in_page", URL: "https://anywhere.test/"}))
}

func TestHTTPFetchInPagePageError(t *testing.T) {
	ae := newExtExecutor(t, &extPage{eval: func(string) (interface{}, error) {
		return jsOut(map[string]interface{}{"__error": "response larger than 10485760 bytes"}), nil
	}})
	wantFail(t, runExt(t, ae, StepDef{ID: "f", Type: "http_fetch_in_page", URL: "https://x.test/"}), "response larger")
}

func TestDownloadRefusedWithoutPermission(t *testing.T) {
	ae := newExtExecutor(t, &extPage{})
	wantFail(t, runExt(t, ae, StepDef{ID: "d", Type: "download", URL: "https://x.test/f.pdf"}), "not permitted")
}

func TestDownloadWritesIntoConfinedWorkdir(t *testing.T) {
	payload := []byte("%PDF-1.4 hello")
	page := &extPage{url: "https://files.site.test/", eval: func(js string) (interface{}, error) {
		return jsOut(map[string]interface{}{"data": base64.StdEncoding.EncodeToString(payload), "type": "application/pdf",
			"disposition": `attachment; filename="report 1.pdf"`, "url": "https://files.site.test/r"}), nil
	}}
	ae := newExtExecutor(t, page)
	ae.SetDownloadsAllowed(true)
	ae.SetPackage(&extPkg{id: "site", domains: []string{"*.site.test"}})
	root := t.TempDir()
	ctx := fsconfine.WithRoot(context.Background(), root)
	step := ae.resolver.ResolveStepDef(StepDef{ID: "d", Type: "download", URL: "/r", VariableName: "file"})

	for i, wantName := range []string{"report 1.pdf", "report 1 (1).pdf"} {
		res, _ := ae.stepDownload(ctx, step)
		wantOK(t, res)
		p := getVar(ae, "file").(string)
		if p != filepath.Join(root, "downloads", "site", wantName) {
			t.Fatalf("download %d at %s", i, p)
		}
		if b, _ := os.ReadFile(p); string(b) != string(payload) {
			t.Fatalf("content = %q", b)
		}
	}
	wantFail(t, runExt(t, ae, StepDef{ID: "d", Type: "download", URL: "https://evil.test/x"}), "off_domain")
}

func TestDownloadHelpers(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd": "passwd",
		"..":               "download",
		"a/b\\c.txt":       "c.txt",
		".hidden":          "hidden",
		"we?ird*name.pdf":  "we_ird_name.pdf",
		"":                 "download",
	}
	for in, want := range cases {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := dispositionFilename(`attachment; filename*=UTF-8''na%20me.csv`); got != "na me.csv" {
		t.Errorf("disposition = %q", got)
	}
	if got := dispositionFilename("inline"); got != "" {
		t.Errorf("disposition = %q", got)
	}

	ae := newExtExecutor(t, &extPage{})
	if _, err := ae.downloadDir(fsconfine.WithRoot(context.Background(), "")); err == nil {
		t.Fatal("an invalid confinement must refuse downloads")
	}
	testhome.SetHome(t, t.TempDir())
	dir, err := ae.downloadDir(context.Background())
	if err != nil || !strings.HasSuffix(dir, filepath.Join(".monoagent", "downloads", "testsite")) {
		t.Fatalf("dir=%s err=%v", dir, err)
	}
	p, err := writeDownload(filepath.Join(t.TempDir(), "x"), "../a.txt", []byte("hi"))
	if err != nil || filepath.Base(p) != "a.txt" {
		t.Fatalf("p=%s err=%v", p, err)
	}
}
