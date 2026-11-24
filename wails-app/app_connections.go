package main

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/connections"
)

// ─────────────────────────────────────────────────────────────────────────────
// Connections
// ─────────────────────────────────────────────────────────────────────────────

// ListConnections returns all saved connections for the active profile, filtered by platform if non-empty.
// `connect list --json` never carries credential material (Connection.Data).
func (a *App) ListConnections(platform string) []connections.SafeConnection {
	if a.signedOut() != nil {
		return nil
	}
	args := []string{"connect", "list"}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	result := []connections.SafeConnection{}
	if err := a.runConnCLI("", &result, args...); err != nil || result == nil {
		return []connections.SafeConnection{}
	}
	return result
}

// PlatformInfo is a frontend-safe representation of a platform (no OAuth secrets).
type PlatformInfo struct {
	ID         string                                   `json:"id"`
	Name       string                                   `json:"name"`
	Category   string                                   `json:"category"`
	ConnectVia string                                   `json:"connectVia"`
	Methods    []string                                 `json:"methods"`
	Fields     map[string][]connections.CredentialField `json:"fields"`
	IconEmoji  string                                   `json:"iconEmoji"`
}

// ListPlatformsJSON returns all platforms as a JSON string (bypasses Wails
// type serialization), from `connect list --all --json`: the CLI also
// carries the platforms its API packages register, which this process's
// registry does not have. A non-empty connectVia keeps only those platforms.
func (a *App) ListPlatformsJSON(connectVia string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	var platforms []struct {
		ID         string
		Name       string
		Category   string
		ConnectVia string
		Methods    []string
		Fields     map[string][]connections.CredentialField
		IconEmoji  string
	}
	if err := a.runConnCLI("", &platforms, "connect", "list", "--all"); err != nil {
		return "[]"
	}
	result := []PlatformInfo{}
	for _, p := range platforms {
		if connectVia != "" && p.ConnectVia != connectVia {
			continue
		}
		fields := p.Fields
		if fields == nil {
			fields = map[string][]connections.CredentialField{}
		}
		result = append(result, PlatformInfo{ID: p.ID, Name: p.Name, Category: p.Category, ConnectVia: p.ConnectVia,
			Methods: p.Methods, Fields: fields, IconEmoji: p.IconEmoji})
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// PlatformNames maps each platform id to its display name, from
// `monoagentcli connect list --all --json` ("generic_api" → "Generic API
// Key"); the credential picker labels its platform groups with it.
func (a *App) PlatformNames() (map[string]string, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var platforms []struct {
		ID   string
		Name string
	}
	if err := a.runConnCLI("", &platforms, "connect", "list", "--all"); err != nil {
		return nil, err
	}
	names := make(map[string]string, len(platforms))
	for _, p := range platforms {
		if p.ID != "" && p.Name != "" {
			names[p.ID] = p.Name
		}
	}
	return names, nil
}

// TestConnection re-validates a connection by ID ("ok" or "error: …").
// The id is tried as a saved connection (`connect test`, which refreshes an
// expired OAuth token first), then as a browser session id (`login test`),
// then as a platform or node type with an active connection.
func (a *App) TestConnection(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	err := a.runConnCLI("", nil, "connect", "test", id)
	if err == nil {
		return "ok"
	}
	if !isCLINotFound(err) {
		return "error: " + err.Error()
	}
	if n, convErr := strconv.Atoi(id); convErr == nil {
		err := a.runConnCLI("", nil, "login", "test", strconv.Itoa(n))
		if err == nil {
			return "ok"
		}
		if !isCLINotFound(err) {
			return "error: " + err.Error()
		}
	}
	for _, c := range a.ListConnections(nodeTypeToPlatform(id)) {
		if c.Status == "active" {
			return "ok"
		}
	}
	return "error: connection not found"
}

// RemoveConnection deletes a connection by ID, scoped to the active profile.
func (a *App) RemoveConnection(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if err := a.runConnCLI("", nil, "connect", "remove", id); err != nil {
		if isCLINotFound(err) {
			return "error: connection not found"
		}
		return "error: " + err.Error()
	}
	return "ok"
}

// GetConnectionsForPlatform returns connections filtered by platform ID.
// Credential material (Connection.Data) is stripped before crossing the Wails IPC boundary.
func (a *App) GetConnectionsForPlatform(platformID string) []connections.SafeConnection {
	if a.signedOut() != nil {
		return nil
	}
	return a.ListConnections(platformID)
}

// GetOAuthCredentials returns the stored OAuth client_id and client_secret for
// a platform, scoped to the active profile, as JSON. Returns JSON
// {"clientID":"...","clientSecret":"..."} or "" if not set. Credentials are
// per-profile because two connections for the same platform under different
// profiles may need different Azure/OAuth app registrations. The secret is
// revealed because the connection form shows it for editing.
func (a *App) GetOAuthCredentials(platformID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	var view struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := a.runConnCLI("", &view, "connect", "get-oauth-client", platformID, "--reveal"); err != nil {
		return ""
	}
	if view.ClientID == "" && view.ClientSecret == "" {
		return ""
	}
	b, _ := json.Marshal(map[string]string{"clientID": view.ClientID, "clientSecret": view.ClientSecret})
	return string(b)
}

// SetOAuthCredentials saves OAuth client_id and client_secret for a platform,
// scoped to the active profile. clientSecret may be empty for public-client
// OAuth apps (e.g. those using PKCE, like a desktop app registration with no
// client secret). The secret travels on the CLI's stdin, never its argv.
func (a *App) SetOAuthCredentials(platformID, clientID, clientSecret string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if clientID == "" {
		return "error: clientID is required"
	}
	args := []string{"connect", "set-oauth-client", platformID, "--client-id", clientID}
	if clientSecret != "" {
		args = append(args, "--client-secret-stdin")
	}
	if err := a.runConnCLI(clientSecret, nil, args...); err != nil {
		return "error: " + err.Error()
	}
	return "ok"
}

// ShowPlatform returns `connect show <platform> --json` verbatim: the
// platform's methods, each method's fields, its OAuth 2.0 connect fields
// and its OAuth 1.0a endpoints. "" when the CLI fails.
func (a *App) ShowPlatform(platformID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	var view json.RawMessage
	if err := a.runConnCLI("", &view, "connect", "show", platformID); err != nil {
		return ""
	}
	return string(view)
}

// ConnectPlatformOAuth starts an OAuth flow with no connect fields; see
// ConnectPlatformOAuthFields.
func (a *App) ConnectPlatformOAuth(platformID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.ConnectPlatformOAuthFields(platformID, nil, "")
}

// SaveConnectionDirect saves a connection directly from the UI with provided field values.
// fieldValuesJSON is a JSON object string (avoids Wails map serialization issues);
// it reaches the CLI on stdin, never its argv.
// Returns "ok:<id>" on success or "error: ..." on failure.
func (a *App) SaveConnectionDirect(platformID string, method string, fieldValuesJSON string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	var fieldValues map[string]interface{}
	if err := json.Unmarshal([]byte(fieldValuesJSON), &fieldValues); err != nil {
		return "error: invalid field values JSON"
	}
	var conn connections.SafeConnection
	if err := a.runConnCLI(fieldValuesJSON, &conn, "connect", "save", platformID, "--method", method, "--stdin-json"); err != nil {
		return "error: " + err.Error()
	}
	return "ok:" + conn.ID
}

// connCLIError is a monoagentcli call that exited non-zero: its exit code
// (cmd/monoagentcli/exitcodes.go: 2 not found, 3 invalid input, 4 auth) and
// its error message.
type connCLIError struct {
	code int
	msg  string
}

func (e *connCLIError) Error() string { return e.msg }

// isCLINotFound reports whether err is the CLI's not-found exit (2).
func isCLINotFound(err error) bool {
	var f *connCLIError
	return errors.As(err, &f) && f.code == 2
}

// runConnCLI is runMonoCLI (--profile <active> --json, stdin, decoded
// stdout) keeping the exit code, which the connection bindings branch on.
// The message is the last stderr line: the CLI prints its error last,
// after any warnings.
func (a *App) runConnCLI(stdin string, result interface{}, args ...string) error {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return err
	}
	full, err := a.scopedArgs(args...)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(a.ctx, cliBin, full...)
	suppressConsole(cmd)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			lines := strings.Split(strings.TrimSpace(string(ee.Stderr)), "\n")
			msg := strings.TrimSpace(lines[len(lines)-1])
			if msg == "" {
				msg = err.Error()
			}
			return &connCLIError{code: ee.ExitCode(), msg: msg}
		}
		return err
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(out, result)
}
