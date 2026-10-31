package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/daemonhb"
)

// execLauncher starts a session's turn as the same `chat --conversation`
// process the desktop app starts, but detached (its own session, output in a
// log file), so it survives the host and the app. The turn journals itself.
type execLauncher struct {
	bin, dbPath, instance, logDir string
	logf                          func(string, ...any)
	// tokens mints the session token a turn gets in its environment (nil: none), and revoke drops
	// a session's tokens when its turn ends; an error means a token may still be live (revokeSession retries it, then reports it).
	tokens func(profileID, conversationID, turnID string) (string, error)
	revoke func(profileID, conversationID string) error
	// revokeBackoff is the first wait between revoke attempts (zero: 200 ms; tests shorten it).
	revokeBackoff time.Duration
	// bind names the turn's process for its token, right after the turn starts.
	bind func(profileID, conversationID, turnID string, pid int, stamp string) error
	// child mints a mission child's task token for its turn and binds it to the turn's process (nil: none; see
	// coder_host_childtoken.go).
	child *childTokenMinter
	// bound, when set, launches turns with peer-credential binding (coder_host_bound_linux.go): no token in the
	// environment, a typed frame over a pipe, the token bound to the process before anything is delivered. Nil is the
	// launch exactly as it was.
	bound *boundLaunch
}

// hostID is what makes a host-owned name safe to put in a file name and a flag.
var hostID = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// hostTurnGrace is how long a stopped turn gets to end before it is killed.
var hostTurnGrace = 15 * time.Second

// revokeSession ends the session's tokens, trying a few times: a failure leaves a live token after its turn.
func (l *execLauncher) revokeSession(profileID, conversationID string) error {
	if l.revoke == nil {
		return nil
	}
	backoff := l.revokeBackoff
	if backoff <= 0 {
		backoff = 200 * time.Millisecond
	}
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if err = l.revoke(profileID, conversationID); err == nil {
			return nil
		}
		if l.logf != nil {
			l.logf("WARNING: ending the session token of %s failed (attempt %d): %v", conversationID, attempt+1, err)
		}
		if attempt < 3 {
			time.Sleep(backoff << attempt)
		}
	}
	return fmt.Errorf("the session token of %s could not be ended: %w", conversationID, err)
}

func (l *execLauncher) args(profileID string, rest ...string) []string {
	return append([]string{"--db-path", l.dbPath, "--profile", profileID, "--json"}, rest...)
}

func (l *execLauncher) Start(profileID, conversationID, turnID, message string, attachments []string, missionToken string) (coderhost.Turn, error) {
	return l.start(profileID, conversationID, turnID, message, attachments, missionToken, 0)
}

// minTurnCap is the smallest spend cap a turn is started under (coderhost's own minimum): with less
// left the session's budget is used up.
const minTurnCap = 1e-6

// StartCapped is Start under a spend cap in USD (the session's budget left).
func (l *execLauncher) StartCapped(profileID, conversationID, turnID, message string, attachments []string, missionToken string, capUSD float64) (coderhost.Turn, error) {
	return l.start(profileID, conversationID, turnID, message, attachments, missionToken, capUSD)
}

func (l *execLauncher) start(profileID, conversationID, turnID, message string, attachments []string, missionToken string, capUSD float64) (coderhost.Turn, error) {
	if capUSD != 0 && capUSD < minTurnCap {
		return nil, fmt.Errorf("the session's budget is used up (under $%g left)", minTurnCap)
	}
	if !hostID.MatchString(conversationID) || !hostID.MatchString(turnID) {
		return nil, fmt.Errorf("conversation and turn ids must be letters, digits, '.', '_' and '-'")
	}
	if err := os.MkdirAll(l.logDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(l.logDir, 0o700); err != nil { // MkdirAll leaves an existing directory as it was
		return nil, err
	}
	logPath := filepath.Join(l.logDir, turnID+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()
	offset, _ := logFile.Seek(0, io.SeekEnd)
	// The prompt goes in through stdin from a private file: it is in no
	// process listing, and a 256 KiB message does not hit the argument limit.
	promptPath := filepath.Join(l.logDir, turnID+".prompt")
	os.Remove(promptPath) // one a crashed host left; O_EXCL below then proves this one is new
	pf, err := os.OpenFile(promptPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_, werr := pf.WriteString(message)
	if cerr := pf.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(promptPath)
		return nil, werr
	}
	prompt, err := os.Open(promptPath)
	if err != nil {
		os.Remove(promptPath)
		return nil, err
	}
	defer prompt.Close()
	rest := []string{"chat", "--conversation", conversationID, "--turn", turnID, "--instance", l.instance, "--prompt-stdin"}
	for _, a := range attachments {
		rest = append(rest, "--attach="+a)
	}
	if capUSD > 0 {
		rest = append(rest, "--budget-usd="+strconv.FormatFloat(capUSD, 'f', 6, 64))
	}
	if missionToken != "" && !hostID.MatchString(missionToken) {
		return nil, fmt.Errorf("the mission confirmation is not a valid token")
	}
	// A mission child's turn gets the task token of its one task, minted here from the host's own child store.
	var ct *childToken
	if l.child != nil {
		if ct, err = l.child.mint(profileID, conversationID, turnID); err != nil {
			os.Remove(promptPath)
			return nil, err
		}
	}
	launched := false
	defer func() {
		if ct != nil && !launched {
			l.child.revokeToken(ct) // not started: the turn's own wait revokes it otherwise
		}
	}()
	pipe := missionToken // what goes over the inherited pipe: a mission confirmation, or a child's task token
	if ct != nil {
		if missionToken != "" {
			os.Remove(promptPath)
			return nil, errors.New("a mission child's turn cannot carry a mission confirmation")
		}
		pipe = ct.secret
	}
	cmd := exec.Command(l.bin, l.args(profileID, rest...)...)
	// The environment is the host's without any token of a session that started the host, plus this
	// turn's own: a turn must never inherit another session's identity (the host can have been
	// started from a master's shell). Tokens never go in the arguments (which any process of the
	// user lists). The session token goes in the environment (the turn's shell reads it there);
	// the mission confirmation goes over an inherited pipe the child reads once and closes, so it
	// is never in any environment block.
	cmd.Env = turnEnv(os.Environ())
	var bd *boundDelivery
	if l.bound != nil {
		if ct != nil {
			os.Remove(promptPath)
			return nil, errors.New("a mission child's turn cannot be launched with peer-credential binding yet")
		}
		if bd, err = l.bound.attach(cmd, pipe); err != nil {
			os.Remove(promptPath)
			return nil, fmt.Errorf("could not prepare the credential pipe: %w", err)
		}
		defer bd.close()
		pipe = "" // the mission confirmation travels in the frame
	}
	if pipe != "" {
		closeToken, terr := coderhost.AttachMissionToken(cmd, pipe)
		if terr != nil {
			os.Remove(promptPath)
			return nil, fmt.Errorf("could not hand over the mission confirmation: %w", terr)
		}
		defer closeToken() // the host keeps no copy of the read end, started or not
	}
	if l.tokens != nil && bd == nil {
		tok, err := l.tokens(profileID, conversationID, turnID)
		if err != nil {
			os.Remove(promptPath)
			return nil, fmt.Errorf("could not mint the session token: %w", err)
		}
		cmd.Env = append(cmd.Env, coderhost.SessionTokenEnv+"="+tok)
	}
	cmd.Dir = filepath.Dir(l.logDir)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = prompt, logFile, logFile
	startProcess := func() error { return clicore.DetachStart(cmd) }
	if bd != nil {
		startProcess = func() error { return bd.start(cmd) }
	}
	if err := startProcess(); err != nil {
		os.Remove(promptPath)
		if rerr := l.revokeSession(profileID, conversationID); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return nil, err
	}
	if runtime.GOOS != "windows" {
		os.Remove(promptPath) // the child holds the open file; nothing is left to read or leak
	}
	// What makes this pid this process, read now: a turn without one could not be
	// found again, spared by the sweep or stopped later.
	stamp := ""
	for i := 0; i < 5 && stamp == ""; i++ {
		if stamp = stampOf(cmd.Process.Pid); stamp == "" {
			time.Sleep(20 * time.Millisecond)
		}
	}
	t := &execTurn{pid: cmd.Process.Pid, stamp: stamp, done: make(chan struct{})}
	go func() {
		awaitExitNoReap(t.pid) // the leader is dead but unreaped: its group is still this turn's to signal
		t.settle(killGroup)
		t.err = cmd.Wait()
		// before done closes: a session that sees its turn end has no token left. The turn is held here while the
		// revoke is retried; a revoke that still fails is reported as the turn's error.
		if rerr := l.revokeSession(profileID, conversationID); rerr != nil {
			t.err = errors.Join(t.err, rerr)
		}
		l.child.revokeToken(ct) // the child's task token ends with its run (nil-safe)
		os.Remove(promptPath)   // before done closes: whoever sees the turn end sees no prompt left
		close(t.done)
	}()
	launched = true // the wait goroutine above revokes the child's token from here on
	if stamp == "" {
		t.stopAndWait()
		return nil, errors.New("could not identify the turn's process (its start time is unreadable), so it was stopped")
	}
	if ct != nil {
		if err := l.child.bind(profileID, conversationID, ct, t.pid, coderhost.ProcStamp(t.pid)); err != nil {
			t.stopAndWait()
			return nil, err
		}
	}
	if bd != nil {
		if err := bd.deliver(profileID, conversationID, turnID, t.pid, coderhost.ProcStamp(t.pid), func() error { return l.revokeSession(profileID, conversationID) }); err != nil {
			t.stopAndWait()
			return nil, err
		}
	} else if l.bind != nil {
		if err := l.bind(profileID, conversationID, turnID, t.pid, coderhost.ProcStamp(t.pid)); errors.Is(err, coderhost.ErrHostInsideTurn) {
			// the host runs inside a session's turn: no token is bound, so the master tools refuse in this turn
			if rerr := l.revokeSession(profileID, conversationID); rerr != nil {
				t.stopAndWait()
				return nil, rerr
			}
			if l.logf != nil {
				l.logf("WARNING: %v; turn %s runs without a usable session token", err, turnID)
			}
		} else if err != nil {
			t.stopAndWait()
			return nil, fmt.Errorf("could not bind the session token to the turn: %w", err)
		}
	}
	if err := awaitAdmission(logPath, offset, t.done); err != nil {
		t.stopAndWait()
		return nil, err
	}
	return t, nil
}

// admissionWait is how long a started turn gets to admit itself.
var admissionWait = 20 * time.Second

// awaitAdmission reads the turn process's first JSON line from its log: the
// admission, or the {"error"} of a turn refused before it was admitted (a
// conversation that does not exist, one the app is running a turn in, a turn
// id used before). A refusal comes back as the error, so the command fails
// with the reason instead of reading "done".
func awaitAdmission(logPath string, offset int64, done <-chan struct{}) error {
	deadline := time.Now().Add(admissionWait)
	var pending []byte // bytes read and not yet part of a complete line
	var lastLine string
	for {
		exited := false
		select {
		case <-done:
			exited = true
		default:
		}
		if f, err := os.Open(logPath); err == nil {
			buf := make([]byte, 32<<10)
			for {
				n, _ := f.ReadAt(buf, offset)
				if n == 0 {
					break
				}
				offset += int64(n)
				pending = append(pending, buf[:n]...)
			}
			f.Close()
		}
		if exited && len(pending) > 0 && pending[len(pending)-1] != '\n' {
			pending = append(pending, '\n') // the last line may have no newline; the process is done writing
		}
		for {
			i := bytes.IndexByte(pending, '\n')
			if i < 0 {
				break
			}
			line := strings.TrimSpace(string(pending[:i]))
			pending = pending[i+1:]
			if line == "" {
				continue
			}
			lastLine = line
			if !strings.HasPrefix(line, "{") {
				continue
			}
			var m struct {
				Admitted bool   `json:"admitted"`
				Existed  bool   `json:"existed"`
				Error    string `json:"error"`
				Code     string `json:"code"`
			}
			if json.Unmarshal([]byte(line), &m) != nil {
				continue
			}
			switch {
			case m.Error != "" && m.Code != "": // the {"error","code"} line of a refused turn, not any JSON log line
				return errors.New(m.Error)
			case m.Existed:
				return errors.New("this turn id was already used")
			case m.Admitted:
				return nil
			}
		}
		if exited {
			if lastLine == "" {
				lastLine = "no output"
			}
			return fmt.Errorf("the turn process exited before the turn was admitted: %s", lastLine)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the turn was not admitted within %s", admissionWait)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (l *execLauncher) Adopt(pid int, stamp string) coderhost.Turn {
	// Without a start time there is nothing to tell this process from a later
	// one that reused the pid, and the turn would be signalled by pid: not adopted.
	if pid <= 1 || stamp == "" || !turnAlive(pid) {
		return nil
	}
	// A start time that cannot be read right now (a ps that failed) is not a
	// different process: the turn is adopted, and signalled only once it reads again.
	if now := stampOf(pid); now != "" && now != stamp {
		return nil
	}
	return &adoptedTurn{pid: pid, stamp: stamp}
}

// Finish records a turn whose process ended without finishing it, through
// `chat history finish`, which does nothing for a turn that finished itself.
func (l *execLauncher) Finish(profileID, conversationID, turnID, reason string) {
	if turnID == "" || !hostID.MatchString(conversationID) || !hostID.MatchString(turnID) {
		return
	}
	os.Remove(filepath.Join(l.logDir, turnID+".prompt")) // a turn a later host adopted has no waiter to do it
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, l.bin, l.args(profileID, "chat", "history", "finish", conversationID, turnID, "--status", "failed", "--reason", reason)...).CombinedOutput()
	if err != nil && l.logf != nil {
		l.logf("coder host: closing turn %s: %v: %s", turnID, err, strings.TrimSpace(string(out)))
	}
}

// stopAndWait stops a turn the host is giving up on and waits (bounded) until its
// process is gone: the caller then frees the session, and the next turn must not
// overlap an agent that was slow to die.
func (t *execTurn) stopAndWait() {
	t.Stop()
	select {
	case <-t.done:
	case <-time.After(hostTurnGrace + 5*time.Second):
	}
}

type execTurn struct {
	pid   int
	stamp string
	done  chan struct{}
	err   error
	once  sync.Once

	mu       sync.Mutex
	stopping bool // Stop was asked for
	gone     bool // the leader has exited (about to be, or already, reaped): the group is never signalled by number again
}

// settle runs once the leader has exited and before it is reaped: whatever of a stopped turn's group ignored the
// SIGTERM goes with it, while the pid still cannot name anything else. After it no group kill by number happens.
func (t *execTurn) settle(kill func(int) error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gone = true
	if t.stopping {
		_ = kill(t.pid)
	}
}

// killGroupIfHeld kills the group at the end of the grace period, only while the leader has not been reaped.
func (t *execTurn) killGroupIfHeld(kill func(int) error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.gone {
		_ = kill(t.pid)
	}
}

func (t *execTurn) PID() int      { return t.pid }
func (t *execTurn) Stamp() string { return t.stamp }
func (t *execTurn) Wait() error   { <-t.done; return t.err }
func (t *execTurn) Stop() {
	t.once.Do(func() {
		t.mu.Lock()
		t.stopping = true
		if !t.gone {
			_ = terminateGroup(t.pid) // t is this process's child, and unreaped while gone is false: its pid is still its own
		}
		t.mu.Unlock()
		grace := hostTurnGrace // read here: the goroutine below can outlive a test that swaps it
		go func() {
			select {
			case <-t.done:
			case <-time.After(grace):
				t.killGroupIfHeld(killGroup)
			}
		}()
	})
}

// adoptedTurn is a turn process a previous host started: not this process's
// child, so it is watched by pid.
type adoptedTurn struct {
	pid   int
	stamp string
	once  sync.Once
	bg    sync.WaitGroup // the stop goroutine; tests wait for it before restoring the variables it reads
}

func (t *adoptedTurn) PID() int      { return t.pid }
func (t *adoptedTurn) Stamp() string { return t.stamp }

func (t *adoptedTurn) gone() bool {
	if !turnAlive(t.pid) {
		return true
	}
	// A live process whose start time cannot be read is not an exit: counting it as
	// one would start the next turn beside a running full-access agent.
	now := stampOf(t.pid)
	return now != "" && now != t.stamp
}

func (t *adoptedTurn) Wait() error {
	for !t.gone() {
		time.Sleep(adoptedPoll)
	}
	return nil
}

// adoptedPoll is how often a turn found again after a host restart is looked at
// (each look can fork a ps).
var adoptedPoll = time.Second

func (t *adoptedTurn) Stop() {
	t.once.Do(func() {
		t.bg.Add(1)
		go func() {
			defer t.bg.Done()
			signalAdopted(t.pid, t.stamp, terminateGroup)
			// Not this process's child, so the group is signalled only while the leader is
			// verifiably the same process. A leader that ends first leaves any child that
			// ignored SIGTERM running: it cannot be told from a stranger by then.
			time.Sleep(hostTurnGrace)
			signalAdopted(t.pid, t.stamp, killGroup)
		}()
	})
}

// signalRetry is how long and how often an adopted turn's start time is read
// again when it cannot be read: a ps that failed once should not lose a stop.
var (
	signalRetries       = 10
	signalRetryInterval = 500 * time.Millisecond
)

// signalAdopted signals the turn once its start time reads and matches; it gives
// up when the process is gone or the start time reads as a different one.
func signalAdopted(pid int, stamp string, sig groupSignal) {
	for i := 0; i < signalRetries; i++ {
		now := stampOf(pid)
		if now == stamp {
			signalTurn(pid, stamp, sig)
			return
		}
		if now != "" || !turnAlive(pid) {
			return
		}
		time.Sleep(signalRetryInterval)
	}
}

// signalTurn sends sig to the turn's process group if pid still is the process
// that has the given start time.
type groupSignal func(pid int) error

func signalTurn(pid int, stamp string, sig groupSignal) {
	// There is a window between this check and the signal; closing it needs a pidfd.
	// It is accepted: the stamp is compared a moment before the kill.
	if pid <= 1 || stamp == "" || stampOf(pid) != stamp {
		return
	}
	_ = sig(pid)
}

// stampOf is pidStamp; a variable so tests can make a start time unreadable.
var stampOf = pidStamp

// turnAlive says whether pid is a process that can still run: it exists and is
// not a zombie (an exited leader nobody reaped, which kill(pid, 0) still accepts).
func turnAlive(pid int) bool {
	if !daemonhb.ProcessAlive(pid) {
		return false
	}
	st := processState(pid)
	return st != "Z" && st != "X"
}

// sweepPrompts removes prompt files a crashed host left behind.
func sweepPrompts(dir string, olderThan time.Duration) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.prompt"))
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil && time.Since(fi.ModTime()) > olderThan {
			os.Remove(f)
		}
	}
}

// hostHeldTurns is the reconcile sweep's skip list: a turn that is the current
// turn of a host session and whose process is still the one the host started
// (alive, same start time), or that the host is still admitting (recorded
// before its process has a pid). A turn whose process is gone is not held,
// however the host ended, so it is interrupted like any other orphan. Each turn
// is looked up as the sweep reaches it. When the host's records cannot be read,
// no host turn is interrupted: closeFn must be called when the sweep is done.
func hostHeldTurns(cfg *Deps) (skip func(ai.Turn) bool, closeFn func()) {
	failClosed := func(t ai.Turn) bool { return isHostInstance(t.OwnerInstanceID) }
	db, err := cfg.OpenDB()
	if err != nil {
		return failClosed, func() {}
	}
	cs := coderhost.NewStore(db.DB)
	return func(t ai.Turn) bool {
		x, ok, err := cs.GetSession(t.ProfileID, t.ConversationID)
		if err != nil {
			return failClosed(t)
		}
		if !ok || x.TurnID != t.ID {
			return false
		}
		if x.TurnPID == 0 { // being admitted: held for as long as the host could still be at it
			updated, perr := time.Parse(time.RFC3339Nano, x.UpdatedAt)
			return perr == nil && time.Since(updated) < admissionWait+10*time.Second
		}
		if x.TurnPID <= 1 || x.TurnStamp == "" || !turnAlive(x.TurnPID) {
			return false
		}
		now := stampOf(x.TurnPID) // unreadable now: still alive, so spared
		return now == "" || now == x.TurnStamp
	}, func() { db.Close() }
}

// isHostInstance is the owner id a host-run turn carries (see hostID).
func isHostInstance(owner string) bool { return strings.HasPrefix(owner, "host-") }

// turnEnv is the environment a turn starts with: the host's without any session token or mission-handoff marker.
func turnEnv(env []string) []string {
	return withoutEnv(env, coderhost.SessionTokenEnv, coderhost.MissionFDEnv, coderhost.CredFDEnv, agenttokens.EnvMissionToken)
}

// withoutEnv returns env without the named variables.
func withoutEnv(env []string, names ...string) []string {
	out := make([]string, 0, len(env))
outer:
	for _, kv := range env {
		for _, n := range names {
			if hasEnvName(kv, n) {
				continue outer
			}
		}
		out = append(out, kv)
	}
	return out
}

// hasEnvName says whether the KEY=value entry kv is the variable n (names are case-insensitive on Windows).
func hasEnvName(kv, n string) bool {
	if len(kv) <= len(n) || kv[len(n)] != '=' {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(kv[:len(n)], n)
	}
	return kv[:len(n)] == n
}
