package ops

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
)

type fakeResolver struct {
	answers [][]netip.Addr // one answer per lookup; the last repeats
	calls   atomic.Int32
}

func (f *fakeResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	i := int(f.calls.Add(1)) - 1
	return f.answers[min(i, len(f.answers)-1)], nil
}

func addrs(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, x := range s {
		out = append(out, netip.MustParseAddr(x))
	}
	return out
}

func TestPublicAddrRanges(t *testing.T) { // T5i
	for a, want := range map[string]bool{
		"93.184.216.34": true, "2606:2800:220:1::1": true,
		"10.0.0.1": false, "192.168.1.1": false, "172.16.0.1": false, "127.0.0.1": false, "::1": false,
		"169.254.169.254": false, "fe80::1": false, "0.0.0.0": false, "100.64.0.1": false, "fc00::1": false,
		"::ffff:10.0.0.1": false, "::ffff:169.254.169.254": false, "::ffff:127.0.0.1": false, "::ffff:93.184.216.34": true,
		"224.0.0.1": false,
	} {
		if got := PublicAddr(netip.MustParseAddr(a)); got != want {
			t.Fatalf("%s: %v", a, got)
		}
	}
}

func TestAnyNonPublicAddressRefusesTheWholeNameAndNothingIsDialled(t *testing.T) { // T5a
	dialled := false
	res := &fakeResolver{answers: [][]netip.Addr{addrs("93.184.216.34", "169.254.169.254")}}
	p := PinnedTransport{Resolver: res, Dial: func(context.Context, string, string) (net.Conn, error) { dialled = true; return nil, errors.New("no") }}
	_, err := p.Do(bg, VaultArgs{Method: "GET", URL: "https://example.com/x", Inject: "authorization"}, []byte("s"))
	if !errors.Is(err, ErrNotPublic) || dialled {
		t.Fatalf("%v dialled %v", err, dialled)
	}
	// the rebinding case: the check and the dial use one lookup, a second never happens
	res = &fakeResolver{answers: [][]netip.Addr{addrs("93.184.216.34"), addrs("10.0.0.5")}}
	var dials []string
	p = PinnedTransport{Resolver: res, Dial: func(_ context.Context, _ string, addr string) (net.Conn, error) {
		dials = append(dials, addr)
		return nil, errors.New("stop")
	}}
	p.Do(bg, VaultArgs{Method: "GET", URL: "https://example.com/x", Inject: "authorization"}, []byte("s"))
	if res.calls.Load() != 1 || len(dials) != 1 || dials[0] != "93.184.216.34:443" {
		t.Fatalf("lookups %d dials %v", res.calls.Load(), dials)
	}
}

func tlsServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, PinnedTransport, *fakeResolver) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	res := &fakeResolver{answers: [][]netip.Addr{addrs("127.0.0.1")}}
	p := PinnedTransport{
		Resolver:     res,
		AllowPrivate: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, // the charter names the range
		Dial: func(ctx context.Context, n, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, n, srv.Listener.Addr().String())
		},
		TLSConfig: &tls.Config{RootCAs: pool},
	}
	return srv, p, res
}

func TestRedirectsAreNotFollowed(t *testing.T) { // T5b
	var hits atomic.Int32
	_, p, _ := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/next" {
			return
		}
		http.Redirect(w, r, "https://169.254.169.254/next", http.StatusFound)
	})
	_, err := p.Do(bg, VaultArgs{Method: "GET", URL: "https://example.com/x", Inject: "authorization"}, []byte("s"))
	if !errors.Is(err, ErrRedirect) || hits.Load() != 1 {
		t.Fatalf("%v hits %d", err, hits.Load())
	}
}

func TestTheSecretGoesToTheInjectionPointAndProxyVariablesAreIgnored(t *testing.T) { // T5f, T5i
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("https_proxy", "http://127.0.0.1:1")
	var got atomic.Value
	_, p, _ := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("X-Token") + "|" + r.Header.Get("X-Keep") + "|" + r.Host)
		w.Write([]byte("hello"))
	})
	resp, err := p.Do(bg, VaultArgs{Method: "GET", URL: "https://example.com/x", Headers: [][2]string{{"x-keep", "1"}}, Inject: "X-Token"}, []byte("sekret"))
	if err != nil || resp.Status != 200 {
		t.Fatalf("%+v %v", resp, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "hello" || got.Load() != "sekret|1|example.com" {
		t.Fatalf("%q %v", b, got.Load())
	}
}
