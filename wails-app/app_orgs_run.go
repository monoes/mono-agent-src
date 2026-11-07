package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// StreamOrgEvents starts a live tail of an org's bus event log. Each event
// line is re-emitted as a Wails "org:event" event {orgName, event}; the
// subprocess is tracked the same way agent chat is (a.runningCmds, keyed so
// a new stream for the same org supersedes the previous one) and killed as
// a process group on Stop/supersede/shutdown — as chat turns are.
//
// streamID is the caller's name for this stream, which StopOrgEvents takes:
// Wails runs each call on its own goroutine, so a stop can arrive before the
// stream it ends is registered, and it must not be lost (#235).
func (a *App) StreamOrgEvents(orgName, streamID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return aiError(err)
	}
	projectRoot := a.orgProjectRoot()
	pid, err := a.profileID()
	if err != nil {
		return aiError(err)
	}
	eventsArgs, err := orgCLIRunArgs(pid, projectRoot, "events", "--follow", "--", orgName)
	if err != nil {
		return aiError(err)
	}
	logSuffix := ""
	if projectRoot != "" {
		logSuffix = fmt.Sprintf(" (project: %s)", projectRoot)
	}
	a.emitLog("ORG", "INFO", fmt.Sprintf("$ %s %s%s", cliBin, strings.Join(eventsArgs, " "), logSuffix))
	cmd := exec.Command(cliBin, eventsArgs...)
	setChatProcessGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return aiError(err)
	}
	cmd.Stderr = a.chatLogWriter()
	if err := cmd.Start(); err != nil {
		a.emitLog("ORG", "ERROR", fmt.Sprintf("org events failed to start: %v", err))
		return aiError(fmt.Errorf("start org events: %w", err))
	}

	key := "orgevents:" + orgName
	if !a.registerOrgEvents(key, streamID, cmd) {
		// Stopped before it was registered: end it now.
		killChatProcessGroup(cmd)
		go func() { _ = waitChatProcess(cmd) }()
		return `{"ok":true,"stopped":true}`
	}

	go func() {
		defer a.unregisterOrgEvents(key, cmd)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			eventsEmit(a.ctx, "org:event", map[string]interface{}{
				"orgName": orgName,
				"event":   json.RawMessage(append([]byte(nil), line...)),
			})
		}
		_ = waitChatProcess(cmd)
		eventsEmit(a.ctx, "org:eventsClosed", map[string]interface{}{"orgName": orgName})
	}()
	return `{"ok":true}`
}

// orgEventStopTTL is how long a stop for a stream that isn't registered yet
// is remembered. The gap it covers is two calls racing, so a minute is
// ample; a stop for a stream that already ended simply expires.
const orgEventStopTTL = time.Minute

// registerOrgEvents records cmd as the event tail under key, superseding the
// previous one, and reports true. It reports false, registering nothing,
// when StopOrgEvents already asked for streamID to stop.
func (a *App) registerOrgEvents(key, streamID string, cmd *exec.Cmd) bool {
	a.runningMu.Lock()
	defer a.runningMu.Unlock()
	a.pruneOrgEventStops()
	if _, stopped := a.orgEventStops[streamID]; stopped {
		delete(a.orgEventStops, streamID)
		return false
	}
	if prev, ok := a.runningCmds[key]; ok {
		killChatProcessGroup(prev)
	}
	a.runningCmds[key] = cmd
	if a.orgEventIDs == nil {
		a.orgEventIDs = make(map[string]string)
	}
	a.orgEventIDs[key] = streamID
	return true
}

// unregisterOrgEvents drops cmd once it has ended, unless a newer stream has
// taken its key.
func (a *App) unregisterOrgEvents(key string, cmd *exec.Cmd) {
	a.runningMu.Lock()
	defer a.runningMu.Unlock()
	if a.runningCmds[key] == cmd {
		delete(a.runningCmds, key)
		delete(a.orgEventIDs, key)
	}
}

// pruneOrgEventStops forgets expired stops. Call with runningMu held.
func (a *App) pruneOrgEventStops() {
	for id, at := range a.orgEventStops {
		if time.Since(at) > orgEventStopTTL {
			delete(a.orgEventStops, id)
		}
	}
}

// StopOrgEvents kills the org event tail StreamOrgEvents started as
// streamID. A newer stream for the same org is left alone; a stream not
// registered yet is killed as soon as it is.
func (a *App) StopOrgEvents(orgName, streamID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	key := "orgevents:" + orgName
	a.runningMu.Lock()
	a.pruneOrgEventStops()
	cmd, ok := a.runningCmds[key]
	ok = ok && a.orgEventIDs[key] == streamID
	if ok {
		delete(a.runningCmds, key)
		delete(a.orgEventIDs, key)
	} else {
		if a.orgEventStops == nil {
			a.orgEventStops = make(map[string]time.Time)
		}
		a.orgEventStops[streamID] = time.Now()
	}
	a.runningMu.Unlock()
	if !ok {
		return `{"ok":false,"error":"no active event stream"}`
	}
	killChatProcessGroup(cmd)
	return `{"ok":true}`
}

// RunOrg starts an org run (`monoagentcli org run <name> --task <task>`) as
// a detached, tracked subprocess — mirroring StreamOrgEvents rather than
// runOrgCLI, since a real run can block for the org's entire lifetime
// (minutes to hours) unless a live `org serve` daemon hands it off almost
// immediately; either way the UI can't wait synchronously for this call to
// return. Progress is observable via the existing Logs page (stdout/stderr
// piped through chatLogWriter) and via the org's own live event tail
// (StreamOrgEvents) if the caller is also subscribed to that. Emits
// "org:runStatus" so any UI can track running/stopped/error without
// polling. There is no companion StopOrg — not needed yet.
func (a *App) RunOrg(orgName, task string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if orgName == "" {
		return aiError(fmt.Errorf("org name required"))
	}
	key := "orgrun:" + orgName
	a.runningMu.Lock()
	if _, ok := a.runningCmds[key]; ok {
		a.runningMu.Unlock()
		return aiError(fmt.Errorf("org %q is already running", orgName))
	}
	a.runningMu.Unlock()

	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return aiError(err)
	}
	projectRoot := a.orgProjectRoot()
	pid, err := a.profileID()
	if err != nil {
		return aiError(err)
	}
	runArgs, err := orgCLIRunArgs(pid, projectRoot, "run", orgName)
	if err != nil {
		return aiError(err)
	}
	logSuffix := ""
	if projectRoot != "" {
		logSuffix = fmt.Sprintf(" (project: %s)", projectRoot)
	}
	if task != "" {
		runArgs = append(runArgs, "--task", task)
	}
	a.emitLog("ORG", "INFO", fmt.Sprintf("$ %s %s%s", cliBin, strings.Join(runArgs, " "), logSuffix))
	cmd := exec.Command(cliBin, runArgs...)
	setChatProcessGroup(cmd)
	stderrTail := &tailCapture{}
	cmd.Stdout = a.chatLogWriter()
	cmd.Stderr = io.MultiWriter(a.chatLogWriter(), stderrTail)
	if err := cmd.Start(); err != nil {
		a.emitLog("ORG", "ERROR", fmt.Sprintf("org run failed to start: %v", err))
		return aiError(fmt.Errorf("start org run: %w", err))
	}

	a.runningMu.Lock()
	a.runningCmds[key] = cmd
	a.runningMu.Unlock()

	eventsEmit(a.ctx, "org:runStatus", orgRunStatusEvent(orgName, "running", "", ""))
	emit := func(ev map[string]interface{}) { eventsEmit(a.ctx, "org:runStatus", ev) }
	// The run id appears once the org writes its runtime file, a moment after
	// the start: repeat the running event with it when it does.
	go func() {
		for i := 0; i < 6; i++ {
			time.Sleep(500 * time.Millisecond)
			a.runningMu.Lock()
			still := a.runningCmds[key] == cmd
			a.runningMu.Unlock()
			if !still {
				return
			}
			if id := a.orgRunID(orgName); id != "" {
				a.emitOrgRunning(key, cmd, orgName, id, emit)
				return
			}
		}
	}()

	go func() {
		waitErr := waitChatProcess(cmd)
		status := "stopped"
		message := ""
		if waitErr != nil {
			status = "error"
			message = orgRunFailureMessage(stderrTail.String(), waitErr)
			a.emitLog("ORG", "ERROR", fmt.Sprintf("org run %s exited: %s", orgName, message))
		} else {
			a.emitLog("ORG", "INFO", fmt.Sprintf("org run %s finished", orgName))
		}
		a.finishOrgRun(key, cmd, orgRunStatusEvent(orgName, status, message, a.orgRunID(orgName)), emit)
	}()

	return `{"ok":true}`
}

// orgEmitMu serializes the org run's terminal event with the delayed
// "running" repeat, so a slow `org status` can never emit running after the
// run ended.
var orgEmitMu sync.Mutex

// emitOrgRunning emits the running event carrying runID, only while cmd is
// still the registered command for key; checked under orgEmitMu, right before
// emitting.
func (a *App) emitOrgRunning(key string, cmd *exec.Cmd, orgName, runID string, emit func(map[string]interface{})) bool {
	orgEmitMu.Lock()
	defer orgEmitMu.Unlock()
	a.runningMu.Lock()
	still := a.runningCmds[key] == cmd
	a.runningMu.Unlock()
	if !still {
		return false
	}
	emit(orgRunStatusEvent(orgName, "running", "", runID))
	return true
}

// finishOrgRun unregisters cmd, then emits the terminal event, both under
// orgEmitMu: any poll result arriving later finds cmd gone and emits nothing.
func (a *App) finishOrgRun(key string, cmd *exec.Cmd, ev map[string]interface{}, emit func(map[string]interface{})) {
	orgEmitMu.Lock()
	defer orgEmitMu.Unlock()
	a.runningMu.Lock()
	if a.runningCmds[key] == cmd {
		delete(a.runningCmds, key)
	}
	a.runningMu.Unlock()
	emit(ev)
}

// orgRunStatusEvent is the org:runStatus payload. runId is omitted while the
// run is not known; the other fields are unchanged.
func orgRunStatusEvent(orgName, status, message, runID string) map[string]interface{} {
	ev := map[string]interface{}{"orgName": orgName, "status": status}
	if status != "running" {
		ev["message"] = message
	}
	if runID != "" {
		ev["runId"] = runID
	}
	return ev
}

// orgRunID reads the org's current (or last) run id from `org status`; ""
// when the status says nothing about a run.
func (a *App) orgRunID(orgName string) string {
	return runIDFromStatus(a.runOrgCLI("status", orgName))
}

// runIDFromStatus is the "run" of an `org status` answer ("" for an error
// payload or a status with no run).
func runIDFromStatus(out string) string {
	var st struct {
		Run string `json:"run"`
	}
	if json.Unmarshal([]byte(out), &st) != nil {
		return ""
	}
	return st.Run
}

// goLogLine matches the Go log lines (timestamped) monoagentcli's startup
// writes to stderr before anything the command itself reports.
var goLogLine = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)

// orgRunFailureMessage is what the toast says when `org run` exits with an
// error: what the command reported last, without the startup log lines
// before it. For a start monomind refused (host preflight R6, daemon lock
// R1) that is monomind's own line and the CLI's hint on what to do (the CLI
// prints exactly that, internal/monomind OrgStartRefusal); else the exit
// error.
func orgRunFailureMessage(tail string, waitErr error) string {
	lines := strings.Split(tail, "\n")
	last := -1
	for i, l := range lines {
		if goLogLine.MatchString(l) {
			last = i
		}
	}
	if msg := strings.TrimSpace(strings.Join(lines[last+1:], "\n")); msg != "" {
		return msg
	}
	if tail != "" {
		return tail
	}
	return waitErr.Error()
}

// tailCapture keeps the last few KB written to it — used to capture a
// failed org run's stderr so the "error" org:runStatus event can carry the
// real reason instead of a bare "exited with an error" (the process's exit
// code alone tells the UI nothing actionable).
type tailCapture struct {
	mu  sync.Mutex
	buf []byte
}

const tailCaptureMax = 4000

func (t *tailCapture) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > tailCaptureMax {
		t.buf = t.buf[len(t.buf)-tailCaptureMax:]
	}
	return len(b), nil
}

func (t *tailCapture) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}
