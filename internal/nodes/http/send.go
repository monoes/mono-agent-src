package httpnodes

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/credhttp"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/tracesig"
)

// clientOptions are the transport-level settings shared by every request a
// node execution makes.
type clientOptions struct {
	timeout         time.Duration
	followRedirects bool
	maxRedirects    int
	insecure        bool
	proxy           *url.URL
	maxBodyBytes    int64
}

// parseClientOptions reads timeout (seconds; timeout_seconds is an alias),
// follow_redirects, max_redirects, allow_unauthorized_certs, proxy and
// max_body_mb.
func parseClientOptions(config map[string]interface{}, node string) (clientOptions, error) {
	o := clientOptions{timeout: 30 * time.Second, followRedirects: true, maxRedirects: 10, maxBodyBytes: 64 << 20}
	wrap := func(err error) error { return fmt.Errorf("%s: %w", node, err) }

	for _, key := range []string{"timeout", "timeout_seconds"} {
		secs, ok, err := configNumber(config, key)
		if err != nil {
			return o, wrap(err)
		}
		if ok {
			if secs <= 0 {
				return o, wrap(fmt.Errorf("%s: must be greater than 0 seconds", key))
			}
			o.timeout = time.Duration(secs * float64(time.Second))
			break
		}
	}
	var err error
	if o.followRedirects, err = configBool(config, "follow_redirects", true); err != nil {
		return o, wrap(err)
	}
	if n, ok, err := configNumber(config, "max_redirects"); err != nil {
		return o, wrap(err)
	} else if ok {
		if n < 0 {
			return o, wrap(fmt.Errorf("max_redirects: must be 0 or more"))
		}
		o.maxRedirects = int(n)
	}
	if o.insecure, err = configBool(config, "allow_unauthorized_certs", false); err != nil {
		return o, wrap(err)
	}
	if p := strings.TrimSpace(configString(config, "proxy")); p != "" {
		pu, err := url.Parse(p)
		if err != nil || pu.Host == "" {
			return o, wrap(fmt.Errorf("proxy: %q is not a proxy URL like http://host:port", p))
		}
		o.proxy = pu
	}
	if mb, ok, err := configNumber(config, "max_body_mb"); err != nil {
		return o, wrap(err)
	} else if ok && mb > 0 {
		o.maxBodyBytes = int64(mb) << 20
	}
	return o, nil
}

// redirectPolicyKey carries a request's redirectPolicy in its context, so
// one client serves requests with different credential headers.
type redirectPolicyKey struct{}

type redirectPolicy struct {
	// sensitive are the headers this request set that may carry
	// credentials: the configured custom headers and the auth header.
	sensitive []string
	// keepCredentials sends them (and Authorization) on to another host
	// too: send_credentials_on_redirect, off by default.
	keepCredentials bool
	// marked: the request carries a credential (credhttp.Mark), so a
	// redirect follows credhttp's policy too.
	marked bool
}

// newHTTPClient builds the client and returns a cleanup that closes its
// idle connections (a transport per execution would otherwise leak them).
func newHTTPClient(o clientOptions) (*http.Client, func()) {
	transport := &http.Transport{}
	if o.proxy != nil {
		transport.Proxy = http.ProxyURL(o.proxy)
	}
	if o.insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // allow_unauthorized_certs is the user's explicit choice
	}
	var rt http.RoundTripper = transport
	if o.proxy != nil || o.insecure {
		rt = viaCheck{base: transport, proxy: o.proxy, insecure: o.insecure}
	}
	client := &http.Client{Timeout: o.timeout, Transport: rt}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !o.followRedirects {
			return http.ErrUseLastResponse
		}
		if len(via) >= o.maxRedirects {
			return fmt.Errorf("stopped after %d redirects", o.maxRedirects)
		}
		// A 307/308 sends the body again, to the host the redirect names.
		if req.GetBody != nil && secrets.UnderAgentAccess(req.Context()) {
			if rc, err := req.GetBody(); err == nil {
				b, _ := io.ReadAll(rc)
				rc.Close()
				if err := checkSecretContent(req.Context(), client, req, b); err != nil {
					return err
				}
			}
		}
		if len(via) == 0 || req.URL.Host == via[0].URL.Host {
			return nil
		}
		// Go drops Authorization and Cookie on a redirect to another
		// domain but forwards every other header verbatim — an API-key
		// header included. The signed chain token never leaves the host.
		req.Header.Del(tracesig.Header)
		pol, _ := req.Context().Value(redirectPolicyKey{}).(redirectPolicy)
		if pol.marked {
			// Also: no body resent to another host (307, 308), no
			// Proxy-Authorization, Cookie or Referer, nothing on an
			// https-to-http hop (credhttp).
			return credhttp.CheckRedirect(req, via)
		}
		if pol.keepCredentials {
			for _, h := range []string{"Authorization", "Cookie"} {
				if v := via[0].Header.Get(h); v != "" {
					req.Header.Set(h, v)
				}
			}
			return nil
		}
		for _, h := range pol.sensitive {
			req.Header.Del(h)
		}
		return nil
	}
	return client, transport.CloseIdleConnections
}

// viaCheck holds a request that carries an agent-access secret and goes
// through a proxy or with certificate checks off to the grant's rules
// (secrets.CheckSendVia); every hop, redirects included, is checked.
type viaCheck struct {
	base     *http.Transport
	proxy    *url.URL
	insecure bool
}

func (v viaCheck) RoundTrip(req *http.Request) (*http.Response, error) {
	if secrets.UnderAgentAccess(req.Context()) {
		texts := carryTexts(req)
		if v.proxy != nil {
			texts = append(texts, v.proxy.String())
		}
		if req.GetBody != nil {
			if rc, err := req.GetBody(); err == nil {
				b, _ := io.ReadAll(rc)
				rc.Close()
				texts = append(texts, string(b))
			}
		}
		if err := secrets.CheckSendVia(req.Context(), v.proxy, v.insecure, texts...); err != nil {
			return nil, err
		}
	}
	return v.base.RoundTrip(req)
}

func (v viaCheck) CloseIdleConnections() { v.base.CloseIdleConnections() }

// outbound is one fully prepared request.
type outbound struct {
	method      string
	u           *url.URL
	headers     []nameValue
	body        []byte
	hasBody     bool
	contentType string
	auth        authPlan
	keepCreds   bool
}

// fetched is a response read into memory (bounded by max_body_mb).
type fetched struct {
	status int
	header http.Header
	body   []byte
	url    *url.URL
}

// send performs ob, answering a Digest challenge when the plan uses
// Digest auth. Transport failures and an over-limit body are errors.
func send(ctx context.Context, client *http.Client, ob *outbound, maxBody int64, config, itemJSON map[string]interface{}) (*fetched, error) {
	pol := redirectPolicy{keepCredentials: ob.keepCreds}
	pol.marked = !ob.keepCreds && (ob.auth != authPlan{} || len(ob.headers) > 0)
	for _, h := range ob.headers {
		pol.sensitive = append(pol.sensitive, h.Name)
	}
	if ob.auth.hdrName != "" {
		pol.sensitive = append(pol.sensitive, ob.auth.hdrName)
	}
	ctx = context.WithValue(ctx, redirectPolicyKey{}, pol)

	req, err := buildRequest(ctx, ob, config, itemJSON, "")
	if err != nil {
		return nil, err
	}
	if err := checkSecretHost(ctx, req, ob.body); err != nil {
		return nil, err
	}
	if err := checkSecretContent(ctx, client, req, ob.body); err != nil {
		return nil, err
	}
	if pol.marked {
		req = credhttp.Mark(req, pol.sensitive...)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && ob.auth.digest {
		if ch, ok := parseDigestChallenge(resp.Header); ok {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			authz, err := digestAuthorization(ch, ob.auth.user, ob.auth.pw, ob.method, ob.u.RequestURI())
			if err != nil {
				return nil, err
			}
			if req, err = buildRequest(ctx, ob, config, itemJSON, authz); err != nil {
				return nil, err
			}
			if pol.marked {
				req = credhttp.Mark(req, pol.sensitive...)
			}
			if resp, err = client.Do(req); err != nil {
				return nil, err
			}
		}
	}
	defer resp.Body.Close()

	// Read at most one byte past the cap so an over-limit body is detected
	// without slurping the whole thing.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if int64(len(data)) > maxBody {
		detail := ""
		if resp.ContentLength >= 0 {
			detail = fmt.Sprintf(" (content-length: %d)", resp.ContentLength)
		}
		return nil, fmt.Errorf("response body exceeds max_body_mb (%d): use max_body_mb to raise%s", maxBody>>20, detail)
	}
	return &fetched{status: resp.StatusCode, header: resp.Header, body: data, url: resp.Request.URL}, nil
}

// buildRequest assembles the http.Request for ob. digestAuthz, when set,
// is the Authorization header answering a Digest challenge.
func buildRequest(ctx context.Context, ob *outbound, config, itemJSON map[string]interface{}, digestAuthz string) (*http.Request, error) {
	var body io.Reader
	if ob.hasBody {
		body = bytes.NewReader(ob.body)
	}
	// The method and URL were validated when ob was built.
	req, _ := http.NewRequestWithContext(ctx, ob.method, ob.u.String(), body)
	if ob.contentType != "" {
		req.Header.Set("Content-Type", ob.contentType)
	}
	for _, h := range ob.headers {
		if strings.EqualFold(h.Name, "Host") {
			req.Host = stringifyValue(h.Value)
			continue
		}
		req.Header.Set(h.Name, stringifyValue(h.Value))
	}
	if err := ob.auth.applyToRequest(req, ob.body); err != nil {
		return nil, err
	}
	if digestAuthz != "" {
		req.Header.Set("Authorization", digestAuthz)
	}
	// A run on a chain passes it on, signed, so that if this request comes
	// back into mono-agent through a webhook the run it starts stays on the
	// chain. Set after every configured header so none can replace it.
	setTraceHeader(ctx, req, config, itemJSON)
	return req, nil
}

// checkSecretHost holds a request that carries a secret read under the
// owner's agent access (`secret agent-access on`) to the hosts that access
// names: a secret in its address, headers or body goes nowhere else.
func checkSecretHost(ctx context.Context, req *http.Request, body []byte) error {
	if !secrets.UnderAgentAccess(ctx) {
		return nil
	}
	texts := append(carryTexts(req), string(body))
	return secrets.CheckSendHost(ctx, req.URL.String(), texts...)
}

// checkSecretContent holds a request whose content holds a secret read under
// agent access: host binding says where a secret may go, not what it is used
// as, and in the body or a query parameter's name it would be published
// (issue #1418). The content is the body and the query's parameter names; the
// auth surfaces are not. A destination on this machine is exempt, but only
// when what carries the request there is too: a proxy that is not this
// machine receives the whole request, body included. Checked on every hop,
// since a redirect sends the body again.
func checkSecretContent(ctx context.Context, client *http.Client, req *http.Request, body []byte) error {
	if !secrets.UnderAgentAccess(ctx) {
		return nil
	}
	own := ownHost(req.URL.Hostname())
	if v, ok := client.Transport.(viaCheck); ok && v.proxy != nil && !ownHost(v.proxy.Hostname()) {
		own = false
	}
	content := []string{string(body)}
	for name := range req.URL.Query() {
		content = append(content, name)
	}
	return secrets.CheckSendContent(ctx, own, "body or query parameter names", content...)
}

// carryTexts are the parts of req a secret could ride in: its address
// (userinfo included), every header value, and the credentials inside a
// Basic token (base64 of "user:secret" is not a form of the secret alone).
func carryTexts(req *http.Request) []string {
	texts := []string{req.URL.String()}
	for _, vs := range req.Header {
		for _, v := range vs {
			texts = append(texts, v)
			if f := strings.Fields(v); len(f) == 2 && strings.EqualFold(f[0], "basic") {
				if b, err := base64.StdEncoding.DecodeString(f[1]); err == nil {
					texts = append(texts, string(b))
					if _, pw, ok := strings.Cut(string(b), ":"); ok {
						texts = append(texts, pw)
					}
				}
			}
		}
	}
	return texts
}
