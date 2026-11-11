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
	// revokeTurn drops ONE turn's tokens (nil: the conversation's, as revoke does); used for a turn the gate refused, whose late
	// exit must not revoke a newer turn's token.
	revokeTurn func(profileID, conversationID, turnID string) error
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
	// onProcess, when set, makes the started process's identity durable (the session's turn_pid and turn_stamp) the moment the
	// process exists, before anything else of the start can fail. If it fails, the start fails and the process is stopped: a process
	// whose identity cannot be recorded is not allowed to run, because a host restart could not find it.
	onProcess func(profileID, conversationID, turnID string, pid int, stamp string, bound bool) error
	// runStamp, when set, says which open-ended run (charter id and generation) a session's turn belongs to; recordRun writes it on the
	// session the moment the turn's process exists, before the waiter starts, so a stop of the run finds the session's tokens. Both
	// nil is the launch exactly as it was; a zero answer stamps nothing. A failed record stops the process like a failed identity record.
	runStamp  func(profileID, conversationID string) (charterID string, generation int64)
	recordRun func(profileID, conversationID, turnID, charterID string, generation int64) error
	// beforeRelease, when set, is asked after the process and its identity exist and before anything is delivered to it (the bound
	// child blocks on its credential frame until then, so a refusal ends a process that has not run). bound says whether the turn is
	// launched with peer-credential binding: a legacy child is already running, so a refusal of one stops a started process. An error
	// fails the start like any other failed delivery (the turn started, is stopped, its tokens end). The context is cancelled when the
	// start gives up on the hook (a deadline, a panic) or returns: a hook that takes something it cannot give back (a ticket) must check it
	// under its own lock first, so a late completion for a refused turn takes nothing.
	beforeRelease func(ctx context.Context, profileID, conversationID, turnID string, bound bool) error
	// place, when set, puts the turn's process where it must be born (a containment cgroup): it adjusts cmd after the launcher built its
	// process attributes (Setsid, pidfd) and before the process exists, and returns a release that the launcher calls right after the start
	// (success or failure). An error refuses the turn before anything runs. Bound launches only: a legacy start with place set fails.
	place func(cmd *exec.Cmd, profileID, conversationID string) (release func(), err error)
	// adopt, when set, is asked for a BOUND turn right after its process was started and identified, before its identity is committed,
	// before the release check and before anything is delivered: it puts the already running process into the run's unit (the macOS
	// session unit records the leader; other platforms place a process at birth and have no adopt). An error ends the child, which has been
	// given nothing, and fails the start. held says the unit has taken over reaping the leader (its hold keeps the number reserved until
	// the unit lets go): the launcher then never waits for the process itself (no cmd.Wait), it only observes the exit without reaping,
	// and that holds on an error too (the hold may have been taken before the refusal). A hook that panics is treated as held.
	adopt func(profileID, conversationID string, pid int, stamp string) (held bool, err error)
	// afterExitRevoke is a test seam: the exit goroutine calls it once its revokes have run, before the turn is done.
	afterExitRevoke func()
}

// afterWaiterStarted is a test seam: it runs right after the exit waiter started, before the legacy token is bound.
var afterWaiterStarted func()

// releaseTimeout bounds the release hook: a hook that does not answer in time refuses (a seam for tests). The hook runs on the start's
// goroutine, outside the host's lock, but a blocked start would hold up the command that asked for it.
var releaseTimeout = 30 * time.Second

// hookRecover runs f and turns a panic into an error, so a faulty hook cannot unwind Start with a spawned, unwaited child.
func hookRecover(what string, f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s panicked: %v", what, r)
		}
	}()
	return f()
}

// boundedHook runs f on its own goroutine, with a recover, and gives up on it after releaseTimeout: the answer is then an error, and
// the context f was given is cancelled (it is cancelled whenever boundedHook returns). A hook that is still running at the deadline is
// left to finish; its late result is dropped, and it must check its context before any effect it cannot take back.
func boundedHook(what string, f func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- hookRecover(what, func() error { return f(ctx) }) }()
	select {
	case err := <-res:
		return err
	case <-time.After(releaseTimeout):
		return fmt.Errorf("%s did not answer in time", what)
	}
}

// hostID is what makes a host-owned name safe to put in a file name and a flag.
var hostID = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// hostTurnWait is how long stopAndWait waits for the exit of a stopped turn (a seam for tests).
var hostTurnWait = func() time.Duration { return hostTurnGrace + 5*time.Second }

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

// revokeTurnTokens ends one turn's tokens, trying a few times like revokeSession.
func (l *execLauncher) revokeTurnTokens(profileID, conversationID, turnID string) error {
	if l.revokeTurn == nil {
		return l.revokeSession(profileID, conversationID)
	}
	backoff := l.revokeBackoff
	if backoff <= 0 {
		backoff = 200 * time.Millisecond
	}
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if err = l.revokeTurn(profileID, conversationID, turnID); err == nil {
			return nil
		}
		if l.logf != nil {
			l.logf("WARNING: ending the session token of turn %s of %s failed (attempt %d): %v", turnID, conversationID, attempt+1, err)
		}
		if attempt < 3 {
			time.Sleep(backoff << attempt)
		}
	}
	return fmt.Errorf("the session token of turn %s of %s could not be ended: %w", turnID, conversationID, err)
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
		mint := l.child.mint
		if l.bound != nil {
			mint = l.child.prepareBound // a bound launch writes the child's v2 token itself
		}
		if ct, err = mint(profileID, conversationID, turnID); err != nil {
			os.Remove(promptPath)
			return nil, err
		}
	}
	launched := false
	defer func() {
		if ct != nil && !launched {
			if rerr := l.child.revokeToken(ct); rerr != nil && l.logf != nil { // not started: the turn's own wait revokes it otherwise
				l.logf("WARNING: %v", rerr)
			}
		}
	}()
	pipe := missionToken // what goes over the inherited pipe: a mission confirmation, or a child's task token
	if ct != nil {
		if missionToken != "" {
			os.Remove(promptPath)
			return nil, errors.New("a mission child's turn cannot carry a mission confirmation")
		}
		pipe = ct.secret // empty for a bound launch: the task token travels in the frame
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
		if bd, err = l.bound.attach(cmd, pipe); err != nil {
			os.Remove(promptPath)
			return nil, fmt.Errorf("could not prepare the credential pipe: %w", err)
		}
		defer bd.close()
		if ct != nil {
			bd.child = ct
		}
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
	startProcess := func() error {
		if l.place != nil {
			return errors.New("placing a turn needs a bound launch; a legacy start would run outside the placement")
		}
		return clicore.DetachStart(cmd)
	}
	if bd != nil {
		startProcess = func() error {
			var pre func(*exec.Cmd) error
			var release func()
			if l.place != nil {
				pre = func(c *exec.Cmd) error {
					return hookRecover("the placement", func() (perr error) {
						release, perr = l.place(c, profileID, conversationID)
						return perr
					})
				}
			}
			err := bd.start(cmd, pre)
			if release != nil {
				// the placement's descriptor is needed until the process exists, no longer; a release that panics is not fatal to the host
				if rerr := hookRecover("the placement release", func() error { release(); return nil }); rerr != nil && l.logf != nil {
					l.logf("coder host: %v", rerr)
				}
			}
			return err
		}
	}
	if err := startProcess(); err != nil {
		os.Remove(promptPath)
		if rerr := l.revokeSession(profileID, conversationID); rerr != nil {
			err = errors.Join(err, &coderhost.TokensLiveError{Err: rerr})
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
	if stamp == "" {
		// A turn that exits at once (a refusal, a missing binary) is already a zombie here, which stampOf does not read; it is our own
		// unreaped child, so its start time still names it. It is then reaped by the waiter and fails with its real exit status.
		stamp = exitedChildStamp(cmd.Process.Pid)
	}
	// The stamp the legacy token is bound with, read while nothing can have reaped the child (the waiter starts later): after a reap the
	// number may name another process, and its stamp would bind the token to it.
	bindStamp := coderhost.ProcStamp(cmd.Process.Pid)
	t := &execTurn{pid: cmd.Process.Pid, stamp: stamp, done: make(chan struct{}), strict: bd != nil}
	t.ownRevoke = func() error {
		err := l.revokeTurnTokens(profileID, conversationID, turnID)
		// a mission child's separate task token dies with the refusal too (nil-safe)
		if err = errors.Join(err, l.child.revokeToken(ct)); err != nil {
			err = &coderhost.TokensLiveError{Err: err}
		}
		return err
	}
	// The waiter (which reaps the child and ends its tokens) starts only AFTER the process's identity is recorded: until then nothing
	// can reap the child, so its pid and group are still its own and can be signalled safely if the record fails.
	held := false // the unit's hold reaps the leader (see adopt): set before the waiter starts, never changed after
	startWaiter := func() {
		go func() {
			observed := false
			if t.strict {
				if holdLeader(t.pid) { // the leader is dead but unreaped: its group is still this turn's to signal
					observed = true
					t.settle()
				} else if !held || turnAlive(t.pid) {
					// its exit cannot be observed without reaping it: end it now, while it is still ours. A HELD leader that is neither
					// running nor a zombie was already reaped by the unit (its number is free): nothing is signalled by number then.
					t.holdFailed(cmd.Process.Kill)
				}
			}
			if held {
				// The unit's hold owns the reap: a leader's session number is its pid, and only an unreaped (zombie) leader keeps the number
				// from being given to someone else while the unit is open. cmd.Wait would reap it (and, with the number reused, could reap a
				// stranger), so the turn ends on the observed exit alone; the exit status is not read (no consumer of it needs it).
				if !observed {
					awaitGoneUnreaped(t.pid)
				}
			} else {
				t.err = cmd.Wait()
			}
			t.markReaped()
			// before done closes: a session that sees its turn end has no token left. The turn is held here while the
			// revoke is retried; a revoke that still fails is reported as the turn's error.
			// The exit ends this turn's own tokens, decided here at launch (the turn id is known): never the conversation's, which
			// by the time a delayed cleanup runs may hold a newer turn's. A launcher without a per-turn revoke (revokeTurn nil)
			// keeps the conversation-wide one.
			if rerr := l.revokeTurnTokens(profileID, conversationID, turnID); rerr != nil {
				t.err = errors.Join(t.err, &coderhost.TokensLiveError{Err: rerr}) // the host holds the session until the token is ended
			}
			if rerr := l.child.revokeToken(ct); rerr != nil { // the child's task token ends with its run (nil-safe); a failure is the turn's error
				t.err = errors.Join(t.err, &coderhost.TokensLiveError{Err: rerr})
			}
			os.Remove(promptPath) // before done closes: whoever sees the turn end sees no prompt left
			if l.afterExitRevoke != nil {
				l.afterExitRevoke()
			}
			close(t.done)
		}()
		launched = true // the wait goroutine revokes the child's token from here on
	}
	if stamp == "" {
		startWaiter()
		return nil, t.fail(errors.New("could not identify the turn's process (its start time is unreadable), so it was stopped"))
	}
	// KNOWN GAP (accepted for now, not built; design sketch on issue #1310): a host crash between the process being created above and
	// this identity commit leaves the session with PID 0. Recovery then cannot adopt the child, treats the turn as gone and settles, and a
	// queued turn may start beside it. Closing it needs a durable launch intent written BEFORE the spawn (a per-turn nonce, or an
	// enumerable unit); build it if it ever occurs.
	// A process whose identity cannot be kept must not outlive this call (a restart could not find it): it is killed at once,
	// leader and group. Nothing waits for the child yet, so the pid cannot have been reused and the group is still its own.
	killUnrecorded := func(err error) error {
		_ = cmd.Process.Kill()
		_ = killGroup(t.pid)
		t.markKilled() // under the lock, before the waiter can reap: Stop, the grace and afterDone never signal this pid or group again
		startWaiter()  // reaps it and ends its tokens
		return t.fail(err)
	}
	if bd != nil && l.adopt != nil {
		// Joins the run's unit before anything else is recorded about the turn or given to it: the child waits for its credentials, so it
		// has done nothing yet. A host that dies from here until the frame is delivered leaves the child with a closed pipe, which ends it
		// (coderhost.StashCredFrame reads the frame first, fatally); the 10 s bound there ends a child whose host is alive but wedged.
		var aerr error
		perr := hookRecover("the adoption", func() (e error) { held, aerr = l.adopt(profileID, conversationID, t.pid, stamp); return aerr })
		if perr != nil {
			if aerr == nil { // a panic: nobody can say whether the hold was taken, so nobody else reaps either
				held = true
			}
			return nil, killUnrecorded(fmt.Errorf("the turn's process could not be put in the run's unit, so it was stopped: %w", perr))
		}
	}
	if l.onProcess != nil {
		if err := l.onProcess(profileID, conversationID, turnID, t.pid, stamp, bd != nil); err != nil {
			return nil, killUnrecorded(fmt.Errorf("could not record the turn's process, so it was stopped: %w", err))
		}
	}
	if l.runStamp != nil && l.recordRun != nil {
		// A zero answer stamps nothing and never clears an earlier stamp (the stamp belongs to the run and is read by generation).
		if err := boundedHook("the run stamp", func(context.Context) error {
			if charterID, gen := l.runStamp(profileID, conversationID); charterID != "" || gen != 0 {
				return l.recordRun(profileID, conversationID, turnID, charterID, gen)
			}
			return nil
		}); err != nil {
			return nil, killUnrecorded(fmt.Errorf("could not record the run of the turn, so it was stopped: %w", err))
		}
	}
	startWaiter()
	if afterWaiterStarted != nil {
		afterWaiterStarted()
	}
	if l.beforeRelease != nil {
		// A refused release is a start that failed after the process existed: StartedError on purpose (the spend cap stays counted, the
		// command fails, nothing retries it). The stop is the ordinary one (SIGTERM, then the grace): a bound child that has not been given
		// its credentials has not run, so it needs no pidfd kill.
		if err := boundedHook("the release check", func(ctx context.Context) error {
			return l.beforeRelease(ctx, profileID, conversationID, turnID, bd != nil)
		}); err != nil {
			return nil, t.fail(fmt.Errorf("the turn was not released: %w", err))
		}
	}
	if ct != nil && bd == nil { // a bound launch binds the child's token inside its own transaction
		if err := l.child.bind(profileID, conversationID, ct, t.pid, bindStamp); err != nil {
			return nil, t.fail(err)
		}
	}
	if bd != nil {
		bd.setOnKill(t.markStopping)
		if err := bd.deliver(profileID, conversationID, turnID, t.pid, coderhost.BoundStamp(t.pid), func() error { return l.revokeSession(profileID, conversationID) }); err != nil {
			return nil, t.fail(err)
		}
		if t.isReaped() {
			// The leader was reaped before this point, so the exit goroutine's revoke has run or is about to, possibly
			// before the mint committed (the bound token is minted after the process exists): that would leave a live
			// token and registration for a dead worker. End them again now. If the leader is not reaped yet, the
			// goroutine's revoke comes after the commit.
			rerr := errors.Join(l.revokeSession(profileID, conversationID), l.child.revokeToken(ct))
			if rerr != nil {
				return nil, &coderhost.StartedError{Err: errors.Join(errors.New("the turn ended while its credentials were being delivered"), &coderhost.TokensLiveError{Err: rerr})}
			}
		}
	} else if l.bind != nil {
		if err := l.bind(profileID, conversationID, turnID, t.pid, bindStamp); errors.Is(err, coderhost.ErrHostInsideTurn) {
			// the host runs inside a session's turn: no token is bound, so the master tools refuse in this turn
			if rerr := l.revokeSession(profileID, conversationID); rerr != nil {
				return nil, t.fail(&coderhost.TokensLiveError{Err: rerr})
			}
			if l.logf != nil {
				l.logf("WARNING: %v; turn %s runs without a usable session token", err, turnID)
			}
		} else if err != nil {
			return nil, t.fail(fmt.Errorf("could not bind the session token to the turn: %w", err))
		}
	}
	if err := awaitAdmission(logPath, offset, t.done); err != nil {
		return nil, t.fail(err)
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
// fail stops the turn and returns err; when the turn's exit could not end its tokens (the exit goroutine's failure, known once the
// process is gone) that failure is part of what comes back, so the host holds the session instead of settling it.
func (t *execTurn) fail(err error) error {
	return &coderhost.StartedError{Err: t.failWith(err)} // the process exists: the turn started, whatever else failed
}

func (t *execTurn) failWith(err error) error {
	if !t.stopAndWait() {
		// The turn had not ended, and its exit's cleanup had not run, when the wait gave up: its tokens may still be live.
		// The host holds the session until they are ended (the late exit also ends the turn's own tokens).
		return errors.Join(err, &coderhost.TokensLiveError{Err: errors.New("the turn had not ended when its start was given up on, so its tokens may still be live"), Ended: t.done})
	}
	if coderhost.TokensMayBeLive(t.err) {
		err = errors.Join(err, t.err)
	}
	return err
}

// stopAndWait stops the turn and waits (bounded) for its exit, cleanup included; false when the bound ran out first.
func (t *execTurn) stopAndWait() bool {
	t.Stop()
	select {
	case <-t.done:
		return true
	case <-time.After(hostTurnWait()):
		return false
	}
}

type execTurn struct {
	pid   int
	stamp string
	done  chan struct{}
	err   error
	once  sync.Once

	// strict is set for a bound turn only. The ordinary (unbound) launcher stops a turn exactly as it always did: the
	// group is signalled by number whenever Stop is called, and its stragglers are killed once the leader has been
	// reaped (the pid-reuse window after the reap is that existing behaviour's). A bound turn is stricter: the leader's
	// exit is observed without reaping it, its stragglers are killed in that window, and the group is never signalled
	// by number after the reap.
	strict bool
	// killed is set when the launcher killed the turn itself before the waiter started: no number-based group signal is sent after it.
	killed bool

	// ownRevoke ends this turn's own tokens (its session token and a mission child's task token) at once, for a refusal.
	ownRevoke func() error

	mu       sync.Mutex
	stopping bool // Stop was asked for (or the launcher is about to kill the leader itself)
	held     bool // strict, and the leader's exit is observed without reaping it
	exited   bool // held, and the leader has exited (not yet reaped, while reaped is false)
	reaped   bool // held, and cmd.Wait has returned: the pid is free, so the group is not signalled by number again

	term, kill func(pid int) error // the group signals; nil is terminateGroup / killGroup
}

func (t *execTurn) terminateGrp() error {
	if t.term != nil {
		return t.term(t.pid)
	}
	return terminateGroup(t.pid)
}

func (t *execTurn) killGrp() error {
	if t.kill != nil {
		return t.kill(t.pid)
	}
	return killGroup(t.pid)
}

// settle runs for a strict turn, once its leader has exited and before it is reaped: whatever of a stopped turn's
// group ignored the SIGTERM goes with it, while the pid still cannot name anything else.
func (t *execTurn) settle() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.held, t.exited = true, true
	// A bound turn's leader has exited and is still unreaped, so its group is still ours: whatever it left running in the group is killed
	// NOW, whether or not a Stop ever comes (a stop that arrives later may find nothing it is allowed to signal, and token revocation does not
	// stop processes). A behaviour difference of bound mode: background processes of a bound turn do not outlive its leader.
	_ = t.killGrp()
	// From here the leader is about to be reaped (cmd.Wait runs next, outside the lock) and its pid and group are free the moment it is: no
	// later Stop, grace expiry or afterDone may signal them by number, in the gap before markReaped as much as after it.
}

// markReaped records that cmd.Wait returned. For a strict turn whose leader was held, no group signal by number
// happens after it.
func (t *execTurn) markReaped() {
	t.mu.Lock()
	t.reaped = true
	t.mu.Unlock()
}

// isReaped says whether cmd.Wait has returned. It is set before the exit goroutine's revokes run.
func (t *execTurn) isReaped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reaped
}

// markStopping records that the turn is being given up on before its leader is killed by other means (the launcher's
// pidfd kill on a failed bound delivery), so the exit that follows kills the group too. Descendants that left the group
// (a setsid of their own) are outside it either way.
func (t *execTurn) markStopping() {
	t.mu.Lock()
	t.stopping = true
	t.mu.Unlock()
}

// pinned says whether the group may be signalled by number: always for an ordinary turn (as before), and for a strict
// one until its held leader is reaped. The caller holds t.mu.
func (t *execTurn) pinned() bool {
	if t.killed { // killed by the launcher before anything could reap it: nothing is signalled by number again
		return false
	}
	return !(t.strict && t.held) // a strict turn whose leader has exited is about to be reaped (settle ran): its group is no longer ours
}

// awaitGoneUnreaped returns once pid is no longer a running process (a zombie counts as not running), bounded; it reaps nothing. It is
// what a held leader's turn waits on when its exit could not be observed through holdLeader (the leader was killed instead).
func awaitGoneUnreaped(pid int) {
	for end := time.Now().Add(heldGoneWait); time.Now().Before(end) && turnAlive(pid); time.Sleep(10 * time.Millisecond) {
	}
}

// heldGoneWait bounds awaitGoneUnreaped (a seam for tests).
var heldGoneWait = 10 * time.Second

// holdLeader waits for a bound leader's exit without reaping it (a seam: tests make it fail).
var holdLeader = awaitExitNoReap

// holdFailed handles a bound leader whose exit cannot be observed unreaped (waitid failed). Its group could then only be signalled by
// number after the reap, with a reused-group risk, so the turn is ended instead: the leader is killed through the Go process handle (a
// no-op once it is done) and, only if that found it still ours, its group; nothing is signalled by number again.
func (t *execTurn) holdFailed(killLeader func() error) {
	if err := killLeader(); err == nil {
		t.mu.Lock()
		_ = t.killGrp()
		t.mu.Unlock()
	}
	t.markKilled()
}

// markKilled records that the launcher killed the turn itself (a failed identity record) while nothing could have reaped it: the pid
// and group may be reused as soon as the waiter reaps the child, so no later Stop, grace expiry or afterDone signals them by number.
func (t *execTurn) markKilled() {
	t.mu.Lock()
	t.killed = true
	t.mu.Unlock()
}

// afterDone is what the stop goroutine does once the leader has been reaped: the stragglers of the group are killed now,
// as they always were; a strict turn's were killed before the reap.
func (t *execTurn) afterDone() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !(t.strict && t.held) && !t.killed {
		_ = t.killGrp()
	}
}

// killGroupIfHeld kills the group at the end of the grace period (not again after a held leader exited: its
// stragglers were killed then, or by the Stop that came after).
func (t *execTurn) killGroupIfHeld() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pinned() {
		_ = t.killGrp()
	}
}

// Refuse stops a turn the gate refused after its process existed, and ends its own tokens (session and a child's task
// token) at once. Its exit, however late, revokes only its own turn's tokens (decided at launch), so the next turn of the
// conversation keeps its own.
func (t *execTurn) Refuse() error {
	t.Stop()
	if t.ownRevoke == nil {
		return nil
	}
	return t.ownRevoke()
}

func (t *execTurn) PID() int      { return t.pid }
func (t *execTurn) Stamp() string { return t.stamp }
func (t *execTurn) Wait() error   { <-t.done; return t.err }
func (t *execTurn) Stop() {
	t.once.Do(func() {
		t.mu.Lock()
		t.stopping = true
		if t.pinned() {
			_ = t.terminateGrp() // t is this process's child: its pid is its own until cmd.Wait reaps it
		}
		t.mu.Unlock()
		grace := hostTurnGrace // read here: the goroutine below can outlive a test that swaps it
		go func() {
			select {
			case <-t.done:
				t.afterDone()
			case <-time.After(grace):
				t.killGroupIfHeld()
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
