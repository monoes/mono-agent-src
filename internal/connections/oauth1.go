package connections

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MethodOAuth1 is the OAuth 1.0a three-legged flow (Trello, Twitter/X v1.1).
const MethodOAuth1 AuthMethod = "oauth1"

// Connection data keys of an OAuth 1.0a connection.
const (
	OAuth1ConsumerKey    = "consumer_key"
	OAuth1ConsumerSecret = "consumer_secret"
	OAuth1Token          = "oauth_token"
	OAuth1TokenSecret    = "oauth_token_secret"
)

// OAuth1Config holds a platform's OAuth 1.0a endpoints (RFC 5849). The
// consumer key and secret are the connection's consumer_key and
// consumer_secret fields; the flow stores oauth_token and
// oauth_token_secret (plus any other keys the access-token response has,
// e.g. Twitter's screen_name).
type OAuth1Config struct {
	RequestTokenURL string
	AuthURL         string // may carry its own query (Trello: scope, expiration, name)
	AccessTokenURL  string
	SignatureMethod string // HMAC-SHA1 (default), HMAC-SHA256, HMAC-SHA512, PLAINTEXT
	CallbackPort    int    // default 9876
}

// OAuth1Params are the inputs of one OAuth 1.0a request signature.
type OAuth1Params struct {
	ConsumerKey, ConsumerSecret string
	Token, TokenSecret          string
	SignatureMethod             string
	Realm                       string
	// Extra oauth_* parameters signed and sent in the header
	// (oauth_callback, oauth_verifier).
	Extra map[string]string
	// Nonce and Timestamp are generated when empty (tests pin them).
	Nonce     string
	Timestamp int64
}

// SignOAuth1 sets req's Authorization header to an OAuth 1.0a signature
// over the method, the base URL, the query and — for a form body — the form
// parameters (RFC 5849 §3.4). body is the request body.
func SignOAuth1(req *http.Request, body []byte, p OAuth1Params) error {
	if p.ConsumerKey == "" {
		return fmt.Errorf("oauth1: the credential has no consumer key")
	}
	method := strings.ToUpper(p.SignatureMethod)
	if method == "" {
		method = "HMAC-SHA1"
	}
	var hf func() hash.Hash
	switch method {
	case "HMAC-SHA1":
		hf = sha1.New
	case "HMAC-SHA256":
		hf = sha256.New
	case "HMAC-SHA512":
		hf = sha512.New
	case "PLAINTEXT":
	default:
		return fmt.Errorf("oauth1: signature method %q is not HMAC-SHA1, HMAC-SHA256, HMAC-SHA512 or PLAINTEXT", p.SignatureMethod)
	}
	if p.Nonce == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return fmt.Errorf("oauth1: nonce: %w", err)
		}
		p.Nonce = hex.EncodeToString(b)
	}
	if p.Timestamp == 0 {
		p.Timestamp = time.Now().Unix()
	}
	oauth := map[string]string{
		"oauth_consumer_key":     p.ConsumerKey,
		"oauth_nonce":            p.Nonce,
		"oauth_signature_method": method,
		"oauth_timestamp":        strconv.FormatInt(p.Timestamp, 10),
		"oauth_version":          "1.0",
	}
	if p.Token != "" {
		oauth["oauth_token"] = p.Token
	}
	for k, v := range p.Extra {
		oauth[k] = v
	}

	key := oauthEscape(p.ConsumerSecret) + "&" + oauthEscape(p.TokenSecret)
	var sig string
	if hf == nil {
		sig = key
	} else {
		var params [][2]string
		for k, vs := range req.URL.Query() {
			for _, v := range vs {
				params = append(params, [2]string{k, v})
			}
		}
		if isFormBody(req.Header.Get("Content-Type")) && len(body) > 0 {
			form, err := url.ParseQuery(string(body))
			if err != nil {
				return fmt.Errorf("oauth1: form body: %w", err)
			}
			for k, vs := range form {
				for _, v := range vs {
					params = append(params, [2]string{k, v})
				}
			}
		}
		for k, v := range oauth {
			params = append(params, [2]string{k, v})
		}
		base := strings.ToUpper(req.Method) + "&" + oauthEscape(oauthBaseURL(req.URL)) + "&" + oauthEscape(normalizeOAuthParams(params))
		m := hmac.New(hf, []byte(key))
		m.Write([]byte(base))
		sig = base64.StdEncoding.EncodeToString(m.Sum(nil))
	}
	oauth["oauth_signature"] = sig

	names := make([]string, 0, len(oauth))
	for k := range oauth {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names)+1)
	if p.Realm != "" {
		parts = append(parts, `realm="`+oauthEscape(p.Realm)+`"`)
	}
	for _, k := range names {
		parts = append(parts, oauthEscape(k)+`="`+oauthEscape(oauth[k])+`"`)
	}
	req.Header.Set("Authorization", "OAuth "+strings.Join(parts, ", "))
	return nil
}

func isFormBody(contentType string) bool {
	mt, _, _ := mime.ParseMediaType(contentType)
	return mt == "application/x-www-form-urlencoded"
}

// oauthBaseURL is the scheme and host in lower case, without a default
// port, plus the path — no query or fragment (RFC 5849 §3.4.1.2).
func oauthBaseURL(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && !(scheme == "http" && port == "80") && !(scheme == "https" && port == "443") {
		host += ":" + port
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return scheme + "://" + host + path
}

// normalizeOAuthParams encodes each name and value, sorts by name then
// value and joins them as name=value&… (RFC 5849 §3.4.1.3.2).
func normalizeOAuthParams(params [][2]string) string {
	enc := make([]string, len(params))
	for i, p := range params {
		enc[i] = oauthEscape(p[0]) + "=" + oauthEscape(p[1])
	}
	sort.Slice(enc, func(i, j int) bool {
		ki, vi, _ := strings.Cut(enc[i], "=")
		kj, vj, _ := strings.Cut(enc[j], "=")
		if ki != kj {
			return ki < kj
		}
		return vi < vj
	})
	return strings.Join(enc, "&")
}

// oauthEscape is RFC 3986 percent-encoding: only unreserved characters stay.
func oauthEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// oauth1Post sends a signed, bodiless POST to a token endpoint and decodes
// its form-encoded answer.
func oauth1Post(endpoint string, p OAuth1Params) (url.Values, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if err := SignOAuth1(req, nil, p); err != nil {
		return nil, err
	}
	resp, err := tokenHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned status %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	vals, err := url.ParseQuery(strings.TrimSpace(string(body)))
	if err != nil || vals.Get("oauth_token") == "" {
		return nil, fmt.Errorf("%s: response has no oauth_token: %s", endpoint, strings.TrimSpace(string(body)))
	}
	return vals, nil
}
