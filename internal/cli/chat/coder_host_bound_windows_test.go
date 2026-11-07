//go:build windows

package chat

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// startBoundSleeper starts a real child through the bound launch (the frame pipe attached, the leader pinned).
func startBoundSleeper(t *testing.T) (*boundDelivery, *exec.Cmd) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), winE2ERole+"=sleep")
	b := &boundLaunch{}
	d, err := b.attach(cmd, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.start(cmd, nil); err != nil {
		d.close()
		t.Fatal(err)
	}
	t.Cleanup(func() { d.close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return d, cmd
}

// The turn's leader is ended through its own pinned handle, and every registry entry gets a handle of its own.
func TestBoundLeaderIsEndedThroughItsOwnHandleAndEntriesHoldTheirOwn(t *testing.T) {
	d, _ := startBoundSleeper(t)
	if d.proc == nil || !d.proc.Alive() {
		t.Fatal("the leader is not pinned and alive after the start")
	}
	h, err := d.holdWorker()
	if err != nil {
		t.Fatal(err)
	}
	if !h.Alive() {
		t.Fatal("the held copy does not see the leader alive")
	}
	_ = h.Close() // an entry closing its handle must not close the turn's
	if !d.proc.Alive() {
		t.Fatal("closing an entry's handle closed the turn's")
	}
	h2, err := d.holdWorker()
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	stopping := false
	d.setOnKill(func() { stopping = true })
	d.kill()
	deadline := time.Now().Add(15 * time.Second)
	for h2.Alive() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if h2.Alive() {
		t.Fatal("the leader survived the kill")
	}
	if !stopping {
		t.Fatal("the turn was not marked as stopping before it was killed")
	}
}

// A placement that replaces the launcher's attributes is refused before anything exists.
func TestBoundStartRefusesAPlacementThatReplacedTheAttributes(t *testing.T) {
	for name, edit := range map[string]func(*exec.Cmd){
		"no inherited handle": func(c *exec.Cmd) { c.SysProcAttr.AdditionalInheritedHandles = nil },
		"no detach flags":     func(c *exec.Cmd) { c.SysProcAttr.CreationFlags = 0 },
		"another struct":      func(c *exec.Cmd) { c.SysProcAttr = nil },
	} {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), winE2ERole+"=sleep")
		d, err := (&boundLaunch{}).attach(cmd, "")
		if err != nil {
			t.Fatal(err)
		}
		err = d.start(cmd, func(c *exec.Cmd) error { edit(c); return nil })
		if err == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		d.close()
		if err == nil || cmd.Process != nil {
			t.Errorf("%s: the turn was started (%v)", name, err)
		}
	}
}
