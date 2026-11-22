package agentinstall

import (
	"net/http"
	"net/url"
	"testing"
)

func TestInstallerRedirectStaysOnAllowedHosts(t *testing.T) {
	cr := New().HTTP.CheckRedirect
	if cr == nil {
		t.Fatal("installer client follows redirects without a host check")
	}
	via := []*http.Request{{}}
	for _, raw := range []string{"https://evil.example/x.sh", "http://antigravity.google/x.sh", "https://antigravity.google:8443/x.sh"} {
		u, _ := url.Parse(raw)
		if err := cr(&http.Request{URL: u}, via); err == nil {
			t.Errorf("redirect to %s allowed", raw)
		}
	}
	u, _ := url.Parse("https://antigravity.google/y.sh")
	if err := cr(&http.Request{URL: u}, via); err != nil {
		t.Errorf("redirect to an allowed host refused: %v", err)
	}
	if err := cr(&http.Request{URL: u}, make([]*http.Request, 4)); err == nil {
		t.Error("4th hop allowed")
	}
}
