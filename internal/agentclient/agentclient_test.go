package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/aitask"
)

const tok = "mat-agentclient-test-token"

func codeOf(err error) string {
	var ae *aitask.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func TestFromEnvNeedsBothVariables(t *testing.T) {
	for _, c := range []struct{ url, token string }{{"", ""}, {"http://x", ""}, {"", tok}, {"  ", tok}} {
		t.Setenv(EnvURL, c.url)
		t.Setenv(EnvToken, c.token)
		if FromEnv() != nil {
			t.Errorf("url %q token %q turned client mode on", c.url, c.token)
		}
	}
	t.Setenv(EnvURL, "not a url")
	t.Setenv(EnvToken, tok)
	c := FromEnv()
	if c == nil {
		t.Fatal("a set pair with a bad URL turned client mode off")
	}
	if _, err := c.Task(context.Background(), "at-1"); codeOf(err) != CodeAPIUnavailable || strings.Contains(err.Error(), tok) {
		t.Errorf("bad URL: %v", err)
	}
}

// The token is sent to the URL it was given for, never on to a redirect target.
func TestTheTokenIsNotFollowedThroughARedirect(t *testing.T) {
	leaked := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization") != "" || r.Header.Get(taskTokenHeader) != ""
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/x", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	_, err := New(srv.URL, tok).Complete(context.Background(), "at-1", "tasktoken", []byte(`{}`))
	if leaked || err == nil {
		t.Fatalf("leaked %v, err %v", leaked, err)
	}
}

func TestRefusalsAreMappedAndScrubbed(t *testing.T) {
	status, body := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := New(srv.URL, tok)
	for _, tc := range []struct {
		status int
		body   string
		code   string
	}{
		{422, `{"error":"bad ` + tok + `","code":"schema_invalid","reason":"r"}`, "schema_invalid"},
		{409, `{"error":"closed","code":"task_closed"}`, "task_closed"},
		{401, `{"error":"missing or invalid bearer credential"}`, CodeAPIRefused},
		{403, `{"error":"this token is not bound to that task"}`, CodeAPIRefused},
		{500, `<html>` + tok + `</html>`, CodeAPIUnavailable},
		{200, `not json`, CodeAPIUnavailable},
	} {
		status, body = tc.status, tc.body
		_, err := c.Task(context.Background(), "at-1")
		if codeOf(err) != tc.code || err == nil || strings.Contains(err.Error(), tok) {
			t.Errorf("%d %s: %v (code %q), want %q with no token", tc.status, tc.body, err, codeOf(err), tc.code)
		}
	}
	var ae *aitask.Error
	status, body = 422, `{"error":"x","code":"schema_invalid","reason":"summary is required"}`
	if _, err := c.Task(context.Background(), "at-1"); !errors.As(err, &ae) || ae.Fields["reason"] != "summary is required" {
		t.Errorf("fields were lost: %v", err)
	}
}

func TestCredentialsGoOverHTTPSOrToThisMachineOnly(t *testing.T) {
	for _, tc := range []struct {
		base string
		ok   bool
	}{
		{"https://example.com/profiles/p", true}, {"https://10.0.0.5:9322/profiles/p", true},
		{"http://127.0.0.1:9322/profiles/p", true}, {"http://localhost:9322/profiles/p", true}, {"http://[::1]:9322/profiles/p", true},
		{"http://example.com/profiles/p", false}, {"http://10.0.0.5:9322/profiles/p", false}, {"http://127.0.0.1.evil.test/p", false},
		{"http://user:pw@127.0.0.1:1/p", false}, {"ftp://127.0.0.1/p", false}, {"//127.0.0.1/p", false},
	} {
		u, err := url.Parse(tc.base)
		if err != nil || secureTarget(u) != tc.ok {
			t.Errorf("%s: allowed %v, want %v", tc.base, err == nil && secureTarget(u), tc.ok)
		}
	}
	// A refused target is never dialled: the call fails before any request.
	_, err := New("http://example.com/profiles/p", tok).Task(context.Background(), "at-1")
	if codeOf(err) != CodeAPIUnavailable || strings.Contains(err.Error(), tok) {
		t.Errorf("plain http to a remote host: %v", err)
	}
}

// Every string of an error document is scrubbed of both credentials, nested ones and keys too.
func TestTheWholeErrorDocumentIsScrubbed(t *testing.T) {
	const task = "task-token-0123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"error":"a ` + tok + `","code":"schema_invalid","reason":"b ` + task + `",` +
			`"deep":{"` + tok + `":["c ` + task + `",{"d":"` + tok + `"}]}}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, tok).Complete(context.Background(), "at-1", task, []byte(`{}`))
	var ae *aitask.Error
	if !errors.As(err, &ae) {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]any{ae.Message, ae.Code, ae.Fields})
	if strings.Contains(string(raw), tok) || strings.Contains(string(raw), task) || ae.Code != "schema_invalid" {
		t.Fatalf("not scrubbed: %s", raw)
	}
}

// A server that answers with garbage naming the task token cannot make the client print it.
func TestTransportErrorsNeverCarryEitherCredential(t *testing.T) {
	const task = "task-token-0123456789"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("HTTP/1.1 " + task + " " + tok + " Broken\r\n\r\n"))
			c.Close()
		}
	}()
	_, err = New("http://"+ln.Addr().String()+"/profiles/p", tok).Complete(context.Background(), "at-1", task, []byte(`{}`))
	if err == nil || strings.Contains(err.Error(), task) || strings.Contains(err.Error(), tok) {
		t.Fatalf("error: %v", err)
	}
}

// A successful answer is scrubbed too, whatever the stored task held.
func TestSuccessfulAnswersAreScrubbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"taskId":"at-1","status":"complete","result":{"summary":"saw ` + tok + ` and task-token-0123456789"},"error":""}`))
	}))
	defer srv.Close()
	task, err := New(srv.URL, tok).Task(context.Background(), "at-1")
	if err != nil || strings.Contains(string(task.Result), tok) {
		t.Fatalf("%s %v", task.Result, err)
	}
	r, err := New(srv.URL, tok).Complete(context.Background(), "at-1", "task-token-0123456789", []byte(`{}`))
	if err != nil || strings.Contains(string(r.Result), "task-token-0123456789") || strings.Contains(string(r.Result), tok) {
		t.Fatalf("%s %v", r.Result, err)
	}
}

// A secret written with an escape in an answer is scrubbed, nested or as a key.
func TestEscapedSecretsInAnAnswerAreScrubbed(t *testing.T) {
	esc := "\\u006d" + tok[1:]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"taskId":"at-1","status":"complete","result":{"` + esc + `":"x ` + esc + `"},"error":"` + esc + `"}`))
	}))
	defer srv.Close()
	task, err := New(srv.URL, tok).Task(context.Background(), "at-1")
	if err != nil || strings.Contains(string(task.Result), tok[1:]) || strings.Contains(task.Error, tok[1:]) {
		t.Fatalf("%s %q %v", task.Result, task.Error, err)
	}
}

// Calls share a client: each call's task token is its own, so a concurrent call cannot clear or
// swap another's (run with -race).
func TestConcurrentCallsKeepTheirOwnTaskTokens(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				buf := make([]byte, 4096)
				n, _ := c.Read(buf)
				// Echo the request's task token in a broken status line.
				var tt string
				for _, l := range strings.Split(string(buf[:n]), "\r\n") {
					if v, ok := strings.CutPrefix(l, "X-Aitask-Token: "); ok {
						tt = v
					}
				}
				_, _ = c.Write([]byte("HTTP/1.1 " + tt + " Broken\r\n\r\n"))
				c.Close()
			}()
		}
	}()
	cl := New("http://"+ln.Addr().String()+"/profiles/p", tok)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task := fmt.Sprintf("task-token-%02d-abcdefghij", i)
			_, err := cl.Complete(context.Background(), "at-1", task, []byte(`{}`))
			if err == nil || strings.Contains(err.Error(), task) {
				t.Errorf("call %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
}
