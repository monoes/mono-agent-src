package connections

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/secrets"
)

// Manager orchestrates all connection flows.
type Manager struct {
	store *Store
}

// NewManager creates a Manager backed by db, which must be migrated
// (ApplyMigrations creates the connections tables). The error is always
// nil; it stays for the callers.
func NewManager(db *sql.DB) (*Manager, error) {
	return &Manager{store: NewStore(db)}, nil
}

// ConnectOptions controls how Connect behaves.
type ConnectOptions struct {
	Method       AuthMethod // force a specific method (zero = prompt if multiple)
	OAuthTimeout time.Duration
	FieldValues  map[string]string // pre-filled field values for non-interactive use
	ProfileID    string            // profile to save the connection under (empty = "default")
	// Scopes narrows an umbrella platform's (google, microsoft) consent to
	// these scope groups; empty requests every group. Error elsewhere.
	Scopes []string
}

// Connect authenticates to platform and saves the result. Returns saved Connection.
func (m *Manager) Connect(ctx context.Context, platformID string, opts ConnectOptions) (*Connection, error) {
	// 1. Look up platformID in Registry
	p, ok := Get(platformID)
	if !ok {
		return nil, fmt.Errorf("connect: unknown platform %q", platformID)
	}
	// Reject bad --scopes before any prompt or browser round-trip.
	if len(nonEmpty(opts.Scopes)) > 0 {
		if err := prepareUmbrellaOAuth(&OAuthConfig{}, platformID, opts.Scopes); err != nil {
			return nil, fmt.Errorf("connect: %w", err)
		}
	}

	// 2. Pick method — use a single reader for all stdin interactions
	stdinReader := bufio.NewReader(os.Stdin)
	// stdin now feeds the connect prompts; saving the connection must not
	// read a file-keyring passphrase from it.
	secrets.MarkStdinConsumed()
	method := opts.Method
	if method == "" {
		method = m.pickMethod(p, stdinReader)
	}

	// 3. Validate chosen method is in p.Methods
	supported := false
	for _, m := range p.Methods {
		if m == method {
			supported = true
			break
		}
	}
	if !supported {
		return nil, fmt.Errorf("connect: method %q not supported by platform %q", method, platformID)
	}

	conn := &Connection{
		Platform:  platformID,
		Method:    method,
		Data:      map[string]interface{}{},
		ProfileID: opts.ProfileID,
	}

	// 4. Switch on method
	switch method {
	case MethodOAuth:
		if p.OAuth != nil && len(p.OAuth.ConnectFields) > 0 {
			if err := promptFields(p.OAuth.ConnectFields, conn, opts.FieldValues, stdinReader); err != nil {
				return nil, fmt.Errorf("connect: fields: %w", err)
			}
		}
		if err := m.connectOAuth(ctx, p, conn, opts.OAuthTimeout, opts.Scopes); err != nil {
			return nil, fmt.Errorf("connect: oauth: %w", err)
		}
	case MethodOAuth1:
		if err := m.connectFields(p, method, conn, opts.FieldValues, stdinReader); err != nil {
			return nil, fmt.Errorf("connect: fields: %w", err)
		}
		if err := m.runOAuth1(ctx, p, conn, opts.OAuthTimeout, nil); err != nil {
			return nil, fmt.Errorf("connect: oauth1: %w", err)
		}
	case MethodAPIKey, MethodAppPass, MethodConnStr, MethodSSHKey:
		if err := m.connectFields(p, method, conn, opts.FieldValues, stdinReader); err != nil {
			return nil, fmt.Errorf("connect: fields: %w", err)
		}
	case MethodBrowser:
		return nil, fmt.Errorf("connect: platform %q requires browser-based login — run `monoagentcli login %s`", platformID, platformID)
	default:
		return nil, fmt.Errorf("connect: unsupported method %q", method)
	}

	// 5. Call ValidateConnection to get accountID
	accountID, err := ValidateConnection(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("connect: validate: %w", err)
	}

	// An umbrella re-consent (e.g. adding a scope group) updates the
	// profile's existing connection for the same account in place, so
	// workflows that reference its ID pick up the new scopes.
	if IsUmbrella(platformID) {
		m.adoptSameAccountConnection(ctx, conn, accountID)
	}

	// 6. Set conn.AccountID, conn.Status="active", conn.LastTested=now
	conn.AccountID = accountID
	conn.Status = "active"
	conn.LastTested = time.Now().UTC().Format(time.RFC3339)

	// 7. Auto-generate conn.Label
	if accountID != "" {
		conn.Label = fmt.Sprintf("%s – %s", p.Name, accountID)
	} else {
		conn.Label = p.Name
	}

	// 8. Save the connection
	if err := m.store.Save(ctx, conn); err != nil {
		return nil, fmt.Errorf("connect: save: %w", err)
	}

	// 9. Print success messages
	fmt.Printf("✓ Connected as %s\n", accountID)
	fmt.Printf("✓ Saved as %s (id: %s)\n", conn.Label, conn.ID)

	// 10. Return conn
	return conn, nil
}

// List returns all connections, optionally filtered by platform.
// Get retrieves a single connection by ID.
func (m *Manager) Get(ctx context.Context, id string) (*Connection, error) {
	return m.store.Get(ctx, id)
}

func (m *Manager) List(ctx context.Context, platform, profileID string) ([]Connection, error) {
	if profileID == "" {
		profileID = "default"
	}
	if platform == "" {
		return m.store.ListAll(ctx, profileID)
	}
	result, err := m.store.ListByPlatform(ctx, platform, profileID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return []Connection{}, nil
	}
	return result, nil
}

// Test re-validates a connection and updates its status, returning the
// resolved account identifier. An OAuth access token that is expired (or
// about to be) is silently refreshed first, so a connection with a working
// refresh_token doesn't test as broken just because an hour has passed.
// Nothing is printed to stdout: callers own their output (the CLI's --json
// mode must be the only thing on stdout).
func (m *Manager) Test(ctx context.Context, id string) (string, error) {
	conn, err := m.store.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("test: get connection: %w", err)
	}
	if conn == nil {
		return "", fmt.Errorf("test: connection %q not found", id)
	}
	if conn.Method == MethodOAuth {
		conn, _ = m.store.ensureFreshToken(ctx, conn)
	}

	accountID, err := ValidateConnection(ctx, conn)
	if err != nil {
		_ = m.store.MarkTested(ctx, id, "error", conn.ProfileID)
		return "", err
	}

	if accountID != "" {
		label := conn.Label
		if p, ok := Get(conn.Platform); ok {
			label = fmt.Sprintf("%s – %s", p.Name, accountID)
		}
		// Re-read before saving: ValidateConnection did a network round-trip
		// during which a concurrent refresh may have rotated the tokens.
		// Saving the pre-validation Data blob would clobber them; only the
		// account_id/label should change here. The refresh lock (same one
		// RefreshToken uses) closes the remaining Get→Save window so a
		// refresh that starts after our re-read can't be overwritten either.
		acquired, lockErr := m.store.acquireRefreshLock(ctx, id)
		if lockErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to acquire refresh lock for connection %s: %v\n", id, lockErr)
		} else if acquired {
			if latest, err := m.store.Get(ctx, id, conn.ProfileID); err == nil && latest != nil {
				conn = latest
			}
			conn.AccountID = accountID
			conn.Label = label
			saveErr := m.store.Save(ctx, conn)
			m.store.releaseRefreshLock(context.WithoutCancel(ctx), id)
			if saveErr != nil {
				return "", fmt.Errorf("test: update account: %w", saveErr)
			}
		} else {
			fmt.Fprintf(os.Stderr, "warning: skipping account update for connection %s: refresh lock held by another process\n", id)
		}
	}

	if err := m.store.MarkTested(ctx, id, "active", conn.ProfileID); err != nil {
		return "", fmt.Errorf("test: mark tested: %w", err)
	}
	return accountID, nil
}

// SaveFields validates and saves a new connection from already-collected
// credential field values (the GUI's form, `connect save`), with no
// prompting. The resolved account, when the platform's validator reports
// one, becomes part of the label.
func (m *Manager) SaveFields(ctx context.Context, platformID string, method AuthMethod, data map[string]interface{}, profileID string) (*Connection, error) {
	p, ok := Get(platformID)
	if !ok {
		return nil, fmt.Errorf("unknown platform %q", platformID)
	}
	if profileID == "" {
		profileID = "default"
	}
	if data == nil {
		data = map[string]interface{}{}
	}
	conn := &Connection{
		Platform:  platformID,
		Method:    method,
		Label:     p.Name,
		Data:      data,
		Status:    "active",
		ProfileID: profileID,
	}
	accountID, err := ValidateConnection(ctx, conn)
	if err != nil {
		return nil, err
	}
	if accountID != "" {
		conn.AccountID = accountID
		conn.Label = fmt.Sprintf("%s – %s", p.Name, accountID)
	}
	if err := m.store.Save(ctx, conn); err != nil {
		return nil, fmt.Errorf("save: %w", err)
	}
	return conn, nil
}

// Remove deletes a connection by ID, scoped to profileID.
func (m *Manager) Remove(ctx context.Context, id, profileID string) error {
	return m.store.Delete(ctx, id, profileID)
}

// Refresh silently exchanges a connection's stored refresh_token for a new
// access_token — no browser, no user interaction — the same mechanism used
// automatically whenever a stale connection is resolved for use. Falls back
// to the full interactive OAuth flow only when there's no refresh_token to
// use at all (e.g. a very old connection saved before refresh_token capture
// was added, or a provider that never issued one).
func (m *Manager) Refresh(ctx context.Context, id string, timeout time.Duration) error {
	conn, err := m.store.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("refresh: get connection: %w", err)
	}
	if conn == nil {
		return fmt.Errorf("refresh: connection %q not found", id)
	}

	if conn.Method != MethodOAuth {
		return fmt.Errorf("refresh: only OAuth connections can be refreshed, got method %q", conn.Method)
	}

	p, ok := Get(conn.Platform)
	if !ok {
		return fmt.Errorf("refresh: unknown platform %q", conn.Platform)
	}

	if refreshToken, _ := conn.Data["refresh_token"].(string); refreshToken != "" {
		if err := m.store.RefreshToken(ctx, conn); err != nil {
			return fmt.Errorf("refresh: %w", err)
		}
		// Re-resolve the account identity/AccountID/Label too — a silent
		// token refresh alone would otherwise leave AccountID/LastTested
		// stale even though the connection is genuinely fresh.
		if accountID, err := ValidateConnection(ctx, conn); err == nil {
			conn.AccountID = accountID
			conn.Status = "active"
			conn.LastTested = time.Now().UTC().Format(time.RFC3339)
			if accountID != "" {
				conn.Label = fmt.Sprintf("%s – %s", p.Name, accountID)
			}
			_ = m.store.Save(ctx, conn)
		}
		return nil
	}

	// No refresh_token on file — only path left is a full interactive re-auth
	// (an umbrella connection asks again for the groups it held).
	if err := m.connectOAuth(ctx, p, conn, timeout, GrantedGroups(p.ID, getStr(conn.Data, "scope"))); err != nil {
		return fmt.Errorf("refresh: oauth: %w", err)
	}

	accountID, err := ValidateConnection(ctx, conn)
	if err != nil {
		return fmt.Errorf("refresh: validate: %w", err)
	}

	conn.AccountID = accountID
	conn.Status = "active"
	conn.LastTested = time.Now().UTC().Format(time.RFC3339)
	if accountID != "" {
		conn.Label = fmt.Sprintf("%s – %s", p.Name, accountID)
	}

	if err := m.store.Save(ctx, conn); err != nil {
		return fmt.Errorf("refresh: save: %w", err)
	}

	return nil
}

// pickMethod prompts the user to select an auth method if there are multiple.
// If only 1 method, returns it directly (no prompt).
func (m *Manager) pickMethod(p PlatformDef, r *bufio.Reader) AuthMethod {
	if len(p.Methods) == 1 {
		return p.Methods[0]
	}

	fmt.Printf("Select authentication method for %s:\n", p.Name)
	for i, method := range p.Methods {
		if i == 0 {
			fmt.Printf("  %d) %s (recommended)\n", i+1, method)
		} else {
			fmt.Printf("  %d) %s\n", i+1, method)
		}
	}
	fmt.Print("Choice [1]: ")

	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		line = "1"
	}

	// Parse the digit
	var idx int
	n, _ := fmt.Sscanf(line, "%d", &idx)
	idx-- // convert to 0-based
	if n == 0 || idx < 0 || idx >= len(p.Methods) {
		if n == 0 {
			fmt.Println("  Invalid input, using method 1.")
		}
		idx = 0
	}

	return p.Methods[idx]
}

// ConnectOAuthWithProgress runs the full OAuth connect flow, calling progress(msg, kind) at each step.
// kind is "info", "success", or "error". Returns the saved Connection on success.
// If clientID/clientSecret are non-empty they override env-var lookup.
// profileID scopes the connection; empty string defaults to "default".
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
		profileID = "default"
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
