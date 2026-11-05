package connections

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/profiledir"
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
	ProfileID    string            // profile to save the connection under (required)
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
		if err := refuseBrowserOpen(); err != nil { // before the prompts: a refused caller must not block on stdin
			return nil, fmt.Errorf("connect: oauth: %w", err)
		}
		if p.OAuth != nil && len(p.OAuth.ConnectFields) > 0 {
			if err := promptFields(p.OAuth.ConnectFields, conn, opts.FieldValues, stdinReader); err != nil {
				return nil, fmt.Errorf("connect: fields: %w", err)
			}
		}
		if err := m.connectOAuth(ctx, p, conn, opts.OAuthTimeout, opts.Scopes); err != nil {
			return nil, fmt.Errorf("connect: oauth: %w", err)
		}
	case MethodOAuth1:
		if err := refuseBrowserOpen(); err != nil {
			return nil, fmt.Errorf("connect: oauth1: %w", err)
		}
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
		return nil, profiledir.ErrNoProfile
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
		return nil, profiledir.ErrNoProfile
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
