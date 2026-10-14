package apipkg

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/connections"
)

// oauthTokenKeys are what an OAuth connection stores (internal/connections).
var oauthTokenKeys = []string{"access_token", "refresh_token", "token_type", "scope", "expires_at", "instance_url"}

// sigV4Now is the signing clock (tests pin it).
var sigV4Now = time.Now

// needsCredential reports whether the package cannot run without one.
func (p *Package) needsCredential() bool {
	if len(p.Credentials) == 0 {
		return false
	}
	for i := range p.Credentials {
		if p.Credentials[i].hasMethod("none") {
			return false
		}
	}
	return true
}

// selectCredential picks the package credential that cred satisfies: the
// first whose required fields are all present (an OAuth-only credential
// needs access_token). With none satisfied it returns the first.
func (p *Package) selectCredential(cred map[string]any) *Credential {
	if len(p.Credentials) == 0 {
		return nil
	}
	for i := range p.Credentials {
		if credentialSatisfied(&p.Credentials[i], cred) {
			return &p.Credentials[i]
		}
	}
	return &p.Credentials[0]
}

func credentialSatisfied(c *Credential, cred map[string]any) bool {
	required := 0
	for _, f := range c.Fields {
		if !f.Required {
			continue
		}
		required++
		if isEmpty(cred[f.Key]) {
			// An OAuth connection carries tokens instead of form fields.
			if c.hasMethod("oauth") && !isEmpty(cred["access_token"]) {
				continue
			}
			return false
		}
	}
	if required == 0 && c.hasMethod("oauth") && len(c.Fields) == 0 {
		return !isEmpty(cred["access_token"])
	}
	return true
}

// injectAuth authenticates req with credential c. body is the request body
// (needed by SigV4). Missing credentials are reported by the caller.
func injectAuth(req *http.Request, c *Credential, sc Scope, body []byte) error {
	if c == nil {
		return nil
	}
	in := c.Inject
	for k, tv := range in.Headers {
		v, err := renderText(tv, sc)
		if err != nil {
			return fmt.Errorf("credential %s: header %s: %w", c.Platform, k, err)
		}
		if v != "" && !onlyPrefix(tv, v, sc) {
			req.Header.Set(k, v)
		}
	}
	if len(in.Query) > 0 {
		q := req.URL.Query()
		for k, tv := range in.Query {
			v, err := renderText(tv, sc)
			if err != nil {
				return fmt.Errorf("credential %s: query %s: %w", c.Platform, k, err)
			}
			if v != "" {
				q.Set(k, v)
			}
		}
		req.URL.RawQuery = q.Encode()
	}
	if in.Basic != nil {
		u, err := renderText(in.Basic.Username, sc)
		if err != nil {
			return fmt.Errorf("credential %s: basic username: %w", c.Platform, err)
		}
		pw, err := renderText(in.Basic.Password, sc)
		if err != nil {
			return fmt.Errorf("credential %s: basic password: %w", c.Platform, err)
		}
		if u != "" || pw != "" {
			req.SetBasicAuth(u, pw)
		}
	}
	if a := in.AzureSharedKey; a != nil {
		if err := signAzureSharedKey(req, a, sc); err != nil {
			return fmt.Errorf("credential %s: %w", c.Platform, err)
		}
	}
	if a := in.AzureCosmos; a != nil {
		if err := signAzureCosmos(req, a, sc); err != nil {
			return fmt.Errorf("credential %s: %w", c.Platform, err)
		}
	}
	if h := in.HMAC; h != nil {
		hsc := sc
		hsc.Req = map[string]any{"method": req.Method, "path": req.URL.EscapedPath(), "query": req.URL.RawQuery, "body": string(body)}
		secret, err := renderText(h.Secret, hsc)
		if err != nil {
			return fmt.Errorf("credential %s: hmac secret: %w", c.Platform, err)
		}
		msg, err := renderText(h.Message, hsc)
		if err != nil {
			return fmt.Errorf("credential %s: hmac message: %w", c.Platform, err)
		}
		var hf func() hash.Hash
		switch strings.ToUpper(h.Alg) {
		case "SHA1":
			hf = sha1.New
		case "SHA512":
			hf = sha512.New
		default:
			hf = sha256.New
		}
		m := hmac.New(hf, []byte(secret))
		m.Write([]byte(msg))
		sig := base64.StdEncoding.EncodeToString(m.Sum(nil))
		if h.Encoding == "hex" {
			sig = hex.EncodeToString(m.Sum(nil))
		}
		req.Header.Set(h.Header, h.Prefix+sig)
	}
	if j := in.JWT; j != nil {
		tok, err := signJWT(j, sc)
		if err != nil {
			return fmt.Errorf("credential %s: jwt: %w", c.Platform, err)
		}
		h, prefix := j.Header, j.Prefix
		if h == "" {
			h = "Authorization"
		}
		if prefix == "" && h == "Authorization" {
			prefix = "Bearer "
		}
		req.Header.Set(h, prefix+tok)
	}
	if o := in.OAuth1; o != nil {
		if err := signOAuth1(req, c, o, sc, body); err != nil {
			return fmt.Errorf("credential %s: %w", c.Platform, err)
		}
	}
	if s := in.AWSSigV4; s != nil {
		var p sigV4Params
		pairs := []struct {
			dst  *string
			tmpl string
			def  string
		}{
			{&p.Service, s.Service, ""},
			{&p.Region, s.Region, ""},
			{&p.AccessKeyID, s.AccessKeyID, "{{ cred.access_key_id }}"},
			{&p.SecretAccessKey, s.SecretAccessKey, "{{ cred.secret_access_key }}"},
			{&p.SessionToken, s.SessionToken, "{{ cred.session_token }}"},
		}
		for _, pr := range pairs {
			t := pr.tmpl
			if t == "" {
				t = pr.def
			}
			v, err := renderText(t, sc)
			if err != nil {
				return fmt.Errorf("credential %s: aws_sigv4: %w", c.Platform, err)
			}
			*pr.dst = v
		}
		if err := signSigV4(req, body, p, sigV4Now()); err != nil {
			return fmt.Errorf("credential %s: %w", c.Platform, err)
		}
	}
	return nil
}

// onlyPrefix reports a header template whose expressions all came out
// empty, leaving only its literal text (e.g. "Bearer "): such a header is
// not sent.
func onlyPrefix(tmplSrc, rendered string, sc Scope) bool {
	t, err := parseTemplate(tmplSrc)
	if err != nil || len(t.parts) < 2 {
		return false
	}
	for _, p := range t.parts {
		if p.expr == nil {
			continue
		}
		v, err := p.expr.eval(sc)
		if err != nil || !isEmpty(v) {
			return false
		}
	}
	return true
}

// signOAuth1 signs req with OAuth 1.0a from the credential's keys.
func signOAuth1(req *http.Request, c *Credential, o *OAuth1Inject, sc Scope, body []byte) error {
	var p connections.OAuth1Params
	method := o.SignatureMethod
	if method == "" && c.OAuth != nil {
		method = c.OAuth.SignatureMethod
	}
	for _, pr := range []struct {
		dst       *string
		tmpl, def string
	}{
		{&p.ConsumerKey, o.ConsumerKey, "{{ cred.consumer_key }}"},
		{&p.ConsumerSecret, o.ConsumerSecret, "{{ cred.consumer_secret }}"},
		{&p.Token, o.Token, "{{ cred.oauth_token }}"},
		{&p.TokenSecret, o.TokenSecret, "{{ cred.oauth_token_secret }}"},
		{&p.Realm, o.Realm, ""},
		{&p.SignatureMethod, method, ""},
	} {
		t := pr.tmpl
		if t == "" {
			t = pr.def
		}
		v, err := renderText(t, sc)
		if err != nil {
			return fmt.Errorf("oauth1: %w", err)
		}
		*pr.dst = v
	}
	return connections.SignOAuth1(req, body, p)
}
