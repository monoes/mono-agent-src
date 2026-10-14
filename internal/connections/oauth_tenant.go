package connections

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// tailorOAuth applies cfg.Tailor (per-tenant URLs) for a connection's data.
func tailorOAuth(cfg OAuthConfig, data map[string]interface{}) (OAuthConfig, error) {
	if cfg.Tailor == nil {
		return cfg, nil
	}
	out, err := cfg.Tailor(cfg, data)
	if err != nil {
		return cfg, fmt.Errorf("oauth endpoints: %w", err)
	}
	return out, nil
}

// fillConnectFields copies the OAuth connect fields from values (falling
// back to keep, the data of a connection being reconnected) into data and
// fails naming the required ones that are missing.
func fillConnectFields(fields []CredentialField, data map[string]interface{}, values map[string]string, keep map[string]interface{}) error {
	var missing []string
	for _, f := range fields {
		v := strings.TrimSpace(values[f.Key])
		if v == "" {
			v = getStr(keep, f.Key)
		}
		if v != "" {
			data[f.Key] = v
		} else if f.Required {
			missing = append(missing, f.Key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("this platform's OAuth needs %s first", strings.Join(missing, ", "))
	}
	return nil
}

// salesforceEnvironment points a sandbox connection at test.salesforce.com.
func salesforceEnvironment(cfg OAuthConfig, data map[string]interface{}) (OAuthConfig, error) {
	if strings.EqualFold(getStr(data, "environment"), "sandbox") {
		cfg.AuthURL = strings.Replace(cfg.AuthURL, "//login.salesforce.com/", "//test.salesforce.com/", 1)
		cfg.TokenURL = strings.Replace(cfg.TokenURL, "//login.salesforce.com/", "//test.salesforce.com/", 1)
	}
	return cfg, nil
}

// ConnectOAuth1 runs a platform's OAuth 1.0a flow with the consumer key and
// secret in fields (plus any other fields of the platform's oauth1 form)
// and saves the connection under profileID, replacing the profile's
// existing OAuth 1.0a connection for the platform (a reconnect).
func (m *Manager) ConnectOAuth1(ctx context.Context, platformID string, fields map[string]string, progress func(msg, kind string), profileID string) (*Connection, error) {
	p, ok := Get(platformID)
	if !ok {
		return nil, fmt.Errorf("unknown platform %q", platformID)
	}
	if profileID == "" {
		profileID = "default"
	}
	conn := &Connection{Platform: platformID, Method: MethodOAuth1, Data: map[string]interface{}{}, ProfileID: profileID}
	// Reconnect: reuse the profile's OAuth 1.0a connection for the platform,
	// replacing its data, rather than adding a second one.
	if existing, _ := m.store.ListByPlatform(ctx, platformID, profileID); len(existing) > 0 {
		for _, e := range existing {
			if e.Method == MethodOAuth1 {
				conn.ID, conn.VaultRef, conn.CreatedAt = e.ID, e.VaultRef, e.CreatedAt
				break
			}
		}
	}
	for _, f := range p.Fields[MethodOAuth1] {
		if v := strings.TrimSpace(fields[f.Key]); v != "" {
			conn.Data[f.Key] = v
		} else if f.Required {
			return nil, fmt.Errorf("connect %s: field %q (%s) is required", platformID, f.Key, f.Label)
		}
	}
	if err := m.runOAuth1(ctx, p, conn, 5*time.Minute, progress); err != nil {
		return nil, err
	}
	accountID, err := ValidateConnection(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}
	conn.AccountID = accountID
	conn.Status = "active"
	conn.LastTested = time.Now().UTC().Format(time.RFC3339)
	conn.Label = p.Name
	if accountID != "" {
		conn.Label = fmt.Sprintf("%s – %s", p.Name, accountID)
	}
	if err := m.store.Save(ctx, conn); err != nil {
		return nil, fmt.Errorf("failed to save: %w", err)
	}
	return conn, nil
}

// runOAuth1 runs the three-legged flow for conn, whose data holds the
// consumer key and secret, and stores the access token and secret (and the
// response's other keys) on it.
func (m *Manager) runOAuth1(ctx context.Context, p PlatformDef, conn *Connection, timeout time.Duration, progress func(msg, kind string)) error {
	if p.OAuth1 == nil {
		return fmt.Errorf("platform %q has no OAuth 1.0a endpoints", p.ID)
	}
	key, secret := getStr(conn.Data, OAuth1ConsumerKey), getStr(conn.Data, OAuth1ConsumerSecret)
	if key == "" || secret == "" {
		return fmt.Errorf("oauth1: the connection needs %s and %s", OAuth1ConsumerKey, OAuth1ConsumerSecret)
	}
	// A generic platform's endpoints and signature method are the
	// connection's fields.
	cfg := *p.OAuth1
	for _, f := range []struct {
		dst *string
		key string
	}{
		{&cfg.RequestTokenURL, OAuth1RequestTokenURL},
		{&cfg.AuthURL, OAuth1AuthorizationURL},
		{&cfg.AccessTokenURL, OAuth1AccessTokenURL},
		{&cfg.SignatureMethod, OAuth1SignatureMethod},
	} {
		if *f.dst == "" {
			*f.dst = getStr(conn.Data, f.key)
		}
	}
	vals, err := RunOAuth1Flow(ctx, cfg, key, secret, timeout, progress)
	if err != nil {
		return err
	}
	if cfg.SignatureMethod != "" && !strings.EqualFold(cfg.SignatureMethod, "HMAC-SHA1") {
		conn.Data[OAuth1SignatureMethod] = cfg.SignatureMethod
	}
	for k, v := range vals {
		if k == "oauth_callback_confirmed" {
			continue
		}
		conn.Data[k] = v
	}
	return nil
}

// PromptMissingFields asks on the terminal, reading answers from in (secret
// ones without echo), for each required field that values lacks and adds
// the answers to values.
func PromptMissingFields(fields []CredentialField, values map[string]string, in io.Reader) error {
	var ask []CredentialField
	for _, f := range fields {
		if f.Required && strings.TrimSpace(values[f.Key]) == "" {
			ask = append(ask, f)
		}
	}
	conn := &Connection{Data: map[string]interface{}{}}
	if err := promptFields(ask, conn, nil, bufio.NewReader(in)); err != nil {
		return err
	}
	for k, v := range conn.Data {
		values[k] = v.(string)
	}
	return nil
}
