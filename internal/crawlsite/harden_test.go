package crawlsite

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
)

// sink returns a writer into a fresh inbox.
func sink(t *testing.T) *capture.Writer {
	t.Helper()
	return &capture.Writer{Inbox: filepath.Join(t.TempDir(), "inbox")}
}

const provePage = `<!doctype html><html><head><title>%s</title></head><body>` +
	`<p>This is a paragraph with enough words, and commas, to score as prose.</p>%s</body></html>`

// localhostSeed rewrites an httptest URL to the "localhost" name, which is a
// different scope host from the literal 127.0.0.1 a second server serves on.
func localhostSeed(rawURL string) string {
	return strings.Replace(rawURL, "127.0.0.1", "localhost", 1)
}

// TestCrawlDoesNotFollowRedirectOutOfScope proves the request to the
// redirect target is never made, rather than made and then discarded: a
// 302 to an internal host is an SSRF whether or not the body is saved.
func TestCrawlDoesNotFollowRedirectOutOfScope(t *testing.T) {
	var internalHits int64
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&internalHits, 1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, provePage, "Internal", "")
	}))
	defer internal.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/jump", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/creds", http.StatusFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, provePage, "Index", `<a href="/jump">jump</a>`)
	})
	public := httptest.NewServer(mux)
	defer public.Close()

	c, err := New(Options{
		Seeds:             []string{localhostSeed(public.URL) + "/"},
		MaxDepth:          1,
		Sink:              sink(t),
		Client:            public.Client(),
		AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sum, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := atomic.LoadInt64(&internalHits); n != 0 {
		t.Errorf("the out-of-scope redirect target was requested %d times; the crawl must refuse the hop, not read it and discard it", n)
	}
	for _, p := range sum.Pages {
		if strings.Contains(p.Title, "Internal") {
			t.Errorf("out-of-scope redirect target was saved: %+v", p)
		}
	}
}

// TestCrawlRefusesPrivateAddresses covers the case a scope check cannot:
// an in-scope hostname that resolves to a loopback or link-local address.
func TestCrawlRefusesPrivateAddresses(t *testing.T) {
	srv := testSite(t)
	var events []Event
	c, err := New(Options{
		Seeds:   []string{srv.URL + "/"},
		Sink:    sink(t),
		Client:  srv.Client(),
		OnEvent: func(e Event) { events = append(events, e) },
		// AllowPrivateHosts deliberately left false: this is the default a
		// crawl of the open web runs with.
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sum, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sum.Pages) != 0 {
		t.Fatalf("a host resolving to 127.0.0.1 must not be fetched, saved %d pages", len(sum.Pages))
	}
	if len(sum.Failed) == 0 || !strings.Contains(sum.Failed[0].Reason, "address a crawl may not reach") {
		t.Errorf("want a failure saying the address was refused, got %+v", sum.Failed)
	}
	// The detail belongs to the operator's terminal, not to the summary —
	// see TestRefusedAddressIsNotEchoedIntoTheSummary.
	if len(events) == 0 || !strings.Contains(events[0].Reason, "loopback") {
		t.Errorf("want an event naming the loopback address, got %+v", events)
	}
}

func TestBlockedAddressCoversThePrivateRanges(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.9.9.9", "::1", "10.0.0.5", "172.16.0.1", "172.31.255.255",
		"192.168.1.1", "169.254.169.254", "fc00::1", "fd12:3456::1", "0.0.0.0", "::",
		"fe80::1", "100.64.0.1",
	}
	for _, ip := range blocked {
		if err := checkDialAddress(net.JoinHostPort(ip, "80")); err == nil {
			t.Errorf("%s should be refused", ip)
		}
	}
	for _, ip := range []string{"93.184.216.34", "8.8.8.8", "172.32.0.1", "2606:2800:220::1"} {
		if err := checkDialAddress(net.JoinHostPort(ip, "80")); err != nil {
			t.Errorf("%s is a public address and should be dialable: %v", ip, err)
		}
	}
}

// IPv6 transition ranges embed an IPv4 address (NAT64, 6to4, Teredo): one that
// carries a private address must be refused like the address itself.
func TestBlockedAddressCoversIPv6TransitionRanges(t *testing.T) {
	for _, ip := range []string{
		"64:ff9b::7f00:1",                      // NAT64 of 127.0.0.1
		"64:ff9b::a9fe:a9fe",                   // NAT64 of 169.254.169.254
		"64:ff9b:1::1",                         // local-use NAT64
		"2002:7f00:1::1",                       // 6to4 of 127.0.0.1
		"2002:c0a8:101::1",                     // 6to4 of 192.168.1.1
		"2001:0:4136:e378:8000:63bf:3fff:fdd2", // Teredo
	} {
		if err := checkDialAddress(net.JoinHostPort(ip, "80")); err == nil {
			t.Errorf("%s should be refused", ip)
		}
	}
}

// TestCrawlRechecksRobotsAfterRedirect: a seed that robots allows must not
// become a page robots forbids by way of a 302.
func TestCrawlRechecksRobotsAfterRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /private\n"))
	})
	mux.HandleFunc("/open", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/private/secret", http.StatusFound)
	})
	mux.HandleFunc("/private/secret", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, provePage, "Secret", "")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(Options{
		Seeds: []string{srv.URL + "/open"}, RespectRobots: true,
		Sink: sink(t), Client: srv.Client(), AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sum, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sum.Pages) != 0 {
		t.Fatalf("a redirect into a robots-disallowed path was saved: %+v", sum.Pages)
	}
	if len(sum.Skipped) == 0 || !strings.Contains(sum.Skipped[0].Reason, "robots") {
		t.Errorf("want a robots skip for the final URL, got %+v", sum.Skipped)
	}
}

// TestCrawlCancelDuringCrawlDelay proves the wait is interruptible: a
// hostile Crawl-delay must not pin the process past a cancelled context.
func TestCrawlCancelDuringCrawlDelay(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 86400\n"))
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, provePage, "A", "")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, provePage, "Index", `<a href="/a">a</a>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(Options{
		Seeds: []string{srv.URL + "/"}, MaxDepth: 1, RespectRobots: true,
		Sink: sink(t), Client: srv.Client(), AllowPrivateHosts: true,
		// Sleep left nil: the real wait, which is what has to be interruptible.
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, rerr := c.Run(ctx)
		done <- rerr
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case rerr := <-done:
		if rerr == nil {
			t.Error("a cancelled crawl should report the cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the crawl slept through a cancelled context")
	}
}

// TestCrawlBoundsTotalRequests: every page here declares the same canonical
// URL, so nothing after the first is ever saved and MaxPages alone never
// stops the walk. The frontier must still be bounded.
func TestCrawlBoundsTotalRequests(t *testing.T) {
	var hits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		var links strings.Builder
		for i := 0; i < 8; i++ {
			_, _ = fmt.Fprintf(&links, `<a href="%s/%d">l</a>`, strings.TrimSuffix(r.URL.Path, "/"), i)
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>T</title>` +
			`<link rel="canonical" href="/canon"></head><body>` +
			`<p>This is a paragraph with enough words, and commas, to score as prose.</p>` +
			links.String() + `</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(Options{
		Seeds: []string{srv.URL + "/"}, MaxDepth: 5, MaxPages: 2,
		Sink: sink(t), Client: srv.Client(), AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sum, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt64(&hits); got > int64(c.opts.MaxRequests) {
		t.Errorf("crawl made %d requests with MaxPages=2 and a budget of %d", got, c.opts.MaxRequests)
	}
	if len(c.seen) > c.opts.MaxRequests {
		t.Errorf("frontier grew to %d URLs, past the %d request budget", len(c.seen), c.opts.MaxRequests)
	}
	if !sum.Truncated {
		t.Error("a crawl stopped with work left must report Truncated")
	}
}

// TestCrawlAsksRobotsAboutTheQueryString: faceted-navigation rules are
// written against the query, and are the most common reason a site asks a
// crawler to stay out of a path it otherwise serves.
func TestCrawlAsksRobotsAboutTheQueryString(t *testing.T) {
	var searchHits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /search?q=\n"))
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&searchHits, 1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, provePage, "Search", "")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, provePage, "Index", `<a href="/search?q=shoes">s</a>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(Options{
		Seeds: []string{srv.URL + "/"}, MaxDepth: 1, RespectRobots: true,
		Sink: sink(t), Client: srv.Client(), AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := atomic.LoadInt64(&searchHits); n != 0 {
		t.Errorf("a path disallowed by its query string was fetched %d times", n)
	}
}

// countingRoundTripper is a RoundTripper the crawl cannot install a dial
// hook into, which is the second half of the address guard.
type countingRoundTripper struct {
	base  http.RoundTripper
	calls int64
}

func (rt *countingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	atomic.AddInt64(&rt.calls, 1)
	return rt.base.RoundTrip(req)
}

func TestCrawlRefusesPrivateAddressesThroughACustomTransport(t *testing.T) {
	srv := testSite(t)
	rt := &countingRoundTripper{base: srv.Client().Transport}
	c, err := New(Options{
		Seeds: []string{srv.URL + "/"}, Sink: sink(t),
		Client: &http.Client{Transport: rt},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sum, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := atomic.LoadInt64(&rt.calls); n != 0 {
		t.Errorf("the request reached the transport %d times; a host on a private address is refused before that", n)
	}
	if len(sum.Pages) != 0 {
		t.Errorf("saved %d pages from a private address", len(sum.Pages))
	}
}

// TestAllowPrivateHostsRelaxesEveryHop: the option has to reach both
// checks. The dial guard and the per-hop redirect check are two different
// pieces of code, and an intranet crawl that only had the first relaxed
// would fetch its seed and then fail on the first redirect — passing every
// single-request test while being useless for what the option is for.
func TestAllowPrivateHostsRelaxesEveryHop(t *testing.T) {
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, provePage, "Target", "")
	}))
	defer second.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/jump", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/target", http.StatusFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, provePage, "Index", `<a href="/jump">jump</a>`)
	})
	first := httptest.NewServer(mux)
	defer first.Close()

	c, err := New(Options{
		// Seeded by name, redirected to a second private host that is in
		// scope only because the operator named it: an intranet in
		// miniature.
		Seeds:             []string{localhostSeed(first.URL) + "/"},
		AllowHosts:        []string{"127.0.0.1"},
		MaxDepth:          1,
		Sink:              sink(t),
		Client:            first.Client(),
		AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sum, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, p := range sum.Pages {
		if p.Title == "Target" {
			return
		}
	}
	t.Errorf("AllowPrivateHosts did not survive the redirect hop; saved %+v, skipped %+v, failed %+v",
		sum.Pages, sum.Skipped, sum.Failed)
}

// TestRefusedAddressIsNotEchoedIntoTheSummary: the summary is serialized
// and can travel — into a report, a ticket, a dashboard — while the events
// go to the operator's terminal. The address a name resolved to belongs in
// the second and not the first: whoever induced a crawl of a guessed
// internal name should not get back confirmation that it exists and what
// it answers on.
func TestRefusedAddressIsNotEchoedIntoTheSummary(t *testing.T) {
	srv := testSite(t)
	var events []Event
	c, err := New(Options{
		// "localhost" is a name, not a literal, so the guard resolves it
		// and refuses what it finds — the shape of an internal-only name.
		Seeds:   []string{localhostSeed(srv.URL) + "/"},
		Sink:    sink(t),
		Client:  srv.Client(),
		OnEvent: func(e Event) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sum, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sum.Failed) != 1 {
		t.Fatalf("want one failure, got %+v", sum.Failed)
	}
	for _, leak := range []string{"127.0.0.1", "::1", "loopback"} {
		if strings.Contains(sum.Failed[0].Reason, leak) {
			t.Errorf("the summary quotes %q back: %q", leak, sum.Failed[0].Reason)
		}
	}
	if !strings.Contains(sum.Failed[0].Reason, "address") {
		t.Errorf("the summary should still say why: %q", sum.Failed[0].Reason)
	}
	// The operator, reading their own terminal, gets the whole story.
	if len(events) != 1 {
		t.Fatalf("want one event, got %+v", events)
	}
	if !strings.Contains(events[0].Reason, "127.0.0.1") && !strings.Contains(events[0].Reason, "::1") {
		t.Errorf("the event should name the address for whoever is debugging it: %q", events[0].Reason)
	}
}
