package connections

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/profiledir"
)

// ConnectOAuthWithProgress runs the full OAuth connect flow, calling progress(msg, kind) at each step.
// kind is "info", "success", or "error". Returns the saved Connection on success.
// If clientID/clientSecret are non-empty they override env-var lookup.
// profileID scopes the connection; an empty one is profiledir.ErrNoProfile.
func (m *Manager) ConnectOAuthWithProgress(ctx context.Context, platformID string, progress func(msg, kind string), clientID, clientSecret, profileID string) (*Connection, error) {
	return m.ConnectOAuthWithScopes(ctx, platformID, progress, clientID, clientSecret, profileID, nil)
}

// ConnectOAuthWithScopes is ConnectOAuthWithProgress with the scope groups
// an umbrella platform (google, microsoft) should request; nil requests
// every group, and scope groups on any other platform are an error.
func (m *Manager) ConnectOAuthWithScopes(ctx context.Context, platformID string, progress func(msg, kind string), clientID, clientSecret, profileID string, scopeGroups []string) (*Connection, error) {
	return m.ConnectOAuthWithFields(ctx, platformID, progress, clientID, clientSecret, profileID, scopeGroups, nil)
}

// ConnectOAuthWithFields is ConnectOAuthWithScopes with the values of the
// platform's OAuth connect fields (a tenant subdomain, a Salesforce
// environment); a reconnect keeps the earlier connection's values for the
// fields not given.
func (m *Manager) ConnectOAuthWithFields(ctx context.Context, platformID string, progress func(msg, kind string), clientID, clientSecret, profileID string, scopeGroups []string, fields map[string]string) (*Connection, error) {
	p, ok := Get(platformID)
	if !ok {
		return nil, fmt.Errorf("unknown platform %q", platformID)
	}
	if p.OAuth == nil {
		return nil, fmt.Errorf("platform %q does not support OAuth", platformID)
	}

	if profileID == "" {
		return nil, profiledir.ErrNoProfile
	}
	// Reuse existing connection for this platform+profile if one exists (reconnect flow).
	conn := &Connection{
		Platform:  platformID,
		Method:    MethodOAuth,
		Data:      map[string]interface{}{},
		ProfileID: profileID,
	}
	if existing, _ := m.store.ListByPlatform(ctx, platformID, profileID); len(existing) > 0 {
		for i := range existing {
			if existing[i].Method == MethodOAuth {
				conn = &existing[i]
				break
			}
		}
	}
	old := conn.Data
	conn.Data = map[string]interface{}{} // clear old tokens
	if err := fillConnectFields(p.OAuth.ConnectFields, conn.Data, fields, old); err != nil {
		return nil, err
	}

	cfg, err := tailorOAuth(*p.OAuth, conn.Data)
	if err != nil {
		return nil, err
	}
	cfg.PlatformID = platformID
	if err := prepareUmbrellaOAuth(&cfg, platformID, scopeGroups); err != nil {
		return nil, err
	}
	if clientID != "" {
		cfg.ClientID = clientID
	}
	if clientSecret != "" {
		cfg.ClientSecret = clientSecret
	}
	if cfg.ClientID == "" {
		cfg.ClientID, cfg.ClientSecret = m.store.lookupOAuthClient(ctx, platformID, profileID)
	}
	envPrefix := oauthEnvPrefix(p.ID)
	if cfg.ClientSecret == "" {
		cfg.ClientSecret = os.Getenv(envPrefix + "CLIENT_SECRET")
	}
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("missing OAuth credentials — set %sCLIENT_ID and %sCLIENT_SECRET", envPrefix, envPrefix)
	}

	// A refused caller is told so before any line claims a page is opening.
	if err := refuseBrowserOpen(); err != nil {
		return nil, err
	}
	if progress != nil {
		progress("Opening browser for authorization…", "info")
	}

	result, err := RunOAuthFlow(ctx, cfg, 5*time.Minute, progress)
	if err != nil {
		return nil, err
	}

	// Persist the client credentials that just worked so silent token refresh
	// works in any later process without env vars or manual set-oauth-client.
	if err := m.store.SaveOAuthClient(ctx, platformID, profileID, cfg.ClientID, cfg.ClientSecret); err != nil && progress != nil {
		progress(fmt.Sprintf("warning: could not persist OAuth client credentials for silent refresh: %v", err), "error")
	}

	copyTokenFields(conn.Data, cfg.TokenFields, result.Extra)
	conn.Data["access_token"] = result.AccessToken
	conn.Data["refresh_token"] = result.RefreshToken
	conn.Data["token_type"] = result.TokenType
	conn.Data["scope"] = result.Scope
	conn.Data["instance_url"] = result.InstanceURL
	if result.ExpiresIn > 0 {
		expiresAt := time.Now().UTC().Add(time.Duration(result.ExpiresIn) * time.Second)
		conn.Data["expires_at"] = expiresAt.Format(time.RFC3339)
	} else {
		conn.Data["expires_at"] = ""
	}
	recordUmbrellaGrant(conn, platformID, cfg.Scopes)
	if cfg.AfterToken != nil {
		if err := cfg.AfterToken(ctx, conn.Data); err != nil {
			return nil, fmt.Errorf("after token: %w", err)
		}
	}

	if progress != nil {
		progress("Validating your account…", "info")
	}

	accountID, err := ValidateConnection(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	conn.AccountID = accountID
	conn.Status = "active"
	conn.LastTested = time.Now().UTC().Format(time.RFC3339)
	if accountID != "" {
		conn.Label = fmt.Sprintf("%s – %s", p.Name, accountID)
	} else {
		conn.Label = p.Name
	}

	if progress != nil {
		progress("Saving connection…", "info")
	}

	if err := m.store.Save(ctx, conn); err != nil {
		return nil, fmt.Errorf("failed to save: %w", err)
	}

	label := accountID
	if label == "" {
		label = conn.Label
	}
	if progress != nil {
		progress(fmt.Sprintf("Connected as %s", label), "success")
	}

	return conn, nil
}

// connectOAuth runs the OAuth flow for a platform and populates conn.Data.
func (m *Manager) connectOAuth(ctx context.Context, p PlatformDef, conn *Connection, timeout time.Duration, scopeGroups []string) error {
	if p.OAuth == nil {
		return fmt.Errorf("connectOAuth: platform %q has no OAuth config", p.ID)
	}

	cfg, err := tailorOAuth(*p.OAuth, conn.Data)
	if err != nil {
		return fmt.Errorf("connectOAuth: %w", err)
	}
	cfg.PlatformID = p.ID
	if err := prepareUmbrellaOAuth(&cfg, p.ID, scopeGroups); err != nil {
		return fmt.Errorf("connectOAuth: %w", err)
	}

	// Stored per-profile credentials first (set via `connect set-oauth-client`
	// or persisted by a previous successful connect), then env vars
	// MONOAGENT_{UPPERCASE_PLATFORM}_CLIENT_ID and _CLIENT_SECRET; umbrella
	// platforms then fall back to their per-product clients.
	if id, sec := m.store.lookupOAuthClient(ctx, p.ID, conn.ProfileID); id != "" {
		cfg.ClientID, cfg.ClientSecret = id, sec
	}
	envPrefix := oauthEnvPrefix(p.ID)

	if cfg.ClientID == "" {
		return fmt.Errorf("connectOAuth: missing ClientID for platform %q (set %sCLIENT_ID)", p.ID, envPrefix)
	}

	result, err := RunOAuthFlow(ctx, cfg, timeout, nil)
	if err != nil {
		return fmt.Errorf("connectOAuth: %w", err)
	}

	// Persist the client credentials that just worked so silent token refresh
	// (connect refresh, ensureFreshToken on use) works in any later process
	// without this env var — without this, the token silently dies after ~1h
	// and only a full interactive re-login can revive it.
	if err := m.store.SaveOAuthClient(ctx, p.ID, conn.ProfileID, cfg.ClientID, cfg.ClientSecret); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not persist OAuth client credentials for silent refresh: %v\n", err)
	}

	// Populate conn.Data
	copyTokenFields(conn.Data, cfg.TokenFields, result.Extra)
	conn.Data["access_token"] = result.AccessToken
	conn.Data["refresh_token"] = result.RefreshToken
	conn.Data["token_type"] = result.TokenType
	conn.Data["scope"] = result.Scope
	conn.Data["instance_url"] = result.InstanceURL

	// expires_at as RFC3339
	if result.ExpiresIn > 0 {
		expiresAt := time.Now().UTC().Add(time.Duration(result.ExpiresIn) * time.Second)
		conn.Data["expires_at"] = expiresAt.Format(time.RFC3339)
	} else {
		conn.Data["expires_at"] = ""
	}
	recordUmbrellaGrant(conn, p.ID, cfg.Scopes)
	if cfg.AfterToken != nil {
		if err := cfg.AfterToken(ctx, conn.Data); err != nil {
			return fmt.Errorf("connectOAuth: after token: %w", err)
		}
	}

	return nil
}

// connectFields prompts the user to fill in credential fields and populates conn.Data.
func (m *Manager) connectFields(p PlatformDef, method AuthMethod, conn *Connection, prefilled map[string]string, reader *bufio.Reader) error {
	fields, ok := p.Fields[method]
	if !ok {
		return fmt.Errorf("connectFields: platform %q has no fields for method %q", p.ID, method)
	}
	return promptFields(fields, conn, prefilled, reader)
}

// promptFields fills conn.Data with fields, from prefilled or the terminal.
func promptFields(fields []CredentialField, conn *Connection, prefilled map[string]string, reader *bufio.Reader) error {
	for _, field := range fields {
		// Check prefilled map first
		if prefilled != nil {
			if val, ok := prefilled[field.Key]; ok {
				if field.Required && val == "" {
					return fmt.Errorf("connectFields: required field %q is empty", field.Key)
				}
				conn.Data[field.Key] = val
				continue
			}
		}

		// Print help if available
		if field.HelpText != "" {
			fmt.Printf("  %s\n", field.HelpText)
		}
		if field.HelpURL != "" {
			fmt.Printf("  See: %s\n", field.HelpURL)
		}

		fmt.Printf("%s: ", field.Label)

		var value string
		if field.Secret {
			value = readSecret(reader)
		} else {
			line, _ := reader.ReadString('\n')
			value = strings.TrimSpace(line)
		}

		if field.Required && value == "" {
			return fmt.Errorf("connectFields: required field %q is empty", field.Key)
		}

		if value != "" {
			conn.Data[field.Key] = value
		}
	}

	return nil
}

// readSecret reads a secret value from the terminal with echo disabled.
func readSecret(r *bufio.Reader) string {
	if err := exec.Command("stty", "-echo").Run(); err == nil {
		defer func() {
			_ = exec.Command("stty", "echo").Run()
			fmt.Println()
		}()
	}
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}
