package account_test

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/library/libraryfake"
)

func TestRefresherSendsTheAudienceAndRotates(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	libraryfake.TrustKey(t)
	_, rt := fake.NewGrant("ada")
	ts, err := account.NewRefresher(fake.URL).Refresh(context.Background(), rt)
	if err != nil || ts.RefreshToken == "" || ts.RefreshToken == rt || fake.LastResource() != account.Audience {
		t.Fatalf("refresh: %v (rotated %v, resource %q)", err, ts != nil && ts.RefreshToken != rt, fake.LastResource())
	}
	if rec, err := account.Verify(ts.AccessToken, time.Now()); err != nil || rec.Sub != "u-ada" {
		t.Fatalf("the access token must verify and name the user: %v", err)
	}
	var refused *account.RefusedError
	if _, err := account.NewRefresher(fake.URL).Refresh(context.Background(), rt); !errors.As(err, &refused) {
		t.Fatalf("a spent refresh token must be a refusal: %v", err)
	}
}

// Spec D27: only invalid_grant is a refusal. Everything else leaves the client in
// grace, and no error ever carries the refresh token. A24: every other failure also
// says whether monoes.me can have rotated the token, which the guard acts on: a
// complete 4xx (monoes.me processed nothing) or a request that never went out leaves
// it good; a 500, which can come after the rotation was committed, and a request that
// went out and was not answered with anything readable do not.
func TestRefresherOnlyInvalidGrantIsARefusal(t *testing.T) {
	cases := []struct {
		name    string
		mode    libraryfake.RefreshMode
		refused bool
		reason  account.Reason
		settled bool // the client knows that monoes.me did not rotate the token (meaningless for a refusal)
	}{
		{"invalid_grant", libraryfake.RefreshInvalidGrant, true, "", false},
		{"invalid_client", libraryfake.RefreshInvalidClient, false, account.ReasonServerError, true},
		{"invalid_target", libraryfake.RefreshInvalidTarget, false, account.ReasonServerError, true},
		{"http 500", libraryfake.RefreshServerError, false, account.ReasonServerError, false},
		{"malformed body", libraryfake.RefreshMalformed, false, account.ReasonServerError, false},
		{"dropped connection", libraryfake.RefreshDrop, false, account.ReasonUnreachable, false},
		{"token rotated, answer lost", libraryfake.RefreshLost, false, account.ReasonUnreachable, false}, // last: it spends rt
	}
	fake := libraryfake.New()
	defer fake.Close()
	_, rt := fake.NewGrant("ada")
	for _, c := range cases {
		fake.SetRefreshMode(c.mode)
		_, err := account.NewRefresher(fake.URL).Refresh(context.Background(), rt)
		var refused *account.RefusedError
		var transient *account.TransientError
		switch {
		case err == nil:
			t.Errorf("%s: no error", c.name)
		case c.refused && !errors.As(err, &refused):
			t.Errorf("%s: want a refusal, got %T %v", c.name, err, err)
		case !c.refused && (errors.As(err, &refused) || !errors.As(err, &transient) || transient.Reason != c.reason || transient.Settled != c.settled):
			t.Errorf("%s: want a transient %s failure, settled %v, got %T %v", c.name, c.reason, c.settled, err, err)
		}
		if err != nil && strings.Contains(err.Error(), rt) {
			t.Errorf("%s: the error carries the refresh token", c.name)
		}
	}
}

func TestRefresherUnreachableAndInsecureHosts(t *testing.T) {
	var transient *account.TransientError
	_, err := account.NewRefresher(refusedURL(t)).Refresh(context.Background(), "rt")
	if !errors.As(err, &transient) || transient.Reason != account.ReasonUnreachable {
		t.Fatalf("no server: %T %v", err, err)
	}
	_, err = account.NewRefresher("http://monoes.example").Refresh(context.Background(), "rt")
	if !errors.As(err, &transient) || transient.Reason != account.ReasonServerError || !strings.Contains(err.Error(), "must be https") {
		t.Fatalf("plain http to a remote host: %T %v", err, err)
	}
}

// A24: Settled says whether the client knows what monoes.me did with the refresh token. It is true while
// the request has not been completely written, because monoes.me cannot have seen it, and when the answer
// is a complete 4xx, because monoes.me processed nothing; it is false for everything else once the request
// is out, because monoes.me may have rotated the token: a 5xx whatever its body (a gateway's 502, 504 or 524,
// or an application's 500, can come after the rotation was committed), a redirect, a reset or a timeout, a
// body cut short under any status, and a 2xx that holds no usable token set. A wrong true would present a
// spent token again and end every install of the account, a wrong false would drop a good one, so each way a
// refresh can fail has a row. (The fake's own failures, invalid_client, invalid_target, a 500, a dropped
// connection, a body that is not JSON and a lost answer, are rows of the test above.)
func TestRefresherSettledIsExactlyWhatTheClientCanKnow(t *testing.T) {
	// tokenServer answers the discovery with a 404, so the refresher uses the default endpoints, and the
	// token endpoint with h.
	tokenServer := func(t *testing.T, h http.HandlerFunc) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/auth/oauth2/token" {
				http.NotFound(w, r)
				return
			}
			h(w, r)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	status := func(code int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		}
	}
	// cutOff promises 400 bytes, sends the status and the start of a body, and ends the connection.
	cutOff := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "400")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"access_token":"`))
		}
	}
	// held reads the whole request, so that the server notices the client leaving, and then answers nothing
	// until the client has gone.
	held := func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}
	refreshAt := func(ctx context.Context, host string) error {
		_, err := account.NewRefresher(host).Refresh(ctx, "refresh-token-under-test")
		return err
	}
	for _, c := range []struct {
		name    string
		run     func(t *testing.T) error
		reason  account.Reason
		settled bool
	}{
		{"nothing listens, so the endpoint discovery fails", func(t *testing.T) error {
			return refreshAt(context.Background(), refusedURL(t))
		}, account.ReasonUnreachable, true},
		{"the caller is gone before the grant is sent", func(t *testing.T) error {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return refreshAt(ctx, tokenServer(t, status(200, `{}`)).URL)
		}, account.ReasonUnreachable, true},
		{"the server is gone after the endpoint was found, so the dial fails", func(t *testing.T) error {
			srv := tokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Connection", "close") // no idle connection is left for the second call to find dead
				_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"b"}`))
			})
			r := account.NewRefresher(srv.URL)
			if _, err := r.Refresh(context.Background(), "refresh-token-under-test"); err != nil {
				t.Fatalf("the first refresh: %v", err)
			}
			srv.Close()
			_, err := r.Refresh(context.Background(), "refresh-token-under-test")
			return err
		}, account.ReasonUnreachable, true},
		{"plain http to a remote host is never sent", func(t *testing.T) error {
			return refreshAt(context.Background(), "http://monoes.example")
		}, account.ReasonServerError, true},
		{"a complete 4xx: a rate limit", func(t *testing.T) error {
			return refreshAt(context.Background(), tokenServer(t, status(429, `{"error":"slow_down"}`)).URL)
		}, account.ReasonServerError, true},
		{"a complete 4xx: a malformed request", func(t *testing.T) error {
			return refreshAt(context.Background(), tokenServer(t, status(400, `{"error":"invalid_request"}`)).URL)
		}, account.ReasonServerError, true},
		{"a 4xx whose body is cut off", func(t *testing.T) error {
			return refreshAt(context.Background(), tokenServer(t, cutOff(429)).URL)
		}, account.ReasonUnreachable, false},
		{"a 500 raised after the rotation was committed", func(t *testing.T) error {
			return refreshAt(context.Background(), tokenServer(t, status(500, `{"error":"server_error"}`)).URL)
		}, account.ReasonServerError, false},
		{"a 502 from a gateway", func(t *testing.T) error {
			return refreshAt(context.Background(), tokenServer(t, status(502, `bad gateway`)).URL)
		}, account.ReasonServerError, false},
		{"a 504 from a gateway", func(t *testing.T) error {
			return refreshAt(context.Background(), tokenServer(t, status(504, `gateway timeout`)).URL)
		}, account.ReasonServerError, false},
		{"a 524 from a gateway: the origin took too long", func(t *testing.T) error {
			return refreshAt(context.Background(), tokenServer(t, status(524, `a timeout occurred`)).URL)
		}, account.ReasonServerError, false},
		{"a 5xx whose body is cut off", func(t *testing.T) error {
			return refreshAt(context.Background(), tokenServer(t, cutOff(503)).URL)
		}, account.ReasonUnreachable, false},
		{"a redirect", func(t *testing.T) error {
			return refreshAt(context.Background(), tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
			}).URL)
		}, account.ReasonServerError, false},
		{"the connection is reset after the request was read", func(t *testing.T) error {
			return refreshAt(context.Background(), tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
					conn.Close()
				}
			}).URL)
		}, account.ReasonUnreachable, false},
		{"a timeout while monoes.me has the request", func(t *testing.T) error {
			// The deadline passes once the server holds the whole request, not
			// after a fixed time that a loaded machine can use up before the
			// request is even written (which is a different, settled, case).
			ctx := passedOnSignal{Context: context.Background(), signal: make(chan struct{})}
			srv := tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body) // the whole request has arrived, so it was written
				close(ctx.signal)
				held(w, r)
			})
			return refreshAt(ctx, srv.URL)
		}, account.ReasonUnreachable, false},
		{"the caller gives up while monoes.me has the request", func(t *testing.T) error {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			srv := tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body) // the whole request has arrived, so it was written
				cancel()
				held(w, r)
			})
			return refreshAt(ctx, srv.URL)
		}, account.ReasonUnreachable, false},
		{"a 200 whose body holds no access token", func(t *testing.T) error {
			return refreshAt(context.Background(), tokenServer(t, status(200, `{"token_type":"Bearer"}`)).URL)
		}, account.ReasonServerError, false},
		{"a 200 that names no refresh token", func(t *testing.T) error {
			return refreshAt(context.Background(), tokenServer(t, status(200, `{"access_token":"a","token_type":"Bearer"}`)).URL)
		}, account.ReasonServerError, false},
		{"a 200 whose body is cut off", func(t *testing.T) error {
			return refreshAt(context.Background(), tokenServer(t, cutOff(200)).URL)
		}, account.ReasonUnreachable, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.run(t)
			var transient *account.TransientError
			if !errors.As(err, &transient) || transient.Reason != c.reason || transient.Settled != c.settled {
				t.Fatalf("got %T %v, want a transient %s failure with settled %v", err, err, c.reason, c.settled)
			}
			if strings.Contains(err.Error(), "refresh-token-under-test") {
				t.Error("the error carries the refresh token")
			}
		})
	}
}

// monoes.me rotates the refresh token at every use (plan A), so its answer to a refresh names the token that
// replaces the one presented. An answer that names none has spent that token and lost its successor: an
// unknown outcome (A24), never "the same one stays good", which would keep a dead token and present it again
// after monoes.me's reuse window. An answer that hands back the very token it was given has not rotated it,
// and that is definitive: the token set carries it.
func TestRefresherReadsAnAnswerWithoutARefreshTokenAsUnknown(t *testing.T) {
	answer := func(body string) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/auth/oauth2/token" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	var transient *account.TransientError
	_, err := account.NewRefresher(answer(`{"access_token":"new-access"}`)).Refresh(context.Background(), "old-refresh")
	if !errors.As(err, &transient) || transient.Reason != account.ReasonServerError || transient.Settled {
		t.Fatalf("a 200 that names no refresh token: %T %v, want a server_error whose outcome is unknown", err, err)
	}
	ts, err := account.NewRefresher(answer(`{"access_token":"new-access","refresh_token":"old-refresh"}`)).Refresh(context.Background(), "old-refresh")
	if err != nil || ts.AccessToken != "new-access" || ts.RefreshToken != "old-refresh" {
		t.Fatalf("a 200 that hands the same refresh token back: %v (access token as sent %v, refresh token kept %v)", err, ts != nil && ts.AccessToken == "new-access", ts != nil && ts.RefreshToken == "old-refresh")
	}
}

// What a refresh sends is the grant, the token, the client and the audience, and
// nothing that names this machine: SECURITY.md says so, and this keeps it true.
func TestRefreshRequestSendsOnlyTheGrantFields(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	libraryfake.TrustKey(t)
	_, rt := fake.NewGrant("ada")
	if _, err := account.NewRefresher(fake.URL).Refresh(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	reqs := fake.TokenRequests()
	got := slices.Sorted(maps.Keys(reqs[len(reqs)-1].Form))
	if want := []string{"client_id", "grant_type", "refresh_token", "resource"}; !slices.Equal(got, want) {
		t.Fatalf("a refresh sent the fields %v, want %v", got, want)
	}
	if h := reqs[len(reqs)-1].Header; h.Get("Authorization") != "" || h.Get("Cookie") != "" {
		t.Fatalf("a refresh carried credentials in its headers: %v", h)
	}
	if fake.Replays != 0 {
		t.Fatal("a refresh replayed a token")
	}
}

// A Refresher that fails never returns a typed-nil error, an interface that holds a nil
// *RefusedError or a nil *TransientError: it is not nil to `err != nil`, errors.As finds a nil
// target in it, and the guard counts it as an ordinary failure. On every error path the token set
// is nil and the error is not nil and is exactly one of the two types.
func TestRefresherNeverReturnsATypedNilError(t *testing.T) {
	fake := libraryfake.New()
	defer fake.Close()
	_, rt := fake.NewGrant("ada")
	live := account.NewRefresher(fake.URL)
	for _, p := range []struct {
		name string
		r    account.Refresher
		mode libraryfake.RefreshMode
	}{
		{"invalid_grant", live, libraryfake.RefreshInvalidGrant},
		{"invalid_client", live, libraryfake.RefreshInvalidClient},
		{"invalid_target", live, libraryfake.RefreshInvalidTarget},
		{"http 500", live, libraryfake.RefreshServerError},
		{"malformed body", live, libraryfake.RefreshMalformed},
		{"dropped connection", live, libraryfake.RefreshDrop},
		{"no server", account.NewRefresher(refusedURL(t)), libraryfake.RefreshOK},
		{"plain http to a remote host", account.NewRefresher("http://monoes.example"), libraryfake.RefreshOK},
	} {
		fake.SetRefreshMode(p.mode)
		ts, err := p.r.Refresh(context.Background(), rt)
		var refused *account.RefusedError
		var transient *account.TransientError
		isRefused, isTransient := errors.As(err, &refused), errors.As(err, &transient)
		if ts != nil || err == nil || isRefused == isTransient || (isRefused && refused == nil) || (isTransient && transient == nil) {
			t.Errorf("%s: token set %v, error %T (refusal %v, transient %v)", p.name, ts != nil, err, isRefused, isTransient)
		}
	}
}

// passedOnSignal is a context whose deadline passes when signal is closed
// rather than at a time, so that a test makes it pass at the moment that
// matters, however slow the machine is.
type passedOnSignal struct {
	context.Context
	signal chan struct{}
}

func (c passedOnSignal) Done() <-chan struct{} { return c.signal }

func (c passedOnSignal) Err() error {
	select {
	case <-c.signal:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
