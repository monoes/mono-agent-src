//go:build darwin

package chat

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// awaitExitNoReap on the real kernel: it returns when the child has exited and leaves it unreaped.

func TestDarwinAwaitExitNoReapWaitsForTheExitAndDoesNotReap(t *testing.T) {
	cmd := exec.Command("sleep", "0.3")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() { done <- awaitExitNoReap(cmd.Process.Pid) }()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("the exit of a running child was not held")
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("awaitExitNoReap did not return after the child exited")
	}
	// unreaped: the number is still the child's (a zombie), so the group it names is still its own to signal
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("the exited child was reaped: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

// A child that is already a zombie is held too (it exited before the wait began).
func TestDarwinAwaitExitNoReapHoldsAChildThatAlreadyExited(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if !awaitExitNoReap(cmd.Process.Pid) {
		t.Fatal("a zombie child was not held")
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("the exited child was reaped: %v", err)
	}
	_ = cmd.Wait()
}

// A number that is gone is not held.
func TestDarwinAwaitExitNoReapRefusesAChildThatWasReaped(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if awaitExitNoReap(cmd.Process.Pid) {
		t.Error("a reaped child was held")
	}
}
