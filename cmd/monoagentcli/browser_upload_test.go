package main

import (
	"errors"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/extension"
)

func writeUploadFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// setFilesCalls are the set_files commands that reached the page.
func setFilesCalls(bt *browserTest) []extension.Command {
	var out []extension.Command
	for _, c := range bt.fake.Calls() {
		if c.Type == extension.CmdSetFiles {
			out = append(out, c)
		}
	}
	return out
}

func TestBrowserUploadSetsTheFilesOnLocalPages(t *testing.T) {
	bt := newBrowserTest(t, true)
	bt.fake.AddTab("http://localhost:3000/", "Coder · Agent", true)
	bt.fake.On(extension.CmdSetFiles, func(*extension.Command) (any, error) { return map[string]any{"filesSet": true, "count": 1}, nil })
	cv := writeUploadFile(t, "cv.pdf", "%PDF-1.4 cv")
	res := bt.runJSON("upload", "input[type=file]", cv)
	if res["uploaded"] != float64(1) {
		t.Fatalf("result = %v", res)
	}
	calls := setFilesCalls(bt)
	if len(calls) != 1 || calls[0].Params["selector"] != "input[type=file]" || calls[0].Params["viaClick"] != nil || calls[0].Params["strict"] != true {
		t.Fatalf("set_files calls = %v", calls)
	}
	var fd []struct {
		Name     string `json:"name"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	}
	raw, _ := json.Marshal(calls[0].Params["fileData"])
	if err := json.Unmarshal(raw, &fd); err != nil {
		t.Fatal(err)
	}
	if fd[0].Name != "cv.pdf" || fd[0].MimeType != "application/pdf" ||
		fd[0].Data != base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 cv")) {
		t.Fatalf("file data = %+v", fd)
	}
	bt.runJSON("upload", "--chooser", "button.upload", cv)
	if calls = setFilesCalls(bt); len(calls) != 2 || calls[1].Params["viaClick"] != true {
		t.Fatalf("the chooser upload did not click through: %v", calls)
	}
}

func TestBrowserUploadBadInput(t *testing.T) {
	bt := newBrowserTest(t, false)
	bt.fake.AddTab("http://localhost:3000/", "g", true)
	for name, args := range map[string][]string{
		"no file":          {"upload", "input"},
		"missing file":     {"upload", "input", filepath.Join(t.TempDir(), "nope.pdf")},
		"a directory":      {"upload", "input", t.TempDir()},
		"hil with a file":  {"upload", "--hil", "x", "input", "a"},
		"hil with chooser": {"upload", "--hil", "x", "--chooser"},
	} {
		if _, err := bt.run(args...); exitCodeFor(err) != 3 {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(setFilesCalls(bt)) != 0 {
		t.Fatal("a refused upload reached the page")
	}
}

// An agent's upload to a real site waits for approval, then goes through
// once, for the files that were approved.
func TestBrowserUploadWaitsForApprovalOnRealSites(t *testing.T) {
	bt := newBrowserTest(t, true)
	db := bt.db().DB
	if err := browserctl.SaveAllowlist(db, []string{"https://jobs.example"}); err != nil {
		t.Fatal(err)
	}
	bt.fake.AddTab("https://jobs.example/apply", "Coder · Agent", true)
	bt.fake.On(extension.CmdSetFiles, func(*extension.Command) (any, error) { return map[string]any{"filesSet": true, "count": 1}, nil })
	cv := writeUploadFile(t, "cv.pdf", "first")
	out, err := bt.run("upload", "--chooser", "#attach", cv)
	if exitCodeFor(err) != 3 || !strings.Contains(out, "awaiting_approval") || !strings.Contains(out, "browser upload --hil") {
		t.Fatalf("held upload: %v %s", err, out)
	}
	if len(setFilesCalls(bt)) != 0 {
		t.Fatal("the held upload reached the page")
	}
	id := strings.Split(strings.Split(out, `"hil_id": "`)[1], `"`)[0]
	if _, err := bt.run("upload", "--hil", id); exitCodeFor(err) != 3 {
		t.Fatalf("upload before approval: %v", err)
	}
	t.Setenv("CLAUDECODE", "")
	if _, err := runHIL(t, bt.cfg, "approve", id); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDECODE", "1")

	// The approval is for the upload command only.
	for _, verb := range []string{"submit", "fill"} {
		if _, err := bt.run(verb, "--hil", id); err == nil {
			t.Fatalf("an upload approval was used by %s", verb)
		}
		if it, _ := browserctl.GetSubmitItem(t.Context(), db, "default", id); it.Status != "approved" {
			t.Fatalf("%s: the approval was not handed back: %s", verb, it.Status)
		}
	}
	// A file changed after the approval is not uploaded.
	if err := os.WriteFile(cv, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bt.run("upload", "--hil", id); exitCodeFor(err) != 3 || jsonCode(err) != "submit_changed" {
		t.Fatalf("upload after the file changed: %v (%s)", err, jsonCode(err))
	}
	if err := os.WriteFile(cv, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := bt.runJSON("upload", "--hil", id)
	if res["status"] != "uploaded" {
		t.Fatalf("approved upload: %v", res)
	}
	calls := setFilesCalls(bt)
	if len(calls) != 1 || calls[0].Params["selector"] != "#attach" || calls[0].Params["viaClick"] != true {
		t.Fatalf("set_files calls = %v", calls)
	}
	if _, err := bt.run("upload", "--hil", id); exitCodeFor(err) != 3 {
		t.Fatalf("one approval allowed a second upload: %v", err)
	}
}

func TestBrowserUploadAtFullNoApprovalsAndForHumans(t *testing.T) {
	bt := newBrowserTest(t, true)
	db := bt.db().DB
	if err := browserctl.SaveAllowlist(db, []string{"https://jobs.example"}); err != nil {
		t.Fatal(err)
	}
	bt.fake.AddTab("https://jobs.example/apply", "Coder · Agent", true)
	bt.fake.On(extension.CmdSetFiles, func(*extension.Command) (any, error) { return map[string]any{"count": 1}, nil })
	cv := writeUploadFile(t, "cv.pdf", "x")
	if _, err := bt.run("upload", "#f", cv); exitCodeFor(err) != 3 {
		t.Fatalf("agent at the default level: %v", err)
	}
	if _, err := browserctl.UpdatePermissions(db, func(p *browserctl.Permissions) error {
		return p.SetLevel(browserctl.KindOther, browserctl.LevelFullNoApprovals)
	}); err != nil {
		t.Fatal(err)
	}
	if res := bt.runJSON("upload", "#f", cv); res["uploaded"] != float64(1) {
		t.Fatalf("agent at full-no-approvals: %v", res)
	}
	t.Setenv("CLAUDECODE", "")
	if _, err := browserctl.UpdatePermissions(db, func(p *browserctl.Permissions) error {
		return p.SetLevel(browserctl.KindOther, browserctl.LevelDev)
	}); err != nil {
		t.Fatal(err)
	}
	if res := bt.runJSON("upload", "#f", cv); res["uploaded"] != float64(1) {
		t.Fatalf("a human's terminal: %v", res)
	}
}

// A selector that matched nothing says what to do about it, including that
// file inputs inside iframes are not reached; a match that is the wrong kind
// of element does not get that hint.
func TestBrowserUploadNoMatchNamesTheIframeLimit(t *testing.T) {
	bt := newBrowserTest(t, true)
	bt.fake.AddTab("http://localhost:3000/", "Coder · Agent", true)
	cv := writeUploadFile(t, "cv.pdf", "%PDF-1.4 cv")
	for name, msg := range map[string]string{
		"new extension": "set_files: no element matched #up in this frame (file inputs inside iframes are not supported)",
		"old extension": "Element not found for selector: #up",
	} {
		bt.fake.On(extension.CmdSetFiles, func(*extension.Command) (any, error) { return nil, errors.New(msg) })
		_, err := bt.run("upload", "#up", cv)
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		got := err.Error()
		for _, want := range []string{"iframes are not supported", "top document"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: %q lacks %q", name, got, want)
			}
		}
		if strings.Count(got, "iframes are not supported") != 1 {
			t.Errorf("%s: the iframe note is repeated: %q", name, got)
		}
	}
	bt.fake.On(extension.CmdSetFiles, func(*extension.Command) (any, error) {
		return nil, errors.New("set_files: the element matched is not a file input")
	})
	_, err := bt.run("upload", "div", cv)
	if err == nil || strings.Contains(err.Error(), "iframe") || !strings.Contains(err.Error(), "not a file input") {
		t.Fatalf("wrong-type error = %v", err)
	}
}
