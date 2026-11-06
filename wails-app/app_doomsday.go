// wails-app/app_doomsday.go
//
// The control room's bindings (docs: monoes/mono-agent-src#1157). The app never reads the run's tables: every
// binding runs `monoagentcli --profile <active> --json doomsday <verb>` and returns the CLI's document. The read
// bindings, the probe and Stop run the verbs listed in doomsdayVerbs, and doomsdayArgv refuses any other verb or flag.
// The one binding that starts a run, DoomsdayStart (the start form file), runs `start` with a fixed argv, only for
// the operator, only with the phrase a person typed in the form, which the CLI derives again and compares. Nothing
// here approves, grants, pauses or resumes anything, and the CLI re-checks the operator gate of every call it gets.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

const (
	// doomsdayReadTimeout bounds a read: each is a few small queries.
	doomsdayReadTimeout = 15 * time.Second
	// doomsdayStopTimeout bounds Stop, which waits for processes to end. Stop is never retried.
	doomsdayStopTimeout = 2 * time.Minute
	// doomsdayMaxOutput caps what is read from the CLI; the documents are a few KB.
	doomsdayMaxOutput = 1 << 20
	// doomsdayStopTextMax caps the Stop output shown (runes).
	doomsdayStopTextMax = 8000
	// doomsdayProbeTTL is how long a probe answer is reused: the nav asks on every render.
	doomsdayProbeTTL = 5 * time.Second
	// doomsdayMaxLimit mirrors the CLI's page cap (store.MaxFeedLimit).
	doomsdayMaxLimit = 200
)

// doomsdayVerbs is the whole of what the app can run through doomsdayArgv, with the flags each takes: the read verbs
// (overview, feed, probe) and stop. render and start are not here: they take a charter and a phrase from the person
// and have their own fixed argv (doomsdayCharterArgv, in the start form file). approve, grant, pause, resume, finish and
// rubric are the terminal's.
var doomsdayVerbs = map[string][]string{
	"overview": nil,
	"feed":     {"--before", "--limit"},
	"stop":     nil,
	"probe":    nil,
}

// doomsdayArgv is the argv of one allowed verb for profile. extra is --flag=value strings; a flag the verb does not
// list, a value that is not a plain non-negative number, or a verb outside doomsdayVerbs is refused.
func doomsdayArgv(profile, verb string, extra ...string) ([]string, error) {
	flags, ok := doomsdayVerbs[verb]
	if !ok {
		return nil, errors.New("the app cannot run doomsday " + verb)
	}
	args := []string{"doomsday", verb}
	for _, e := range extra {
		name, val, found := strings.Cut(e, "=")
		allowed := false
		for _, f := range flags {
			allowed = allowed || f == name
		}
		if _, err := strconv.ParseUint(val, 10, 63); !allowed || !found || err != nil {
			return nil, errors.New("doomsday " + verb + " does not take " + e)
		}
		args = append(args, e)
	}
	return scopedCLIArgs(profile, args...)
}

// cappedBuffer keeps the first max bytes and drops the rest without blocking the writer.
type cappedBuffer struct {
	max       int
	buf       []byte
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := c.max - len(c.buf); room < len(p) {
		c.truncated = true
		p = p[:max(room, 0)]
	}
	c.buf = append(c.buf, p...)
	return n, nil
}

// runDoomsday runs one allowed verb and returns its stdout, stderr, whether stdout was cut at the cap, and the run
// error. profile comes from the app's selection, never from the page.
func (a *App) runDoomsday(timeout time.Duration, verb string, extra ...string) (stdout, stderr string, truncated bool, err error) {
	pid, err := a.profileID()
	if err != nil {
		return "", "", false, err
	}
	return a.runDoomsdayFor(pid, timeout, verb, extra...)
}

func (a *App) runDoomsdayFor(pid string, timeout time.Duration, verb string, extra ...string) (stdout, stderr string, truncated bool, err error) {
	full, err := doomsdayArgv(pid, verb, extra...)
	if err != nil {
		return "", "", false, err
	}
	return a.execDoomsday(timeout, full, nil)
}

// execDoomsday runs the CLI with an argv that doomsdayArgv or doomsdayCharterArgv built, with stdin (nil for none).
func (a *App) execDoomsday(timeout time.Duration, full []string, stdin io.Reader) (stdout, stderr string, truncated bool, err error) {
	return a.execDoomsdayGrace(timeout, 0, full, stdin)
}

// execDoomsdayGrace is execDoomsday where a timed-out or cancelled CLI is sent SIGTERM first and killed only after grace
// (0: killed at once, as Stop wants). The probe stops its test unit on SIGTERM; a kill would leak it. Where a signal
// cannot be sent (Windows) it is killed. A context that ended is part of the error, so callers can tell a cut-off command
// from one that exited by itself.
func (a *App) execDoomsdayGrace(timeout, grace time.Duration, full []string, stdin io.Reader) (stdout, stderr string, truncated bool, err error) {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return "", "", false, err
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliBin, full...)
	cmd.Stdin = stdin
	if grace > 0 {
		cmd.Cancel = func() error {
			if cmd.Process.Signal(syscall.SIGTERM) != nil {
				return cmd.Process.Kill()
			}
			return nil
		}
		cmd.WaitDelay = grace
	}
	suppressConsole(cmd)
	out, errb := &cappedBuffer{max: doomsdayMaxOutput}, &cappedBuffer{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, errb
	err = cmd.Run()
	if err != nil && ctx.Err() != nil {
		err = fmt.Errorf("%w: %v", ctx.Err(), err)
	}
	return string(out.buf), string(errb.buf), out.truncated, err
}

// doomsdayRead runs a read verb and returns its document verbatim, or an {"error"} document.
func (a *App) doomsdayRead(verb string, extra ...string) string {
	out, errText, truncated, err := a.runDoomsday(doomsdayReadTimeout, verb, extra...)
	if truncated {
		return aiError(errors.New("the control room document was too large to read"))
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// A failed read exits non-zero but still prints its document (available:false, reason:error, error_code);
			// that is the page's error state, not a failed poll and never "off".
			// The real CLI then prints main's generic {"error"} envelope as a second document; only the first
			// is the control room's, and it alone goes to the page.
			if first, ok := doomsdayReadError(out); ok {
				return first
			}
			return cliResultJSON("monoagentcli", []byte(out), &exec.ExitError{ProcessState: ee.ProcessState, Stderr: []byte(errText)})
		}
		return aiError(err)
	}
	return cliResultJSON("monoagentcli", []byte(out), nil)
}

// doomsdayReadError returns the first JSON value of out when it is the control room's read-failure document:
// schema_version 1, available false, reason "error". On a failed read the real CLI prints that document and then
// main's generic {"error"} envelope, so only the first value is decoded and anything after it is ignored. out is
// already capped by the caller's buffer. Anything else on a non-zero exit is handled as a failed command.
func doomsdayReadError(out string) (string, bool) {
	var raw json.RawMessage
	if json.NewDecoder(strings.NewReader(out)).Decode(&raw) != nil {
		return "", false
	}
	var d struct {
		SchemaVersion int    `json:"schema_version"`
		Available     bool   `json:"available"`
		Reason        string `json:"reason"`
	}
	if json.Unmarshal(raw, &d) != nil || d.SchemaVersion != 1 || d.Available || d.Reason != "error" {
		return "", false
	}
	return string(raw), true
}

// DoomsdayOverview is `doomsday overview`: the run, tree clusters, spend and integrity, or available:false.
func (a *App) DoomsdayOverview() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.doomsdayRead("overview")
}

// DoomsdayFeed is `doomsday feed [--before=N] [--limit=N]`: one page of the decision feed, newest first. before 0
// starts at the newest; limit 0 is the CLI's default and is capped at doomsdayMaxLimit.
func (a *App) DoomsdayFeed(before, limit int64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if before < 0 || limit < 0 || limit > doomsdayMaxLimit {
		return taskRefusal("the feed page is --before >= 0 and --limit 0.." + strconv.Itoa(doomsdayMaxLimit))
	}
	var extra []string
	if before > 0 {
		extra = append(extra, "--before="+strconv.FormatInt(before, 10))
	}
	if limit > 0 {
		extra = append(extra, "--limit="+strconv.FormatInt(limit, 10))
	}
	return a.doomsdayRead("feed", extra...)
}

// doomsdayProbeDoc is what the nav needs: whether the mode exists in this build and whether the profile has a run.
// It carries no run data.
type doomsdayProbeDoc struct {
	ModeAvailable bool `json:"mode_available"`
	RunExists     bool `json:"run_exists"`
}

// doomsdayProbeCache holds the last probe answer. mu guards the fields only and is never held across the CLI run;
// gen is bumped by invalidation, and an answer computed under an older generation is not stored, so Stop never waits
// behind an in-flight probe.
type doomsdayProbeCache struct {
	mu      sync.Mutex
	gen     atomic.Uint64
	profile string
	at      time.Time
	doc     string
}

var doomsdayProbe doomsdayProbeCache

// invalidate drops the cached answer now and makes any probe already running discard its result.
func (c *doomsdayProbeCache) invalidate() {
	c.gen.Add(1)
	c.mu.Lock()
	c.at = time.Time{}
	c.mu.Unlock()
}

// DoomsdayProbe answers {"mode_available","run_exists"} and is reused for doomsdayProbeTTL, but only when the CLI
// gave a definite answer: a valid overview document that is available, or says off or no_run. Every other outcome (a
// timeout, a spawn failure, an old CLI, garbage, a refusal, a non-zero exit, a read-error document) says nothing
// about the mode or the run, so it answers {"error"} and caches nothing; the page keeps its last good answer. The CLI
// is the same read as the control room's overview (it has no cheaper availability verb), so the caller paces it (see
// useControlRoomProbe).
func (a *App) DoomsdayProbe() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	pid, _ := a.profileID()
	c := &doomsdayProbe
	c.mu.Lock()
	if c.profile == pid && time.Since(c.at) < doomsdayProbeTTL {
		doc := c.doc
		c.mu.Unlock()
		return doc
	}
	gen := c.gen.Load()
	c.mu.Unlock()
	var ov struct {
		SchemaVersion int    `json:"schema_version"`
		Available     bool   `json:"available"`
		Reason        string `json:"reason"`
	}
	if json.Unmarshal([]byte(a.doomsdayRead("overview")), &ov) != nil || ov.SchemaVersion != 1 ||
		!(ov.Available || ov.Reason == "off" || ov.Reason == "no_run") {
		return `{"error":"the control room could not be read"}`
	}
	b, _ := json.Marshal(doomsdayProbeDoc{ModeAvailable: ov.Available || ov.Reason == "no_run", RunExists: ov.Available})
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen.Load() == gen {
		c.profile, c.at, c.doc = pid, time.Now(), string(b)
	}
	return string(b)
}

// doomsdayStopMu keeps two Stops from running at once; a second click while one runs is refused, not queued.
var doomsdayStopMu sync.Mutex

// DoomsdayStop runs `doomsday stop` for the active profile and nothing else: no argument comes from the page. The
// answer is {"ok","stopped","output","detail","code"}: output is the CLI's own words (NOT stopped, survivors,
// NOT RECORDED) and ok is true only for exit status 0. It is refused here when this process is not the user's own,
// and the CLI checks the same gate again. It is never retried.
//
// Stop is the one doomsday binding that works signed out (openBindings in app_account_gate_test.go): the operator
// stops a run whatever the monoes.me session says. So it begins without a.signedOut() and calls no account or network
// function before the CLI; the operator gate below is its only check. It acts on the profile selected in memory, else
// (cold start while signed out, nothing selected yet) on the last selected profile id read from the local state file
// (stopProfileID); with neither it answers with the no-profile text that names `doomsday stop --profile <id>`.
func (a *App) DoomsdayStop() string {
	if why := opgate.Refusal(); why != "" {
		return stopDoc(false, "", why, "operator_only")
	}
	if !doomsdayStopMu.TryLock() {
		return stopDoc(false, "", "a stop is already running", "busy")
	}
	defer doomsdayStopMu.Unlock()
	defer doomsdayProbe.invalidate()
	doomsdayGrants.dropAll() // a Stop ends any preview that was waiting to be started
	pid, err := a.stopProfileID()
	if err != nil {
		return stopDoc(false, "", err.Error(), "")
	}
	out, errText, _, err := a.runDoomsdayFor(pid, doomsdayStopTimeout, "stop")
	if err == nil {
		return stopDoc(true, out, "", "")
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
	return stopDoc(false, out, detail, ce.Code)
}

func stopDoc(ok bool, output, detail, code string) string {
	output = clipRunes(strings.TrimSpace(output), doomsdayStopTextMax)
	b, _ := json.Marshal(map[string]any{
		"ok": ok, "stopped": ok && strings.HasPrefix(output, "stopped"),
		"output": output, "detail": clipRunes(detail, doomsdayStopTextMax), "code": code,
	})
	return string(b)
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "..."
}
