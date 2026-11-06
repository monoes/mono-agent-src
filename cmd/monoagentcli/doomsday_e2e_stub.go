//go:build e2estub

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// THE STUB-RUNTIME END-TO-END BUILD (tag e2estub). It exists only for the browser end-to-end of the open-ended chat mode
// (scratch HOME, private network namespace, a fake monomind): it turns the mode's ready switch on and puts a fake service
// manager and cgroup tree where the host's containment would be, because a private namespace has neither. No shipped build
// carries this file; nothing here is reachable without the tag.

// The two switches of the negative runs are files in the scratch HOME, read at every use: .e2e-refuse-entry makes the unit of a
// run fail to start (the entry is refused after the boss exists), .e2e-fail-first-turn makes the first-turn queue fail.
func e2eSwitch(name string) bool {
	_, err := os.Stat(filepath.Join(os.Getenv("HOME"), name))
	return err == nil
}

func init() {
	coderhost.SetOpenModeReady(true)
	doomsdayFirstTurnHook = func() error {
		if e2eSwitch(".e2e-fail-first-turn") {
			return errors.New("e2e stub: the first-turn queue is made to fail")
		}
		return nil
	}
	doomsdayWiringHook = func(w *doomsdaycli.Wiring) {
		world := &e2eWorld{}
		w.Probe = func() error { return nil }
		w.Kernel = e2eKernel{w: world}
		w.Host = world
		w.Binding = func(context.Context) (bool, error) { return true, nil }
		if w.Witness != nil {
			w.Witness.Secrets = &stop.FileSecretStore{Dir: filepath.Join(os.Getenv("HOME"), ".e2e-secrets")}
		}
	}
}

type e2eWorld struct {
	mu      sync.Mutex
	present bool
	ids     map[string]uint64
}

func (w *e2eWorld) StartUnit(context.Context, string, string) error {
	if e2eSwitch(".e2e-refuse-entry") {
		return errors.New("e2e stub: the unit is made to fail")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.present = true
	return nil
}

func (w *e2eWorld) StopUnit(context.Context, string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.present = false
	return nil
}

type e2eKernel struct{ w *e2eWorld }

var errE2EAbsent = errors.New("e2e stub: absent")

func (e2eKernel) BootID() (string, error)        { return "e2e-boot", nil }
func (e2eKernel) CurrentUID() uint32             { return uint32(os.Getuid()) }
func (e2eKernel) DelegatedRoot() (string, error) { return "/e2e/user@1", nil }
func (e2eKernel) SelfCgroup() (string, error)    { return "/e2e-self", nil }
func (k e2eKernel) OpenCgroup(path string) (stop.Cgroup, error) {
	k.w.mu.Lock()
	defer k.w.mu.Unlock()
	if !k.w.present {
		return nil, errE2EAbsent
	}
	return e2eCgroup{w: k.w, path: path}, nil
}
func (e2eKernel) IsAbsent(err error) bool                   { return errors.Is(err, errE2EAbsent) }
func (e2eKernel) ManagerControlGroup(string) (string, bool) { return "", false }
func (e2eKernel) ListUnitNames(string) ([]string, error)    { return nil, nil }

type e2eCgroup struct {
	w    *e2eWorld
	path string
}

func (c e2eCgroup) Identity() (stop.CgroupIdentity, error) {
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	if c.w.ids == nil {
		c.w.ids = map[string]uint64{}
	}
	id, ok := c.w.ids[c.path]
	if !ok {
		id = 7 + uint64(len(c.w.ids))
		c.w.ids[c.path] = id
	}
	return stop.CgroupIdentity{ID: id, UID: uint32(os.Getuid()), IsCgroup2: true}, nil
}
func (e2eCgroup) Subgroups() ([]string, error) { return nil, nil }
func (e2eCgroup) Procs(string) ([]int, error)  { return []int{100}, nil }
func (e2eCgroup) ProcInfo(_ string, pid int) (string, string, error) {
	return fmt.Sprintf("stamp-%d", pid), "proc", nil
}
func (e2eCgroup) Signal(string, int, syscall.Signal) error { return nil }
func (e2eCgroup) Kill(string) error                        { return nil }
func (e2eCgroup) Populated(string) (bool, error)           { return false, nil }
func (e2eCgroup) Rmdir(string) error                       { return nil }
func (e2eCgroup) Close() error                             { return nil }
