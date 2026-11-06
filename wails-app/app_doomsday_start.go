// wails-app: the start form of the open-ended chat mode
//
// The start form of the open-ended chat mode: the preview of a charter, the start with the phrase the person typed,
// and the entry screen's availability. They are the CLI's own verbs (`doomsday render`, `start`, `probe`): the app
// holds no authority. The charter goes to the CLI on stdin, the phrase as --gui-phrase, which the CLI derives again
// from the charter it rendered and compares, and the boss session as --boss-session. The argv is fixed here
// (doomsdayCharterArgv); nothing the page sends is a flag.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

const (
	// doomsdayCharterMax mirrors the CLI's charter cap.
	doomsdayCharterMax = 1 << 20
	// doomsdayPhraseMax bounds the typed phrase: the real one is about 45 characters.
	doomsdayPhraseMax = 200
	// doomsdayRenderTimeout and doomsdayStartTimeout cover the real containment probe, which on Windows and macOS runs
	// its host checks (up to about 30 seconds) before anything is shown or created.
	// doomsdayProbeGrace is how long a timed-out preview, probe or start has to end after SIGTERM before it is killed.
	doomsdayProbeGrace = 8 * time.Second
	// doomsdayRenderPerMinute bounds previews per profile: each runs the real containment probe.
	doomsdayRenderPerMinute = 10
)

// The timeouts are variables so a test can shorten them.
var (
	doomsdayRenderTimeout = 2 * time.Minute
	doomsdayStartTimeout  = 3 * time.Minute
)

// renderLimiter allows doomsdayRenderPerMinute previews per profile in any minute.
type renderLimiter struct {
	mu   sync.Mutex
	seen map[string][]time.Time
}

var doomsdayRenders renderLimiter

// doomsdayRenderMu keeps previews one at a time: each runs the real containment probe.
var doomsdayRenderMu sync.Mutex

func (l *renderLimiter) reset() { l.mu.Lock(); l.seen = nil; l.mu.Unlock() }

func (l *renderLimiter) allow(profile string) bool {
	now := doomsdayNow()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string][]time.Time{}
	}
	recent := l.seen[profile][:0:0]
	for _, t := range l.seen[profile] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= doomsdayRenderPerMinute {
		l.seen[profile] = recent
		return false
	}
	l.seen[profile] = append(recent, now)
	return true
}

// doomsdayStartMu keeps two starts from running at once; a second click while one runs is refused, not queued.
var doomsdayStartMu sync.Mutex

// doomsdayCharterArgv is the argv of the two verbs that take a charter: `render --charter -` and
// `start --charter - --gui-phrase=<phrase> --boss-session=N`. The phrase goes in the --flag=value form, so text that
// starts with a dash is a value and never a flag. No other flag is ever added (--yes-i-lift, --hash, --everything and
// --lift are not the form's: the charter's own "lines" carry what is lifted).
func doomsdayCharterArgv(profile, verb, phrase string, boss int64) ([]string, error) {
	args := []string{"doomsday", verb, "--charter", "-"}
	switch verb {
	case "render":
	case "start":
		if boss <= 0 {
			return nil, errors.New("a start needs the boss session")
		}
		args = append(args, "--gui-phrase="+phrase, "--boss-session="+strconv.FormatInt(boss, 10))
	default:
		return nil, errors.New("the app cannot run doomsday " + verb + " with a charter")
	}
	return scopedCLIArgs(profile, args...)
}

// checkCharterJSON refuses what cannot be a charter before the CLI runs: not text, too large, not one JSON object. The
// CLI validates the charter itself (unknown fields, limits, pins, the lines); this only keeps garbage off its stdin.
func checkCharterJSON(s string) error {
	var obj map[string]json.RawMessage
	switch {
	case len(s) == 0 || len(s) > doomsdayCharterMax || !utf8.ValidString(s):
		return errors.New("the charter is empty, too large or not text")
	case json.Unmarshal([]byte(s), &obj) != nil || obj == nil:
		return errors.New("the charter is not one JSON object")
	}
	return nil
}

// checkPhrase refuses a phrase that is empty, long, or has a line break or other control character in it.
func checkPhrase(p string) error {
	if strings.TrimSpace(p) == "" || utf8.RuneCountInString(p) > doomsdayPhraseMax || !utf8.ValidString(p) {
		return errors.New("type the phrase")
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return errors.New("the phrase is one line")
		}
	}
	return nil
}

// DoomsdayOpenEntry is `doomsday probe`: whether an open-ended run can start here and, when not, why, the platform and
// the plain statement of what the mode does not defend against on it. It is what the entry screen shows first.
func (a *App) DoomsdayOpenEntry() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if why := opgate.Refusal(); why != "" {
		return taskRefusal("only the user can open the open-ended start form: " + why + " (operator_only)")
	}
	pid, err := a.profileID()
	if err != nil {
		return aiError(err)
	}
	// The probe starts a transient unit: it runs only when the person opens the entry screen or presses a button, is
	// shared by concurrent callers and kept 60 s per profile. Nothing polls it.
	return doomsdayEntry.get(pid, func() (string, bool) {
		argv, aerr := doomsdayArgv(pid, "probe")
		if aerr != nil {
			return aiError(aerr), false
		}
		out, errText, truncated, err := a.execDoomsdayGrace(doomsdayRenderTimeout, doomsdayProbeGrace, argv, nil)
		doc := charterResult(out, errText, truncated, err)
		var d struct {
			Available *bool  `json:"mode_available"`
			Error     string `json:"error"`
		}
		return doc, err == nil && json.Unmarshal([]byte(doc), &d) == nil && d.Available != nil && d.Error == ""
	})
}

// DoomsdayRender is `doomsday render --charter -`: the preview the person reads before typing the phrase (the full
// hash, every lifted line, the plain limits of the mode). It writes nothing. The document does not carry the phrase.
func (a *App) DoomsdayRender(charterJSON string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if why := opgate.Refusal(); why != "" {
		return taskRefusal("only the user can preview an open-ended start: " + why + " (operator_only)")
	}
	if err := checkCharterJSON(charterJSON); err != nil {
		return taskRefusal(err.Error())
	}
	pid, err := a.profileID()
	if err != nil {
		return aiError(err)
	}
	if !doomsdayRenderMu.TryLock() {
		return `{"error":"a preview is already running: wait for it","code":"busy"}`
	}
	defer doomsdayRenderMu.Unlock()
	if !doomsdayRenders.allow(pid) {
		return `{"error":"too many previews in a minute: wait a little","code":"rate_limited"}`
	}
	argv, err := doomsdayCharterArgv(pid, "render", "", 0)
	if err != nil {
		return aiError(err)
	}
	out, errText, truncated, err := a.execDoomsdayGrace(doomsdayRenderTimeout, doomsdayProbeGrace, argv, strings.NewReader(charterJSON))
	doc := charterResult(out, errText, truncated, err)
	if err != nil || truncated {
		return doc
	}
	facts, ferr := factsOf([]byte(out))
	if ferr != nil {
		return taskRefusal(ferr.Error())
	}
	token, terr := doomsdayGrants.issue(pid, charterJSON, facts)
	if terr != nil {
		return aiError(terr)
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(out), &m) != nil {
		return taskRefusal("the preview could not be read")
	}
	m["start_token"], _ = json.Marshal(token)
	b, _ := json.Marshal(m)
	return string(b)
}

// DoomsdayStart starts the open-ended run of a charter the person has read, with the phrase they typed, the token of
// the preview of exactly this charter, and the boss session (the id of the top-level lead session that is the run's
// boss; the chat flow creates it, the app does not choose it; none is refused). Besides the operator gate it needs two
// proofs a script in the page cannot give: the token (single use, ten minutes, this profile, these charter bytes) and a
// click in a native dialog built here from the CLI's own preview. Where no native dialog can be shown it refuses. The CLI
// re-checks the operator gate, derives the phrase from the charter and its hash and compares, and records `gui`.
// The answer is {"ok","started","state","output","detail","code"}: state is started, not_started or unknown (the CLI
// was killed or timed out, or said the run started but could not finish: the run may have started). Never retried.
func (a *App) DoomsdayStart(charterJSON, phrase, token string, bossSession int64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if why := opgate.Refusal(); why != "" {
		return startDoc("not_started", "", why, "operator_only")
	}
	if err := checkCharterJSON(charterJSON); err != nil {
		return startDoc("not_started", "", err.Error(), "invalid_input")
	}
	if err := checkPhrase(phrase); err != nil {
		return startDoc("not_started", "", err.Error(), "invalid_input")
	}
	if bossSession <= 0 {
		return startDoc("not_started", "", "a start needs the boss session of the chat", "invalid_input")
	}
	if dialogOpen.Load() {
		return startDoc("not_started", "", "a confirmation dialog is still open: answer or close it first", "busy")
	}
	// One start at a time, dialog included: a second click while a confirmation is open is refused, not queued.
	if !doomsdayStartMu.TryLock() {
		return startDoc("not_started", "", "a start is already running", "busy")
	}
	defer doomsdayStartMu.Unlock()
	defer doomsdayProbe.invalidate()
	pid, err := a.profileID()
	if err != nil {
		return startDoc("not_started", "", err.Error(), "")
	}
	defer doomsdayEntry.invalidate(pid)
	facts, err := doomsdayGrants.peek(pid, token, charterJSON)
	if err != nil {
		return startDoc("not_started", "", err.Error(), "token")
	}
	if strings.TrimSpace(phrase) != facts.expectedPhrase() { // before the dialog, and nothing is spent
		return startDoc("not_started", "", "the phrase does not match the charter shown; nothing started", "phrase")
	}
	shown := doomsdayNow()
	yes, returned, err := askNative(a, facts.dialogBody())
	if !returned {
		return startDoc("not_started", "", "the confirmation did not return in time; nothing started", "declined")
	}
	if err != nil {
		return startDoc("not_started", "", err.Error(), "no_native_dialog")
	}
	if yes && doomsdayNow().Sub(shown) < dialogMinThink {
		// A double-click or a held Enter can answer a dialog that has no default response: too fast is not a decision.
		return startDoc("not_started", "", "the confirmation was answered too fast to be a decision; nothing started", "declined")
	}
	if !yes {
		return startDoc("not_started", "", "the confirmation was declined; nothing started", "declined")
	}
	if err := doomsdayGrants.consume(pid, token); err != nil { // spent now: this preview can never start a second run
		return startDoc("not_started", "", err.Error(), "token")
	}
	argv, err := doomsdayCharterArgv(pid, "start", phrase, bossSession)
	if err != nil {
		return startDoc("not_started", "", err.Error(), "")
	}
	out, errText, _, err := a.execDoomsdayGrace(doomsdayStartTimeout, doomsdayProbeGrace, argv, strings.NewReader(charterJSON))
	if err == nil {
		return startDoc("started", out, "", "")
	}
	var ce struct{ Error, Code string }
	_ = json.Unmarshal([]byte(lastJSONLine(out)), &ce)
	detail := cliStderr([]byte(errText))
	if ce.Error != "" {
		detail, out = ce.Error, ""
	}
	if detail == "" {
		detail = err.Error()
	}
	if startMayHaveHappened(err, ce.Error != "", detail, errText) {
		return startDoc("unknown", "", "the run may have started: check the Control room and press Stop if it is running. "+detail, "unknown")
	}
	return startDoc("not_started", out, detail, ce.Code)
}

// doomsdayDialogTimeout is how long a start waits for the native dialog before it gives the start lock back and counts
// the start as declined (a dialog that never returns must not leave every later start busy).
var doomsdayDialogTimeout = 5 * time.Minute

// dialogOpen is true while a native confirmation is on screen, including one the start stopped waiting for: it stays up
// until the person dismisses it, and a second one must not be stacked on it.
var dialogOpen atomic.Bool

// askNative shows the confirmation and waits for it at most doomsdayDialogTimeout. returned is false when it did not
// come back in time; a late answer is dropped.
func askNative(a *App, body string) (yes, returned bool, err error) {
	type answer struct {
		yes bool
		err error
	}
	ch := make(chan answer, 1)
	ask := confirmNative // read once, here: the goroutine below must not read the variable again
	dialogOpen.Store(true)
	go func() {
		y, e := ask(a, "Start an open-ended run", body)
		dialogOpen.Store(false) // the dialog is really gone only now, not when the wait gave up
		ch <- answer{y, e}
	}()
	select {
	case r := <-ch:
		return r.yes, true, r.err
	case <-time.After(doomsdayDialogTimeout):
		return false, false, nil
	}
}

// crashRe matches a Go crash report: its marker at the START of a line. The charter text the CLI echoes on stderr is one
// escaped line after a fixed label ("Goal: ..."), so it cannot start a line with these words.
var crashRe = regexp.MustCompile(`(?m)^(panic: |fatal error: |goroutine [0-9]+ \[)`)

// startMayHaveHappened says whether a failed start leaves the run's state unclear. Only two outcomes are "not started":
// the CLI could not be run at all, or it exited by itself with an explicit refusal (its JSON error envelope) and nothing
// that says otherwise. Anything else is unknown: the command's context ended, a signal killed it, it panicked, it said
// "started but", or it failed with no envelope (an external kill or a native crash can follow a run that already started;
// on Windows a killed process exits with code 1, which no exit code distinguishes).
func startMayHaveHappened(err error, envelope bool, detail, stderr string) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	if strings.Contains(detail, " started but ") || crashRe.MatchString(stderr) {
		return true
	}
	var xe *exec.Error
	var pe *fs.PathError
	if errors.As(err, &xe) || errors.As(err, &pe) {
		return false // could not be run at all
	}
	return !envelope
}

// lastJSONLine is the last line of out that is a JSON object: some paths print text before the document, and a failure
// is followed by the generic error envelope.
func lastJSONLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.HasPrefix(l, "{") && json.Valid([]byte(l)) {
			return l
		}
	}
	return ""
}

func startDoc(state, output, detail, code string) string {
	b, _ := json.Marshal(map[string]any{
		"ok": state == "started", "started": state == "started", "state": state,
		"output": clipRunes(strings.TrimSpace(output), doomsdayStopTextMax), "detail": clipRunes(detail, doomsdayStopTextMax), "code": code,
	})
	return string(b)
}

// charterResult is the CLI's document verbatim, or the {"error","code"} document it printed on a failure.
func charterResult(out, errText string, truncated bool, err error) string {
	if truncated {
		return aiError(errors.New("the answer was too large to read"))
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return cliResultJSON("monoagentcli", []byte(out), &exec.ExitError{ProcessState: ee.ProcessState, Stderr: []byte(errText)})
		}
		return aiError(err)
	}
	return cliResultJSON("monoagentcli", []byte(out), nil)
}
