package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ─────────────────────────────────────────────────────────────────────────────
// Connections
// ─────────────────────────────────────────────────────────────────────────────

// CredentialOption is a lightweight connection summary used to populate
// credential dropdowns in the workflow node inspector.
type CredentialOption struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Platform string `json:"platform"`
	Method   string `json:"method"`
}

// ListCredentialsForNode returns the active profile's connections a
// workflow node type can use (`connect for-node`).
func (a *App) ListCredentialsForNode(nodeType string) []CredentialOption {
	opts := []CredentialOption{}
	if err := a.runConnCLI("", &opts, "connect", "for-node", nodeType); err != nil || opts == nil {
		return []CredentialOption{}
	}
	return opts
}

// ListConnections returns all saved connections for the active profile, filtered by platform if non-empty.
// `connect list --json` never carries credential material (Connection.Data).
func (a *App) ListConnections(platform string) []connections.SafeConnection {
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
	return a.ListConnections(platformID)
}

// GetOAuthCredentials returns the stored OAuth client_id and client_secret for
// a platform, scoped to the active profile, as JSON. Returns JSON
// {"clientID":"...","clientSecret":"..."} or "" if not set. Credentials are
// per-profile because two connections for the same platform under different
// profiles may need different Azure/OAuth app registrations. The secret is
// revealed because the connection form shows it for editing.
func (a *App) GetOAuthCredentials(platformID string) string {
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
	var view json.RawMessage
	if err := a.runConnCLI("", &view, "connect", "show", platformID); err != nil {
		return ""
	}
	return string(view)
}

// ConnectPlatformOAuth starts an OAuth flow with no connect fields; see
// ConnectPlatformOAuthFields.
func (a *App) ConnectPlatformOAuth(platformID string) string {
	return a.ConnectPlatformOAuthFields(platformID, nil, "")
}

// oauthFlow is a running OAuth flow; oauthFlows maps a platform to its
// *oauthFlow (a pointer, so a finished flow removes only itself).
type oauthFlow struct{ cancel context.CancelFunc }

var oauthFlows sync.Map

// trackOAuthFlow registers a new flow for platformID, cancelling the one
// it replaces, and returns its context and the func that ends it.
func trackOAuthFlow(parent context.Context, platformID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	f := &oauthFlow{cancel: cancel}
	if prev, loaded := oauthFlows.Swap(platformID, f); loaded {
		prev.(*oauthFlow).cancel()
	}
	return ctx, func() {
		oauthFlows.CompareAndDelete(platformID, f)
		cancel()
	}
}

// ConnectPlatformOAuthFields starts an OAuth 2.0 or 1.0a flow (`connect
// oauth <platform>`) in a background goroutine; an OAuth 2.0 flow uses the
// OAuth client stored for the active profile. fieldPairs are key=value
// connect fields passed as --field; secretFieldsJSON, a JSON object of the
// secret ones, goes on the CLI's stdin (--stdin-json), never its argv.
// Emits "conn:progress" events with {platform, message, kind} and a final
// "conn:done" event with {platform, success, accountID?, error?}; the CLI
// reports an unknown platform or one without OAuth there. Returns
// "started" immediately, or "error: ..." when the CLI cannot be found.
func (a *App) ConnectPlatformOAuthFields(platformID string, fieldPairs []string, secretFieldsJSON string) string {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	args := oauthConnectArgs(a.getActiveProfileID(), platformID, fieldPairs, secretFieldsJSON != "")
	ctx, finish := trackOAuthFlow(a.ctx, platformID)

	go func() {
		defer finish()
		emit := func(msg, kind string) {
			runtime.EventsEmit(a.ctx, "conn:progress", map[string]interface{}{
				"platform": platformID,
				"message":  msg,
				"kind":     kind,
			})
		}
		done := func(fields map[string]interface{}) {
			fields["platform"] = platformID
			runtime.EventsEmit(a.ctx, "conn:done", fields)
		}
		conn, err := runOAuthConnect(ctx, cliBin, args, secretFieldsJSON, emit)
		if err != nil {
			if ctx.Err() != nil && a.ctx.Err() == nil {
				done(map[string]interface{}{"success": false, "cancelled": true, "error": "Cancelled"})
				return
			}
			done(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		done(map[string]interface{}{"success": true, "accountID": conn.AccountID})
	}()

	return "started"
}

// CancelPlatformOAuth stops the platform's running OAuth flow, which then
// ends with a "conn:done" event marked cancelled. "ok", or "error: ..."
// when no flow is running.
func (a *App) CancelPlatformOAuth(platformID string) string {
	f, ok := oauthFlows.LoadAndDelete(platformID)
	if !ok {
		return "error: no OAuth flow is running for " + platformID
	}
	f.(*oauthFlow).cancel()
	return "ok"
}

// oauthConnectArgs is the argv of `connect oauth`: each key=value pair as
// a --field, and --stdin-json when secret fields come on stdin.
func oauthConnectArgs(profileID, platformID string, fieldPairs []string, secretsOnStdin bool) []string {
	args := []string{"--profile", profileID, "--json", "connect", "oauth", platformID}
	for _, kv := range fieldPairs {
		args = append(args, "--field", kv)
	}
	if secretsOnStdin {
		args = append(args, "--stdin-json")
	}
	return args
}

// runOAuthConnect runs `connect oauth` with args (and stdin), forwarding
// each progress line it writes to stderr, and returns the saved connection.
// The flow prints its "Opening browser: <url>" lines on stdout ahead of the
// connection JSON; they are forwarded as progress too, so the URL is in
// the app when no browser opens.
func runOAuthConnect(ctx context.Context, cliBin string, args []string, stdin string, emit func(msg, kind string)) (*connections.SafeConnection, error) {
	cmd := exec.CommandContext(ctx, cliBin, args...)
	suppressConsole(cmd)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var mu sync.Mutex
	syncEmit := func(msg, kind string) { mu.Lock(); defer mu.Unlock(); emit(msg, kind) }
	var out strings.Builder
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		scanner := bufio.NewScanner(stdout)
		var held string // a line ending in ":" waits for the URL on the next
		for scanner.Scan() {
			line := scanner.Text()
			if out.Len() == 0 && !strings.HasPrefix(line, "{") {
				msg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "→"))
				if held != "" {
					msg, held = held+" "+msg, ""
				}
				if strings.HasSuffix(msg, ":") {
					held = msg
				} else if msg != "" {
					syncEmit(msg, "info")
				}
				continue
			}
			out.WriteString(line + "\n")
		}
	}()
	var lastErr string
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		var ev struct {
			Message string `json:"message"`
			Kind    string `json:"kind"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Message != "" {
			syncEmit(ev.Message, ev.Kind)
		} else if line != "" {
			lastErr = line
		}
	}
	<-outDone
	if err := cmd.Wait(); err != nil {
		if lastErr != "" {
			return nil, errors.New(lastErr)
		}
		return nil, err
	}
	var conn connections.SafeConnection
	if err := json.Unmarshal([]byte(out.String()), &conn); err != nil {
		return nil, fmt.Errorf("reading the saved connection: %w", err)
	}
	return &conn, nil
}

// LoginSocial spawns `monoagentcli login <platform>` as a subprocess, which
// opens the login page as a new tab in the user's real, already-running
// Chrome (via the mono-agent extension) and returns immediately. Log in
// happens entirely by hand; the UI should then call ConfirmSocialLogin once
// the user says they're done, which is the only step that reads cookies
// back out of that tab. Splitting these apart (rather than one call that
// auto-polls until login completes) is what lets bot-verification
// challenges (Google sign-in, Cloudflare, etc.) succeed — continuous
// automated activity during the challenge is itself a signal those systems
// detect. Emits "conn:opened" (tab is up, waiting for the user) or
// "conn:done" with success:false on failure to open it.
func (a *App) LoginSocial(platform string) string {
	pid := strings.ToLower(platform)
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	emit := func(msg, kind string) {
		runtime.EventsEmit(a.ctx, "conn:progress", map[string]interface{}{
			"platform": pid,
			"message":  msg,
			"kind":     kind,
		})
	}

	go func() {
		cmd := exec.CommandContext(a.ctx, cliBin, "--profile", a.getActiveProfileID(), "login", pid)
		suppressConsole(cmd)
		stderr, _ := cmd.StderrPipe()

		if startErr := cmd.Start(); startErr != nil {
			emit(fmt.Sprintf("Failed to start login: %v", startErr), "error")
			runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": false, "error": startErr.Error()})
			return
		}

		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			emit(scanner.Text(), "info")
		}

		if waitErr := cmd.Wait(); waitErr != nil {
			runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": false, "error": waitErr.Error()})
			return
		}
		runtime.EventsEmit(a.ctx, "conn:opened", map[string]interface{}{"platform": pid})
	}()

	return "started"
}

// ConfirmSocialLogin spawns `monoagentcli login confirm <platform>`, the
// one step that reconnects to the Chrome tab LoginSocial opened and reads
// its cookies to capture the session. Call this after the user has finished
// logging in (and any bot-verification challenge) by hand.
// Progress/result are streamed via the same "conn:progress"/"conn:done"
// events LoginSocial uses.
func (a *App) ConfirmSocialLogin(platform string) string {
	pid := strings.ToLower(platform)
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	emit := func(msg, kind string) {
		runtime.EventsEmit(a.ctx, "conn:progress", map[string]interface{}{
			"platform": pid,
			"message":  msg,
			"kind":     kind,
		})
	}

	go func() {
		cmd := exec.CommandContext(a.ctx, cliBin, "--profile", a.getActiveProfileID(), "login", "confirm", pid)
		suppressConsole(cmd)
		stdout, _ := cmd.StdoutPipe()
		stderr, _ := cmd.StderrPipe()

		if startErr := cmd.Start(); startErr != nil {
			emit(fmt.Sprintf("Failed to start: %v", startErr), "error")
			runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": false, "error": startErr.Error()})
			return
		}

		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				emit(scanner.Text(), "info")
			}
		}()

		scanner := bufio.NewScanner(stdout)
		var username string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "username: ") {
				username, _ = strings.CutPrefix(line, "username: ")
			}
		}

		waitErr := cmd.Wait()
		if waitErr != nil {
			runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": false, "error": waitErr.Error()})
			return
		}
		runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": true, "accountID": username})
	}()

	return "started"
}

// SaveConnectionDirect saves a connection directly from the UI with provided field values.
// fieldValuesJSON is a JSON object string (avoids Wails map serialization issues);
// it reaches the CLI on stdin, never its argv.
// Returns "ok:<id>" on success or "error: ..." on failure.
func (a *App) SaveConnectionDirect(platformID string, method string, fieldValuesJSON string) string {
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
	cmd := exec.CommandContext(a.ctx, cliBin, append([]string{"--profile", a.getActiveProfileID(), "--json"}, args...)...)
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
