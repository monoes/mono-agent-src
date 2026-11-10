// wails-app: the start of a mission's pursuit from the app (docs: design-s8b-app-start).
//
// Two bindings and nothing else: PursuitStartPreview (the CLI's `task session pursuit preview`, which writes nothing) and
// PursuitStart (the CLI's `task session pursuit begin`, which confirms the goal and starts the pursuit in one transaction).
// The app holds no authority and no number: the criteria, the dollars, the limits' defaults and the phrase come from the
// CLI's document. A start needs, besides the operator gate and the typed phrase, the token of a preview of this session and a
// click on "Start pursuit" in a native dialog of the operating system whose words Go builds from that preview (the goal last,
// as data). A script in the page can raise the dialog; it cannot click it. That a person may click it by habit is a known and
// accepted limit (SECURITY.md); the dialog waits at least pursuitMinThink before a "Yes" counts.
package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

const (
	// pursuitDialogTitle and pursuitStartLabel are the title of the confirmation and its affirmative button.
	pursuitDialogTitle = "Start a mission pursuit"
	pursuitStartLabel  = "Start pursuit"
	// pursuitMinThink: a "Yes" sooner than this after the dialog was shown is not a decision (it counts as declined).
	pursuitMinThink = 2000 * time.Millisecond
)

var (
	// pursuitStartMu keeps two starts from running at once, the dialog included: a second click is refused, not queued.
	pursuitStartMu sync.Mutex
	// pursuitPreviewMu keeps previews one at a time; pursuitPreviews bounds them per profile.
	pursuitPreviewMu sync.Mutex
	pursuitPreviews  renderLimiter
)

// pursuitPreviewArgv is the argv of `task session pursuit preview`: a limit left at 0 is left out, so the CLI's default applies.
func pursuitPreviewArgv(profile, session string, lim PursuitLimits) ([]string, error) {
	args := []string{"task", "session", "pursuit", "preview", session}
	args = appendLimitFlags(args, lim, false)
	return scopedCLIArgs(profile, args...)
}

func appendLimitFlags(args []string, lim PursuitLimits, all bool) []string {
	for _, f := range []struct {
		name string
		v    int
	}{{"--parked-max=", lim.ParkedMax}, {"--pct=", lim.Pct}, {"--wakes-max=", lim.WakesMax}} {
		if all || f.v > 0 {
			args = append(args, f.name+strconv.Itoa(f.v))
		}
	}
	return args
}

// pursuitBeginArgv is the ONLY argv in this module that begins a pursuit (TestAppBuildsPursuitBeginOnlyThroughTheTokenPath):
// the verb, the session, the goal hash, the consent digest and the chat the preview showed, the three limits, and the phrase in
// the --flag=value form so that text starting with a dash is a value and never a flag. Nothing the page sent is a flag.
func pursuitBeginArgv(profile string, f pursuitFacts, phrase string) ([]string, error) {
	args := []string{"task", "session", "pursuit", "begin", strconv.FormatInt(f.session, 10),
		"--goal-hash=" + f.hash, "--consent=" + f.digest, "--chat=" + f.chat}
	args = appendLimitFlags(args, PursuitLimits{ParkedMax: f.limits.ParkedMax, Pct: f.limits.Pct, WakesMax: f.limits.WakesMax}, true)
	args = append(args, "--gui-phrase="+phrase)
	return scopedCLIArgs(profile, args...)
}

func pursuitLimitsOK(lim PursuitLimits, zeroIsDefault bool) bool {
	for _, v := range []int{lim.ParkedMax, lim.Pct, lim.WakesMax} {
		if v < 0 || v > pursuitSane || (v == 0 && !zeroIsDefault) {
			return false
		}
	}
	return true
}

// PursuitStartPreview is the preview of a mission's pursuit the person reads before typing the phrase: the goal whole, the
// limits, the dollars, the host, what blocks it. It writes nothing. limits left at 0 take the CLI's defaults. The answer is
// the CLI's document plus "start_token" (the proof PursuitStart needs); a preview the app cannot read plainly is refused with
// code "unreadable" and has no token. A newer preview of the session replaces the older one's token.
func (a *App) PursuitStartPreview(sessionId int64, limits PursuitLimits) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if why := opgate.Refusal(); why != "" {
		return taskRefusal("only the user can preview a pursuit start: " + why + " (operator_only)")
	}
	sid, bad := sessionIDArg(sessionId)
	if bad != "" {
		return bad
	}
	if !pursuitLimitsOK(limits, true) {
		return taskRefusal("a limit is a whole number of 1 or more (leave it at 0 for the default)")
	}
	pid, err := a.profileID()
	if err != nil {
		return aiError(err)
	}
	if !pursuitPreviewMu.TryLock() {
		return `{"error":"a preview is already running: wait for it","code":"busy"}`
	}
	defer pursuitPreviewMu.Unlock()
	if !pursuitPreviews.allow(pid) {
		return `{"error":"too many previews in a minute: wait a little","code":"rate_limited"}`
	}
	argv, err := pursuitPreviewArgv(pid, sid, limits)
	if err != nil {
		return aiError(err)
	}
	out, errText, truncated, err := a.execDoomsdayGrace(doomsdayReadTimeout, doomsdayProbeGrace, argv, nil)
	doc := charterResult(out, errText, truncated, err)
	if err != nil || truncated {
		return doc
	}
	facts, ferr := pursuitFactsOf([]byte(out), sessionId, limits)
	if errors.Is(ferr, errGoalTooLong) {
		return aiError(&codedError{msg: ferr.Error(), code: "goal_too_long"})
	}
	if ferr != nil {
		return aiError(&codedError{msg: ferr.Error(), code: "unreadable"})
	}
	token, terr := pursuitGrants.issue(pid, facts)
	if terr != nil {
		return aiError(terr)
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(out), &m) != nil {
		return aiError(&codedError{msg: "the preview could not be read", code: "unreadable"})
	}
	m["start_token"], _ = json.Marshal(token)
	b, _ := json.Marshal(m)
	return string(b)
}

// PursuitStart begins the pursuit of a mission whose preview the person read: it confirms the goal and starts the pursuit in
// one CLI step. It needs the operator gate, limits as the preview resolved them, the phrase the person typed (compared with
// the CLI's template, filled, before any dialog), the token of the preview, and a click on "Start pursuit" in a native
// dialog. Where no dialog can be shown it refuses. The answer is {"ok","started","state","output","detail","code"} as for
// the open-ended start: state is started, not_started or unknown (the CLI was killed or timed out, or printed no document:
// the pursuit may have started; the page reads the status and never retries). code is the CLI's own (consent_stale, stale,
// already_started, blocked, chat_mismatch, hidden_characters, criterion_too_long, invalid_input) or one of the app's:
// operator_only, invalid_input, busy, token, phrase, blocked, declined, no_native_dialog, unknown.
func (a *App) PursuitStart(sessionId int64, limits PursuitLimits, phrase, token string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	no := func(detail, code string) string { return startDoc("not_started", "", detail, code) }
	if why := opgate.Refusal(); why != "" {
		return no(why, "operator_only")
	}
	if _, bad := sessionIDArg(sessionId); bad != "" {
		return no("a session id is a positive number", "invalid_input")
	}
	if !pursuitLimitsOK(limits, false) {
		return no("a start names the three limits the preview showed", "invalid_input")
	}
	if err := checkPhrase(phrase); err != nil {
		return no(err.Error(), "invalid_input")
	}
	if dialogOpen.Load() {
		return no("a confirmation dialog is still open: answer or close it first", "busy")
	}
	if !pursuitStartMu.TryLock() {
		return no("a start is already running", "busy")
	}
	defer pursuitStartMu.Unlock()
	pid, err := a.profileID()
	if err != nil {
		return no(err.Error(), "")
	}
	facts, err := pursuitGrants.peek(pid, sessionId, token, limits)
	if err != nil {
		return no(err.Error(), "token")
	}
	if len(facts.blockers) > 0 || !facts.loopOn {
		return no("something blocks this pursuit ("+strings.Join(facts.blockers, ", ")+"); nothing started", "blocked")
	}
	if strings.TrimSpace(phrase) != facts.expectedPhrase() { // before the dialog, and nothing is spent
		return no("the phrase does not match the pursuit shown; nothing started", "phrase")
	}
	shown := doomsdayNow()
	yes, returned, err := askNativeTitled(a, pursuitDialogTitle, facts.dialogBody())
	if at := dialogShownAt(); at.After(shown) { // the think time runs from when the dialog was on screen, not from any wait for another one
		shown = at
	}
	switch {
	case !returned:
		return no("the confirmation did not return in time; nothing started", "declined")
	case errors.Is(err, errDialogBusy):
		return no(err.Error(), "busy")
	case err != nil:
		return no(err.Error(), "no_native_dialog")
	case yes && doomsdayNow().Sub(shown) < pursuitMinThink:
		pursuitGrants.noteDecline(pid, sessionId, token)
		return no("the confirmation was answered too fast to be a decision; nothing started", "declined")
	case !yes:
		pursuitGrants.noteDecline(pid, sessionId, token)
		return no("the confirmation was declined; nothing started", "declined")
	}
	if err := pursuitGrants.consume(pid, sessionId, token); err != nil { // spent now: this preview can never begin a second time
		return no(err.Error(), "token")
	}
	return a.beginPursuit(pid, facts, phrase)
}

// beginPursuit runs the CLI's begin for facts the person confirmed and turns its end into the start document.
func (a *App) beginPursuit(pid string, f pursuitFacts, phrase string) string {
	argv, err := pursuitBeginArgv(pid, f, phrase)
	if err != nil {
		return startDoc("not_started", "", err.Error(), "")
	}
	out, errText, truncated, err := a.execDoomsdayGrace(doomsdayStartTimeout, doomsdayProbeGrace, argv, nil)
	if err == nil {
		var ok struct {
			Via     string          `json:"via"`
			Pursuit json.RawMessage `json:"pursuit"`
		}
		if !truncated && json.Unmarshal([]byte(lastJSONLine(out)), &ok) == nil && len(ok.Pursuit) > 0 {
			return startDoc("started", out, "", "")
		}
		// exit 0 but no document the app can read: the CLI got as far as committing, so the page reads the status
		return startDoc("unknown", "", "the pursuit may have started: check its status and do not press Start again", "unknown")
	}
	var ce struct{ Error, Code string }
	_ = json.Unmarshal([]byte(lastJSONLine(out)), &ce)
	detail := tailLines(cliStderr([]byte(errText)), 4, 400)
	if ce.Error != "" {
		detail = ce.Error
	}
	if detail == "" {
		detail = err.Error()
	}
	if startMayHaveHappened(err, ce.Error != "", detail, errText) {
		return startDoc("unknown", "", "the pursuit may have started: check its status and do not press Start again. "+detail, "unknown")
	}
	return startDoc("not_started", "", detail, ce.Code)
}
