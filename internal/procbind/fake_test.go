package procbind

import (
	"errors"
	"fmt"
)

// A simulated process table. A Handle names an incarnation (a *fproc); Parent and Start read by
// NUMBER, as /proc does, so a number that was recycled answers for its new holder. hook runs
// before every call into the table, numbered from 0, and may change the world.
type fproc struct {
	pid, ppid int
	start     uint64
	alive     bool
}

type fcall struct {
	op string
	p  *fproc
}

type world struct {
	procs  []*fproc
	hook   func(n int)
	calls  int
	log    []fcall
	closed int
	fail   map[string]error // op -> error, to simulate an unreadable table
}

func (w *world) spawn(pid, ppid int, start uint64) *fproc {
	p := &fproc{pid: pid, ppid: ppid, start: start, alive: true}
	w.procs = append(w.procs, p)
	return p
}

// exit ends p; its live children go to init.
func (w *world) exit(p *fproc) {
	p.alive = false
	for _, q := range w.procs {
		if q.alive && q.ppid == p.pid {
			q.ppid = 1
		}
	}
}

func (w *world) byPid(pid int) *fproc {
	for _, p := range w.procs {
		if p.alive && p.pid == pid {
			return p
		}
	}
	return nil
}

func (w *world) tick(op string, p *fproc) error {
	w.log = append(w.log, fcall{op, p})
	if w.hook != nil {
		w.hook(w.calls)
	}
	w.calls++
	return w.fail[op]
}

type fhandle struct {
	w *world
	p *fproc
}

func (w *world) handle(p *fproc) Handle { return &fhandle{w, p} }

func (h *fhandle) Alive() bool {
	_ = h.w.tick("alive", h.p)
	return h.p.alive
}

func (h *fhandle) Pid() (int, error) {
	if err := h.w.tick("pid", h.p); err != nil {
		return 0, err
	}
	if !h.p.alive {
		return 0, errors.New("gone")
	}
	return h.p.pid, nil
}

func (h *fhandle) Close() error { h.w.closed++; return nil }

type fprocs struct{ w *world }

func (f fprocs) Open(pid int) (Handle, error) {
	if err := f.w.tick("open", nil); err != nil {
		return nil, err
	}
	p := f.w.byPid(pid)
	if p == nil {
		return nil, fmt.Errorf("no process %d", pid)
	}
	return f.w.handle(p), nil
}

// read finds the process that holds h's number now, after the number was read from h.
func (f fprocs) read(op string, h Handle) (*fproc, error) {
	pid, err := h.Pid()
	if err != nil {
		return nil, err
	}
	if err := f.w.tick(op, h.(*fhandle).p); err != nil {
		return nil, err
	}
	q := f.w.byPid(pid)
	if q == nil {
		return nil, errors.New("no such process")
	}
	return q, nil
}

func (f fprocs) Parent(h Handle) (int, error) {
	q, err := f.read("parent", h)
	if err != nil {
		return 0, err
	}
	return q.ppid, nil
}

func (f fprocs) Start(h Handle) (uint64, error) {
	q, err := f.read("start", h)
	if err != nil {
		return 0, err
	}
	return q.start, nil
}
