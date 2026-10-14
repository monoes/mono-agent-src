package apipkg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/monoes/mono-agent/internal/connections"
)

// oauthTemplated reports whether an OAuth block's endpoints, scopes or
// extra params read connection fields (per-tenant OAuth).
func oauthTemplated(o *OAuthSpec) bool {
	if hasTemplate(o.AuthURL) || hasTemplate(o.TokenURL) {
		return true
	}
	for _, v := range o.ExtraParams {
		if hasTemplate(v) {
			return true
		}
	}
	for _, v := range o.Scopes {
		if hasTemplate(v) {
			return true
		}
	}
	return false
}

// tailorOAuth renders o's templated endpoints, scopes and extra params with
// a connection's data (its connect fields) into cfg.
func tailorOAuth(o *OAuthSpec, cfg connections.OAuthConfig, data map[string]interface{}) (connections.OAuthConfig, error) {
	sc := Scope{Cred: data}
	var err error
	render := func(name, t string) string {
		if err != nil {
			return ""
		}
		v, e := renderText(t, sc)
		if e != nil {
			err = fmt.Errorf("oauth %s: %w", name, e)
		}
		return v
	}
	cfg.AuthURL = render("auth_url", o.AuthURL)
	cfg.TokenURL = render("token_url", o.TokenURL)
	if len(o.Scopes) > 0 {
		cfg.Scopes = make([]string, 0, len(o.Scopes))
		for _, s := range o.Scopes {
			if v := render("scopes", s); v != "" {
				cfg.Scopes = append(cfg.Scopes, v)
			}
		}
	}
	if len(o.ExtraParams) > 0 {
		cfg.ExtraParams = make(map[string]string, len(o.ExtraParams))
		for k, t := range o.ExtraParams {
			if v := render("extra_params."+k, t); v != "" {
				cfg.ExtraParams[k] = v
			}
		}
	}
	return cfg, err
}

// metaCache holds metadata lookups by (URL template, token hash).
var metaCache sync.Map

// fetchMetadata sends the metadata request with cred (holding the OAuth
// token) and returns the values of m.Fields found in its JSON response.
func fetchMetadata(ctx context.Context, m *MetadataSpec, cred map[string]any) (map[string]any, error) {
	sc := Scope{Cred: cred}
	u, err := renderText(m.URL, sc)
	if err != nil {
		return nil, fmt.Errorf("oauth metadata url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("oauth metadata: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if err := applyHeaders(req.Header, m.Headers, sc); err != nil {
		return nil, fmt.Errorf("oauth metadata: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth metadata: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyEcho))
	if err != nil {
		return nil, fmt.Errorf("oauth metadata: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth metadata: HTTP %d: %s", resp.StatusCode, errorBodyEcho(body))
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("oauth metadata: response is not JSON: %s", errorBodyEcho(body))
	}
	out := map[string]any{}
	for k, path := range m.Fields {
		if v := lookupPath(doc, strings.Split(path, ".")); !isEmpty(v) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("oauth metadata: the response has none of %v", m.Fields)
	}
	return out, nil
}

// withMetadata adds the credential's metadata values to cred when an OAuth
// connection lacks them (connected before the lookup existed, or saved by
// hand), caching the lookup per token.
func withMetadata(ctx context.Context, c *Credential, cred map[string]any) (map[string]any, error) {
	if c == nil || c.OAuth == nil || c.OAuth.Metadata == nil || isEmpty(cred["access_token"]) {
		return cred, nil
	}
	m := c.OAuth.Metadata
	complete := true
	for k := range m.Fields {
		if isEmpty(cred[k]) {
			complete = false
		}
	}
	if complete {
		return cred, nil
	}
	sum := sha256.Sum256([]byte(stringify(cred["access_token"])))
	key := m.URL + "\x00" + hex.EncodeToString(sum[:])
	var vals map[string]any
	if v, ok := metaCache.Load(key); ok {
		vals = v.(map[string]any)
	} else {
		var err error
		if vals, err = fetchMetadata(ctx, m, cred); err != nil {
			return nil, fmt.Errorf("credential %s: %w", c.Platform, err)
		}
		metaCache.Store(key, vals)
	}
	out := make(map[string]any, len(cred)+len(vals))
	for k, v := range cred {
		out[k] = v
	}
	for k, v := range vals {
		if isEmpty(out[k]) {
			out[k] = v
		}
	}
	return out, nil
}
