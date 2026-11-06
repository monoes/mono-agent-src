// wails-app: the start form of the open-ended chat mode
//
// The start form of the open-ended chat mode: the preview of a charter, the start with the phrase the person typed,
// and the entry screen's availability. They are the CLI's own verbs (`doomsday render`, `start`, `probe`): the app
// holds no authority. The charter goes to the CLI on stdin, the phrase as --gui-phrase, which the CLI derives again
// from the charter it rendered and compares, and the boss session as --boss-session. The argv is fixed here
// (doomsdayCharterArgv); nothing the page sends is a flag.
package main

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"
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
	doomsdayRenderTimeout = 2 * time.Minute
	doomsdayStartTimeout  = 3 * time.Minute
)

// doomsdayStartMu keeps two starts from running at once; a second click while one runs is refused, not queued.
var doomsdayStartMu sync.Mutex

// doomsdayCharterArgv is the argv of the two verbs that take a charter: `render --charter -` and
// `start --charter - --gui-phrase=<phrase> [--boss-session=N]`. The phrase goes in the --flag=value form, so text that
// starts with a dash is a value and never a flag. No other flag is ever added (--yes-i-lift, --hash, --everything and
// --lift are not the form's: the charter's own "lines" carry what is lifted).
func doomsdayCharterArgv(profile, verb, phrase string, boss int64) ([]string, error) {
	args := []string{"doomsday", verb, "--charter", "-"}
	switch verb {
	case "render":
	case "start":
		args = append(args, "--gui-phrase="+phrase)
		if boss > 0 {
			args = append(args, "--boss-session="+strconv.FormatInt(boss, 10))
		}
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
	out, errText, truncated, err := a.runDoomsday(doomsdayRenderTimeout, "probe")
	return charterResult(out, errText, truncated, err)
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
	argv, err := doomsdayCharterArgv(pid, "render", "", 0)
	if err != nil {
		return aiError(err)
	}
	out, errText, truncated, err := a.execDoomsday(doomsdayRenderTimeout, argv, strings.NewReader(charterJSON))
	return charterResult(out, errText, truncated, err)
}

// DoomsdayStart starts the open-ended run of a charter the person has read, with the phrase they typed. bossSession is
// the id of the top-level lead session that is the run's boss, 0 for none (then no boss tool works): the chat flow that
// creates the boss supplies it, the app does not choose it. The CLI re-checks the operator gate, derives the phrase from
// the charter and its hash and compares, and records the acknowledgement as `gui`. The answer is
// {"ok","started","output","detail","code"}: output is the CLI's own document, ok is true only for exit status 0. It is
// refused here when this process is not the user's own, and it is never retried.
func (a *App) DoomsdayStart(charterJSON, phrase string, bossSession int64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if why := opgate.Refusal(); why != "" {
		return startDoc(false, "", why, "operator_only")
	}
	if err := checkCharterJSON(charterJSON); err != nil {
		return startDoc(false, "", err.Error(), "invalid_input")
	}
	if err := checkPhrase(phrase); err != nil {
		return startDoc(false, "", err.Error(), "invalid_input")
	}
	if bossSession < 0 {
		return startDoc(false, "", "the boss session is a session id", "invalid_input")
	}
	if !doomsdayStartMu.TryLock() {
		return startDoc(false, "", "a start is already running", "busy")
	}
	defer doomsdayStartMu.Unlock()
	defer doomsdayProbe.invalidate()
	pid, err := a.profileID()
	if err != nil {
		return startDoc(false, "", err.Error(), "")
	}
	argv, err := doomsdayCharterArgv(pid, "start", phrase, bossSession)
	if err != nil {
		return startDoc(false, "", err.Error(), "")
	}
	out, errText, _, err := a.execDoomsday(doomsdayStartTimeout, argv, strings.NewReader(charterJSON))
	if err == nil {
		return startDoc(true, out, "", "")
	}
	var ce struct{ Error, Code string }
	_ = json.Unmarshal([]byte(strings.TrimSpace(out)), &ce)
	detail := cliStderr([]byte(errText))
	if ce.Error != "" {
		detail, out = ce.Error, ""
	}
	if detail == "" {
		detail = err.Error()
	}
	return startDoc(false, out, detail, ce.Code)
}

func startDoc(ok bool, output, detail, code string) string {
	b, _ := json.Marshal(map[string]any{
		"ok": ok, "started": ok,
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
