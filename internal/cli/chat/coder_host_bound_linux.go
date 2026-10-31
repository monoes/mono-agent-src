//go:build linux

package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
)

// A turn launched with peer-credential binding on is started and handed its credentials in this order (design 3c):
//
//  1. the process is started with a held pidfd, with NO token in its environment or arguments, and the read end of a
//     frame pipe (the host keeps the write end, close-on-exec, so no other turn can inherit it);
//  2. its start stamp is read and it is confirmed alive;
//  3. the token is registered, pending, with a handle of its own (a dup of the pidfd);
//  4. ONE write transaction re-reads that the session's current turn is this turn and inserts the token and its
//     binding (worker pid, stamp, host generation);
//  5. the revocation row is read, the entry is activated, and only then is the frame written and the pipe closed.
//
// A failure before step 5 kills the turn through the pidfd, unregisters the entry and revokes whatever was inserted:
// nothing was delivered. The turn itself waits for the frame (10 s) and ends if it does not come.

// boundHost is what the bound launch needs of the host's binding services.
type boundHost interface {
	Ensure(ctx context.Context, profile string) error
	Generation(profile string) (string, bool)
}

// boundLaunch is the configuration of the bound launch; a launcher without one launches exactly as it always did.
type boundLaunch struct {
	db     *sql.DB
	host   boundHost
	binder *procbind.Binder // the registry the host's services answer from
	// afterMint is a test seam: it runs between the transaction of step 4 and the revocation read of step 5.
	afterMint func()
}

// boundDelivery is one turn's frame pipe, pidfd and registration.
type boundDelivery struct {
	b            *boundLaunch
	r, w         *os.File
	pidfd        int
	missionToken string
	// onKill, when set, is called before the leader is killed (the launcher marks the turn as stopping, so its group
	// is killed with the leader's exit, before the reap).
	onKill func()
}

// attach prepares cmd for a bound launch: the frame pipe on descriptor 3 and its marker, nothing else.
func (b *boundLaunch) attach(cmd *exec.Cmd, missionToken string) (*boundDelivery, error) {
	r, w, err := os.Pipe() // close-on-exec both ends; the child gets r only through ExtraFiles
	if err != nil {
		return nil, err
	}
	if len(cmd.ExtraFiles) != 0 {
		r.Close()
		w.Close()
		return nil, errors.New("extra files already set")
	}
	cmd.ExtraFiles = []*os.File{r}
	cmd.Env = append(cmd.Env, coderhost.CredFDEnv+"=3")
	return &boundDelivery{b: b, r: r, w: w, pidfd: -1, missionToken: missionToken}, nil
}

// start starts the process detached with a pidfd; the host's copy of the read end is closed whether it started or not.
func (d *boundDelivery) start(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, PidFD: &d.pidfd}
	err := cmd.Start()
	d.r.Close()
	if err != nil {
		return err
	}
	if d.pidfd < 0 { // a kernel that gave no pidfd cannot run a bound turn
		_ = cmd.Process.Kill()
		return errors.New("the kernel gave no pidfd for the turn, so it cannot be bound")
	}
	return nil
}

// kill ends the turn's leader through its own pidfd (never by pid or group).
// setOnKill sets the hook called before the leader is killed.
func (d *boundDelivery) setOnKill(f func()) { d.onKill = f }

func (d *boundDelivery) kill() {
	if d.onKill != nil {
		d.onKill() // before the signal: the exit it causes must find the turn already marked as being stopped
	}
	if d.pidfd >= 0 {
		_ = unix.PidfdSendSignal(d.pidfd, unix.SIGKILL, nil, 0)
	}
}

// close releases the pipe and the pidfd; safe to call once.
func (d *boundDelivery) close() {
	d.w.Close()
	d.r.Close()
	if d.pidfd >= 0 {
		unix.Close(d.pidfd)
		d.pidfd = -1
	}
}

// deliver runs steps 2 to 5 for the turn whose process is pid with start stamp. On error nothing was delivered and the
// process has been killed; the caller waits for it as for any other turn.
func (d *boundDelivery) deliver(profile, conv, turn string, pid int, stamp string, revoke func() error) error {
	b := d.b
	fail := func(err error) error {
		d.kill()
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dupfd, err := unix.FcntlInt(uintptr(d.pidfd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return fail(fmt.Errorf("could not hold the turn's pidfd: %w", err))
	}
	h := procbind.HandleFromPidfd(dupfd)
	if !h.Alive() {
		_ = h.Close()
		return fail(errors.New("the turn ended before its credentials could be bound"))
	}
	generation, ok := b.host.Generation(profile)
	if !ok {
		_ = h.Close()
		return fail(errors.New("the binding service has no generation for this profile"))
	}
	secret, hash, err := procbind.NewSecret(procbind.SessionPrefixV2)
	if err != nil {
		_ = h.Close()
		return fail(err)
	}
	id, err := procbind.NewTokenID()
	if err != nil {
		_ = h.Close()
		return fail(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	expires := now.Add(coderhost.SessionTokenTTL)
	scope := procbind.SessionScope(conv, turn)
	binder := b.binder
	gen, err := binder.Register(procbind.EntrySpec{TokenID: id, Kind: procbind.KindSession, Profile: profile, Scope: scope, Session: conv, Turn: turn, Expires: expires}, h)
	if err != nil { // the registry closed the handle it was given
		return fail(fmt.Errorf("could not register the turn's token: %w", err))
	}
	minted := false
	undo := func(err error) error {
		binder.Reap(profile, id, gen)
		d.kill()
		if minted {
			if rerr := revoke(); rerr != nil {
				err = errors.Join(err, rerr)
			}
		}
		return err
	}
	key, err := procbind.RowKey(ctx, b.db, profile) // the mint: it may create the profile's key
	if err != nil {
		return undo(fmt.Errorf("could not get the row key: %w", err))
	}
	req := procbind.MintRequest{
		Profile: profile, WorkerPID: pid, WorkerStamp: stamp, HostGeneration: generation, Now: now,
		Tokens: []procbind.MintToken{{Kind: procbind.KindSession, ID: id, Hash: hash, Scope: scope, Expires: expires, ConversationID: conv, TurnID: turn}},
		Check: func(ctx context.Context, x procbind.DBTX) error {
			var current string
			if err := x.QueryRowContext(ctx, `SELECT turn_id FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profile, conv).Scan(&current); err != nil {
				return errors.New("the session is gone")
			}
			if current != turn {
				return errors.New("the session's current turn is not this turn")
			}
			return nil
		},
	}
	if err := validateMint(req, key, profile); err != nil {
		return undo(err)
	}
	if err = procbind.MintTx(ctx, b.db, key, req); err != nil {
		// an error does not prove that nothing was written (a commit can fail after it was applied): the token is ended
		// by its own id, so a row that did land is revoked and cannot be accepted
		err = fmt.Errorf("could not bind the turn's token: %w", err)
		if rerr := b.revokeByID(ctx, profile, procbind.KindSession, id); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return undo(err)
	}
	minted = true
	if b.afterMint != nil {
		b.afterMint()
	}
	if rv, err := procbind.IsRevoked(ctx, b.db, procbind.KindSession, id); err != nil || rv {
		return undo(errors.New("the turn's token was revoked before it was delivered"))
	}
	if err := binder.Activate(profile, id, gen); err != nil {
		return undo(errors.New("the turn's registration ended before it was delivered"))
	}
	frame := procbind.CredFrame{SessionToken: procbind.Secret(secret)}
	if d.missionToken != "" {
		frame.MissionConfirmation = procbind.Secret(d.missionToken)
	}
	raw, err := frame.Encode()
	frame.Zero()
	if err != nil {
		return undo(err)
	}
	_, werr := d.w.Write(raw) // one atomic write: the whole frame is within PIPE_BUF
	procbind.Secret(raw).Zero()
	if cerr := d.w.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return undo(fmt.Errorf("could not hand the credentials to the turn: %w", werr))
	}
	return nil
}

// validateMint is the caller's check of what MintTx does not look at: the request names the profile whose key signs the
// rows, every token's scope belongs to its kind, and it ends after the mint.
func validateMint(req procbind.MintRequest, key []byte, profile string) error {
	if len(key) == 0 || req.Profile != profile {
		return errors.New("the mint does not name the profile whose key signs it")
	}
	for _, t := range req.Tokens {
		want := "session:"
		if t.Kind == procbind.KindMission {
			want = "mission:"
		}
		if !strings.HasPrefix(t.Scope, want) {
			return fmt.Errorf("the scope of a %s token is not a %s scope", t.Kind, t.Kind)
		}
		if !t.Expires.After(req.Now) {
			return errors.New("the token would already be expired")
		}
	}
	return nil
}

// revokeByID ends one v2 token by its id (and its live registration), whether or not its mint is known to have landed.
func (b *boundLaunch) revokeByID(ctx context.Context, profile string, kind procbind.Kind, id string) error {
	sel := procbind.Selector{Profile: profile, Kind: kind, TokenID: id}
	return procbind.Revoker{DB: b.db, Binder: b.binder}.Run(ctx, []procbind.Kind{kind}, procbind.Scope{Profile: profile, TokenID: id},
		func(*sql.Conn) ([]procbind.Selector, error) { return []procbind.Selector{sel}, nil })
}
