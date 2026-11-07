package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/monoes/mono-agent/internal/connections"
)

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
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	profile, err := a.profileID()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	args, err := oauthConnectArgs(profile, platformID, fieldPairs, secretFieldsJSON != "")
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	ctx, finish := trackOAuthFlow(a.ctx, platformID)

	go func() {
		defer finish()
		emit := func(msg, kind string) {
			eventsEmit(a.ctx, "conn:progress", map[string]interface{}{
				"platform": platformID,
				"message":  msg,
				"kind":     kind,
			})
		}
		done := func(fields map[string]interface{}) {
			fields["platform"] = platformID
			eventsEmit(a.ctx, "conn:done", fields)
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
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	f, ok := oauthFlows.LoadAndDelete(platformID)
	if !ok {
		return "error: no OAuth flow is running for " + platformID
	}
	f.(*oauthFlow).cancel()
	return "ok"
}

// oauthConnectArgs is the argv of `connect oauth`: each key=value pair as
// a --field, and --stdin-json when secret fields come on stdin.
func oauthConnectArgs(profileID, platformID string, fieldPairs []string, secretsOnStdin bool) ([]string, error) {
	args, err := scopedCLIArgs(profileID, "connect", "oauth", platformID)
	if err != nil {
		return nil, err
	}
	for _, kv := range fieldPairs {
		args = append(args, "--field", kv)
	}
	if secretsOnStdin {
		args = append(args, "--stdin-json")
	}
	return args, nil
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
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	pid := strings.ToLower(platform)
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	profile, err := a.profileID()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	emit := func(msg, kind string) {
		eventsEmit(a.ctx, "conn:progress", map[string]interface{}{
			"platform": pid,
			"message":  msg,
			"kind":     kind,
		})
	}

	go func() {
		cmd := exec.CommandContext(a.ctx, cliBin, "--profile", profile, "login", pid)
		suppressConsole(cmd)
		stderr, _ := cmd.StderrPipe()

		if startErr := cmd.Start(); startErr != nil {
			emit(fmt.Sprintf("Failed to start login: %v", startErr), "error")
			eventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": false, "error": startErr.Error()})
			return
		}

		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			emit(scanner.Text(), "info")
		}

		if waitErr := cmd.Wait(); waitErr != nil {
			eventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": false, "error": waitErr.Error()})
			return
		}
		eventsEmit(a.ctx, "conn:opened", map[string]interface{}{"platform": pid})
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
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	pid := strings.ToLower(platform)
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	profile, err := a.profileID()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	emit := func(msg, kind string) {
		eventsEmit(a.ctx, "conn:progress", map[string]interface{}{
			"platform": pid,
			"message":  msg,
			"kind":     kind,
		})
	}

	go func() {
		cmd := exec.CommandContext(a.ctx, cliBin, "--profile", profile, "login", "confirm", pid)
		suppressConsole(cmd)
		stdout, _ := cmd.StdoutPipe()
		stderr, _ := cmd.StderrPipe()

		if startErr := cmd.Start(); startErr != nil {
			emit(fmt.Sprintf("Failed to start: %v", startErr), "error")
			eventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": false, "error": startErr.Error()})
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
			eventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": false, "error": waitErr.Error()})
			return
		}
		eventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": true, "accountID": username})
	}()

	return "started"
}
