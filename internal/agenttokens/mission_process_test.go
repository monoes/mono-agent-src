package agenttokens

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/procchain"
)

func needAncestry(t *testing.T) {
	t.Helper()
	if !procchain.Supported {
		t.Skip("process ancestry is read on Linux only: elsewhere a mission token is checked without it (documented fallback)")
	}
}

func mintMission(t *testing.T, s *Store) (Token, string) {
	t.Helper()
	tok, secret, err := s.CreateMission(context.Background(), "default", missionB, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok, secret
}

// A token that names no process (an old row, or an unlisted runtime) is accepted from anyone.
func TestUnboundMissionTokenKeepsWorking(t *testing.T) {
	s := newStore(t)
	_, secret := mintMission(t, s)
	if _, err := s.VerifyMission(context.Background(), "default", secret); err != nil {
		t.Fatalf("an unbound token was refused: %v", err)
	}
}

// The calling process, and so everything it starts, is inside its own tree.
func TestBoundMissionTokenAcceptsTheProcessAndItsDescendants(t *testing.T) {
	needAncestry(t)
	s := newStore(t)
	tok, secret := mintMission(t, s)
	ctx := context.Background()
	if err := s.BindMissionProcess(ctx, "default", tok.ID, os.Getpid(), procchain.Stamp(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", secret); err != nil {
		t.Fatalf("the bound process was refused: %v", err)
	}
	// a descendant: the process that started this test is in this process's parent chain
	tok2, secret2 := mintMission(t, s)
	parent := os.Getppid()
	if err := s.BindMissionProcess(ctx, "default", tok2.ID, parent, procchain.Stamp(parent)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", secret2); err != nil {
		t.Fatalf("a descendant of the bound process was refused: %v", err)
	}
}

// A sibling that read the token is not in the worker's tree.
func TestBoundMissionTokenRefusesAProcessOutsideTheTree(t *testing.T) {
	needAncestry(t)
	s := newStore(t)
	tok, secret := mintMission(t, s)
	sib := exec.Command("sleep", "30")
	if err := sib.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sib.Process.Kill(); _ = sib.Wait() })
	ctx := context.Background()
	// the token belongs to the worker "sleep"; this test process is a sibling of it, not inside it
	if err := s.BindMissionProcess(ctx, "default", tok.ID, sib.Process.Pid, procchain.Stamp(sib.Process.Pid)); err != nil {
		t.Fatal(err)
	}
	_, err := s.VerifyMission(ctx, "default", secret)
	if !errors.Is(err, ErrNotInWorkerTree) || !errors.Is(err, ErrInvalid) {
		t.Fatalf("a process outside the worker's tree was accepted: %v", err)
	}
}

// A pid reused by another process has another start stamp.
func TestBoundMissionTokenRefusesAReusedPid(t *testing.T) {
	needAncestry(t)
	s := newStore(t)
	tok, secret := mintMission(t, s)
	ctx := context.Background()
	if err := s.BindMissionProcess(ctx, "default", tok.ID, os.Getpid(), "start:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", secret); !errors.Is(err, ErrNotInWorkerTree) {
		t.Fatalf("a reused pid was accepted: %v", err)
	}
}

// A caller whose chain cannot be read to its end and does not show the process is refused (it
// fails closed: a caller that hides its ancestry is not trusted); one whose partial chain shows
// the process is accepted.
func TestBoundMissionTokenWithAnUnreadableChainFailsClosed(t *testing.T) {
	needAncestry(t)
	s := newStore(t)
	tok, secret := mintMission(t, s)
	ctx := context.Background()
	if err := s.BindMissionProcess(ctx, "default", tok.ID, 4242424, "start:9"); err != nil {
		t.Fatal(err)
	}
	orig := procchain.Chain
	t.Cleanup(func() { procchain.Chain = orig })
	procchain.Chain = func() ([]int, bool) { return []int{os.Getpid()}, false }
	if _, err := s.VerifyMission(ctx, "default", secret); !errors.Is(err, ErrNotInWorkerTree) {
		t.Fatalf("an unreadable chain was accepted: %v", err)
	}
	procchain.Chain = func() ([]int, bool) { return nil, false }
	if _, err := s.VerifyMission(ctx, "default", secret); !errors.Is(err, ErrNotInWorkerTree) {
		t.Fatalf("an empty unreadable chain was accepted: %v", err)
	}
}

// The conductor's path: the pid of a process it really started is bound, and a caller below it
// (its chain shows that process) is accepted while this test process, outside it, is not.
func TestBoundMissionTokenOfARealChildProcess(t *testing.T) {
	needAncestry(t)
	s := newStore(t)
	tok, secret := mintMission(t, s)
	ctx := context.Background()
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := child.Process.Pid
	if err := s.BindMissionProcess(ctx, "default", tok.ID, pid, ProcessStamp(pid)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", secret); !errors.Is(err, ErrNotInWorkerTree) {
		t.Fatalf("the child's sibling was accepted: %v", err)
	}
	orig := procchain.Chain
	t.Cleanup(func() { procchain.Chain = orig })
	procchain.Chain = func() ([]int, bool) { return []int{os.Getpid() + 1, pid, os.Getpid()}, true } // a tool of the child
	if _, err := s.VerifyMission(ctx, "default", secret); err != nil {
		t.Fatalf("a descendant of the real child was refused: %v", err)
	}
}

// A bind of a revoked token is an error, not a silent no-op.
func TestBindMissionProcessOfARevokedTokenFails(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	tok, _ := mintMission(t, s)
	if _, err := s.RevokeOwner(ctx, missionB.Owner); err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	stamp := ProcessStamp(pid)
	if stamp == "" {
		stamp = "start:1"
	}
	if err := s.BindMissionProcess(ctx, "default", tok.ID, pid, stamp); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a revoked token was bound: %v", err)
	}
}

// The refusal says why in words, so it does not read like a bad token.
func TestOutsideTreeRefusalExplainsItself(t *testing.T) {
	for _, w := range []string{"process tree", "detached", "reparented"} {
		if !strings.Contains(ErrNotInWorkerTree.Error(), w) {
			t.Errorf("the refusal does not mention %q: %v", w, ErrNotInWorkerTree)
		}
	}
}

// The recorded process is signed: editing it, or moving it to another token, is refused.
func TestBoundMissionTokenProcessIsTamperEvident(t *testing.T) {
	needAncestry(t)
	s := newStore(t)
	ctx := context.Background()
	tok, secret := mintMission(t, s)
	other, otherSecret := mintMission(t, s)
	self := os.Getpid()
	if err := s.BindMissionProcess(ctx, "default", tok.ID, self, procchain.Stamp(self)); err != nil {
		t.Fatal(err)
	}
	// a pid edited in place (the caller would now be outside the tree, but the row is not genuine)
	if _, err := s.db.Exec(`UPDATE agent_tokens SET mission_pid = ? WHERE id = ?`, self+1, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", secret); !errors.Is(err, ErrInvalid) || errors.Is(err, ErrNotInWorkerTree) {
		t.Fatalf("an edited process record was not refused as forged: %v", err)
	}
	// a record copied onto another token
	if err := s.BindMissionProcess(ctx, "default", tok.ID, self, procchain.Stamp(self)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE agent_tokens SET mission_pid = (SELECT mission_pid FROM agent_tokens WHERE id = ?),
		mission_stamp = (SELECT mission_stamp FROM agent_tokens WHERE id = ?), mission_mac = (SELECT mission_mac FROM agent_tokens WHERE id = ?) WHERE id = ?`,
		tok.ID, tok.ID, tok.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", otherSecret); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a process record moved to another token was accepted: %v", err)
	}
}

// Revoking and the sweep act as before on a bound token.
func TestBoundMissionTokenIsRevokedByItsOwner(t *testing.T) {
	needAncestry(t)
	s := newStore(t)
	ctx := context.Background()
	tok, secret := mintMission(t, s)
	if err := s.BindMissionProcess(ctx, "default", tok.ID, os.Getpid(), procchain.Stamp(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RevokeOwner(ctx, missionB.Owner); err != nil || n != 1 {
		t.Fatalf("revoke: %d %v", n, err)
	}
	if _, err := s.VerifyMission(ctx, "default", secret); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a revoked bound token verified: %v", err)
	}
}

func TestBindMissionProcessRefusesWhatIsNotAProcess(t *testing.T) {
	s := newStore(t)
	tok, _ := mintMission(t, s)
	if err := s.BindMissionProcess(context.Background(), "default", tok.ID, 1, "start:1"); err == nil {
		t.Fatal("pid 1 was bound")
	}
	if err := s.BindMissionProcess(context.Background(), "other", tok.ID, 99, "start:1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another profile bound it: %v", err)
	}
}
