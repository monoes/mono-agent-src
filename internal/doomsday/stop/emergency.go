package stop

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// UnrecordedFlag is the in-memory stop-unrecorded flag (spec 5.3 step 5): set when an emergency message
// arrives, cleared only when the marker AND the database intent are durable. While it is set, an orderly
// shutdown writes the fail-closed record instead of the clean one.
type UnrecordedFlag struct{ v atomic.Bool }

func (f *UnrecordedFlag) Set()        { f.v.Store(true) }
func (f *UnrecordedFlag) Clear()      { f.v.Store(false) }
func (f *UnrecordedFlag) IsSet() bool { return f.v.Load() }

// ErrSecretUnreadable is the explicit failure of a missing or unreadable per-run secret: the sender falls back
// to the independent containment attempt and the generation is treated as stop-requested.
var ErrSecretUnreadable = errors.New("stop: the per-run secret is unreadable")

// SockPath is the emergency socket of one generation, in the private run directory.
func (r RunDir) SockPath(gen uint64) string { return r.Dir + fmt.Sprintf("/e%d.sock", gen) }

func emergencyMAC(secret []byte, gen uint64) string {
	h := hmac.New(sha256.New, secret)
	fmt.Fprintf(h, "monoagent-doomsday-emergency-v1\x00%d", gen)
	return hex.EncodeToString(h.Sum(nil))
}

// NewEmergencyHandler is what the host does on receipt: close the barrier in memory and cancel in-flight
// contexts (Seal), set the stop-unrecorded flag, and start the Stop order through kick.
func NewEmergencyHandler(g *Gate, flag *UnrecordedFlag, kick func(gen uint64)) func(gen uint64) {
	return func(gen uint64) {
		if flag != nil {
			flag.Set()
		}
		if g != nil {
			g.Seal()
		}
		if kick != nil {
			kick(gen)
		}
	}
}

// EmergencyServer listens on a Unix socket accepted only for the operator's UID, authenticated by the per-run
// secret (not derived from the vault key, so a failed key lookup cannot block it).
type EmergencyServer struct {
	Dir    RunDir
	Gen    uint64
	UID    uint32                         // the only accepted peer uid
	Peer   func(net.Conn) (uint32, error) // peer credentials; defaults to the platform implementation
	OnStop func(gen uint64)
}

// Start binds the socket and serves in the background until ctx ends. A bind error is returned: a host that cannot be
// reached by the emergency message must not run the generation (a Stop would find no socket and take the host for absent).
func (s *EmergencyServer) Start(ctx context.Context) error {
	ln, err := s.listen(ctx)
	if err != nil {
		return err
	}
	go func() { _ = s.accept(ctx, ln) }()
	return nil
}

// Serve accepts until ctx ends.
func (s *EmergencyServer) Serve(ctx context.Context) error {
	ln, err := s.listen(ctx)
	if err != nil {
		return err
	}
	return s.accept(ctx, ln)
}

func (s *EmergencyServer) listen(ctx context.Context) (net.Listener, error) {
	if err := s.Dir.fsys().MkdirAll(s.Dir.Dir, 0o700); err != nil {
		return nil, err
	}
	path := s.Dir.SockPath(s.Gen)
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	go func() { <-ctx.Done(); ln.Close() }()
	return ln, nil
}

func (s *EmergencyServer) accept(ctx context.Context, ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handle(c)
	}
}

func (s *EmergencyServer) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	peer := s.Peer
	if peer == nil {
		peer = peerUID
	}
	uid, err := peer(c)
	if err != nil || uid != s.UID {
		fmt.Fprintln(c, "ERR peer")
		return
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return
	}
	f := strings.Fields(line)
	if len(f) != 3 || f[0] != "STOP" || f[1] != fmt.Sprint(s.Gen) {
		fmt.Fprintln(c, "ERR request")
		return
	}
	secret, serr := s.Dir.ReadSecret(s.Gen)
	if serr != nil {
		// Explicit failure, reported; a same-UID message can only cause a stop (fail closed), so the generation
		// is treated as stop-requested.
		s.OnStop(s.Gen)
		fmt.Fprintln(c, "ERR secret-unreadable stop-requested")
		return
	}
	if !hmac.Equal([]byte(f[2]), []byte(emergencyMAC(secret, s.Gen))) {
		fmt.Fprintln(c, "ERR auth")
		return
	}
	s.OnStop(s.Gen)
	fmt.Fprintln(c, "OK")
}

// SendEmergency tells a running host to close its barrier. ErrSecretUnreadable means the sender could not sign
// and must fall back to the independent path (marker plus containment kill by the CLI).
func SendEmergency(r RunDir, gen uint64) error {
	secret, err := r.ReadSecret(gen)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSecretUnreadable, err)
	}
	c, err := net.DialTimeout("unix", r.SockPath(gen), 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(c, "STOP %d %s\n", gen, emergencyMAC(secret, gen))
	reply, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(reply) != "OK" {
		return fmt.Errorf("stop: emergency channel replied %q", strings.TrimSpace(reply))
	}
	return nil
}
