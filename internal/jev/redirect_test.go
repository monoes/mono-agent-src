package jev

import (
	"context"
	"net/http"
	"testing"

	"github.com/monoes/mono-agent/internal/credhttp/credhttptest"
)

// A Jev request carries the API key: a redirect to another host (another
// port of the same machine included) follows without it, and a 307 that
// would resend the questions there isn't followed.
func TestJevRequestsKeepTheKeyOnRedirect(t *testing.T) {
	get := credhttptest.NewPair(t, http.StatusFound, `{"data":[]}`)
	t.Setenv("TYPESAFE_BASE_URL", get.From)
	c, err := NewClient("jev-k3y", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := get.Got(); len(got) != 1 || get.Leaked("jev-k3y") != "" {
		t.Errorf("a GET's redirect carried %+v", got)
	}
	post := credhttptest.NewPair(t, http.StatusTemporaryRedirect, `{}`)
	c.BaseURL, c.Retries = post.From, 0
	_, _ = c.post(context.Background(), "/v1/systemone", []byte(`{"questions":{}}`))
	if got := post.Got(); len(got) != 0 {
		t.Errorf("a POST's 307 reached the other host: %+v", got)
	}
}
