package apipkg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// AWS AssumeRole (n8n's "AWS (Assume Role)" credential): a connection with
// role_arn calls STS AssumeRole with its keys and signs with the role's
// temporary credentials instead. external_id and role_session_name are
// optional; region picks the regional STS endpoint (else the global one).

type stsCreds struct {
	keyID, secret, session string
	expires                time.Time
}

// stsCache holds temporary role credentials by (endpoint, base key, secret
// hash, role, external id, session name) until shortly before they expire.
var stsCache sync.Map

// stsEndpoint is the STS URL and signing region for a connection region.
func stsEndpoint(region string) (string, string) {
	if region == "" {
		return "https://sts.amazonaws.com/", "us-east-1"
	}
	return "https://sts." + region + ".amazonaws.com/", region
}

// withAssumedRole returns cred with access_key_id, secret_access_key and
// session_token replaced by temporary credentials of cred.role_arn, or cred
// itself when the credential does not sign with SigV4 or names no role.
func withAssumedRole(ctx context.Context, c *Credential, cred map[string]any) (map[string]any, error) {
	if c == nil || c.Inject.AWSSigV4 == nil {
		return cred, nil
	}
	role := strings.TrimSpace(stringify(cred["role_arn"]))
	if role == "" {
		return cred, nil
	}
	s := c.Inject.AWSSigV4
	sc := Scope{Cred: cred}
	base := sigV4Params{Service: "sts"}
	for _, pr := range []struct {
		dst       *string
		tmpl, def string
	}{
		{&base.AccessKeyID, s.AccessKeyID, "{{ cred.access_key_id }}"},
		{&base.SecretAccessKey, s.SecretAccessKey, "{{ cred.secret_access_key }}"},
		{&base.SessionToken, s.SessionToken, "{{ cred.session_token }}"},
	} {
		t := pr.tmpl
		if t == "" {
			t = pr.def
		}
		v, err := renderText(t, sc)
		if err != nil {
			return nil, fmt.Errorf("credential %s: assume role: %w", c.Platform, err)
		}
		*pr.dst = v
	}
	if base.AccessKeyID == "" || base.SecretAccessKey == "" {
		return nil, fmt.Errorf("credential %s: assume role %s: the connection needs access_key_id and secret_access_key to call STS", c.Platform, role)
	}
	externalID := strings.TrimSpace(stringify(cred["external_id"]))
	session := strings.TrimSpace(stringify(cred["role_session_name"]))
	if session == "" {
		session = "mono-agent"
	}
	var endpoint string
	endpoint, base.Region = stsEndpoint(strings.TrimSpace(stringify(cred["region"])))

	sum := sha256.Sum256([]byte(base.SecretAccessKey))
	key := strings.Join([]string{endpoint, base.AccessKeyID, hex.EncodeToString(sum[:8]), role, externalID, session}, "\x00")
	var tc stsCreds
	if v, ok := stsCache.Load(key); ok && time.Now().Before(v.(stsCreds).expires) {
		tc = v.(stsCreds)
	} else {
		var err error
		if tc, err = assumeRole(ctx, endpoint, base, role, externalID, session); err != nil {
			return nil, fmt.Errorf("credential %s: assume role %s: %w", c.Platform, role, err)
		}
		stsCache.Store(key, tc)
	}
	out := make(map[string]any, len(cred))
	for k, v := range cred {
		out[k] = v
	}
	out["access_key_id"] = tc.keyID
	out["secret_access_key"] = tc.secret
	out["session_token"] = tc.session
	return out, nil
}

// assumeRole calls STS AssumeRole (signed with base) and returns the
// role's temporary credentials.
func assumeRole(ctx context.Context, endpoint string, base sigV4Params, role, externalID, session string) (stsCreds, error) {
	form := url.Values{
		"Action": {"AssumeRole"}, "Version": {"2011-06-15"},
		"RoleArn": {role}, "RoleSessionName": {session},
	}
	if externalID != "" {
		form.Set("ExternalId", externalID)
	}
	body := []byte(form.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return stsCreds{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	if err := signSigV4(req, body, base, sigV4Now()); err != nil {
		return stsCreds{}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return stsCreds{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyEcho))
	if err != nil {
		return stsCreds{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return stsCreds{}, fmt.Errorf("STS HTTP %d: %s", resp.StatusCode, errorBodyEcho(raw))
	}
	var r struct {
		C struct {
			KeyID   string `xml:"AccessKeyId"`
			Secret  string `xml:"SecretAccessKey"`
			Session string `xml:"SessionToken"`
			Expires string `xml:"Expiration"`
		} `xml:"AssumeRoleResult>Credentials"`
	}
	if err := xml.Unmarshal(raw, &r); err != nil || r.C.KeyID == "" || r.C.Secret == "" {
		return stsCreds{}, fmt.Errorf("STS response has no credentials: %s", errorBodyEcho(raw))
	}
	exp, err := time.Parse(time.RFC3339, r.C.Expires)
	if err != nil {
		exp = time.Now().Add(15 * time.Minute) // the shortest role session
	}
	return stsCreds{keyID: r.C.KeyID, secret: r.C.Secret, session: r.C.Session, expires: exp.Add(-time.Minute)}, nil
}
