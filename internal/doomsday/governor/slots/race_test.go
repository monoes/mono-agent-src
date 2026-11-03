package slots

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// fillTo holds n process slots with throwaway members.
func fillTo(t *testing.T, e *env, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := e.s.Reserve(ctx, e.l, capProcess, proc(1000+i)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInProcessLastSlotRaceAdmitsExactlyOne(t *testing.T) {
	for round := 0; round < 25; round++ {
		e := newEnv(t)
		fillTo(t, e, capProcess-1)
		var wins, noSlot atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for g := 0; g < 12; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				<-start
				err := e.s.Reserve(ctx, e.l, capProcess, proc(g))
				switch {
				case err == nil:
					wins.Add(1)
				case errors.Is(err, governor.ErrNoSlot):
					noSlot.Add(1)
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}(g)
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 || noSlot.Load() != 11 {
			t.Fatalf("round %d: wins=%d noSlot=%d", round, wins.Load(), noSlot.Load())
		}
		if n := e.live(t, governor.KindProcess, ""); n != capProcess {
			t.Fatalf("round %d: live=%d", round, n)
		}
	}
}

func TestInProcessSessionRaceRespectsBothCaps(t *testing.T) {
	e := newEnv(t)
	var wins atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			if e.s.Reserve(ctx, e.l, capProcess, sess("t1", g)) == nil {
				wins.Add(1)
			}
		}(g)
	}
	close(start)
	wg.Wait()
	if wins.Load() != capTree || e.live(t, governor.KindSession, "t1") != capTree || e.live(t, governor.KindProcess, "") != capTree {
		t.Fatalf("wins=%d session=%d process=%d", wins.Load(), e.live(t, governor.KindSession, "t1"), e.live(t, governor.KindProcess, ""))
	}
}

// Child processes: the test binary re-executes itself with MODE set and works on the shared database file.
const (
	envMode   = "GOV_SLOTS_CHILD"
	envDB     = "GOV_SLOTS_DB"
	envGo     = "GOV_SLOTS_GO"
	envMember = "GOV_SLOTS_MEMBER"
)

func TestHelperChild(t *testing.T) {
	mode := os.Getenv(envMode)
	if mode == "" {
		t.Skip("child helper")
	}
	e := openEnv(t, os.Getenv(envDB), false)
	member := os.Getenv(envMember)
	switch mode {
	case "race":
		for { // spin until the parent releases both children at once
			if _, err := os.Stat(os.Getenv(envGo)); err == nil {
				break
			}
			time.Sleep(200 * time.Microsecond)
		}
		err := e.s.Reserve(ctx, e.l, capProcess, governor.Request{Kind: governor.KindProcess, MemberID: member})
		switch {
		case err == nil:
			fmt.Println("RESULT WIN")
		case errors.Is(err, governor.ErrNoSlot):
			fmt.Println("RESULT NOSLOT")
		default:
			fmt.Println("RESULT ERR", err)
		}
	case "crash":
		e.s.afterProcessInsert = func() error { os.Exit(3); return nil } // dies inside the open transaction
		_ = e.s.Reserve(ctx, e.l, capProcess, governor.Request{Kind: governor.KindSession, Tree: "t1", MemberID: member})
		fmt.Println("RESULT SURVIVED")
	}
}

func child(t *testing.T, mode, db, goFile, member string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperChild$", "-test.v")
	cmd.Env = append(os.Environ(), envMode+"="+mode, envDB+"="+db, envGo+"="+goFile, envMember+"="+member)
	return cmd
}

func TestTwoProcessLastSlotRaceAdmitsExactlyOne(t *testing.T) {
	rounds := 12
	if testing.Short() {
		rounds = 3
	}
	for round := 0; round < rounds; round++ {
		dir := t.TempDir()
		path := dir + "/dd.db"
		migrate(t, path)
		e := openEnv(t, path, true)
		fillTo(t, e, capProcess-1)
		goFile := dir + "/go"
		var outs [2]strings.Builder
		var cmds [2]*exec.Cmd
		for i := range cmds {
			cmds[i] = child(t, "race", path, goFile, fmt.Sprintf("child-%d", i))
			cmds[i].Stdout = &outs[i]
			if err := cmds[i].Start(); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(150 * time.Millisecond) // both children are past start-up and spinning
		if err := os.WriteFile(goFile, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		for i := range cmds {
			if err := cmds[i].Wait(); err != nil {
				t.Fatalf("child %d: %v\n%s", i, err, outs[i].String())
			}
		}
		wins, noSlot := 0, 0
		for i := range outs {
			s := outs[i].String()
			switch {
			case strings.Contains(s, "RESULT WIN"):
				wins++
			case strings.Contains(s, "RESULT NOSLOT"):
				noSlot++
			default:
				t.Fatalf("round %d child %d: %s", round, i, s)
			}
		}
		if wins != 1 || noSlot != 1 {
			t.Fatalf("round %d: wins=%d noSlot=%d, want exactly one winner", round, wins, noSlot)
		}
		if n := e.live(t, governor.KindProcess, ""); n != capProcess {
			t.Fatalf("round %d: live=%d, want %d", round, n, capProcess)
		}
	}
}

func TestProcessKilledInsideReservationLeavesNoHalfHeldSession(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/dd.db"
	migrate(t, path)
	e := openEnv(t, path, true)
	cmd := child(t, "crash", path, "", "crashed")
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 || strings.Contains(string(out), "SURVIVED") {
		t.Fatalf("child did not die mid-reservation: %v\n%s", err, out)
	}
	if p, s := e.live(t, governor.KindProcess, ""), e.live(t, governor.KindSession, ""); p != 0 || s != 0 {
		t.Fatalf("a crash mid-reserve left process=%d session=%d", p, s)
	}
	for i := 0; i < capProcess; i++ { // the lock the dead child held is gone: the whole cap is usable
		if err := e.s.Reserve(ctx, e.l, capProcess, proc(i)); err != nil {
			t.Fatalf("slot %d after the crash: %v", i, err)
		}
	}
}
