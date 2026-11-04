package connections

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeOpen puts an `open` on PATH that records its argument, so a flow that reaches the real opener is seen.
func fakeOpen(t *testing.T) (opened func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "opened")
	script := "#!/bin/sh\necho \"$1\" >> " + log + "\n"
	if err := os.WriteFile(filepath.Join(dir, "open"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() string {
		time.Sleep(100 * time.Millisecond) // the opener is started, not waited for
		b, _ := os.ReadFile(log)
		return strings.TrimSpace(string(b))
	}
}

func setBrowserRefusal(t *testing.T, why string) {
	t.Helper()
	prev := BrowserRefusal
	BrowserRefusal = func() string { return why }
	t.Cleanup(func() { BrowserRefusal = prev })
}

// A non-operator's OAuth 2.0 flow stops before it opens a page; the operator's still opens it (#1424).
func TestRunOAuthFlow_BrowserOpenIsTheUsersOwn(t *testing.T) {
	cfg := OAuthConfig{AuthURL: "https://provider.invalid/authorize", TokenURL: "https://provider.invalid/token", ClientID: "id", CallbackPort: freePort(t)}
	for _, tc := range []struct {
		why       string
		wantOpens bool
	}{{"this process runs inside a coder turn", false}, {"", true}} {
		opened := fakeOpen(t)
		setBrowserRefusal(t, tc.why)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		_, err := RunOAuthFlow(ctx, cfg, time.Second, nil)
		cancel()
		got := opened()
		if tc.wantOpens {
			if got == "" || err == nil {
				t.Errorf("operator: opened %q, err %v; want the page opened and the wait to end on its own", got, err)
			}
			continue
		}
		if got != "" || err == nil || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("non-operator: opened %q, err %v; want nothing opened and the refusal", got, err)
		}
	}
}

// Same for OAuth 1.0a: the refusal comes before the request token is asked for and before the page opens.
func TestRunOAuth1Flow_BrowserOpenIsTheUsersOwn(t *testing.T) {
	prov := newFakeOAuth1Provider(t)
	port := freePort(t)
	pages := stubOAuth1Browser(t, port, func(u *url.URL) string { return "" })
	setBrowserRefusal(t, "this process runs inside a coder turn")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := RunOAuth1Flow(ctx, OAuth1Config{RequestTokenURL: prov.URL + "/request_token", AuthURL: prov.URL + "/authorize", AccessTokenURL: prov.URL + "/access_token", CallbackPort: port}, "k", "s", time.Second, nil)
	if err == nil || !strings.Contains(err.Error(), "coder turn") || *pages != "" {
		t.Errorf("err %v, page opened %q; want the refusal and nothing opened", err, *pages)
	}
}
